package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	trustedObs = "33b0bd6e33d44bf962f8321f362054a9a7bdc24bd7dae27ccf6e34e93acbe49a"
	aliasA     = "6fba000000000000000000000000000000000000000000000000000000000001"
	aliasB     = "4a40000000000000000000000000000000000000000000000000000000000002"
)

func hopAliasConfig(sources ...string) *Config {
	return &Config{HopAliases: &HopAliasesConfig{Observers: []string{strings.ToUpper(trustedObs)}, Sources: sources}}
}

func statusWithAliases(t *testing.T, observerID string, aliases interface{}, retained bool) *mockMessage {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{"origin": "BD6E-obs-910-sfo", "hop_aliases": aliases})
	if err != nil {
		t.Fatal(err)
	}
	return &mockMessage{topic: "meshcore/SFO/" + observerID + "/status", payload: body, retained: retained}
}

func nodeName(t *testing.T, s *Store, key string) (string, bool) {
	t.Helper()
	var name, role string
	err := s.db.QueryRow(`SELECT name, role FROM nodes WHERE public_key = ?`, key).Scan(&name, &role)
	if err != nil {
		return "", false
	}
	if role != "repeater" {
		t.Errorf("alias %s role = %q, want repeater", key[:4], role)
	}
	return name, true
}

func TestHopAliasesFromTrustedScopedObserver(t *testing.T) {
	s := newTestStore(t)
	cfg := hopAliasConfig("SACMesh")
	scoped := strings.ToUpper(trustedObs) + "@g3910@SFO"
	aliases := []map[string]string{
		{"key": strings.ToUpper(aliasA), "label": "BD6E g3910→g3910"},
		{"key": aliasB, "label": "BD6E g3910→rak910\x07"},
		{"key": "zz", "label": "bad key"},
		{"key": aliasA, "label": "duplicate"},
	}
	handleMessage(s, "SACMesh", MQTTSource{Name: "SACMesh"}, statusWithAliases(t, scoped, aliases, false), nil, nil, cfg)

	if got, ok := nodeName(t, s, aliasA); !ok || got != "BD6E g3910→g3910" {
		t.Fatalf("alias A name = %q (%v)", got, ok)
	}
	if got, _ := nodeName(t, s, aliasB); got != "BD6E g3910→rak910" {
		t.Fatalf("alias B label not cleaned: %q", got)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM hop_aliases WHERE owner = ?`, trustedObs).Scan(&n)
	if n != 2 {
		t.Fatalf("hop_aliases rows = %d, want 2", n)
	}

	// A later status that drops alias B removes it; renaming A updates it.
	handleMessage(s, "SACMesh", MQTTSource{Name: "SACMesh"},
		statusWithAliases(t, scoped, []map[string]string{{"key": aliasA, "label": "BD6E renamed"}}, false), nil, nil, cfg)
	if _, ok := nodeName(t, s, aliasB); ok {
		t.Fatal("stale alias B still in nodes")
	}
	if got, _ := nodeName(t, s, aliasA); got != "BD6E renamed" {
		t.Fatalf("alias A not relabelled: %q", got)
	}
}

func TestHopAliasesIgnoredWhenUntrusted(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		source   string
		observer string
		retained bool
	}{
		{"feature off", &Config{}, "SACMesh", trustedObs, false},
		{"other observer", hopAliasConfig(), "SACMesh", strings.Repeat("ab", 32), false},
		{"other source", hopAliasConfig("SACMesh"), "gomesh", trustedObs, false},
		{"retained replay", hopAliasConfig(), "SACMesh", trustedObs, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			msg := statusWithAliases(t, tc.observer, []map[string]string{{"key": aliasA, "label": "x"}}, tc.retained)
			handleMessage(s, tc.source, MQTTSource{Name: tc.source}, msg, nil, nil, tc.cfg)
			if _, ok := nodeName(t, s, aliasA); ok {
				t.Fatal("alias written from an untrusted status")
			}
		})
	}
}

func TestHopAliasesNeverTakeOverRealNode(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertNode(aliasA, "Real Repeater", "repeater", nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReplaceHopAliases(trustedObs, []hopAlias{{Key: aliasA, Label: "spoof"}, {Key: aliasB, Label: "ok"}}, "2026-11-19T19:00:00Z")
	if err != nil || n != 1 {
		t.Fatalf("ReplaceHopAliases = %d, %v; want 1 named", n, err)
	}
	if got, _ := nodeName(t, s, aliasA); got != "Real Repeater" {
		t.Fatalf("real node renamed to %q", got)
	}
	// Dropping every alias must not delete the real node either.
	if _, err := s.ReplaceHopAliases(trustedObs, nil, "2026-11-19T19:05:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, ok := nodeName(t, s, aliasA); !ok {
		t.Fatal("real node deleted")
	}
	if _, ok := nodeName(t, s, aliasB); ok {
		t.Fatal("alias B not removed")
	}
}

func TestHopAliasesOneOwnerPerKey(t *testing.T) {
	s := newTestStore(t)
	other := strings.Repeat("cd", 32)
	if _, err := s.ReplaceHopAliases(trustedObs, []hopAlias{{Key: aliasA, Label: "first"}}, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceHopAliases(other, []hopAlias{{Key: aliasA, Label: "second"}}, "t2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := nodeName(t, s, aliasA); got != "first" {
		t.Fatalf("second owner relabelled alias: %q", got)
	}
}

func TestParseHopAliasesLimits(t *testing.T) {
	if _, ok := parseHopAliases("nope"); ok {
		t.Fatal("non-list accepted")
	}
	var items []interface{}
	for i := 0; i < maxHopAliases+5; i++ {
		key := strings.Repeat("0", 62) + string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16])
		items = append(items, map[string]interface{}{"key": "1" + key[1:], "label": strings.Repeat("x", 100)})
	}
	got, ok := parseHopAliases(items)
	if !ok || len(got) != maxHopAliases {
		t.Fatalf("parsed %d aliases, want %d", len(got), maxHopAliases)
	}
	if n := len([]rune(got[0].Label)); n != maxHopAliasLabelRn {
		t.Fatalf("label runes = %d", n)
	}
}
