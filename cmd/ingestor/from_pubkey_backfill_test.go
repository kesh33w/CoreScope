package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func advertBackfillStore(t testing.TB) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE transmissions(id INTEGER PRIMARY KEY,payload_type INTEGER,from_pubkey TEXT,decoded_json TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db}
}

func TestAdvertBackfillBoundsNonAdvertScan(t *testing.T) {
	s := advertBackfillStore(t)
	for i := 1; i <= 600; i++ {
		if _, err := s.db.Exec(`INSERT INTO transmissions VALUES(?,5,NULL,'{}')`, i); err != nil {
			t.Fatal(err)
		}
	}
	batch, last, err := s.readAdvertBackfillBatch(0, 600, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 || last != 256 {
		t.Fatalf("non-advert scan escaped bound: batch=%d last=%d", len(batch), last)
	}
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+advertBackfillBatchSQL, 0, 600, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d
	}
	if !strings.Contains(plan, "INTEGER PRIMARY KEY") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("unbounded plan: %s", plan)
	}
}

func TestAdvertBackfillPreservesDataAndYieldsWriter(t *testing.T) {
	s := advertBackfillStore(t)
	for i := 1; i <= 520; i++ {
		_, err := s.db.Exec(`INSERT INTO transmissions VALUES(?,4,NULL,?)`, i, fmt.Sprintf(`{"pubKey":"%064x"}`, i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO transmissions VALUES(521,5,NULL,'{}'),(522,4,'existing','{}'),(523,4,NULL,'bad json'),(524,4,NULL,NULL),(90000000,4,NULL,'{}')`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.BackfillFromPubkey(5000, time.Millisecond, func(total, processed int64, done bool) {
		calls++
		// This synchronous callback proves the only DB connection is released
		// at a batch boundary, rather than held across the entire history.
		if _, err := s.db.Exec(`UPDATE transmissions SET decoded_json=decoded_json WHERE id=521`); err != nil {
			t.Fatal(err)
		}
		if total < processed {
			t.Fatalf("progress total=%d processed=%d", total, processed)
		}
	})
	if calls < 3 {
		t.Fatalf("expected bounded progress, got %d", calls)
	}
	var missing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM transmissions WHERE payload_type=4 AND from_pubkey IS NULL`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("missed %d", missing)
	}
	var got string
	if err := s.db.QueryRow(`SELECT from_pubkey FROM transmissions WHERE id=522`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "existing" {
		t.Fatal(got)
	}
	if err := s.db.QueryRow(`SELECT from_pubkey FROM transmissions WHERE id=523`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("missing sentinel: %q", got)
	}
	var nonAdvert sql.NullString
	if err := s.db.QueryRow(`SELECT from_pubkey FROM transmissions WHERE id=521`).Scan(&nonAdvert); err != nil {
		t.Fatal(err)
	}
	if nonAdvert.Valid {
		t.Fatal("changed non-advert")
	}
	s.BackfillFromPubkey(10, 0, nil)
}

func TestAdvertBackfillEmptyAndSnapshot(t *testing.T) {
	s := advertBackfillStore(t)
	done := false
	s.BackfillFromPubkey(0, 0, func(total, processed int64, finished bool) {
		done = finished
		if total != 0 || processed != 0 {
			t.Fatal("empty progress")
		}
	})
	if !done {
		t.Fatal("empty scan not completed")
	}
	if _, err := s.db.Exec(`INSERT INTO transmissions VALUES(1,4,NULL,?), (2,4,NULL,'{}')`, fmt.Sprintf(`{"pubKey":"%064x"}`, 1)); err != nil {
		t.Fatal(err)
	}
	inserted := false
	s.BackfillFromPubkey(1, 0, func(total, processed int64, finished bool) {
		if !inserted {
			inserted = true
			if _, err := s.db.Exec(`INSERT INTO transmissions VALUES(3,4,NULL,'{}')`); err != nil {
				t.Fatal(err)
			}
		}
	})
	var got string
	if err := s.db.QueryRow(`SELECT from_pubkey FROM transmissions WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != fmt.Sprintf("%064x", 1) {
		t.Fatalf("lost extracted key: %q", got)
	}
	var later sql.NullString
	if err := s.db.QueryRow(`SELECT from_pubkey FROM transmissions WHERE id=3`).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if later.Valid {
		t.Fatal("scan exceeded startup snapshot")
	}
}

func BenchmarkAdvertBackfillNoMatches100K(b *testing.B) {
	s := advertBackfillStore(b)
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000) INSERT INTO transmissions SELECT x,5,NULL,'{}' FROM n`); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.BackfillFromPubkey(256, 0, nil)
	}
}
