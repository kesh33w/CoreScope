package main

// Feed turns the raw meshfeed observation stream (meshcore/<IATA>/<observer>/packets
// and /status) into derived feeds. It holds all state in memory and is driven
// by the caller with explicit timestamps, so it has no I/O and is testable.

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	TopicDecodedRoot   = "meshcore-decoded"
	TopicMergedRoot    = "meshcore-merged"
	TopicNodesRoot     = "meshcore-nodes"
	TopicObserversRoot = "meshcore-observers"
	TopicHealth        = "meshfeed-health/publisher"
)

// Out is one MQTT publication. A retained Out with a nil Payload clears the
// retained message for that topic.
type Out struct {
	Topic   string
	Payload []byte
	Retain  bool
}

type FeedConfig struct {
	MergeWindow          time.Duration // how long to gather observations of one packet
	MaxPendingMerges     int
	NodeRepublish        time.Duration // re-publish an unchanged node at most this often
	ObserverRepublish    time.Duration // re-publish observer packet counters at most this often
	NodeExpiry           time.Duration // clear retained nodes not advertised for this long
	ObserverExpiry       time.Duration
	MaxObservationsMerge int
}

func DefaultFeedConfig() FeedConfig {
	return FeedConfig{
		MergeWindow:          8 * time.Second,
		MaxPendingMerges:     20000,
		NodeRepublish:        30 * time.Minute,
		ObserverRepublish:    60 * time.Second,
		NodeExpiry:           14 * 24 * time.Hour,
		ObserverExpiry:       7 * 24 * time.Hour,
		MaxObservationsMerge: 200,
	}
}

type Counts struct {
	Packets       uint64 `json:"packets"`
	Statuses      uint64 `json:"statuses"`
	Invalid       uint64 `json:"invalid"`
	DecodeErrors  uint64 `json:"decode_errors"`
	Decrypted     uint64 `json:"decrypted"`
	Merged        uint64 `json:"merged"`
	MergeDropped  uint64 `json:"merge_dropped"`
	NodeUpdates   uint64 `json:"node_updates"`
	BadSignatures uint64 `json:"bad_signatures"`
}

