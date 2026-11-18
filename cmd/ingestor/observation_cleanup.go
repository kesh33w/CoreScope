package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

const cleanupBatchSize = 500

var cleanupMu sync.Mutex

// Backups are ordinary SQLite tables in the same database: backup and deletion
// commit atomically under the sole ingestor writer. No foreign keys or retention
// job can remove these archive rows. They are not included in measurement views.
func ensureCleanupSchema(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS observation_cleanup_jobs (id TEXT PRIMARY KEY, request_json TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', cursor INTEGER NOT NULL DEFAULT 0, deleted INTEGER NOT NULL DEFAULT 0, restored INTEGER NOT NULL DEFAULT 0, completed_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO observation_cleanup_revision(id) VALUES(1)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_observers AS SELECT CAST('' AS TEXT) AS cleanup_id,ob.* FROM observers ob WHERE 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_observers ON observation_cleanup_observers(cleanup_id,id)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_obs AS SELECT CAST('' AS TEXT) AS cleanup_id, CAST('' AS TEXT) AS observer_key,o.* FROM observations o WHERE 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_obs ON observation_cleanup_obs(cleanup_id,id)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_tx AS SELECT CAST('' AS TEXT) AS cleanup_id,t.* FROM transmissions t WHERE 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_tx ON observation_cleanup_tx(cleanup_id,id)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_radios AS SELECT CAST('' AS TEXT) AS cleanup_id,r.* FROM observation_radios r WHERE 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_radios ON observation_cleanup_radios(cleanup_id,observation_id)`,
		`CREATE TABLE IF NOT EXISTS observation_cleanup_advert AS SELECT CAST('' AS TEXT) AS cleanup_id,e.* FROM advert_route_evidence e WHERE 0`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cleanup_advert ON observation_cleanup_advert(cleanup_id,tx_id,bit)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}

	// Refuse unsafe archive layouts after a future source schema migration. No
	// delete happens until every archive can store every current column.
	for _, p := range [][3]string{{"observations", "observation_cleanup_obs", "observer_key"}, {"transmissions", "observation_cleanup_tx", ""}, {"observers", "observation_cleanup_observers", ""}, {"observation_radios", "observation_cleanup_radios", ""}, {"advert_route_evidence", "observation_cleanup_advert", ""}} {
		live, e := cleanupColumns(tx, p[0])
		if e != nil {
			return e
		}
		arch, e := cleanupColumns(tx, p[1])
		if e != nil {
			return e
		}
		expected := append([]string{"cleanup_id"}, live...)
		if p[2] != "" {
			expected = append([]string{"cleanup_id", p[2]}, live...)
		}
		if strings.Join(expected, ",") != strings.Join(arch, ",") {
			return fmt.Errorf("archive schema mismatch for %s; archive migration required before cleanup", p[0])
		}
	}
	return nil
}

