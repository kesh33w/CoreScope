// meshfeed-publisher reads meshfeed's raw MeshCore stream and publishes
// derived feeds: decoded packets, merged per-packet views, a retained node
// directory and a retained observer roster. See feed.go for the topics.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	Broker       string            `json:"broker"`
	UsernameEnv  string            `json:"usernameEnv"`
	PasswordEnv  string            `json:"passwordEnv"`
	ClientID     string            `json:"clientId"`
	ChannelKeys  map[string]string `json:"channelKeys"`
	HashChannels []string          `json:"hashChannels"`
	MergeWindowS float64           `json:"mergeWindowSeconds"`
	SeedSeconds  float64           `json:"seedSeconds"`
}

// loadChannelKeys mirrors the ingestor's priority: builtin Public, then keys
// derived from hashChannels, then explicit channelKeys (highest).
func loadChannelKeys(cfg Config) map[string]string {
	keys := map[string]string{"Public": "8b3387e9c5cdea6ac9e5edbaa115cd72"}
	for _, raw := range cfg.HashChannels {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		h := sha256.Sum256([]byte(name))
		keys[name] = hex.EncodeToString(h[:16])
	}
	for name, key := range cfg.ChannelKeys {
		if b, err := hex.DecodeString(key); err == nil && len(b) == 16 {
			keys[name] = strings.ToLower(key)
		} else {
			log.Printf("ignoring channel key %q: want 32 hex characters", name)
		}
	}
	return keys
}

func main() {
	configPath := flag.String("config", "/config/publisher.json", "config file")
	flag.Parse()
	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	if cfg.Broker == "" {
		cfg.Broker = "tcp://broker:1883"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "meshfeed-publisher"
	}
	if cfg.SeedSeconds <= 0 {
		cfg.SeedSeconds = 5
	}
	feedCfg := DefaultFeedConfig()
	if cfg.MergeWindowS > 0 {
		feedCfg.MergeWindow = time.Duration(cfg.MergeWindowS * float64(time.Second))
	}
	keys := loadChannelKeys(cfg)
	log.Printf("meshfeed-publisher: %d channel keys, merge window %s", len(keys), feedCfg.MergeWindow)

	feed := NewFeed(feedCfg, keys)
	var mu sync.Mutex // guards feed; paho delivers on its own goroutines
	started := time.Now()

	opts := mqtt.NewClientOptions().AddBroker(cfg.Broker).SetClientID(cfg.ClientID).
		SetCleanSession(true).SetAutoReconnect(true).SetMaxReconnectInterval(15*time.Second).
		SetConnectRetry(true).SetConnectRetryInterval(2*time.Second).SetKeepAlive(20*time.Second).
		SetOrderMatters(false).
		SetBinaryWill(TopicHealth, mustJSON(map[string]string{"status": "offline"}), 0, true)
	if cfg.UsernameEnv != "" {
		opts.SetUsername(os.Getenv(cfg.UsernameEnv))
	}
	if cfg.PasswordEnv != "" {
		opts.SetPassword(os.Getenv(cfg.PasswordEnv))
	}

	var client mqtt.Client
	publish := func(outs []Out) {
		for _, o := range outs {
			// QoS 0, fire and forget: a slow broker must not stall decoding.
			client.Publish(o.Topic, 0, o.Retain, o.Payload)
		}
	}
	onRaw := func(_ mqtt.Client, m mqtt.Message) {
		if m.Retained() {
			return // the raw feed is live-only; never treat retained data as new
		}
		mu.Lock()
		outs := feed.Handle(m.Topic(), m.Payload(), time.Now())
		mu.Unlock()
		publish(outs)
	}
	onSeed := func(_ mqtt.Client, m mqtt.Message) {
		if !m.Retained() {
			return // our own live publications echo back; only stored state seeds
		}
		mu.Lock()
		feed.Seed(m.Topic(), m.Payload(), time.Now())
		mu.Unlock()
	}
	seeded := false
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		log.Printf("connected to %s", cfg.Broker)
		if !seeded {
			c.Subscribe(TopicNodesRoot+"/#", 0, onSeed)
			c.Subscribe(TopicObserversRoot+"/+/+", 0, onSeed)
			go func() {
				time.Sleep(time.Duration(cfg.SeedSeconds * float64(time.Second)))
				c.Unsubscribe(TopicNodesRoot+"/#", TopicObserversRoot+"/+/+")
				mu.Lock()
				log.Printf("seeded %d nodes, %d observers from retained state", feed.NodeCount(), feed.ObserverCount())
				mu.Unlock()
			}()
			seeded = true
		}
		c.SubscribeMultiple(map[string]byte{"meshcore/+/+/packets": 0, "meshcore/+/+/status": 0}, onRaw)
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) { log.Printf("connection lost: %v", err) })
	client = mqtt.NewClient(opts)
	client.Connect()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	health := time.NewTicker(30 * time.Second)
	defer health.Stop()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case now := <-ticker.C:
			mu.Lock()
			outs := feed.Tick(now)
			mu.Unlock()
			publish(outs)
		case now := <-health.C:
			mu.Lock()
			h := map[string]interface{}{
				"status": "online", "started_at": started.UTC().Format(time.RFC3339),
				"updated_at": now.UTC().Format(time.RFC3339), "uptime_seconds": int(now.Sub(started).Seconds()),
				"counts": feed.Counts, "nodes": feed.NodeCount(), "observers": feed.ObserverCount(),
				"pending_merges": feed.PendingMerges(), "channel_keys": len(keys),
			}
			mu.Unlock()
			publish([]Out{{Topic: TopicHealth, Payload: mustJSON(h), Retain: true}})
		case <-stop:
			client.Publish(TopicHealth, 0, true, mustJSON(map[string]string{"status": "offline"})).WaitTimeout(2 * time.Second)
			client.Disconnect(500)
			return
		}
	}
}
