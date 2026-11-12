package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScopedObserverPublicKeyProjectionBoundaries(t *testing.T) {
	key := strings.Repeat("AB", 32)
	for _, id := range []string{"legacy-name", key, key + "@bad@radio@SMF", key + "@@SMF", strings.Repeat("Z", 64) + "@bay@SFO"} {
		if got := observerPublicKey(id); got != id {
			t.Errorf("unscoped/malformed ID %q changed to %q", id, got)
		}
	}
	if got := observerPublicKey(key + "@bay@SFO"); got != key {
		t.Errorf("actual public key = %q", got)
	}
	cfg := &Config{ObserverBlacklist: []string{strings.ToLower(key)}}
	if !cfg.IsObserverBlacklisted(key + "@bay@SFO") {
		t.Error("existing public-key blacklist must still apply to its feeds")
	}
}

func TestScopedObserverAPIKeepsSeparateRecordsAndNodeLocations(t *testing.T) {
	db := setupTestDB(t)
	key := strings.Repeat("AB", 32)
	if _, err := db.conn.Exec(`INSERT INTO nodes(public_key,name,role,lat,lon) VALUES(?,'Bridge','repeater',38.5,-121.5)`, strings.ToLower(key)); err != nil {
		t.Fatal(err)
	}
	for _, feed := range []struct{ radio, region string }{{"host-rf", "SMF"}, {"bay", "SFO"}} {
		if _, err := db.conn.Exec(`INSERT INTO observers(id,name,iata) VALUES(?,'Bridge',?)`, key+"@"+feed.radio+"@"+feed.region, feed.region); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{db: db, cfg: &Config{}}
	resp, err := srv.buildObserversDefaultResponse()
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Observers) != 2 {
		t.Fatalf("API feed rows=%d", len(resp.Observers))
	}
	for _, row := range resp.Observers {
		if !strings.Contains(row.ID, "@") || row.Lat == nil || row.Lon == nil || row.NodeRole != "repeater" {
			t.Errorf("feed lost its separate ID or real-node location: %+v", row)
		}
	}
	// Reach queries project only the node endpoint, never the observer filter.
	rows, err := srv.readReachRows(context.Background(), `SELECT ?, 'cd', 4, '["ab"]', NULL`, []interface{}{key + "@bay@SFO"})
	if err != nil || len(rows) != 1 || rows[0].observerPK != key {
		t.Fatalf("reach rows=%+v, err=%v", rows, err)
	}
	store := ngTestStore(nil, nil)
	store.db = db
	srv.store = store
	nodes := srv.buildNodeInfoMap()
	if _, ok := nodes[strings.ToLower(key)]; !ok {
		t.Errorf("observer-only real key not enriched: %v", nodes)
	}
	for k := range nodes {
		if strings.Contains(k, "@") {
			t.Errorf("feed ID used as node key: %q", k)
		}
	}
	store.nodeCache = []nodeInfo{{PublicKey: strings.ToLower(key), Role: "repeater"}}
	store.packets = []*StoreTx{{Observations: []*StoreObs{{ObserverID: key + "@host-rf@SMF"}}}}
	collisions := store.computeHashCollisions("SMF", "")
	stats := collisions["by_size"].(map[string]interface{})["1"].(map[string]interface{})["stats"].(map[string]interface{})
	if stats["total_nodes"] != 1 {
		t.Errorf("regional node projection lost the real bridge node: %v", stats)
	}
}

func TestScopedObserverNodeBatteryAndRelayMetadata(t *testing.T) {
	db := setupTestDB(t)
	key := strings.Repeat("AB", 32)
	ids := []string{key, key + "@host-rf@SMF", key + "@bay@SFO"}
	for _, ddl := range []string{`ALTER TABLE observers ADD COLUMN can_relay INTEGER DEFAULT 1`, `ALTER TABLE observers ADD COLUMN can_relay_seen INTEGER DEFAULT 0`} {
		if _, err := db.conn.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for i, id := range ids {
		if _, err := db.conn.Exec(`INSERT INTO observers(id,name,iata,can_relay,can_relay_seen) VALUES(?,?,'SMF',0,1)`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.conn.Exec(`INSERT INTO observer_metrics(observer_id,timestamp,battery_mv) VALUES(?,?,?)`, id, now.Add(time.Duration(i)*time.Second).Format(time.RFC3339), 3700+i); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := db.GetNodeBatteryHistory(strings.ToLower(key), now.Add(-time.Hour).Format(time.RFC3339))
	if err != nil || len(samples) != 3 {
		t.Fatalf("battery samples=%+v, err=%v, want history plus both feeds", samples, err)
	}
	for _, get := range []func() ([]string, error){db.GetNonRelayObserverPubkeys, db.GetCanRelaySeenObserverPubkeys} {
		keys, err := get()
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range keys {
			if got != strings.ToLower(key) {
				t.Errorf("node metadata returned non-public-key %q", got)
			}
		}
	}
	rows := buildDirectObserverRows(map[string]*directHeardAgg{ids[1]: {Count: 1}, ids[2]: {Count: 1}}, map[string]struct{}{strings.ToLower(key): {}}, map[string]struct{}{strings.ToLower(key): {}})
	if len(rows) != 2 {
		t.Fatalf("direct feed rows=%d", len(rows))
	}
	for _, row := range rows {
		if row.CanRelay == nil || *row.CanRelay {
			t.Errorf("scoped listener metadata missing: %+v", row)
		}
	}
}

func TestScopedObserverHopContextAndGraphUseRealKey(t *testing.T) {
	key := strings.Repeat("AB", 32)
	id := key + "@host-rf@SMF"
	tx, pm := hopContextFixture()
	tx.ObserverID = id
	for _, keys := range [][]string{buildHopContextPubkeys(tx, pm), buildAggregateHopContextPubkeys([]*StoreTx{tx}, pm)} {
		found := false
		for _, k := range keys {
			if k == strings.ToLower(key) {
				found = true
			}
			if strings.Contains(k, "@") {
				t.Errorf("scoped ID used as public key: %s", k)
			}
		}
		if !found {
			t.Errorf("actual observer public key absent from hop context: %v", keys)
		}
	}
	other := strings.Repeat("CD", 32)
	graphTx := ngMakeTx(1, PayloadADVERT, ngFromNodeJSON(strings.ToLower(other)), []*StoreObs{ngMakeObs(id, "[]", nowStr, ngFloatPtr(-10))})
	g := BuildFromStore(ngTestStore([]nodeInfo{{PublicKey: strings.ToLower(key), Role: "repeater"}, {PublicKey: strings.ToLower(other), Role: "repeater"}}, []*StoreTx{graphTx}))
	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected one real-node graph edge, got %+v", edges)
	}
	if strings.Contains(edges[0].NodeA, "@") || strings.Contains(edges[0].NodeB, "@") {
		t.Fatalf("feed ID exposed as graph public key: %+v", edges[0])
	}
}
