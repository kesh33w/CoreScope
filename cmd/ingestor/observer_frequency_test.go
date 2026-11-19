package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestObserverFrequencyUnits(t *testing.T) {
	for _, s := range []string{"909.875,62.5,9,5", "909875 kHz", "909875000 Hz", "909.875 MHz", "909.8750001"} {
		f, ok := parseObserverFrequency(s)
		if !ok || f < 909.87499 || f > 909.87501 {
			t.Fatalf("%s: %v %v", s, f, ok)
		}
	}
	for _, s := range []string{"NaN", "Inf", "", "SMF", "-909.875", "0"} {
		if _, ok := parseObserverFrequency(s); ok {
			t.Fatal(s)
		}
	}
}

func TestObserverFrequencyGate(t *testing.T) {
	now := time.Now().UTC()
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{"SMF": {MHz: 909.875, DropUnknown: true, MaxStatusAgeSec: 60}}}
	parts := []string{"meshcore", "SMF", "observer", "packets"}
	check := func(tag string, m map[string]interface{}, at time.Time, want string) {
		t.Helper()
		if got := c.observerFrequencyDecision(tag, parts, m, at); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	check("a", nil, now, "unknown")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "909.875,62.5,9,5"}, false, now)
	check("a", nil, now, "")
	check("b", nil, now, "unknown")
	check("a", nil, now.Add(61*time.Second), "unknown")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "910.525,62.5,7,5"}, false, now)
	check("a", nil, now, "mismatch")
	check("a", map[string]interface{}{"radio": "909.875 MHz"}, now, "")
	check("a", map[string]interface{}{"radio": "bad"}, now, "unknown")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "909.875", "radios": []interface{}{map[string]interface{}{"id": "secondary", "radio": "910.525"}}}, false, now)
	check("a", map[string]interface{}{"rx_radio_id": "secondary"}, now, "mismatch")
	check("a", map[string]interface{}{"rx_radio_id": "unseen"}, now, "unknown")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "909.875", "status": "offline"}, false, now)
	check("a", nil, now, "unknown")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "909.875"}, true, now)
	check("a", nil, now, "unknown") // undated retained snapshot cannot authorize reception
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "909.875", "timestamp": now.Format(time.RFC3339)}, true, now)
	check("a", nil, now, "")
	c.recordObserverFrequency("a", parts, map[string]interface{}{"radio": "910.525", "timestamp": now.Add(-2 * time.Hour).Format(time.RFC3339)}, true, now)
	check("a", nil, now, "unknown")
	parts[1] = "SFO"
	check("a", nil, now, "")
}

func TestFrequencyRuleValidation(t *testing.T) {
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{" smf ": {MHz: 909.875}}}
	if err := c.validateFrequencyRules(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ObserverFrequencyRules["SMF"]; !ok {
		t.Fatal("not normalized")
	}
	c.ObserverFrequencyRules["SMF"] = ObserverFrequencyRule{MHz: -1}
	if c.validateFrequencyRules() == nil {
		t.Fatal("accepted invalid")
	}
}
func TestFrequencyCacheBounded(t *testing.T) {
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{"SMF": {MHz: 909.875}}}
	now := time.Now()
	for i := 0; i < maxFrequencyObservers+10; i++ {
		c.recordObserverFrequency("a", []string{"meshcore", "SMF", fmt.Sprint(i)}, map[string]interface{}{"radio": "909.875"}, false, now)
	}
	if len(c.frequencyStates) != maxFrequencyObservers {
		t.Fatal(len(c.frequencyStates))
	}
}
func TestFrequencyHandleMessageDropsOnlyBadReception(t *testing.T) {
	s := newTestStore(t)
	cfg := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{"SMF": {MHz: 909.875}}}
	send := func(id, kind string, payload string) {
		handleMessage(s, "test", MQTTSource{}, &mockMessage{topic: "meshcore/SMF/" + id + "/" + kind, payload: []byte(payload)}, nil, nil, cfg)
	}
	send("bad", "status", `{"radio":"910.525,62.5,7,5"}`)
	send("good", "status", `{"radio":"909.875,62.5,9,5"}`)
	raw := `{"raw":"0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"}`
	send("bad", "packets", raw)
	send("good", "packets", raw)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("observations %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observers WHERE id='bad'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("status should remain for recovery", n, err)
	}
	send("bad", "status", `{"radio":"909.875,62.5,9,5"}`)
	send("bad", "packets", raw)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("recovered observations %d %v", n, err)
	}
}

