package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Comparisons are on-demand reads, never an ingest task or a second packet store.
// Bound candidate rows, returned rows, duration and concurrency independently.
const comparisonScanCap = 50000
const comparisonObserverCap = 10000

var comparisonSlots = make(chan struct{}, 2)

type ComparisonReception struct {
	ID          int64    `json:"id"`
	ObserverID  string   `json:"observer_id"`
	Hash        string   `json:"hash"`
	Timestamp   float64  `json:"timestamp"`
	RawHex      string   `json:"raw_hex"`
	PathJSON    string   `json:"path_json"`
	SNR         *float64 `json:"snr"`
	RouteType   int      `json:"route_type"`
	PayloadType int      `json:"payload_type"`
}
type ObserverComparisonResponse struct {
	Packets     []ComparisonReception `json:"packets"`
	Since       string                `json:"since"`
	Until       string                `json:"until"`
	Scanned     int                   `json:"scanned"`
	ScanCap     int                   `json:"scan_cap"`
	ObserverCap int                   `json:"observer_cap"`
	Truncated   bool                  `json:"truncated"`
}

func (s *Server) handleObserverComparison(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a, b := strings.TrimSpace(q.Get("a")), strings.TrimSpace(q.Get("b"))
	since, e1 := time.Parse(time.RFC3339Nano, q.Get("since"))
	until, e2 := time.Parse(time.RFC3339Nano, q.Get("until"))
	if a == "" || b == "" || a == b || len(a) > 256 || len(b) > 256 || e1 != nil || e2 != nil || !until.After(since) || until.Sub(since) > 24*time.Hour {
		writeError(w, 400, "Choose two different observers and a time window of at most 24 hours.")
		return
	}
	if s.cfg != nil && (s.cfg.IsObserverBlacklisted(a) || s.cfg.IsObserverBlacklisted(b)) {
		writeError(w, 404, "Observer not found")
		return
	}
	if s.db == nil {
		writeError(w, 503, "Reception database is unavailable.")
		return
	}
	if !s.db.isV3 {
		writeError(w, 501, "Reception comparisons require a v3 observation database.")
		return
	}
	select {
	case comparisonSlots <- struct{}{}:
		defer func() { <-comparisonSlots }()
	default:
		writeError(w, 429, "Comparison busy. Try again shortly.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	result, err := s.db.queryObserverComparison(ctx, []string{a, b}, since, until, comparisonScanCap, comparisonObserverCap)
	if err != nil {
		writeError(w, 503, "Comparison could not finish. Try a shorter time window.")
		return
	}
	writeJSON(w, result)
}

func (db *DB) queryObserverComparison(ctx context.Context, ids []string, since, until time.Time, scanCap, observerCap int) (*ObserverComparisonResponse, error) {
	result := &ObserverComparisonResponse{Packets: make([]ComparisonReception, 0), Since: since.UTC().Format(time.RFC3339Nano), Until: until.UTC().Format(time.RFC3339Nano), ScanCap: scanCap, ObserverCap: observerCap}
	// The inner LIMIT is a hard bound on candidates even for an idle observer in
	// a busy region. The timestamp index avoids walking that observer's history.
	// Do not COUNT(*), load decoded_json, or substitute a canonical transmission
	// frame for absent reception-specific bytes. That would invent paired SNR.
	if !db.isV3 {
		return nil, errors.New("reception comparison requires v3 numeric timestamps")
	}
	observerCol, join := "obs.id", "obs.rowid=o.observer_idx"
	lo, hi := float64(since.UnixNano())/1e9, float64(until.UnixNano())/1e9
	stamp := "o.timestamp"
	raw := "NULL"
	if db.hasObsRawHex {
		raw = "o.raw_hex"
	}
	query := fmt.Sprintf(`SELECT o.id,%s,%s,t.hash,%s,o.path_json,o.snr,t.route_type,t.payload_type,o.direction
 FROM (SELECT * FROM observations INDEXED BY idx_observations_timestamp WHERE timestamp>=? AND timestamp<? ORDER BY timestamp DESC LIMIT ?) o
 LEFT JOIN observers obs ON %s
 LEFT JOIN transmissions t ON t.id=o.transmission_id AND %s IN (?,?)`, observerCol, stamp, raw, join, observerCol)
	rows, err := db.conn.QueryContext(ctx, query, lo, hi, scanCap+1, ids[0], ids[1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		if result.Scanned == scanCap {
			result.Truncated = true
			break
		}
		result.Scanned++
		var p ComparisonReception
		var id, hash, rawHex, path, direction sql.NullString
		var ts, snr sql.NullFloat64
		var route, payload sql.NullInt64
		if err := rows.Scan(&p.ID, &id, &ts, &hash, &rawHex, &path, &snr, &route, &payload, &direction); err != nil {
			return nil, err
		}
		if direction.String == "tx" || !hash.Valid || (id.String != ids[0] && id.String != ids[1]) {
			continue
		}
		if counts[id.String] >= observerCap {
			result.Truncated = true
			continue
		}
		counts[id.String]++
		p.ObserverID = id.String
		p.Hash = hash.String
		p.Timestamp = ts.Float64
		p.RawHex = rawHex.String
		p.PathJSON = path.String
		p.RouteType = -1 // Unknown without reception-specific header bytes.
		if len(p.RawHex) >= 2 {
			if header, err := hex.DecodeString(p.RawHex[:2]); err == nil {
				p.RouteType = decodeHeader(header[0]).RouteType
			}
		}
		p.PayloadType = int(payload.Int64)
		if snr.Valid {
			v := snr.Float64
			p.SNR = &v
		}
		result.Packets = append(result.Packets, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
