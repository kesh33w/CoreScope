package main

import (
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Hop aliases are path-hop identities that never advertise, so CoreScope
// has no name for them. HydraHop replaces its own path hop with a per-antenna
// alias (which antenna heard the packet and which one sent it) and lists those
// aliases, with labels, in its MQTT status message as
//
//	"hop_aliases": [{"key": "<64 hex>", "label": "BD6E g3910→rak910"}, ...]
//
// For an observer trusted by config (Config.HopAliasOwner), each alias becomes
// a nodes row with role "repeater" and no location, so every existing name
// lookup (path resolution, /api/nodes, the browser hop resolver) shows the
// label with no server or frontend change. The hop_aliases table marks those
// rows as aliases: a later status that drops an alias removes its row, and a
// key that already belongs to a real node is never taken over.

const (
	maxHopAliases      = 64
	maxHopAliasLabelRn = 48
)

type hopAlias struct {
	Key   string
	Label string
}

// parseHopAliases validates the status field. ok is false when the field is
// not a list at all; invalid entries are skipped individually.
func parseHopAliases(v interface{}) (aliases []hopAlias, ok bool) {
	items, isList := v.([]interface{})
	if !isList {
		return nil, false
	}
	seen := map[string]bool{}
	for _, item := range items {
		if len(aliases) >= maxHopAliases {
			break
		}
		m, isMap := item.(map[string]interface{})
		if !isMap {
			continue
		}
		key, _ := m["key"].(string)
		key = strings.ToLower(strings.TrimSpace(key))
		if b, err := hex.DecodeString(key); err != nil || len(b) != 32 || seen[key] {
			continue
		}
		label := cleanHopAliasLabel(m["label"])
		if label == "" {
			continue
		}
		seen[key] = true
		aliases = append(aliases, hopAlias{Key: key, Label: label})
	}
	return aliases, true
}

func cleanHopAliasLabel(v interface{}) string {
	s, _ := v.(string)
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n >= maxHopAliasLabelRn {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ReplaceHopAliases makes owner's aliases exactly the given set, in one
// transaction. Returns how many aliases are now named.
func (s *Store) ReplaceHopAliases(owner string, aliases []hopAlias, now string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	keep := map[string]bool{}
	for _, a := range aliases {
		var current string
		err := tx.QueryRow(`SELECT owner FROM hop_aliases WHERE alias_key = ?`, a.Key).Scan(&current)
		switch {
		case err == nil && current != owner:
			continue // claimed by another trusted observer
		case err != nil:
			// Not an alias yet: never take over a node that exists already.
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key = ?`, a.Key).Scan(&n); err != nil {
				return 0, err
			}
			if n > 0 {
				continue
			}
		}
		if _, err := tx.Exec(`INSERT INTO hop_aliases (alias_key, owner, label, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(alias_key) DO UPDATE SET label = excluded.label, updated_at = excluded.updated_at`,
			a.Key, owner, a.Label, now); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count) VALUES (?, ?, 'repeater', ?, ?, 0)
			ON CONFLICT(public_key) DO UPDATE SET name = excluded.name, role = 'repeater', last_seen = excluded.last_seen`,
			a.Key, a.Label, now, now); err != nil {
			return 0, err
		}
		keep[a.Key] = true
	}

	rows, err := tx.Query(`SELECT alias_key FROM hop_aliases WHERE owner = ?`, owner)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil && !keep[k] {
			stale = append(stale, k)
		}
	}
	rows.Close()
	for _, k := range stale {
		if _, err := tx.Exec(`DELETE FROM nodes WHERE public_key = ?`, k); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM hop_aliases WHERE alias_key = ?`, k); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		s.Stats.WriteErrors.Add(1)
		return 0, err
	}
	return len(keep), nil
}
