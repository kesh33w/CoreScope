package prunequeue

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupSelection(t *testing.T) {
	for _, s := range []CleanupSelection{{}, {ObserverID: "x", From: 20, To: 10}, {ObserverID: "x", MaxObservationID: -1}} {
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	s := CleanupSelection{ObserverID: "x' OR 1=1", IATA: "smf", From: 1, To: 2, MaxObservationID: 3}
	if e := s.Validate(); e != nil {
		t.Fatal(e)
	}
	q, a := s.Where()
	if strings.Contains(q, s.ObserverID) || len(a) != 5 {
		t.Fatalf("unbound query %s %v", q, a)
	}
}
func TestCleanupQueueRoundTrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	r := CleanupRequest{ID: "abcd1234", RequestedAt: time.Now().UTC(), Selection: CleanupSelection{ObserverID: "observer", MaxObservationID: 42}}
	if e := WriteCleanupRequest(db, r); e != nil {
		t.Fatal(e)
	}
	p, e := ListCleanupPending(db)
	if e != nil || len(p) != 1 {
		t.Fatalf("pending %v %v", p, e)
	}
	got, e := ReadCleanupRequest(p[0])
	if e != nil || got.Selection != r.Selection {
		t.Fatalf("read %+v %v", got, e)
	}
	if e := WriteCleanupResult(db, CleanupResult{ID: r.ID, ArchiveID: r.ID, Deleted: 42}); e != nil {
		t.Fatal(e)
	}
	exists, e := CleanupRequestExists(db, r.ID)
	if e != nil || exists {
		t.Fatalf("exists %v %v", exists, e)
	}
	res, e := ReadCleanupResult(db, r.ID)
	if e != nil || res.Deleted != 42 {
		t.Fatalf("result %+v %v", res, e)
	}
	r.ID = "../bad"
	if WriteCleanupRequest(db, r) == nil {
		t.Fatal("path traversal accepted")
	}
	r.ID = "abc"
	r.RestoreID = "../bad"
	if WriteCleanupRequest(db, r) == nil {
		t.Fatal("restore traversal accepted")
	}
}
