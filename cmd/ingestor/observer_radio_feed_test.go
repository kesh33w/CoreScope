package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The topic remains the actual authenticated public key. An explicit radio
// selector distinguishes observations and status, without rewriting old rows.
func TestHandleMessageObserverRadioFeeds(t *testing.T) {
	s := newTestStore(t)
	key := strings.Repeat("AB", 32)
	if err := s.UpsertObserver(key, "Historical bridge", "SMF", nil); err != nil {
		t.Fatal(err)
	}
	var legacyRow int64
	if err := s.db.QueryRow(`SELECT rowid FROM observers WHERE id=?`, key).Scan(&legacyRow); err != nil {
		t.Fatal(err)
	}
	feeds := []struct{ radio, region, profile string }{{"host-rf", "SMF", "909.875,62.5,9,5"}, {"bay", "SFO", "910.525,62.5,7,5"}}
	for _, feed := range feeds {
		for _, retained := range []bool{true, false} {
			status, _ := json.Marshal(map[string]interface{}{"origin": "Bridge", "origin_id": key, "observer_radio_id": feed.radio, "radio": feed.profile, "stats": map[string]interface{}{"battery_mv": 4100}})
			handleMessage(s, "test", MQTTSource{}, &mockMessage{topic: "meshcore/" + feed.region + "/" + key + "/status", payload: status, retained: retained}, nil, nil, &Config{})
		}
		packet, _ := json.Marshal(map[string]interface{}{"origin": "Bridge", "origin_id": key, "observer_radio_id": feed.radio, "raw": "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"})
		handleMessage(s, "test", MQTTSource{}, &mockMessage{topic: "meshcore/" + feed.region + "/" + key + "/packets", payload: packet}, nil, nil, &Config{})
		id := key + "@" + feed.radio + "@" + feed.region
		var region, radio string
		if err := s.db.QueryRow(`SELECT iata,radio FROM observers WHERE id=?`, id).Scan(&region, &radio); err != nil {
			t.Fatalf("separate %s observer missing: %v", feed.region, err)
		}
		if region != feed.region || radio != feed.profile {
			t.Fatalf("%s metadata = %q %q", id, region, radio)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations o JOIN observers b ON b.rowid=o.observer_idx WHERE b.id=?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s receptions = %d, err %v", id, n, err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observer_metrics WHERE observer_id=?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s metrics = %d, err %v", id, n, err)
		}
	}
	var rows, transmissions int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observers`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatalf("observer rows = %d, want two feeds plus preserved history", rows)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&transmissions); err != nil {
		t.Fatal(err)
	}
	if transmissions != 1 {
		t.Fatalf("transmissions = %d, want shared transmission with separate receptions", transmissions)
	}
	var row int64
	var name, region string
	if err := s.db.QueryRow(`SELECT rowid,name,iata FROM observers WHERE id=?`, key).Scan(&row, &name, &region); err != nil {
		t.Fatal(err)
	}
	if row != legacyRow || name != "Historical bridge" || region != "SMF" {
		t.Fatalf("legacy history was changed: %d %q %q", row, name, region)
	}
}

func TestObserverRadioFeedLegacyAndInvalidSelectors(t *testing.T) {
	key := strings.Repeat("AB", 32)
	for _, radio := range []interface{}{nil, "", "bad@radio", "bad/radio", strings.Repeat("x", 33), 42} {
		parts := []string{"meshcore", "SMF", key, "status"}
		if got := observerIDForMessage(parts, map[string]interface{}{"observer_radio_id": radio}); got != key {
			t.Errorf("invalid radio %v changed legacy ID to %q", radio, got)
		}
	}
	if got := observerIDForMessage([]string{"meshcore", "SMF", "legacy-name"}, map[string]interface{}{"observer_radio_id": "bay"}); got != "legacy-name" {
		t.Errorf("legacy named observer changed to %q", got)
	}
}
