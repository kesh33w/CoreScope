package prunequeue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanupSelection deliberately contains no inferred historical RF frequency.
// IATA is the observer's CURRENT label, not a per-packet measurement.
type CleanupSelection struct {
	ObserverID       string `json:"observerId"`
	IATA             string `json:"iata,omitempty"`
	From             int64  `json:"from,omitempty"`
	To               int64  `json:"to,omitempty"`
	MaxObservationID int64  `json:"maxObservationId"`
}

func (s CleanupSelection) Validate() error {
	if strings.TrimSpace(s.ObserverID) == "" || len(s.ObserverID) > 512 {
		return errors.New("exact observerId required")
	}
	if len(s.IATA) > 16 || strings.ContainsAny(s.IATA, " \t\r\n") {
		return errors.New("invalid IATA")
	}
	if s.From < 0 || s.To < 0 || (s.To > 0 && s.From > s.To) {
		return errors.New("invalid timestamp range")
	}
	if s.MaxObservationID < 0 {
		return errors.New("invalid observation cutoff")
	}
	return nil
}

// Where is shared by read-only preview and writer, with bound parameters only.
func (s CleanupSelection) Where() (string, []interface{}) {
	q := "ob.id = ? AND o.id <= ?"
	a := []interface{}{s.ObserverID, s.MaxObservationID}
	if s.IATA != "" {
		q += " AND UPPER(COALESCE(ob.iata,'')) = ?"
		a = append(a, strings.ToUpper(s.IATA))
	}
	if s.From > 0 {
		q += " AND o.timestamp >= ?"
		a = append(a, s.From)
	}
	if s.To > 0 {
		q += " AND o.timestamp <= ?"
		a = append(a, s.To)
	}
	return q, a
}

type CleanupRequest struct {
	ID          string           `json:"id"`
	RequestedAt time.Time        `json:"requestedAt"`
	Selection   CleanupSelection `json:"selection"`
	RestoreID   string           `json:"restoreId,omitempty"`
}
type CleanupResult struct {
	ID          string    `json:"id"`
	RequestedAt time.Time `json:"requestedAt"`
	CompletedAt time.Time `json:"completedAt"`
	Deleted     int64     `json:"deleted"`
	Restored    int64     `json:"restored"`
	ArchiveID   string    `json:"archiveId"`
	Error       string    `json:"error,omitempty"`
}

func CleanupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "observation-cleanup")
}
func cleanupPath(dbPath, kind, id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid cleanup id")
	}
	return filepath.Join(CleanupDir(dbPath), kind+"-"+id+".json"), nil
}
func writeCleanupFile(p string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".cleanup-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func WriteCleanupRequest(dbPath string, r CleanupRequest) error {
	p, err := cleanupPath(dbPath, "request", r.ID)
	if err != nil {
		return err
	}
	if r.RestoreID != "" {
		if !validID(r.RestoreID) {
			return errors.New("invalid archive id")
		}
	} else if err = r.Selection.Validate(); err != nil {
		return err
	}
	return writeCleanupFile(p, r)
}
func WriteCleanupResult(dbPath string, r CleanupResult) error {
	p, err := cleanupPath(dbPath, "result", r.ID)
	if err != nil {
		return err
	}
	if err = writeCleanupFile(p, r); err != nil {
		return err
	}
	p, _ = cleanupPath(dbPath, "request", r.ID)
	return os.Remove(p)
}
func ReadCleanupResult(dbPath, id string) (*CleanupResult, error) {
	p, err := cleanupPath(dbPath, "result", id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r CleanupResult
	err = json.Unmarshal(b, &r)
	return &r, err
}
func ReadCleanupRequest(path string) (*CleanupRequest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r CleanupRequest
	err = json.Unmarshal(b, &r)
	return &r, err
}
func CleanupRequestExists(dbPath, id string) (bool, error) {
	p, e := cleanupPath(dbPath, "request", id)
	if e != nil {
		return false, e
	}
	_, e = os.Stat(p)
	if os.IsNotExist(e) {
		return false, nil
	}
	return e == nil, e
}
func ListCleanupPending(dbPath string) ([]string, error) {
	es, e := os.ReadDir(CleanupDir(dbPath))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var out []string
	for _, v := range es {
		n := v.Name()
		if !v.IsDir() && strings.HasPrefix(n, "request-") && strings.HasSuffix(n, ".json") {
			out = append(out, filepath.Join(CleanupDir(dbPath), n))
		}
	}
	return out, nil
}
