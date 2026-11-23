package main

import (
	"crypto/aes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func advertHex(pub ed25519.PublicKey, priv ed25519.PrivateKey, ts uint32, name string) string {
	appdata := append([]byte{0x81}, append([]byte(name), 0)...)
	msg := append(append(append([]byte{}, pub...), make([]byte, 4)...), appdata...)
	binary.LittleEndian.PutUint32(msg[32:36], ts)
	sig := ed25519.Sign(priv, msg)
	payload := append(append(append([]byte{}, pub...), msg[32:36]...), sig...)
	payload = append(payload, appdata...)
	return hex.EncodeToString(append([]byte{0x11, 0x00}, payload...))
}

// grpTxtHex builds a FLOOD GRP_TXT encrypted with the given 16-byte key.
func grpTxtHex(key []byte, text string, hops []byte) string {
	pt := make([]byte, 5)
	binary.LittleEndian.PutUint32(pt, 1700000000)
	pt = append(pt, []byte(text)...)
	for len(pt)%16 != 0 {
		pt = append(pt, 0)
	}
	block, _ := aes.NewCipher(key)
	ct := make([]byte, len(pt))
	for i := 0; i < len(pt); i += 16 {
		block.Encrypt(ct[i:i+16], pt[i:i+16])
	}
	mac := hmac.New(sha256.New, append(append([]byte{}, key...), make([]byte, 16)...))
	mac.Write(ct)
	h := sha256.Sum256(key)
	payload := append([]byte{h[0]}, mac.Sum(nil)[:2]...)
	payload = append(payload, ct...)
	pkt := append([]byte{0x05<<2 | 0x01, byte(len(hops))}, hops...)
	return hex.EncodeToString(append(pkt, payload...))
}

func pkt(raw string, snr float64) []byte {
	b, _ := json.Marshal(map[string]interface{}{"raw": raw, "SNR": snr, "origin": "Obs", "timestamp": "2026-11-23T12:00:00Z",
		"_corescope_bridge": map[string]interface{}{"source": "https://example"}})
	return b
}

func find(outs []Out, prefix string) []Out {
	var r []Out
	for _, o := range outs {
		if strings.HasPrefix(o.Topic, prefix) {
			r = append(r, o)
		}
	}
	return r
}

func TestAdvertPublishesRetainedNodeOnce(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := NewFeed(DefaultFeedConfig(), nil)
	outs := f.Handle("meshcore/SMF/AAAA/packets", pkt(advertHex(pub, priv, 100, "Alpha"), 5), t0)
	nodes := find(outs, TopicNodesRoot+"/")
	if len(nodes) != 1 || !nodes[0].Retain {
		t.Fatalf("want one retained node, got %+v", outs)
	}
	var n Node
	json.Unmarshal(nodes[0].Payload, &n)
	if n.Name != "Alpha" || n.PublicKey != strings.ToUpper(hex.EncodeToString(pub)) || n.LastRegion != "SMF" {
		t.Fatalf("bad node %+v", n)
	}
	if len(find(outs, TopicDecodedRoot+"/SMF/AAAA/packets")) != 1 {
		t.Fatal("missing decoded publication")
	}
	// Same advert heard by a second observer: unchanged, not republished.
	if got := find(f.Handle("meshcore/SFO/BBBB/packets", pkt(advertHex(pub, priv, 100, "Alpha"), 1), t0.Add(time.Second)), TopicNodesRoot); len(got) != 0 {
		t.Fatal("unchanged node republished")
	}
	// Older advert must not roll the record back.
	if got := find(f.Handle("meshcore/SMF/AAAA/packets", pkt(advertHex(pub, priv, 50, "Old"), 1), t0.Add(2*time.Second)), TopicNodesRoot); len(got) != 0 {
		t.Fatal("older advert accepted")
	}
	// Rename publishes.
	if got := find(f.Handle("meshcore/SMF/AAAA/packets", pkt(advertHex(pub, priv, 200, "Beta"), 1), t0.Add(3*time.Second)), TopicNodesRoot); len(got) != 1 {
		t.Fatal("rename not published")
	}
}

func TestForgedAdvertIgnored(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	raw := []byte(advertHex(pub, priv, 100, "Alpha"))
	raw[len(raw)-4] = 'f' // corrupt the name after signing
	if raw[len(raw)-3] == 'f' {
		raw[len(raw)-3] = 'e'
	} else {
		raw[len(raw)-3] = 'f'
	}
	f := NewFeed(DefaultFeedConfig(), nil)
	if got := find(f.Handle("meshcore/SMF/AAAA/packets", pkt(string(raw), 1), t0), TopicNodesRoot); len(got) != 0 {
		t.Fatal("forged advert accepted")
	}
	if f.Counts.BadSignatures != 1 {
		t.Fatalf("bad signature not counted: %+v", f.Counts)
	}
}

