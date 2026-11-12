package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComparisonWindowValidation(t *testing.T) {
	s := &Server{}
	for _, q := range []string{"", "?a=a&b=b&since=bad&until=bad", "?a=a&b=a&since=2026-01-01T00:00:00Z&until=2026-01-01T01:00:00Z", "?a=a&b=b&since=2026-01-01T00:00:00Z&until=2026-01-03T00:00:00Z"} {
		w := httptest.NewRecorder()
		s.handleObserverComparison(w, httptest.NewRequest("GET", "/api/observer-comparison"+q, nil))
		if w.Code != 400 {
			t.Fatalf("%s: %d", q, w.Code)
		}
	}
}

func TestComparisonUsesObservationBytesAndHalfOpenWindow(t *testing.T) {
	db := setupTestDB(t)
	db.hasObsRawHex = true
	db.conn.Exec("INSERT INTO observers(id,name) VALUES ('test-a','Test A')")
	_, _ = seedDistinctFrames(t, db, "comparisonhash")
	var id string
	if err := db.conn.QueryRow("SELECT id FROM observers WHERE rowid=1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	// Three in-window receptions: keep distinct paths, exclude end boundary.
	_, err := db.conn.Exec("UPDATE observations SET timestamp=?", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.conn.Exec("UPDATE observations SET timestamp=? WHERE id=(SELECT max(id) FROM observations)", now+1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.queryObserverComparison(context.Background(), []string{id, "absent"}, time.Unix(now, 0), time.Unix(now+1, 0), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Packets) != 3 {
		t.Fatalf("want 3 actual receptions, got %d", len(r.Packets))
	}
	if r.Packets[0].RawHex == "CANON0000" {
		t.Fatal("canonical bytes must not replace reception bytes")
	}
	b, _ := json.Marshal(r)
	if !json.Valid(b) {
		t.Fatal("bad response")
	}
}

func TestComparisonScanCapAndMissingRaw(t *testing.T) {
	db := setupTestDB(t)
	db.hasObsRawHex = true
	db.conn.Exec("INSERT INTO observers(id,name) VALUES ('test-a','Test A')")
	_, _ = seedDistinctFrames(t, db, "comparisoncap")
	var id string
	_ = db.conn.QueryRow("SELECT id FROM observers WHERE rowid=1").Scan(&id)
	r, err := db.queryObserverComparison(context.Background(), []string{id, "absent"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || r.Scanned != 2 || len(r.Packets) > 2 {
		t.Fatalf("scan must be capped: %+v", r)
	}
	db.hasObsRawHex = false
	r, err = db.queryObserverComparison(context.Background(), []string{id, "absent"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Packets {
		if p.RawHex != "" {
			t.Fatal("cannot infer missing observation raw bytes")
		}
	}
}

func TestComparisonRejectsLegacyTimestamps(t *testing.T) {
	db := setupTestDBV2(t)
	_, err := db.queryObserverComparison(context.Background(), []string{"a", "b"}, time.Now().Add(-time.Hour), time.Now(), 100, 100)
	if err == nil {
		t.Fatal("legacy date text must not be compared as numeric timestamps")
	}
}

func TestComparisonExcludesTXAndCapsObserver(t *testing.T) {
	db := setupTestDB(t)
	db.hasObsRawHex = true
	db.conn.Exec("INSERT INTO observers(id,name) VALUES ('a','A')")
	_, ids := seedDistinctFrames(t, db, "cap-test")
	db.conn.Exec("UPDATE observations SET direction='tx' WHERE id=?", ids[0])
	r, err := db.queryObserverComparison(context.Background(), []string{"a", "b"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || len(r.Packets) != 2 {
		t.Fatalf("bad capped result: %+v", r)
	}
	for _, p := range r.Packets {
		if p.ID == int64(ids[0]) {
			t.Fatal("TX is not an RX observation")
		}
	}
}

func TestComparisonHidesBlacklistedObservers(t *testing.T) {
	s := &Server{cfg: &Config{ObserverBlacklist: []string{"a"}}}
	w := httptest.NewRecorder()
	s.handleObserverComparison(w, httptest.NewRequest("GET", "/api/observer-comparison?a=a&b=b&since=2026-01-01T00:00:00Z&until=2026-01-01T01:00:00Z", nil))
	if w.Code != 404 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestComparisonRouteComesFromReception(t *testing.T) {
	db := setupTestDB(t)
	db.hasObsRawHex = true
	db.conn.Exec("INSERT INTO observers(id,name) VALUES ('a','A')")
	_, ids := seedDistinctFrames(t, db, "route-test")
	db.conn.Exec("UPDATE observations SET raw_hex='12aabb' WHERE id=?", ids[0]) // direct, while canonical transmission is flood
	db.conn.Exec("UPDATE observations SET raw_hex=NULL WHERE id=?", ids[1])
	r, err := db.queryObserverComparison(context.Background(), []string{"a", "b"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Packets {
		if p.ID == int64(ids[0]) && p.RouteType != 2 {
			t.Fatal("used canonical route")
		}
		if p.ID == int64(ids[1]) && p.RouteType != -1 {
			t.Fatal("invented missing route")
		}
	}
}
