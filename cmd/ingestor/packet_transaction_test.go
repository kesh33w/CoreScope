package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestInsertTransmissionCommitsPacketTogether(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "packet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	data := &PacketData{Hash: "packet-commit", RawHex: "1100aa", Timestamp: "2026-01-02T00:00:00Z", PathJSON: "[]"}
	created, err := s.InsertTransmission(data)
	if err != nil || !created {
		t.Fatalf("insert: created=%v err=%v", created, err)
	}
	var transmissions, observations int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&transmissions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if transmissions != 1 || observations != 1 || s.Stats.WALCommits.Load() != 1 {
		t.Fatalf("committed packet: transmissions=%d observations=%d commits=%d", transmissions, observations, s.Stats.WALCommits.Load())
	}
}

func TestRawHandleMessageObserverSharesPacketCommit(t *testing.T) {
	s := newTestStore(t)
	source := MQTTSource{Name: "test"}
	msg := &mockMessage{
		topic:   "meshcore/SJC/observer-packet/packets",
		payload: []byte(`{"raw":"0A00D69FD7A5A7475DB07337749AE61FA53A4788E976","origin":"Bay Observer"}`),
	}
	for i := 1; i <= 2; i++ {
		handleMessage(s, "test", source, msg, nil, nil, &Config{})
		var transmissions, observations, observers, packetCount int
		var name, iata string
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&transmissions); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&observations); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*), MAX(packet_count), MAX(name), MAX(iata) FROM observers WHERE id='observer-packet'`).Scan(&observers, &packetCount, &name, &iata); err != nil {
			t.Fatal(err)
		}
		if transmissions != 1 || observations != i || observers != 1 || packetCount != i || name != "Bay Observer" || iata != "SJC" {
			t.Fatalf("packet %d: transmissions=%d observations=%d observers=%d count=%d name=%q iata=%q", i, transmissions, observations, observers, packetCount, name, iata)
		}
		if s.Stats.WALCommits.Load() != int64(i) || s.Stats.ObserverUpserts.Load() != int64(i) {
			t.Fatalf("packet %d: commits=%d observerUpserts=%d", i, s.Stats.WALCommits.Load(), s.Stats.ObserverUpserts.Load())
		}
	}
	var first, second sql.NullInt64
	rows, err := s.db.Query(`SELECT observer_idx FROM observations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() || rows.Scan(&first) != nil || !rows.Next() || rows.Scan(&second) != nil || rows.Next() || rows.Err() != nil {
		t.Fatal("could not inspect first and repeated packet observations")
	}
	if first.Valid || !second.Valid {
		t.Fatalf("observer creation order changed: first=%v second=%v", first, second)
	}
}

func TestRawHandleMessageDoesNotCommitObserverOnObservationFailure(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_observation BEFORE INSERT ON observations BEGIN SELECT RAISE(ABORT, 'test observation failure'); END`); err != nil {
		t.Fatal(err)
	}
	msg := &mockMessage{
		topic:   "meshcore/SMF/rollback-observer/packets",
		payload: []byte(`{"raw":"0A00D69FD7A5A7475DB07337749AE61FA53A4788E976","origin":"Rollback"}`),
	}
	handleMessage(s, "test", MQTTSource{Name: "test"}, msg, nil, nil, &Config{})
	for _, table := range []string{"transmissions", "observations", "observers"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after rollback", table, count)
		}
	}
	if s.Stats.ObserverUpserts.Load() != 0 || s.Stats.WALCommits.Load() != 0 {
		t.Fatalf("stats advanced after rollback: observers=%d commits=%d", s.Stats.ObserverUpserts.Load(), s.Stats.WALCommits.Load())
	}
}

func TestInsertTransmissionRollsBackOnObservationFailure(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "packet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	if _, err := s.db.Exec(`CREATE TRIGGER fail_observation BEFORE INSERT ON observations BEGIN SELECT RAISE(ABORT, 'test observation failure'); END`); err != nil {
		t.Fatal(err)
	}
	data := &PacketData{Hash: "packet-rollback", RawHex: "1100aa", Timestamp: "2026-01-02T00:00:00Z", PathJSON: "[]"}
	if _, err := s.InsertTransmission(data); err == nil {
		t.Fatal("expected observation failure")
	}
	var transmissions, observations int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&transmissions); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if transmissions != 0 || observations != 0 || s.Stats.WALCommits.Load() != 0 {
		t.Fatalf("rolled-back packet: transmissions=%d observations=%d commits=%d", transmissions, observations, s.Stats.WALCommits.Load())
	}
}

func TestObservedPacketRollsBackExistingObserverAndCounters(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserverAt("existing", "Before", "SMF", nil, "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE observers SET inactive=1 WHERE id='existing'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_reactivation BEFORE UPDATE OF inactive ON observers BEGIN SELECT RAISE(ABORT, 'reactivation failed'); END`); err != nil {
		t.Fatal(err)
	}
	data := &PacketData{Hash: "observer-rollback", RawHex: "1100aa", ObserverID: "existing", ObserverName: "After", Region: "SMF", PathJSON: "[]"}
	if _, err := s.InsertObservedTransmission(data); err == nil {
		t.Fatal("expected failure")
	}
	var name, last string
	var inactive, packets int
	if err := s.db.QueryRow(`SELECT name,last_seen,inactive,packet_count FROM observers WHERE id='existing'`).Scan(&name, &last, &inactive, &packets); err != nil {
		t.Fatal(err)
	}
	if name != "Before" || last != "2020-01-01T00:00:00Z" || inactive != 1 || packets != 1 {
		t.Fatalf("observer changed after rollback: %q %q %d %d", name, last, inactive, packets)
	}
	for _, table := range []string{"transmissions", "observations"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s retained %d rows", table, n)
		}
	}
	if s.Stats.TransmissionsInserted.Load() != 0 || s.Stats.ObservationsInserted.Load() != 0 || s.Stats.WALCommits.Load() != 0 || s.Stats.ObserverUpserts.Load() != 1 {
		t.Fatal("rolled-back writes advanced success counters")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_reactivation`); err != nil {
		t.Fatal(err)
	}
	data.ObserverName = strings.Repeat("x", maxObserverTextLen+10)
	if _, err := s.InsertObservedTransmission(data); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT name,inactive,packet_count FROM observers WHERE id='existing'`).Scan(&name, &inactive, &packets); err != nil {
		t.Fatal(err)
	}
	if len(name) != maxObserverTextLen || inactive != 0 || packets != 2 {
		t.Fatalf("observer update: name length=%d inactive=%d packets=%d", len(name), inactive, packets)
	}
}