func TestMergeAcrossObserversAndDecrypt(t *testing.T) {
	key, _ := hex.DecodeString("8b3387e9c5cdea6ac9e5edbaa115cd72")
	f := NewFeed(DefaultFeedConfig(), map[string]string{"Public": "8b3387e9c5cdea6ac9e5edbaa115cd72"})
	a := grpTxtHex(key, "Bob: hello", []byte{0xAB})
	b := grpTxtHex(key, "Bob: hello", []byte{0xAB, 0xCD}) // same packet, longer route
	outs := f.Handle("meshcore/SMF/AAAA/packets", pkt(a, 5), t0)
	dec := find(outs, TopicDecodedRoot)
	if len(dec) != 1 || !strings.Contains(string(dec[0].Payload), `"text":"Bob: hello"`) {
		t.Fatalf("not decrypted: %s", dec[0].Payload)
	}
	f.Handle("meshcore/SFO/BBBB/packets", pkt(b, -3), t0.Add(2*time.Second))
	if len(find(f.Tick(t0.Add(5*time.Second)), TopicMergedRoot)) != 0 {
		t.Fatal("merged before window closed")
	}
	merged := find(f.Tick(t0.Add(9*time.Second)), TopicMergedRoot+"/GRP_TXT")
	if len(merged) != 1 {
		t.Fatalf("want one merged message, got %d", len(merged))
	}
	var m struct {
		ObserverCount int           `json:"observer_count"`
		Regions       []string      `json:"regions"`
		Observations  []observation `json:"observations"`
	}
	json.Unmarshal(merged[0].Payload, &m)
	if m.ObserverCount != 2 || len(m.Regions) != 2 || len(m.Observations[1].Hops) != 2 {
		t.Fatalf("bad merge %s", merged[0].Payload)
	}
	if f.PendingMerges() != 0 || f.Counts.Decrypted != 2 {
		t.Fatalf("state %+v pending %d", f.Counts, f.PendingMerges())
	}
}

func TestStatusLivenessKeepsHardwareSnapshot(t *testing.T) {
	f := NewFeed(DefaultFeedConfig(), nil)
	outs := f.Handle("meshcore/SMF/AAAA/status", []byte(`{"origin":"Obs","status":"snapshot","model":"Heltec V4","firmware":"1.9"}`), t0)
	if len(find(outs, TopicObserversRoot+"/SMF/AAAA")) != 1 {
		t.Fatal("first status not published")
	}
	outs = f.Handle("meshcore/SMF/AAAA/status", []byte(`{"origin":"Obs","status":"online","_corescope_bridge":{"x":1}}`), t0.Add(time.Second))
	if len(outs) != 0 {
		t.Fatal("liveness-only status should not republish immediately")
	}
	o := f.observers["SMF/AAAA"]
	if o.Status["model"] != "Heltec V4" {
		t.Fatalf("snapshot wiped: %+v", o.Status)
	}
	if _, leaked := o.Status["_corescope_bridge"]; leaked {
		t.Fatal("provenance copied into roster")
	}
	// Throttled roster refresh happens on tick after the republish interval.
	if len(find(f.Tick(t0.Add(30*time.Second)), TopicObserversRoot)) != 0 {
		t.Fatal("republished too early")
	}
	if len(find(f.Tick(t0.Add(61*time.Second)), TopicObserversRoot)) != 1 {
		t.Fatal("dirty observer not republished")
	}
}

func TestSeedAndExpiry(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	f1 := NewFeed(DefaultFeedConfig(), nil)
	node := find(f1.Handle("meshcore/SMF/AAAA/packets", pkt(advertHex(pub, priv, 100, "Alpha"), 5), t0), TopicNodesRoot)[0]

	f2 := NewFeed(DefaultFeedConfig(), nil)
	f2.Seed(node.Topic, node.Payload, t0.Add(time.Hour))
	if f2.NodeCount() != 1 {
		t.Fatal("seed failed")
	}
	// The restored node is not re-announced by an identical advert.
	if len(find(f2.Handle("meshcore/SMF/AAAA/packets", pkt(advertHex(pub, priv, 100, "Alpha"), 5), t0.Add(time.Hour)), TopicNodesRoot)) != 0 {
		t.Fatal("seeded node republished")
	}
	cleared := find(f2.Tick(t0.Add(15*24*time.Hour)), TopicNodesRoot)
	if len(cleared) != 1 || cleared[0].Payload != nil || !cleared[0].Retain {
		t.Fatalf("expired node not cleared: %+v", cleared)
	}
}

func TestRejectsBadInput(t *testing.T) {
	f := NewFeed(DefaultFeedConfig(), nil)
	for _, topic := range []string{"meshcore/SMF/packets", "other/SMF/A/packets", "meshcore/SMF/A/neighbors"} {
		if outs := f.Handle(topic, pkt("1100", 1), t0); outs != nil {
			t.Fatalf("%s accepted", topic)
		}
	}
	f.Handle("meshcore/SMF/A/packets", []byte("not json"), t0)
	f.Handle("meshcore/SMF/A/packets", []byte(`{"raw":"zz"}`), t0)
	if f.Counts.Invalid != 1 || f.Counts.DecodeErrors != 1 {
		t.Fatalf("counts %+v", f.Counts)
	}
}

func TestLoadChannelKeys(t *testing.T) {
	keys := loadChannelKeys(Config{HashChannels: []string{"test", "#test", ""}, ChannelKeys: map[string]string{"Priv": "00112233445566778899AABBCCDDEEFF", "Bad": "xyz"}})
	h := sha256.Sum256([]byte("#test"))
	if keys["#test"] != hex.EncodeToString(h[:16]) || keys["Priv"] != "00112233445566778899aabbccddeeff" || keys["Public"] == "" || keys["Bad"] != "" {
		t.Fatalf("keys %+v", keys)
	}
}
