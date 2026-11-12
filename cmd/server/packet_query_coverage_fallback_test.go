package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// A recently reheard transmission can have an old FirstSeen, leaving
// oldestLoaded earlier than the complete RAM window. Packet lists must
// use the same coverage boundary as analytics before choosing RAM or SQL.
func TestPacketQueryFallbackUsesCompleteCoverage(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		name := "packets"
		if grouped {
			name = "grouped"
		}
		for _, edge := range []string{"retention", "eviction", "startup-cap", "eviction-equal", "startup-cap-equal"} {
			t.Run(name+"/"+edge, func(t *testing.T) {
				db, err := OpenDB(createTestDBWithAgedPackets(t, 10, 50))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 2})
				if err := store.Load(); err != nil {
					t.Fatal(err)
				}
				if len(store.packets) != 10 {
					t.Fatalf("loaded %d packets, want 10", len(store.packets))
				}
				now := time.Now().UTC()
				store.oldestLoaded = now.Add(-72 * time.Hour).Format(time.RFC3339)
				if edge != "retention" {
					store.retentionHours = 0
				}
				if edge == "eviction" {
					store.evictedThrough = now.Add(-2 * time.Hour).Format(time.RFC3339)
				}
				if edge == "startup-cap" {
					store.loadCapEdge = now.Add(-2 * time.Hour).Format(time.RFC3339)
				}
				if edge == "eviction-equal" {
					store.evictedThrough = now.Add(-49 * time.Hour).Format(time.RFC3339)
				}
				if edge == "startup-cap-equal" {
					store.loadCapEdge = now.Add(-49 * time.Hour).Format(time.RFC3339)
				}
				query := store.QueryPackets
				if grouped {
					query = store.QueryGroupedPackets
				}
				for _, tc := range []struct {
					name, since, until string
					total              int
				}{
					{"since", now.Add(-49 * time.Hour).Format(time.RFC3339), "", 60},
					{"until", "", now.Add(-47 * time.Hour).Format(time.RFC3339), 50},
					{"until-with-recent-end", "", now.Add(-10 * time.Minute).Format(time.RFC3339), 60},
					{"bounded-old", now.Add(-49 * time.Hour).Format(time.RFC3339), now.Add(-47 * time.Hour).Format(time.RFC3339), 50},
				} {
					t.Run(tc.name, func(t *testing.T) {
						result := query(PacketQuery{Since: tc.since, Until: tc.until, Limit: 100, Order: "ASC"})
						if result.Total != tc.total || len(result.Packets) != tc.total {
							t.Fatalf("history query total=%d rows=%d, want %d from SQLite", result.Total, len(result.Packets), tc.total)
						}
					})
				}
				before := atomic.LoadInt64(&store.queryCount)
				recent := query(PacketQuery{Since: now.Add(-45 * time.Minute).Format(time.RFC3339), Limit: 100})
				if recent.Total != 10 || atomic.LoadInt64(&store.queryCount) != before+1 {
					t.Fatalf("recent query must stay in RAM: total=%d queries=%d", recent.Total, atomic.LoadInt64(&store.queryCount)-before)
				}
				before = atomic.LoadInt64(&store.queryCount)
				unbounded := query(PacketQuery{Limit: 100})
				if unbounded.Total != 10 || atomic.LoadInt64(&store.queryCount) != before+1 {
					t.Fatalf("default live list must stay in RAM: total=%d queries=%d", unbounded.Total, atomic.LoadInt64(&store.queryCount)-before)
				}
			})
		}
	}
}