func cleanupColumns(tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def interface{}
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
func quotedColumns(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
	}
	return strings.Join(out, ",")
}
func idList(ids []int64) (string, []interface{}) {
	p := make([]string, len(ids))
	a := make([]interface{}, len(ids))
	for i, id := range ids {
		p[i] = "?"
		a[i] = id
	}
	return strings.Join(p, ","), a
}
func scanCleanupIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ProcessObservationCleanup is resumable after a process crash. Each commit
// advances a durable cursor and records its exact counts; re-running an ID is
// idempotent and cannot silently change its selection.
func (s *Store) ProcessObservationCleanup(req prunequeue.CleanupRequest) (prunequeue.CleanupResult, error) {
	return s.processObservationCleanup(req, runtime.Gosched)
}
func (s *Store) processObservationCleanup(req prunequeue.CleanupRequest, between func()) (prunequeue.CleanupResult, error) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	result := prunequeue.CleanupResult{ID: req.ID, RequestedAt: req.RequestedAt, ArchiveID: req.ID}
	if req.RestoreID != "" {
		result.ArchiveID = req.RestoreID
	}
	// Queue's writer validates IDs too; the direct entry point must not trust callers.
	if req.ID == "" {
		return result, fmt.Errorf("missing cleanup id")
	}
	if req.RestoreID == "" {
		if err := req.Selection.Validate(); err != nil {
			return result, err
		}
	}
	encoded, _ := json.Marshal(req)
	err := s.WriterTx("observation_cleanup", func(tx *sql.Tx) error {
		if err := ensureCleanupSchema(tx); err != nil {
			return err
		}
		if req.RestoreID != "" {
			var state string
			if err := tx.QueryRow(`SELECT status FROM observation_cleanup_jobs WHERE id=?`, req.RestoreID).Scan(&state); err != nil {
				return fmt.Errorf("archive not found: %w", err)
			}
			if state != "done" && state != "failed" {
				return fmt.Errorf("archive job must finish before restore")
			}
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO observation_cleanup_jobs(id,request_json) VALUES(?,?)`, req.ID, string(encoded))
		if err != nil {
			return err
		}
		var existing string
		if err = tx.QueryRow(`SELECT request_json FROM observation_cleanup_jobs WHERE id=?`, req.ID).Scan(&existing); err != nil {
			return err
		}
		if existing != string(encoded) {
			return fmt.Errorf("cleanup id already belongs to a different request")
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for {
		done := false
		err = s.WriterTx("observation_cleanup", func(tx *sql.Tx) error {
			var state string
			var cursor int64
			if err := tx.QueryRow(`SELECT status,cursor,deleted,restored FROM observation_cleanup_jobs WHERE id=?`, req.ID).Scan(&state, &cursor, &result.Deleted, &result.Restored); err != nil {
				return err
			}
			if state == "done" {
				done = true
				return nil
			}
			var n, last int64
			var err error
			if req.RestoreID == "" {
				n, last, err = cleanupDeleteBatch(tx, req, cursor)
				result.Deleted += n
			} else {
				n, last, err = cleanupRestoreBatch(tx, req, cursor)
				result.Restored += n
			}
			if err != nil {
				return err
			}
			if last == 0 {
				done = true
				_, err = tx.Exec(`UPDATE observation_cleanup_jobs SET status='done',completed_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), req.ID)
				return err
			}
			if _, err = tx.Exec(`UPDATE observation_cleanup_jobs SET cursor=?,deleted=?,restored=? WHERE id=?`, last, result.Deleted, result.Restored, req.ID); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE observation_cleanup_revision SET revision=revision+1 WHERE id=1`)
			return err
		})
		if err != nil { // Reload committed counts: never count a rolled-back batch.
			// A partial job is restorable even if a later batch failed.
			_ = s.WriterTx("observation_cleanup", func(tx *sql.Tx) error {
				_, e := tx.Exec(`UPDATE observation_cleanup_jobs SET status='failed' WHERE id=? AND status!='done'`, req.ID)
				return e
			})
			_ = s.db.QueryRow(`SELECT deleted,restored FROM observation_cleanup_jobs WHERE id=?`, req.ID).Scan(&result.Deleted, &result.Restored)
			return result, err
		}
		if done {
			result.CompletedAt = time.Now().UTC()
			return result, nil
		}
		between()
	}
}

func cleanupDeleteBatch(tx *sql.Tx, req prunequeue.CleanupRequest, cursor int64) (int64, int64, error) {
	where, args := req.Selection.Where()
	args = append(args, cursor, cleanupBatchSize)
	rows, err := tx.Query(`SELECT o.id FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE `+where+` AND o.id>? ORDER BY o.id LIMIT ?`, args...)
	if err != nil {
		return 0, 0, err
	}
	ids, err := scanCleanupIDs(rows)
	if err != nil || len(ids) == 0 {
		return 0, 0, err
	}
	ph, a := idList(ids)
	backupArgs := append([]interface{}{req.ID}, a...)
	// Complete backup rows and sparse radio metadata are written before delete.
	for _, q := range []string{
		`INSERT OR IGNORE INTO observation_cleanup_observers SELECT ?,ob.* FROM observers ob WHERE ob.rowid IN (SELECT observer_idx FROM observations WHERE id IN (` + ph + `))`,
		`INSERT OR IGNORE INTO observation_cleanup_tx SELECT ?,t.* FROM transmissions t WHERE t.id IN (SELECT transmission_id FROM observations WHERE id IN (` + ph + `))`,
		`INSERT INTO observation_cleanup_obs SELECT ?,ob.id,o.* FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE o.id IN (` + ph + `)`,
		`INSERT OR IGNORE INTO observation_cleanup_radios SELECT ?,r.* FROM observation_radios r WHERE observation_id IN (` + ph + `)`,
		`INSERT OR IGNORE INTO observation_cleanup_advert SELECT ?,e.* FROM advert_route_evidence e WHERE tx_id IN (SELECT transmission_id FROM observations WHERE id IN (` + ph + `))`,
	} {
		if _, err = tx.Exec(q, backupArgs...); err != nil {
			return 0, 0, fmt.Errorf("archive rows: %w", err)
		}
	}
	// Verify backed-up count before any deletion. A failed archive transaction has no side effects.
	var backed int
	if err = tx.QueryRow(`SELECT count(*) FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (`+ph+`)`, backupArgs...).Scan(&backed); err != nil {
		return 0, 0, err
	}
	if backed != len(ids) {
		return 0, 0, fmt.Errorf("archive count mismatch")
	}
	if _, err = tx.Exec(`DELETE FROM observation_radios WHERE observation_id IN (`+ph+`)`, a...); err != nil {
		return 0, 0, err
	}
	res, err := tx.Exec(`DELETE FROM observations WHERE id IN (`+ph+`)`, a...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	// The observation-hops delete trigger removes only these observations' hops.
	// Orphan transmissions no longer represent any received observation. Preserve
	// every shared transmission, and do not touch mobile-client evidence/registry.
	affected := `SELECT transmission_id FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (` + ph + `)`
	for _, q := range []string{
		`DELETE FROM advert_route_evidence WHERE tx_id IN (` + affected + `) AND NOT EXISTS(SELECT 1 FROM observations WHERE transmission_id=tx_id)`,
		`DELETE FROM tx_rewrite_feed WHERE tx_id IN (` + affected + `) AND NOT EXISTS(SELECT 1 FROM observations WHERE transmission_id=tx_id)`,
		`DELETE FROM transmissions WHERE id IN (` + affected + `) AND NOT EXISTS(SELECT 1 FROM observations WHERE transmission_id=transmissions.id)`,
		`UPDATE transmissions SET last_seen=COALESCE((SELECT MAX(timestamp) FROM observations WHERE transmission_id=transmissions.id),last_seen) WHERE id IN (` + affected + `)`,
	} {
		if _, err = tx.Exec(q, backupArgs...); err != nil {
			return 0, 0, err
		}
	}
	return n, ids[len(ids)-1], nil
}

func cleanupRestoreBatch(tx *sql.Tx, req prunequeue.CleanupRequest, cursor int64) (int64, int64, error) {
	rows, err := tx.Query(`SELECT id FROM observation_cleanup_obs WHERE cleanup_id=? AND id>? ORDER BY id LIMIT ?`, req.RestoreID, cursor, cleanupBatchSize)
	if err != nil {
		return 0, 0, err
	}
	ids, err := scanCleanupIDs(rows)
	if err != nil || len(ids) == 0 {
		return 0, 0, err
	}
	ph, a := idList(ids)
	args := append([]interface{}{req.RestoreID}, a...)
	// Restoring never replaces current observations or current canonical payloads.
	// A transmission hash may have reappeared since cleanup under a new rowid.
	tc, err := cleanupColumns(tx, "transmissions")
	if err != nil {
		return 0, 0, err
	}
	var target, source []string
	for _, c := range tc {
		if c == "id" {
			continue
		}
		target = append(target, c)
		source = append(source, `b."`+c+`"`)
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO transmissions (`+quotedColumns(target)+`) SELECT `+strings.Join(source, ",")+` FROM observation_cleanup_tx b WHERE b.cleanup_id=? AND b.id IN (SELECT transmission_id FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (`+ph+`))`, append([]interface{}{req.RestoreID}, args...)...)
	if err != nil {
		return 0, 0, err
	}
	// Restore a missing observer from its complete archived row, without
	// overwriting any current status/settings. New rowids are mapped below.
	bc, err := cleanupColumns(tx, "observers")
	if err != nil {
		return 0, 0, err
	}
	bs := make([]string, len(bc))
	for i, c := range bc {
		bs[i] = `b."` + c + `"`
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO observers (`+quotedColumns(bc)+`) SELECT `+strings.Join(bs, ",")+` FROM observation_cleanup_observers b WHERE b.cleanup_id=? AND b.id IN (SELECT observer_key FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (`+ph+`))`, append([]interface{}{req.RestoreID}, args...)...)
	if err != nil {
		return 0, 0, err
	}
	var missing int
	if err = tx.QueryRow(`SELECT count(*) FROM observation_cleanup_obs b WHERE cleanup_id=? AND id IN (`+ph+`) AND NOT EXISTS(SELECT 1 FROM observers WHERE id=b.observer_key)`, args...).Scan(&missing); err != nil {
		return 0, 0, err
	}
	if missing > 0 {
		return 0, 0, fmt.Errorf("observer no longer exists; cannot safely restore %d archived rows", missing)
	}
	oc, err := cleanupColumns(tx, "observations")
	if err != nil {
		return 0, 0, err
	}
	source = nil
	for _, c := range oc {
		switch c {
		case "transmission_id":
			source = append(source, "t.id")
		case "observer_idx":
			source = append(source, "ob.rowid")
		default:
			source = append(source, `b."`+c+`"`)
		}
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO observations (`+quotedColumns(oc)+`) SELECT `+strings.Join(source, ",")+` FROM observation_cleanup_obs b JOIN observation_cleanup_tx bt ON bt.cleanup_id=b.cleanup_id AND bt.id=b.transmission_id JOIN transmissions t ON t.hash=bt.hash JOIN observers ob ON ob.id=b.observer_key WHERE b.cleanup_id=? AND b.id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	_, err = tx.Exec(`INSERT OR IGNORE INTO observation_radios(observation_id,radio_id) SELECT r.observation_id,r.radio_id FROM observation_cleanup_radios r JOIN observations o ON o.id=r.observation_id JOIN observation_cleanup_obs b ON b.cleanup_id=r.cleanup_id AND b.id=o.id JOIN observers ob ON ob.rowid=o.observer_idx AND ob.id=b.observer_key JOIN observation_cleanup_tx bt ON bt.cleanup_id=b.cleanup_id AND bt.id=b.transmission_id JOIN transmissions t ON t.id=o.transmission_id AND t.hash=bt.hash WHERE o.timestamp=b.timestamp AND COALESCE(o.path_json,'')=COALESCE(b.path_json,'') AND COALESCE(o.raw_hex,'')=COALESCE(b.raw_hex,'') AND r.cleanup_id=? AND r.observation_id IN (`+ph+`)`, args...)
	if err != nil {
		return 0, 0, err
	}
	// Reconstitute archived advert evidence against current hash identities.
	_, err = tx.Exec(`INSERT OR IGNORE INTO advert_route_evidence(tx_id,bit) SELECT t.id,e.bit FROM observation_cleanup_advert e JOIN observation_cleanup_tx bt ON bt.cleanup_id=e.cleanup_id AND bt.id=e.tx_id JOIN transmissions t ON t.hash=bt.hash WHERE e.cleanup_id=? AND e.tx_id IN (SELECT transmission_id FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (`+ph+`))`, append([]interface{}{req.RestoreID}, args...)...)
	if err != nil {
		return 0, 0, err
	}
	_, err = tx.Exec(`UPDATE transmissions SET last_seen=MAX(last_seen,COALESCE((SELECT MAX(timestamp) FROM observations WHERE transmission_id=transmissions.id),0)) WHERE hash IN (SELECT hash FROM observation_cleanup_tx WHERE cleanup_id=? AND id IN (SELECT transmission_id FROM observation_cleanup_obs WHERE cleanup_id=? AND id IN (`+ph+`)))`, append([]interface{}{req.RestoreID}, args...)...)
	return n, ids[len(ids)-1], err
}

func (s *Store) RunPendingObservationCleanups() {
	paths, err := prunequeue.ListCleanupPending(s.path)
	if err != nil {
		log.Printf("[observation-cleanup] list: %v", err)
		return
	}
	for _, p := range paths {
		req, err := prunequeue.ReadCleanupRequest(p)
		if err != nil {
			log.Printf("[observation-cleanup] read %s: %v", p, err)
			continue
		}
		res, err := s.ProcessObservationCleanup(*req)
		if err != nil {
			res.Error = err.Error()
		}
		if err = prunequeue.WriteCleanupResult(s.path, res); err != nil {
			log.Printf("[observation-cleanup] result: %v", err)
		}
	}
}
