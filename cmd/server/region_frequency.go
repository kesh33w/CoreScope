// Package main: frequency mode for the region filter.
//
// By default the region filter (?region=, the dropdown on every page) groups
// observers by their MQTT IATA code. With config regionFilter.mode
// "frequency" it groups them by the frequency their radio reports instead, so
// one deployment that aggregates several networks (909.875, 910.525, 927.875
// MHz, ...) can be viewed one network at a time.
//
// The switch is deployment-wide rather than per request: the same ?region=
// parameter, region caches, node home-region placements and SQL filters all
// key on one observer → region-key mapping (observerRegionKeys), which is the
// IATA code in iata mode and the frequency in frequency mode. Iata mode keeps
// the original SQL unchanged.
//
// An observer's frequency is its CURRENT radio setting (observers.radio, the
// last one it reported). An observer that retunes moves to the new frequency
// along with the packets it heard before; an observer that never reported a
// radio setting is in no frequency. The dropdown lists the frequencies of
// observers seen in the last regionFrequencyListWindow; config regions can
// give a frequency a label ("910.525": "US 910").
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// RegionFilterConfig is config regionFilter.
type RegionFilterConfig struct {
	// Mode is "iata" (default) or "frequency".
	Mode string `json:"mode,omitempty"`
}

// RegionByFrequency reports whether regionFilter.mode is "frequency".
func (c *Config) RegionByFrequency() bool {
	return c != nil && c.RegionFilter != nil &&
		strings.EqualFold(strings.TrimSpace(c.RegionFilter.Mode), "frequency")
}

// regionFrequencyListWindow bounds the dropdown to observers seen recently,
// so a long-gone observer's one-off setting does not stay listed forever.
// Filtering by an unlisted frequency still works.
const regionFrequencyListWindow = 7 * 24 * time.Hour

// frequencyRegionKey returns the region key for a radio setting
// ("910.525,62.5,7,5" → "910.525"), or "" when it has no usable frequency.
// The first field is read like the ingestor's observerFrequencyRules
// (parseObserverFrequency): an explicit MHz/kHz/Hz unit, or a bare number
// scaled by magnitude, since some uploaders report 910525000 or 910525. It is
// rounded to kHz, so one setting reported with different float spellings
// lands on one key.
func frequencyRegionKey(radio string) string {
	s := strings.ToLower(strings.TrimSpace(strings.SplitN(radio, ",", 2)[0]))
	scale := 0.0
	switch {
	case strings.HasSuffix(s, "mhz"):
		s, scale = strings.TrimSpace(strings.TrimSuffix(s, "mhz")), 1
	case strings.HasSuffix(s, "khz"):
		s, scale = strings.TrimSpace(strings.TrimSuffix(s, "khz")), 1e-3
	case strings.HasSuffix(s, "hz"):
		s, scale = strings.TrimSpace(strings.TrimSuffix(s, "hz")), 1e-6
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return ""
	}
	if scale == 0 {
		scale = 1
		if f >= 1e7 {
			scale = 1e-6
		} else if f >= 1e4 {
			scale = 1e-3
		}
	}
	f *= scale
	if f < 100 || f > 2500 {
		return ""
	}
	return strconv.FormatFloat(math.Round(f*1000)/1000, 'f', -1, 64)
}

// canonicalFrequencyToken returns a ?region= token that is a frequency in
// MHz in its canonical form ("910.5250" → "910.525"), or "" when tok is not
// one.
func canonicalFrequencyToken(tok string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(tok), 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return ""
	}
	return strconv.FormatFloat(math.Round(f*1000)/1000, 'f', -1, 64)
}

// SetRegionByFrequency switches the DB's region filters to frequency mode.
// Call once at startup, before serving.
func (db *DB) SetRegionByFrequency(on bool) { db.regionByFrequency = on }

