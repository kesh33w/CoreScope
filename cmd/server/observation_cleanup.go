package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

// Cleanup API is read-only against measurement data. Commands are handed to
// the single writer via a private filesystem queue, like geo-pruning.
type cleanupPreview struct {
	Selection              prunequeue.CleanupSelection `json:"selection"`
	MaxObservationID       int64                       `json:"maxObservationId"`
	Count                  int64                       `json:"count"`
	Transmissions          int64                       `json:"transmissions"`
	ExclusiveTransmissions int64                       `json:"exclusiveTransmissions"`
	RestartRequired        bool                        `json:"restartRequired"`
}
type cleanupAccepted struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
	StatusURL string `json:"statusUrl"`
}
type cleanupStatus struct {
	RequestID       string `json:"requestId"`
	Status          string `json:"status"`
	Deleted         int64  `json:"deleted"`
	Restored        int64  `json:"restored"`
	ArchiveID       string `json:"archiveId,omitempty"`
	Error           string `json:"error,omitempty"`
	RestartRequired bool   `json:"restartRequired"`
}

func cleanupDecode(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		writeError(w, 400, "invalid cleanup request")
		return false
	}
	if e := d.Decode(new(json.RawMessage)); e != io.EOF {
		writeError(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func (s *Server) handleCleanupCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, struct {
		WriteEnabled              bool `json:"writeEnabled"`
		FrequencyHistoryAvailable bool `json:"frequencyHistoryAvailable"`
		RestartRequired           bool `json:"restartRequired"`
	}{s.cfg != nil && s.cfg.APIKey != "" && !IsWeakAPIKey(s.cfg.APIKey), false, true})
}
func (s *Server) cleanupPreview(ctx context.Context, sel prunequeue.CleanupSelection) (cleanupPreview, error) {
	p := cleanupPreview{Selection: sel, MaxObservationID: sel.MaxObservationID, RestartRequired: true}
	where, args := sel.Where()
	q := `SELECT COUNT(*),COUNT(DISTINCT o.transmission_id) FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE ` + where
	if e := s.db.conn.QueryRowContext(ctx, q, args...).Scan(&p.Count, &p.Transmissions); e != nil {
		return p, e
	}
	// Count only transmissions whose ENTIRE retained observation set is selected.
	// Index on transmission_id bounds this anti-join to the selected observer's
	// transmissions; no per-observation HTTP or unbounded result materialization.
	q = `WITH selected AS (SELECT o.id,o.transmission_id FROM observations o JOIN observers ob ON ob.rowid=o.observer_idx WHERE ` + where + `) SELECT COUNT(*) FROM (SELECT DISTINCT transmission_id FROM selected) t WHERE NOT EXISTS (SELECT 1 FROM observations other WHERE other.transmission_id=t.transmission_id AND NOT EXISTS (SELECT 1 FROM selected x WHERE x.id=other.id))`
	e := s.db.conn.QueryRowContext(ctx, q, args...).Scan(&p.ExclusiveTransmissions)
	return p, e
}
func (s *Server) handleCleanupPreview(w http.ResponseWriter, r *http.Request) {
	var sel prunequeue.CleanupSelection
	if !cleanupDecode(w, r, &sel) {
		return
	}
	sel.ObserverID = strings.TrimSpace(sel.ObserverID)
	sel.IATA = strings.ToUpper(strings.TrimSpace(sel.IATA))
	if e := sel.Validate(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if e := s.db.conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM observations").Scan(&sel.MaxObservationID); e != nil {
		writeError(w, 500, "cannot read observation snapshot")
		return
	}
	now := time.Now().Unix()
	if sel.To == 0 || sel.To > now {
		sel.To = now
	}
	if e := sel.Validate(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	p, e := s.cleanupPreview(ctx, sel)
	if e != nil {
		writeError(w, 500, "cannot preview observation cleanup")
		return
	}
	writeJSON(w, p)
}
func (s *Server) handleCleanupSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Selection        prunequeue.CleanupSelection `json:"selection"`
		MaxObservationID int64                       `json:"maxObservationId"`
		ExpectedCount    int64                       `json:"expectedCount"`
	}
	if !cleanupDecode(w, r, &body) {
		return
	}
	body.Selection.MaxObservationID = body.MaxObservationID
	if e := body.Selection.Validate(); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	if body.MaxObservationID <= 0 || body.ExpectedCount <= 0 || body.Selection.To <= 0 || body.Selection.To > time.Now().Unix() {
		writeError(w, 400, "nonempty preview snapshot required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, e := s.cleanupPreview(ctx, body.Selection)
	if e != nil {
		writeError(w, 500, "cannot recheck cleanup snapshot")
		return
	}
	if p.Count != body.ExpectedCount {
		writeError(w, http.StatusConflict, "selection changed; preview again")
		return
	}
	req := prunequeue.CleanupRequest{ID: prunequeue.NewID(), RequestedAt: time.Now().UTC(), Selection: body.Selection}
	if e := prunequeue.WriteCleanupRequest(s.db.path, req); e != nil {
		writeError(w, 500, "cannot queue cleanup")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, cleanupAccepted{req.ID, "pending", "/api/admin/observation-cleanup/status?id=" + req.ID})
}
func (s *Server) handleCleanupRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !cleanupDecode(w, r, &body) {
		return
	}
	res, e := prunequeue.ReadCleanupResult(s.db.path, body.ID)
	if e != nil {
		writeError(w, 400, "invalid archive id")
		return
	}
	if res == nil || res.ArchiveID == "" {
		writeError(w, 404, "cleanup archive not found")
		return
	}
	req := prunequeue.CleanupRequest{ID: prunequeue.NewID(), RequestedAt: time.Now().UTC(), RestoreID: res.ArchiveID}
	if e := prunequeue.WriteCleanupRequest(s.db.path, req); e != nil {
		writeError(w, 500, "cannot queue restore")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, cleanupAccepted{req.ID, "pending", "/api/admin/observation-cleanup/status?id=" + req.ID})
}
func (s *Server) handleCleanupStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	res, e := prunequeue.ReadCleanupResult(s.db.path, id)
	if e != nil {
		writeError(w, 400, "invalid cleanup id or unreadable result")
		return
	}
	if res != nil {
		state := "done"
		if res.Error != "" {
			state = "error"
		}
		writeJSON(w, cleanupStatus{id, state, res.Deleted, res.Restored, res.ArchiveID, res.Error, res.Deleted > 0 || res.Restored > 0})
		return
	}
	pending, e := prunequeue.CleanupRequestExists(s.db.path, id)
	if e != nil {
		writeError(w, 400, "invalid cleanup id")
		return
	}
	if !pending {
		writeError(w, 404, "cleanup job not found")
		return
	}
	writeJSON(w, cleanupStatus{RequestID: id, Status: "pending"})
}
