package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// A concurrent analyzer on its own read-only connection must see a consistent
// WAL snapshot, never half-archived/half-deleted data or broken shared joins.
func TestObservationCleanupIndependentReaderSnapshot(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 1100)
	reader, err := sql.Open("sqlite3", "file:"+s.path+"?mode=ro&_busy_timeout=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	snapshot, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()
	var before int
	if err = snapshot.QueryRow(`SELECT count(*) FROM observations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1101 {
		t.Fatalf("before %d", before)
	}
	result, err := s.processObservationCleanup(cleanupReq(1100), func() {
		var count, dangling int
		if e := snapshot.QueryRow(`SELECT count(*) FROM observations`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if e := snapshot.QueryRow(`SELECT count(*) FROM observations o LEFT JOIN transmissions t ON t.id=o.transmission_id WHERE t.id IS NULL`).Scan(&dangling); e != nil {
			t.Fatal(e)
		}
		if count != before || dangling != 0 {
			t.Fatalf("inconsistent reader count=%d dangling=%d", count, dangling)
		}
	})
	if err != nil || result.Deleted != 1100 {
		t.Fatalf("cleanup %+v %v", result, err)
	}
	if err = snapshot.Commit(); err != nil {
		t.Fatal(err)
	}
	var after int
	if err = reader.QueryRow(`SELECT count(*) FROM observations`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("new reader snapshot count %d", after)
	}
	rows, err := reader.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// With batch commits, every cleanup chunk and the finished job must be
// committed when processObservationCleanup returns, not one interval later.
func TestObservationCleanupCommitsUnderBatchCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup-batch.db")
	s, err := OpenStoreWithBatchCommit(path, 300, "NORMAL", maxBatchCommit, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cleanupSeed(t, s, 1100)
	result, err := s.processObservationCleanup(cleanupReq(1100), func() {})
	if err != nil || result.Deleted != 1100 {
		t.Fatalf("cleanup %+v %v", result, err)
	}
	reader, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var left int
	var status string
	if err = reader.QueryRow(`SELECT count(*) FROM observations`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err = reader.QueryRow(`SELECT status FROM observation_cleanup_jobs WHERE id=?`, "abc123").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if left != 1 || status != "done" {
		t.Fatalf("independent reader sees %d observations, job %q", left, status)
	}
}