type observation struct {
	Region    string   `json:"region"`
	Observer  string   `json:"observer"`
	Origin    string   `json:"origin,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	SNR       *float64 `json:"snr,omitempty"`
	RSSI      *float64 `json:"rssi,omitempty"`
	Hops      []string `json:"hops"`
}

type pendingMerge struct {
	first, last  time.Time
	raw          string
	decoded      *DecodedPacket
	observations []observation
}

// Node is the retained node-directory record (meshcore-nodes/<PUBKEY>).
type Node struct {
	PublicKey       string   `json:"public_key"`
	Name            string   `json:"name,omitempty"`
	Role            string   `json:"role,omitempty"`
	Lat             *float64 `json:"lat,omitempty"`
	Lon             *float64 `json:"lon,omitempty"`
	AdvertTimestamp uint32   `json:"advert_timestamp,omitempty"`
	FirstSeenAt     string   `json:"first_seen_at"`
	LastAdvertAt    string   `json:"last_advert_at"`
	LastRegion      string   `json:"last_region,omitempty"`
	LastObserver    string   `json:"last_observer,omitempty"`

	lastAdvert    time.Time
	lastPublished time.Time
}

// Observer is the retained roster record (meshcore-observers/<IATA>/<ID>).
type Observer struct {
	ID           string                 `json:"id"`
	Region       string                 `json:"region"`
	Name         string                 `json:"name,omitempty"`
	Status       map[string]interface{} `json:"status,omitempty"`
	LastStatusAt string                 `json:"last_status_at,omitempty"`
	LastPacketAt string                 `json:"last_packet_at,omitempty"`
	PacketCount  uint64                 `json:"packets_since_publisher_start"`

	lastSeen      time.Time
	lastPublished time.Time
	dirty         bool
}

type Feed struct {
	cfg       FeedConfig
	keys      map[string]string
	merges    map[string]*pendingMerge
	nodes     map[string]*Node
	observers map[string]*Observer
	Counts    Counts
}

func NewFeed(cfg FeedConfig, channelKeys map[string]string) *Feed {
	return &Feed{cfg: cfg, keys: channelKeys, merges: map[string]*pendingMerge{},
		nodes: map[string]*Node{}, observers: map[string]*Observer{}}
}

func (f *Feed) NodeCount() int     { return len(f.nodes) }
func (f *Feed) ObserverCount() int { return len(f.observers) }
func (f *Feed) PendingMerges() int { return len(f.merges) }

// splitTopic returns region, observer and kind for meshcore/<r>/<o>/<kind>.
func splitTopic(topic string) (string, string, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "meshcore" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

func number(msg map[string]interface{}, keys ...string) *float64 {
	for _, k := range keys {
		if v, ok := msg[k].(float64); ok {
			return &v
		}
	}
	return nil
}

func str(msg map[string]interface{}, key string) string {
	s, _ := msg[key].(string)
	return s
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain structs and maps are marshalled here
	}
	return b
}

// Handle processes one raw-feed message and returns publications to make now.
func (f *Feed) Handle(topic string, payload []byte, now time.Time) []Out {
	region, observerID, kind, ok := splitTopic(topic)
	if !ok || (kind != "packets" && kind != "status") {
		return nil
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil || msg == nil {
		f.Counts.Invalid++
		return nil
	}
	if kind == "status" {
		return f.handleStatus(region, observerID, msg, now)
	}
	return f.handlePacket(region, observerID, msg, now)
}

func (f *Feed) observer(region, id string) *Observer {
	key := region + "/" + id
	o := f.observers[key]
	if o == nil {
		o = &Observer{ID: id, Region: region}
		f.observers[key] = o
	}
	return o
}

// Status fields copied into the roster: plain scalar values only, never the
// bridge's provenance block or nested objects.
func statusFields(msg map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range msg {
		if strings.HasPrefix(k, "_") || k == "origin" || k == "timestamp" {
			continue
		}
		switch t := v.(type) {
		case string:
			if len(t) <= 512 {
				out[k] = t
			}
		case float64, bool:
			out[k] = t
		}
	}
	return out
}

func sameStatus(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (f *Feed) handleStatus(region, id string, msg map[string]interface{}, now time.Time) []Out {
	f.Counts.Statuses++
	o := f.observer(region, id)
	if name := str(msg, "origin"); name != "" {
		o.Name = name
	}
	fields := statusFields(msg)
	changed := !sameStatus(fields, o.Status)
	// Liveness-only status messages (status=online with no hardware fields)
	// must not wipe a richer snapshot learned earlier.
	if changed && len(fields) > 1 || o.Status == nil {
		merged := map[string]interface{}{}
		for k, v := range o.Status {
			merged[k] = v
		}
		for k, v := range fields {
			merged[k] = v
		}
		changed = !sameStatus(merged, o.Status)
		o.Status = merged
	} else {
		changed = false
	}
	o.LastStatusAt = now.UTC().Format(time.RFC3339)
	o.lastSeen = now
	o.dirty = true
	if changed {
		return []Out{f.publishObserver(o, now)}
	}
	return nil
}

func (f *Feed) publishObserver(o *Observer, now time.Time) Out {
	o.lastPublished = now
	o.dirty = false
	return Out{Topic: TopicObserversRoot + "/" + o.Region + "/" + o.ID, Payload: mustJSON(o), Retain: true}
}

func (f *Feed) handlePacket(region, observerID string, msg map[string]interface{}, now time.Time) []Out {
	raw := strings.TrimSpace(str(msg, "raw"))
	if raw == "" {
		f.Counts.Invalid++
		return nil
	}
	f.Counts.Packets++
	o := f.observer(region, observerID)
	if name := str(msg, "origin"); name != "" {
		o.Name = name
	}
	o.PacketCount++
	o.LastPacketAt = now.UTC().Format(time.RFC3339)
	o.lastSeen = now
	o.dirty = true

	decoded, err := DecodePacket(raw, f.keys, true)
	if err != nil {
		f.Counts.DecodeErrors++
		return nil
	}
	if decoded.Payload.DecryptionStatus == "decrypted" {
		f.Counts.Decrypted++
	}
	hash := ComputeContentHash(decoded.Raw)
	snr := number(msg, "SNR", "snr")
	rssi := number(msg, "RSSI", "rssi")

	out := []Out{}
	decodedMsg := map[string]interface{}{
		"hash": hash, "region": region, "observer": observerID,
		"origin": str(msg, "origin"), "timestamp": str(msg, "timestamp"),
		"received_at": now.UTC().Format(time.RFC3339Nano),
		"raw":         decoded.Raw, "decoded": decoded,
	}
	if snr != nil {
		decodedMsg["snr"] = *snr
	}
	if rssi != nil {
		decodedMsg["rssi"] = *rssi
	}
	if d := str(msg, "direction"); d != "" {
		decodedMsg["direction"] = d
	}
	if p, ok := msg["_corescope_bridge"].(map[string]interface{}); ok {
		decodedMsg["_corescope_bridge"] = p
	}
	out = append(out, Out{Topic: TopicDecodedRoot + "/" + region + "/" + observerID + "/packets", Payload: mustJSON(decodedMsg)})

	hops := decoded.Path.Hops
	if hops == nil {
		hops = []string{}
	}
	obs := observation{Region: region, Observer: observerID, Origin: str(msg, "origin"),
		Timestamp: str(msg, "timestamp"), SNR: snr, RSSI: rssi, Hops: hops}
	m := f.merges[hash]
	if m == nil {
		if len(f.merges) >= f.cfg.MaxPendingMerges {
			f.Counts.MergeDropped++
		} else {
			m = &pendingMerge{first: now, raw: decoded.Raw, decoded: decoded}
			f.merges[hash] = m
		}
	}
	if m != nil {
		m.last = now
		if len(m.observations) < f.cfg.MaxObservationsMerge {
			m.observations = append(m.observations, obs)
		}
	}

	if decoded.Header.PayloadType == PayloadADVERT {
		out = append(out, f.handleAdvert(decoded, region, observerID, now)...)
	}
	return out
}

func (f *Feed) handleAdvert(d *DecodedPacket, region, observerID string, now time.Time) []Out {
	p := d.Payload
	key := strings.ToUpper(p.PubKey)
	if len(key) != 64 || p.SignatureValid == nil || !*p.SignatureValid {
		f.Counts.BadSignatures++
		return nil
	}
	n := f.nodes[key]
	nowISO := now.UTC().Format(time.RFC3339)
	if n == nil {
		n = &Node{PublicKey: key, FirstSeenAt: nowISO}
		f.nodes[key] = n
	} else if p.Timestamp != 0 && n.AdvertTimestamp != 0 && p.Timestamp < n.AdvertTimestamp {
		// Older advert (delayed or replayed) must not roll the record back.
		return nil
	}
	role := advertRole(p.Flags)
	changed := n.Name != p.Name || n.Role != role || !sameFloat(n.Lat, p.Lat) || !sameFloat(n.Lon, p.Lon)
	if p.Name != "" {
		n.Name = p.Name
	}
	n.Role, n.Lat, n.Lon = role, p.Lat, p.Lon
	n.AdvertTimestamp = p.Timestamp
	n.LastAdvertAt, n.lastAdvert = nowISO, now
	n.LastRegion, n.LastObserver = region, observerID
	if changed || now.Sub(n.lastPublished) >= f.cfg.NodeRepublish {
		n.lastPublished = now
		f.Counts.NodeUpdates++
		return []Out{{Topic: TopicNodesRoot + "/" + key, Payload: mustJSON(n), Retain: true}}
	}
	return nil
}

func sameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Tick flushes completed merges, throttled observer updates and expiries.
func (f *Feed) Tick(now time.Time) []Out {
	var out []Out
	for hash, m := range f.merges {
		if now.Sub(m.first) < f.cfg.MergeWindow {
			continue
		}
		delete(f.merges, hash)
		f.Counts.Merged++
		regions := []string{}
		seen := map[string]bool{}
		for _, o := range m.observations {
			if !seen[o.Region] {
				seen[o.Region] = true
				regions = append(regions, o.Region)
			}
		}
		msg := map[string]interface{}{
			"hash": hash, "payload_type": m.decoded.Header.PayloadTypeName,
			"route_type": m.decoded.Header.RouteTypeName,
			"first_seen": m.first.UTC().Format(time.RFC3339Nano),
			"last_seen":  m.last.UTC().Format(time.RFC3339Nano),
			"raw":        m.raw, "decoded": m.decoded,
			"observer_count": len(m.observations), "regions": regions,
			"observations": m.observations,
		}
		typeName := m.decoded.Header.PayloadTypeName
		if typeName == "" {
			typeName = "UNKNOWN"
		}
		out = append(out, Out{Topic: TopicMergedRoot + "/" + typeName, Payload: mustJSON(msg)})
	}
	for key, o := range f.observers {
		if now.Sub(o.lastSeen) > f.cfg.ObserverExpiry {
			delete(f.observers, key)
			out = append(out, Out{Topic: TopicObserversRoot + "/" + key, Retain: true})
			continue
		}
		if o.dirty && now.Sub(o.lastPublished) >= f.cfg.ObserverRepublish {
			out = append(out, f.publishObserver(o, now))
		}
	}
	for key, n := range f.nodes {
		if now.Sub(n.lastAdvert) > f.cfg.NodeExpiry {
			delete(f.nodes, key)
			out = append(out, Out{Topic: TopicNodesRoot + "/" + key, Retain: true})
		}
	}
	return out
}

// Seed restores state from a retained message published by an earlier run,
// so a restart neither forgets nodes nor re-announces them all.
func (f *Feed) Seed(topic string, payload []byte, now time.Time) {
	if len(payload) == 0 {
		return
	}
	switch {
	case strings.HasPrefix(topic, TopicNodesRoot+"/"):
		var n Node
		if json.Unmarshal(payload, &n) != nil || len(n.PublicKey) != 64 {
			return
		}
		if _, exists := f.nodes[n.PublicKey]; exists {
			return
		}
		t, err := time.Parse(time.RFC3339, n.LastAdvertAt)
		if err != nil {
			return
		}
		n.lastAdvert, n.lastPublished = t, now
		f.nodes[n.PublicKey] = &n
	case strings.HasPrefix(topic, TopicObserversRoot+"/"):
		var o Observer
		if json.Unmarshal(payload, &o) != nil || o.ID == "" || o.Region == "" {
			return
		}
		key := o.Region + "/" + o.ID
		if _, exists := f.observers[key]; exists {
			return
		}
		last := o.LastPacketAt
		if o.LastStatusAt > last {
			last = o.LastStatusAt
		}
		t, err := time.Parse(time.RFC3339, last)
		if err != nil {
			return
		}
		o.lastSeen, o.lastPublished = t, now
		o.PacketCount = 0
		f.observers[key] = &o
	}
}
