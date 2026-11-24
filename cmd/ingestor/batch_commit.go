package main

// Grouped commits for the single SQLite writer (opt-in: db.batchCommitMs).
//
// Each observation used to cost about three separate commits (the packet
// transaction plus two observer upserts). Every commit appends each 4 KB page
// it touched to the WAL, so the same index leaf pages were rewritten hundreds
// of times a second. Measured on the WAN aggregate, grouping one second of
// commits cuts WAL page writes about sixfold.
//
// The wrapper sits between database/sql and mattn/go-sqlite3 on the writer's
// only connection (SetMaxOpenConns(1)). In batch mode:
//   - top-level writes run inside one shared BEGIN IMMEDIATE transaction;
//   - an application transaction becomes a SAVEPOINT in it, so its Rollback
//     still undoes only its own work;
//   - the shared transaction commits once it is batchCommitMs old (checked
//     after each write and by a background ticker), before any PRAGMA, VACUUM
//     or DDL, before cancellable work, and on Close.
//
// Durability: a process crash or power loss can drop up to one interval of
// writes; the database itself stays consistent (WAL). Other processes (the
// server) see new rows up to one interval later.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// maxBatchCommit bounds the interval below the 5 s busy_timeout so another
// writer (e.g. a migration tool) can always get the lock in time.
const maxBatchCommit = 4 * time.Second

// batchLostTotal counts shared transactions SQLite rolled back on its own
// (e.g. an I/O error). Logged; surfaced for diagnosis.
var batchLostTotal atomic.Int64

type batchConnector struct {
	dsn        string
	interval   time.Duration
	checkpoint int // wal_autocheckpoint pages; 0 = SQLite default
	base       *sqlite3.SQLiteDriver
}

func (b *batchConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := b.base.Open(b.dsn)
	if err != nil {
		return nil, err
	}
	conn := c.(*sqlite3.SQLiteConn)
	if b.checkpoint > 0 {
		if _, err := conn.Exec(fmt.Sprintf("PRAGMA wal_autocheckpoint=%d", b.checkpoint), nil); err != nil {
			conn.Close()
			return nil, err
		}
	}
	// The 2 MB page cache from the DSN holds a 1 s batch (~1 MB measured on
	// the WAN aggregate), so nothing spills to the WAL before commit.
	return &batchConn{c: conn, interval: b.interval}, nil
}

func (b *batchConnector) Driver() driver.Driver { return b.base }

// openBatchedDB returns a *sql.DB whose connections group commits.
func openBatchedDB(dsn string, interval time.Duration, checkpointPages int) (*sql.DB, error) {
	if interval <= 0 || interval > maxBatchCommit {
		return nil, fmt.Errorf("db.batchCommitMs must be from 1 to %d", maxBatchCommit.Milliseconds())
	}
	if checkpointPages < 0 || checkpointPages > 1000000 {
		return nil, fmt.Errorf("db.walAutocheckpointPages must be from 0 to 1000000")
	}
	return sql.OpenDB(&batchConnector{dsn: dsn, interval: interval, checkpoint: checkpointPages, base: &sqlite3.SQLiteDriver{}}), nil
}

type stmtKind int

const (
	kindRead    stmtKind = iota // SELECT, EXPLAIN: run in whatever is open
	kindWrite                   // DML: joins the shared transaction
	kindControl                 // PRAGMA, VACUUM, DDL, explicit txn control: commit the batch first
)

