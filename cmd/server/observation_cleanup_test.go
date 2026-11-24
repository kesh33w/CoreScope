package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/prunequeue"
)

func cleanupTestServer(t *testing.T) *Server {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.db")
	db, e := sql.Open("sqlite3", p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`CREATE TABLE observers(idx INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, name TEXT, iata TEXT); CREATE TABLE observations(id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, timestamp INTEGER); INSERT INTO observers(id,name,iata) VALUES('bad','Bad','SMF'),('good','Good','SMF'); INSERT INTO observations VALUES(1,10,1,100),(2,10,2,100),(3,11,1,200);`)
	if e != nil {
		t.Fatal(e)
	}
	return &Server{db: &DB{conn: db, path: p}, cfg: &Config{APIKey: strings.Repeat("K", 40)}}
}
func TestCleanupPreviewAndSnapshot(t *testing.T) {
	s := cleanupTestServer(t)
	w := httptest.NewRecorder()
	s.handleCleanupPreview(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"observerId":"bad","iata":"SMF"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p cleanupPreview
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if p.Count != 2 || p.Transmissions != 2 || p.ExclusiveTransmissions != 1 || p.MaxObservationID != 3 {
		t.Fatalf("%+v", p)
	}
	s.db.conn.Exec(`INSERT INTO observations VALUES(4,12,1,300)`)
	body := `{"selection":{"observerId":"bad","iata":"SMF","to":1000},"maxObservationId":3,"expectedCount":2}`
	w = httptest.NewRecorder()
	s.handleCleanupSubmit(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	ps, e := prunequeue.ListCleanupPending(s.db.path)
	if e != nil || len(ps) != 1 {
		t.Fatal(ps, e)
	}
	req, e := prunequeue.ReadCleanupRequest(ps[0])
	if e != nil || req.Selection.MaxObservationID != 3 {
		t.Fatal(req, e)
	}
	var n int
	s.db.conn.QueryRow("SELECT COUNT(*) FROM observations").Scan(&n)
	if n != 4 {
		t.Fatal("server modified observations")
	}
}
func TestCleanupRejectsUnsafeAndChangedSelection(t *testing.T) {
	s := cleanupTestServer(t)
	for _, body := range []string{`{}`, `{"observerId":"bad","from":9,"to":1}`, `{"observerId":"bad","extra":1}`} {
		w := httptest.NewRecorder()
		s.handleCleanupPreview(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.handleCleanupSubmit(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"selection":{"observerId":"bad","to":1000},"maxObservationId":3,"expectedCount":1}`)))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.requireAdmin(http.HandlerFunc(s.handleCleanupSubmit)).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestCleanupObserverSearchIncludesBlockedHistory(t *testing.T) {
	s := cleanupTestServer(t)
	s.cfg.ObserverBlacklist = []string{"bad"}
	s.db.conn.Exec(`UPDATE observers SET name='AB0OO-910' WHERE id='bad'; UPDATE observers SET name='AB0OO-909' WHERE id='good'`)
	w := httptest.NewRecorder()
	s.handleCleanupObservers(w, httptest.NewRequest("GET", "/?q=ab0oo", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		Observers []cleanupObserver `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Observers) != 2 {
		t.Fatal(got)
	}
	o := got.Observers[1]
	if o.ID != "bad" || !o.Blocked || o.Observations != 2 || o.LastSeen != 200 {
		t.Fatal(o)
	}
	w = httptest.NewRecorder()
	s.handleCleanupObservers(w, httptest.NewRequest("GET", "/?q=%25%27", nil))
	if strings.Contains(w.Body.String(), "AB0OO") {
		t.Fatal("search wildcard or quote interpreted as SQL")
	}
	w = httptest.NewRecorder()
	s.handleCleanupObservers(w, httptest.NewRequest("GET", "/?q=a", nil))
	if strings.Contains(w.Body.String(), "AB0OO") {
		t.Fatal("unbounded search")
	}
}