// observerRegionKeys returns observer ID → region key: the frequency in
// frequency mode, else the trimmed, upper-cased IATA code. Observers without
// one are absent. When recentSince is non-empty only observers with
// last_seen after it are returned.
func (db *DB) observerRegionKeys(recentSince string) (map[string]string, error) {
	col := "iata"
	if db.regionByFrequency {
		col = "radio"
	}
	q := "SELECT id, " + col + " FROM observers WHERE " + col + " IS NOT NULL AND " + col + " != ''"
	var args []interface{}
	if recentSince != "" {
		q += " AND last_seen > ?"
		args = append(args, recentSince)
	}
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, 64)
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		var key string
		if db.regionByFrequency {
			key = frequencyRegionKey(v)
		} else {
			key = strings.TrimSpace(strings.ToUpper(v))
		}
		if key != "" {
			out[id] = key
		}
	}
	return out, rows.Err()
}

// regionObserverIDs returns the IDs of observers whose region key is one of
// codes (frequency mode only; iata mode matches in SQL).
func (db *DB) regionObserverIDs(codes []string) ([]string, error) {
	keys, err := db.observerRegionKeys("")
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(codes))
	for _, c := range codes {
		want[c] = true
	}
	ids := make([]string, 0, len(keys))
	for id, k := range keys {
		if want[k] {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// regionObsCond returns a SQL condition, and its arguments, that is true for
// an observers row (table alias alias; "" for none) in one of codes. See
// regionObsMatcher.
func (db *DB) regionObsCond(alias string, codes []string) (string, []interface{}, error) {
	cond, args, err := db.regionObsMatcher(codes)
	if err != nil {
		return "", nil, err
	}
	return cond(alias), args, nil
}

// regionObsMatcher returns a builder for the SQL condition "this observers
// row (alias) is in one of codes", and the arguments every such condition
// takes, so a statement can use it under several aliases with one lookup. In
// iata mode it is the original UPPER(TRIM(iata)) IN (...); in frequency mode
// the matching observer IDs are resolved here and passed as one JSON
// parameter. codes must be non-empty and normalised (normalizeRegionCodes).
func (db *DB) regionObsMatcher(codes []string) (func(alias string) string, []interface{}, error) {
	col := func(alias, c string) string {
		if alias == "" {
			return c
		}
		return alias + "." + c
	}
	if !db.regionByFrequency {
		args := make([]interface{}, len(codes))
		for i, c := range codes {
			args[i] = c
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(codes)), ",")
		return func(alias string) string {
			return fmt.Sprintf("UPPER(TRIM(%s)) IN (%s)", col(alias, "iata"), ph)
		}, args, nil
	}
	ids, err := db.regionObserverIDs(codes)
	if err != nil {
		return nil, nil, err
	}
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, nil, err
	}
	return func(alias string) string {
		return col(alias, "id") + " IN (SELECT value FROM json_each(?))"
	}, []interface{}{string(idsJSON)}, nil
}

// regionWhere is regionObsCond for a raw ?region= value in a WHERE builder
// that cannot return an error: an empty selection or a failed lookup matches
// no observer.
func (db *DB) regionWhere(alias, regionParam string) (string, []interface{}) {
	codes := normalizeRegionCodes(regionParam)
	if len(codes) == 0 {
		return "1", nil
	}
	cond, args, err := db.regionObsCond(alias, codes)
	if err != nil {
		return "0", nil
	}
	return cond, args
}

// regionFilterOptions is GET /api/config/regions in frequency mode: each
// frequency a recently seen observer reports, labelled from config regions
// when set, else "<MHz> MHz".
func (s *Server) regionFilterOptions() map[string]string {
	out := make(map[string]string)
	since := time.Now().Add(-regionFrequencyListWindow).UTC().Format(time.RFC3339)
	keys, err := s.db.observerRegionKeys(since)
	if err != nil {
		return out
	}
	labels := make(map[string]string, len(s.cfg.Regions))
	for k, v := range s.cfg.Regions {
		if f := canonicalFrequencyToken(k); f != "" {
			labels[f] = v
		}
	}
	for _, f := range keys {
		if l, ok := labels[f]; ok && strings.TrimSpace(l) != "" {
			out[f] = l
		} else {
			out[f] = f + " MHz"
		}
	}
	return out
}
