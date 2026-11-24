package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// CORESCOPE_TEST_BATCH_COMMIT_MS=200 go test ./... runs the whole suite with
// grouped commits.
func TestMain(m *testing.M) {
	if ms, err := strconv.Atoi(os.Getenv("CORESCOPE_TEST_BATCH_COMMIT_MS")); err == nil && ms > 0 {
		testBatchCommit = time.Duration(ms) * time.Millisecond
	}
	os.Exit(m.Run())
}

func openBatchTestStore(t *testing.T, interval time.Duration) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "batch.db")
	s, err := OpenStoreWithBatchCommit(path, 300, "NORMAL", interval, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TABLE batch_probe (v INTEGER)"); err != nil {
		t.Fatal(err)
	}
	return s, path
}

// committed reads through a separate connection, as the server does.
func committed(t *testing.T, path string) int {
	t.Helper()
	other, err := sql.Open("sqlite3", path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var n int
	if err := other.QueryRow("SELECT count(*) FROM batch_probe").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBatchCommitGroupsWritesUntilInterval(t *testing.T) {
	s, path := openBatchTestStore(t, 300*time.Millisecond)
	defer s.Close()
	for i := 0; i < 5; i++ {
		if _, err := s.db.Exec("INSERT INTO batch_probe VALUES (?)", i); err != nil {
			t.Fatal(err)
		}
	}
	var own int
	if err := s.db.QueryRow("SELECT count(*) FROM batch_probe").Scan(&own); err != nil || own != 5 {
		t.Fatalf("writer must see its own batch: %d %v", own, err)
	}
	if n := committed(t, path); n != 0 {
		t.Fatalf("batch committed early: %d rows visible", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for committed(t, path) != 5 {
		if time.Now().After(deadline) {
			t.Fatal("idle batch was never committed by the flusher")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBatchCommitTxRollbackKeepsEarlierWrites(t *testing.T) {
	s, path := openBatchTestStore(t, maxBatchCommit)
	if _, err := s.db.Exec("INSERT INTO batch_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO batch_probe VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO batch_probe VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != sql.ErrTxDone {
		t.Fatalf("second commit: %v", err)
	}
	if err := s.Close(); err != nil { // Close commits the open batch
		t.Fatal(err)
	}
	if n := committed(t, path); n != 2 {
		t.Fatalf("want rows 1 and 3 committed, got %d", n)
	}
}

func TestBatchCommitPragmaAndCancellableWritesCommitFirst(t *testing.T) {
	s, path := openBatchTestStore(t, maxBatchCommit)
	defer s.Close()
	if _, err := s.db.Exec("INSERT INTO batch_probe VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	s.Checkpoint()
	if n := committed(t, path); n != 1 {
		t.Fatalf("PRAGMA must commit the batch first, got %d", n)
	}
	if _, err := s.db.Exec("INSERT INTO batch_probe VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "INSERT INTO batch_probe VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if n := committed(t, path); n != 3 {
		t.Fatalf("cancellable write must not share the batch, got %d", n)
	}
}

func TestBatchCommitRejectsBadInterval(t *testing.T) {
	for _, d := range []time.Duration{-time.Millisecond, 5 * time.Second} {
		if _, err := openBatchedDB("x.db", d, 0); err == nil {
			t.Fatalf("interval %s accepted", d)
		}
	}
}

func TestBatchCommitClassify(t *testing.T) {
	cases := map[string]stmtKind{
		"SELECT 1":                           kindRead,
		"  -- c\n select 1":                  kindRead,
		"/* x */ INSERT INTO t VALUES (1)":   kindWrite,
		"update t set v=1":                   kindWrite,
		"WITH x AS (SELECT 1) DELETE FROM t": kindWrite,
		"PRAGMA wal_checkpoint(TRUNCATE)":    kindControl,
		"VACUUM":                             kindControl,
		"CREATE INDEX i ON t(v)":             kindControl,
		"BEGIN":                              kindControl,
	}
	for q, want := range cases {
		if got := classify(q); got != want {
			t.Errorf("classify(%q) = %d, want %d", q, got, want)
		}
	}
}

// A transaction the caller opens with a plain BEGIN statement on a pinned
// connection (dbschema's observers rebuild does this) runs as-is: its writes
// do not open the shared batch, and its own ROLLBACK or COMMIT ends it.
func TestBatchCommitCallerBeginStatementRunsAsIs(t *testing.T) {
	s, path := openBatchTestStore(t, maxBatchCommit)
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.Exec("INSERT INTO batch_probe VALUES (1)"); err != nil {
		t.Fatal(err) // leaves the shared batch open
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"ROLLBACK", "COMMIT"} {
		for _, q := range []string{"BEGIN IMMEDIATE", "INSERT INTO batch_probe VALUES (2)", end} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	conn.Close()
	// BEGIN committed the open batch first; ROLLBACK dropped only its own row.
	if n := committed(t, path); n != 2 {
		t.Fatalf("committed rows = %d, want 2", n)
	}
}

// A pre-v3.19 observers table (implicit rowid only) is rebuilt when the
// ingestor opens with grouped commits, as the WAN aggregate does.
func TestBatchCommitOpenRebuildsLegacyObservers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE observers (id TEXT PRIMARY KEY, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT, packet_count INTEGER DEFAULT 0);
		INSERT INTO observers (id, name) VALUES ('a', 'A'), ('b', 'B'); DELETE FROM observers WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	s, err := OpenStoreWithBatchCommit(path, 300, "NORMAL", time.Second, 0)
	if err != nil {
		t.Fatalf("open legacy DB with grouped commits: %v", err)
	}
	defer s.Close()
	var idx int64
	if err := s.db.QueryRow("SELECT idx FROM observers WHERE id = 'b'").Scan(&idx); err != nil || idx != 2 {
		t.Fatalf("observer b idx = %d, %v; want its old rowid 2", idx, err)
	}
}
