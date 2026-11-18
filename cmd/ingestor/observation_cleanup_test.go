package main

import (
	"database/sql"
	"fmt"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/prunequeue"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cleanupTestStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "cleanup.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func cleanupSeed(t *testing.T, s *Store, n int) {
	t.Helper()
	e := s.WriterTx("test", func(tx *sql.Tx) error {
		if _, e := tx.Exec(`INSERT INTO observers(id,name,iata) VALUES('bad','wrong','SMF'),('good','other','SFO')`); e != nil {
			return e
		}
		for i := 1; i <= n; i++ {
			if _, e := tx.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen) VALUES(?, '00',?, '2026-01-01T00:00:00Z',?)`, i, fmt.Sprint(i), 100+i); e != nil {
				return e
			}
			if _, e := tx.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,timestamp,path_json) VALUES(?,?,1,?,'["aa"]')`, i, i, 100+i); e != nil {
				return e
			}
			if _, e := tx.Exec(`INSERT INTO observation_radios(observation_id,radio_id) VALUES(?,'wrong-radio')`, i); e != nil {
				return e
			}
		}
		_, e := tx.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,timestamp,path_json) VALUES(?,1,2,50,'["bb"]')`, n+1)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
func cleanupReq(n int) prunequeue.CleanupRequest {
	return prunequeue.CleanupRequest{ID: "abc123", RequestedAt: time.Now().UTC(), Selection: prunequeue.CleanupSelection{ObserverID: "bad", IATA: "SMF", MaxObservationID: int64(n)}}
}
func cleanupCount(t *testing.T, s *Store, q string) int {
	t.Helper()
	var n int
	if e := s.db.QueryRow(q).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestObservationCleanupBulkPreservesSharedAndRestores(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 2501)
	req := cleanupReq(2501)
	batches := 0
	r, e := s.processObservationCleanup(req, func() { batches++ })
	if e != nil {
		t.Fatal(e)
	}
	if r.Deleted != 2501 || batches != 6 {
		t.Fatalf("result %+v batches %d", r, batches)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 1 {
		t.Fatalf("other observation count %d", n)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM transmissions"); n != 1 {
		t.Fatalf("shared transmission count %d", n)
	}
	if n := cleanupCount(t, s, "SELECT last_seen FROM transmissions WHERE id=1"); n != 50 {
		t.Fatalf("last_seen %d", n)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observation_cleanup_obs"); n != 2501 {
		t.Fatalf("archive %d", n)
	}
	r, e = s.ProcessObservationCleanup(req)
	if e != nil || r.Deleted != 2501 {
		t.Fatalf("idempotent %+v %v", r, e)
	}
	restore := prunequeue.CleanupRequest{ID: "def456", RequestedAt: time.Now().UTC(), RestoreID: req.ID}
	r, e = s.ProcessObservationCleanup(restore)
	if e != nil {
		t.Fatal(e)
	}
	if r.Restored != 2501 {
		t.Fatalf("restore %+v", r)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 2502 {
		t.Fatalf("restored total %d", n)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observation_radios"); n != 2501 {
		t.Fatalf("restored radios %d", n)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations o LEFT JOIN transmissions t ON t.id=o.transmission_id WHERE t.id IS NULL"); n != 0 {
		t.Fatalf("orphans %d", n)
	}
	var ok string
	if e = s.db.QueryRow("PRAGMA integrity_check").Scan(&ok); e != nil || ok != "ok" {
		t.Fatalf("integrity %s %v", ok, e)
	}
}
func TestObservationCleanupArchiveFailureNoDeletion(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 10)
	if e := s.WriterTx("test", func(tx *sql.Tx) error {
		if e := ensureCleanupSchema(tx); e != nil {
			return e
		}
		_, e := tx.Exec(`CREATE TRIGGER fail_archive BEFORE INSERT ON observation_cleanup_obs BEGIN SELECT RAISE(ABORT,'archive failure');END`)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	r, e := s.ProcessObservationCleanup(cleanupReq(10))
	if e == nil || r.Deleted != 0 {
		t.Fatalf("result %+v %v", r, e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 11 {
		t.Fatalf("deleted despite failed archive %d", n)
	}
}
func TestObservationCleanupCutoffAndInterleavedIngestion(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 1001)
	req := cleanupReq(1001)
	req.Selection.To = 2000
	once := false
	r, e := s.processObservationCleanup(req, func() {
		if once {
			return
		}
		once = true
		// Shared reader and writer both run between bounded commits. Fresh observation
		// of an archived hash gets a new transmission ID; restore must map by hash.
		if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 502 {
			t.Fatalf("reader count %d", n)
		}
		e := s.WriterTx("test", func(tx *sql.Tx) error {
			_, e := tx.Exec(`INSERT INTO transmissions(raw_hex,hash,first_seen,last_seen) VALUES('00','2','2026-11-18',3000); INSERT INTO observations(transmission_id,observer_idx,timestamp,path_json) SELECT id,1,3000,'[]' FROM transmissions WHERE hash='2'; UPDATE observations SET timestamp=4000 WHERE id=1000`)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	if r.Deleted != 1000 {
		t.Fatalf("deleted %d", r.Deleted)
	}
	restore := prunequeue.CleanupRequest{ID: "eee444", RequestedAt: time.Now().UTC(), RestoreID: req.ID}
	r, e = s.ProcessObservationCleanup(restore)
	if e != nil {
		t.Fatal(e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations o JOIN transmissions t ON t.id=o.transmission_id WHERE t.hash='2'"); n != 2 {
		t.Fatalf("reingested shared hash rows %d", n)
	}
	if n := cleanupCount(t, s, "SELECT timestamp FROM observations WHERE id=1000"); n != 4000 {
		t.Fatal("fresh updated row overwritten")
	}
}
func TestObservationCleanupFiltersAndQueue(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 20)
	req := cleanupReq(20)
	req.Selection.From = 105
	req.Selection.To = 110
	if e := prunequeue.WriteCleanupRequest(s.path, req); e != nil {
		t.Fatal(e)
	}
	s.RunPendingObservationCleanups()
	r, e := prunequeue.ReadCleanupResult(s.path, req.ID)
	if e != nil || r == nil || r.Deleted != 6 || r.Error != "" {
		t.Fatalf("result %+v %v", r, e)
	}
	req.ID = "123fff"
	req.Selection.IATA = "SFO"
	r2, e := s.ProcessObservationCleanup(req)
	if e != nil || r2.Deleted != 0 {
		t.Fatalf("iata mismatch %+v %v", r2, e)
	}
}

func TestObservationCleanupPartialFailureRecoverableAndObserverRestored(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 1100)
	req := cleanupReq(1100)
	once := false
	r, e := s.processObservationCleanup(req, func() {
		if once {
			return
		}
		once = true
		if _, e := s.db.Exec(`CREATE TRIGGER fail_next_archive BEFORE INSERT ON observation_cleanup_obs BEGIN SELECT RAISE(ABORT,'disk failure simulation');END`); e != nil {
			t.Fatal(e)
		}
	})
	if e == nil || r.Deleted != 500 {
		t.Fatalf("partial %+v %v", r, e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 601 {
		t.Fatalf("partial count %d", n)
	}
	if _, e := s.db.Exec(`DROP TRIGGER fail_next_archive; DELETE FROM observers WHERE id='bad'`); e != nil {
		t.Fatal(e)
	}
	rr, e := s.ProcessObservationCleanup(prunequeue.CleanupRequest{ID: "fef123", RequestedAt: time.Now().UTC(), RestoreID: req.ID})
	if e != nil || rr.Restored != 500 {
		t.Fatalf("partial restore %+v %v", rr, e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE ob.id='bad'"); n != 500 {
		t.Fatalf("observer restore total %d", n)
	}
}
func TestObservationCleanupSchemaMismatchFailsClosed(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 3)
	if e := s.WriterTx("test", ensureCleanupSchema); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(`ALTER TABLE observations ADD COLUMN future_metadata TEXT`); e != nil {
		t.Fatal(e)
	}
	r, e := s.ProcessObservationCleanup(cleanupReq(3))
	if e == nil || r.Deleted != 0 {
		t.Fatalf("schema %+v %v", r, e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observations"); n != 4 {
		t.Fatalf("deleted despite mismatch %d", n)
	}
}
func TestObservationCleanupRestoreDoesNotOverwriteNewObservationRadio(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 3)
	req := cleanupReq(3)
	if _, e := s.ProcessObservationCleanup(req); e != nil {
		t.Fatal(e)
	}
	// Another import reuses an old observation id, same observer but different
	// transmission. Restore may skip it but must never assign old radio metadata.
	if _, e := s.db.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,timestamp,path_json) VALUES(2,1,1,9000,'["cc"]')`); e != nil {
		t.Fatal(e)
	}
	r, e := s.ProcessObservationCleanup(prunequeue.CleanupRequest{ID: "ad1234", RequestedAt: time.Now().UTC(), RestoreID: req.ID})
	if e != nil {
		t.Fatal(e)
	}
	if r.Restored != 2 {
		t.Fatalf("restored %+v", r)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observation_radios WHERE observation_id=2"); n != 0 {
		t.Fatalf("wrong radio attached %d", n)
	}
}