func TestFrequencyUnknownAndTolerance(t *testing.T) {
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{"SMF": {MHz: 909.875}}}
	p := []string{"meshcore", "SMF", "observer"}
	now := time.Now()
	if got := c.observerFrequencyDecision("a", p, nil, now); got != "" {
		t.Fatal(got)
	}
	for _, v := range []struct{ radio, want string }{{"909.8755", ""}, {"909.87551", "mismatch"}, {"909.8745", ""}, {"bad", ""}} {
		if got := c.observerFrequencyDecision("a", p, map[string]interface{}{"radio": v.radio}, now); got != v.want {
			t.Fatalf("%s: %s", v.radio, got)
		}
	}
	c.recordObserverFrequency("a", p, map[string]interface{}{"radio": "910.525"}, false, now)
	if got := c.observerFrequencyDecision("a", p, nil, now.Add(time.Hour)); got != "" {
		t.Fatal(got)
	}
}
func TestFrequencyFeedCaseAndSourceIdentity(t *testing.T) {
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{"SMF": {MHz: 909.875, DropUnknown: true}}}
	p := []string{"meshcore", "SMF", strings.Repeat("AB", 32)}
	now := time.Now()
	a := MQTTSource{Name: "same", frequencySourceID: "source-0"}
	b := MQTTSource{Name: "same", frequencySourceID: "source-1"}
	c.recordObserverFrequency(a.frequencyIdentity(a.Name), p, map[string]interface{}{"radio": "909.875", "observer_radio_id": "auxA"}, false, now)
	for _, v := range []struct{ source, feed, want string }{{"source-0", "auxA", ""}, {"source-0", "auxa", "unknown"}, {b.frequencyIdentity(b.Name), "auxA", "unknown"}} {
		if got := c.observerFrequencyDecision(v.source, p, map[string]interface{}{"observer_radio_id": v.feed}, now); got != v.want {
			t.Fatalf("%+v got %s", v, got)
		}
	}
}

func TestObserverFrequencyIndependentRegionPolicies(t *testing.T) {
	now := time.Now().UTC()
	c := &Config{ObserverFrequencyRules: map[string]ObserverFrequencyRule{
		"SMF": {MHz: 909.875}, "SFO": {MHz: 910.525}, "LHR": {MHz: 869.525, DropUnknown: true},
	}}
	for _, tc := range []struct{ region, radio, want string }{
		{"SMF", "909.875", ""}, {"SMF", "910.525", "mismatch"},
		{"SFO", "910.525", ""}, {"SFO", "909.875", "mismatch"},
		{"LHR", "869.525", ""}, {"LHR", "", "unknown"},
		{"JFK", "869.525", ""}, {"JFK", "", ""},
	} {
		if got := c.observerFrequencyDecision("broker", []string{"meshcore", tc.region, "same-observer", "packets"}, map[string]interface{}{"radio": tc.radio}, now); got != tc.want {
			t.Fatalf("%s/%s: got %q want %q", tc.region, tc.radio, got, tc.want)
		}
	}
	delete(c.ObserverFrequencyRules, "LHR")
	if got := c.observerFrequencyDecision("broker", []string{"meshcore", "LHR", "same-observer"}, nil, now); got != "" {
		t.Fatal("removed region should no longer filter", got)
	}
}