func classify(query string) stmtKind {
	q := strings.TrimSpace(query)
	for strings.HasPrefix(q, "--") || strings.HasPrefix(q, "/*") {
		if strings.HasPrefix(q, "--") {
			i := strings.IndexByte(q, '\n')
			if i < 0 {
				return kindRead
			}
			q = strings.TrimSpace(q[i+1:])
		} else {
			i := strings.Index(q, "*/")
			if i < 0 {
				return kindRead
			}
			q = strings.TrimSpace(q[i+2:])
		}
	}
	end := strings.IndexFunc(q, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') })
	if end < 0 {
		end = len(q)
	}
	switch strings.ToUpper(q[:end]) {
	case "SELECT", "EXPLAIN", "VALUES":
		return kindRead
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "WITH":
		return kindWrite
	default:
		return kindControl
	}
}

type batchConn struct {
	c        *sqlite3.SQLiteConn
	interval time.Duration
	mu       sync.Mutex // guards the fields below against the flusher's Raw call
	outer    bool       // shared transaction open
	opened   time.Time
	depth    int // open application savepoints
	seq      int
}

// healthy notices a shared transaction SQLite ended by itself.
func (b *batchConn) healthy() {
	if b.outer && b.c.AutoCommit() {
		batchLostTotal.Add(1)
		log.Printf("[db-batch] WARN: SQLite rolled back the open batch (up to %s of writes lost)", b.interval)
		b.outer, b.depth = false, 0
	}
}

func (b *batchConn) begin() error {
	b.healthy()
	if b.outer {
		return nil
	}
	if _, err := b.c.Exec("BEGIN IMMEDIATE", nil); err != nil {
		return err
	}
	b.outer, b.opened = true, time.Now()
	return nil
}

// commit ends the shared transaction. Never called with savepoints open.
func (b *batchConn) commit() error {
	b.healthy()
	if !b.outer || b.depth > 0 {
		return nil
	}
	_, err := b.c.Exec("COMMIT", nil)
	if err != nil && !b.c.AutoCommit() {
		// Keep the transaction; a later commit retries. Logged by the caller.
		return err
	}
	b.outer = false
	return err
}

func (b *batchConn) commitIfDue() error {
	if b.outer && b.depth == 0 && time.Since(b.opened) >= b.interval {
		return b.commit()
	}
	return nil
}

// before prepares the connection for one top-level statement.
func (b *batchConn) before(ctx context.Context, kind stmtKind) error {
	if b.depth > 0 {
		return nil // inside an application transaction: run as-is
	}
	if !b.outer && !b.c.AutoCommit() {
		// The caller opened its own transaction with a plain BEGIN statement
		// (e.g. dbschema's observers rebuild on a pinned connection): run
		// as-is until its own COMMIT or ROLLBACK ends it.
		return nil
	}
	switch {
	case kind == kindControl:
		return b.commit()
	case kind == kindWrite:
		// A cancelled statement can roll back its whole transaction, so
		// cancellable work never shares one with other writers.
		if ctx.Done() != nil {
			return b.commit()
		}
		return b.begin()
	}
	return nil
}

func (b *batchConn) after(kind stmtKind) {
	if kind == kindWrite && b.depth == 0 {
		if err := b.commitIfDue(); err != nil {
			log.Printf("[db-batch] commit: %v", err)
		}
	}
}

func (b *batchConn) Prepare(query string) (driver.Stmt, error) {
	return b.PrepareContext(context.Background(), query)
}

func (b *batchConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := b.c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &batchStmt{s: s, conn: b, kind: classify(query)}, nil
}

func (b *batchConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kind := classify(query)
	if err := b.before(ctx, kind); err != nil {
		return nil, err
	}
	res, err := b.c.ExecContext(ctx, query, args)
	b.after(kind)
	return res, err
}

func (b *batchConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	kind := classify(query)
	if err := b.before(ctx, kind); err != nil {
		return nil, err
	}
	// A write via Query (RETURNING) commits on a later write or the ticker:
	// its rows are still being read here.
	return b.c.QueryContext(ctx, query, args)
}

func (b *batchConn) Begin() (driver.Tx, error) {
	return b.BeginTx(context.Background(), driver.TxOptions{})
}

func (b *batchConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.depth == 0 && ctx.Done() != nil {
		if err := b.commit(); err != nil {
			return nil, err
		}
	}
	if err := b.begin(); err != nil {
		return nil, err
	}
	b.seq++
	name := fmt.Sprintf("cs_batch_%d", b.seq)
	if _, err := b.c.Exec("SAVEPOINT "+name, nil); err != nil {
		return nil, err
	}
	b.depth++
	return &batchTx{conn: b, name: name}, nil
}

func (b *batchConn) Ping(ctx context.Context) error { return b.c.Ping(ctx) }

func (b *batchConn) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.depth = 0
	if err := b.commit(); err != nil {
		log.Printf("[db-batch] final commit: %v", err)
	}
	return b.c.Close()
}

// flushIfDue is called by the background ticker through sql.Conn.Raw.
func (b *batchConn) flushIfDue() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.commitIfDue()
}

type batchTx struct {
	conn *batchConn
	name string
	done bool
}

func (t *batchTx) finish(release bool) error {
	b := t.conn
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	b.healthy()
	if b.depth == 0 {
		return errors.New("batch transaction was rolled back by SQLite")
	}
	var err error
	if !release {
		_, err = b.c.Exec("ROLLBACK TO "+t.name, nil)
	}
	if _, rerr := b.c.Exec("RELEASE "+t.name, nil); err == nil {
		err = rerr
	}
	b.depth--
	if b.depth == 0 {
		if cerr := b.commitIfDue(); cerr != nil {
			log.Printf("[db-batch] commit: %v", cerr)
		}
	}
	return err
}

func (t *batchTx) Commit() error   { return t.finish(true) }
func (t *batchTx) Rollback() error { return t.finish(false) }

type batchStmt struct {
	s    driver.Stmt // mattn's statement: implements the Context variants
	conn *batchConn
	kind stmtKind
}

func (s *batchStmt) Close() error  { return s.s.Close() }
func (s *batchStmt) NumInput() int { return s.s.NumInput() }

func (s *batchStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), valuesToNamed(args))
}

func (s *batchStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), valuesToNamed(args))
}

func (s *batchStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	b := s.conn
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.before(ctx, s.kind); err != nil {
		return nil, err
	}
	res, err := s.s.(driver.StmtExecContext).ExecContext(ctx, args)
	b.after(s.kind)
	return res, err
}

func (s *batchStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	b := s.conn
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.before(ctx, s.kind); err != nil {
		return nil, err
	}
	return s.s.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return named
}

// flushBatch commits the open grouped-commit batch now, so independent
// readers see every write made so far and it survives a crash. Without batch
// commits it does nothing. database/sql hands the single connection to Raw
// only when no statement or transaction holds it.
func (s *Store) flushBatch() error {
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(dc any) error {
		b, ok := dc.(*batchConn)
		if !ok {
			return nil
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.commit()
	})
}

// startBatchFlusher commits an idle batch once it is due. database/sql hands
// the single connection to Raw only when no statement or transaction holds
// it, so this never interleaves with a writer.
func startBatchFlusher(db *sql.DB, interval time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval / 2)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			conn, err := db.Conn(ctx)
			if err != nil {
				continue // closing, or cancelled
			}
			err = conn.Raw(func(dc any) error {
				if b, ok := dc.(*batchConn); ok {
					return b.flushIfDue()
				}
				return nil
			})
			conn.Close()
			if err != nil {
				log.Printf("[db-batch] commit: %v", err)
			}
		}
	}()
	return func() { cancel(); <-done }
}