func TestObservationCleanupCoveredHopRoundTrip(t *testing.T) {
	s := cleanupTestStore(t)
	if e := dbschema.EnsureObservationHopsSchema(s.db, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(`UPDATE observation_hops_coverage SET since=0 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	cleanupSeed(t, s, 3)
	before := cleanupCount(t, s, "SELECT count(*) FROM observation_hops")
	if before != 4 {
		t.Fatalf("seed hops %d", before)
	}
	req := cleanupReq(3)
	if _, e := s.ProcessObservationCleanup(req); e != nil {
		t.Fatal(e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observation_hops"); n != 1 {
		t.Fatalf("remaining hops %d", n)
	}
	if _, e := s.ProcessObservationCleanup(prunequeue.CleanupRequest{ID: "dad123", RequestedAt: time.Now().UTC(), RestoreID: req.ID}); e != nil {
		t.Fatal(e)
	}
	if n := cleanupCount(t, s, "SELECT count(*) FROM observation_hops"); n != before {
		t.Fatalf("restored hops %d", n)
	}
}

func TestObservationCleanupSelectionUsesObserverIndex(t *testing.T) {
	s := cleanupTestStore(t)
	cleanupSeed(t, s, 5)
	if _, e := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_observations_observer_idx_timestamp ON observations(observer_idx,timestamp)`); e != nil {
		t.Fatal(e)
	}
	sel := cleanupReq(5).Selection
	sel.To = 9999
	where, args := sel.Where()
	args = append(args, 0, cleanupBatchSize)
	rows, e := s.db.Query(`EXPLAIN QUERY PLAN SELECT o.id FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE `+where+` AND o.id>? ORDER BY o.id LIMIT ?`, args...)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var d string
		if e = rows.Scan(&a, &b, &c, &d); e != nil {
			t.Fatal(e)
		}
		plan += d + "\n"
	}
	t.Log(plan)
	if !strings.Contains(plan, "SEARCH o USING INDEX idx_observations_observer_idx") && !strings.Contains(plan, "SEARCH o USING COVERING INDEX idx_observations_observer_idx") {
		t.Fatalf("expected observer-index range scan, got %s", plan)
	}
}
