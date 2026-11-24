// Package main: frequency mode for the region filter.
//
// By default the region filter (?region=, the dropdown on every page) groups
// observers by their MQTT IATA code. With config regionFilter.mode
// "frequency" it groups them by the frequency their radio reports AND by
// their IATA code, so one deployment that aggregates several networks
// (909.875, 910.525, 927.875 MHz, ...) can be viewed one network at a time,
// and one region of a network at a time.
//
// In frequency mode ?region= takes both kinds of token: numbers are
// frequencies in MHz, anything else an IATA code ("910.525,SJC,OAK"). An
// observer matches when its frequency is one of the selected frequencies (or
// none is selected) and its IATA code is one of the selected codes (or none
// is selected). Each observer's region key is "<frequency>|<IATA>"
// (observerRegionKey); region caches, node home-region placements and SQL
// filters all key on it. Iata mode keeps the original keys and SQL.
//
// The switch is deployment-wide rather than per request. An observer's
// frequency is its CURRENT radio setting (observers.radio, the last one it
// reported). An observer that retunes moves to the new frequency along with
// the packets it heard before; one that never reported a radio setting
// matches only selections without a frequency. The dropdowns list the
// frequencies and codes of observers seen in the last
// regionFrequencyListWindow; config regions can give a frequency a label
// ("910.525": "US 910").
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
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

// observerRegionKey is an observer's region key in frequency mode:
// "<frequency>|<IATA>", either part possibly empty.
func observerRegionKey(freq, iata string) string { return freq + "|" + iata }

// regionSelection is a normalised ?region= value in frequency mode: the
// frequencies and the IATA codes it names. An empty set does not constrain.
type regionSelection struct {
	freqs, iatas map[string]bool
}

// isFrequencyToken reports whether a normalised region token is a frequency
// (normalizeRegionCodes leaves those in canonical numeric form).
func isFrequencyToken(code string) bool { return canonicalFrequencyToken(code) != "" }

func newRegionSelection(codes []string) regionSelection {
	sel := regionSelection{freqs: map[string]bool{}, iatas: map[string]bool{}}
	for _, c := range codes {
		if isFrequencyToken(c) {
			sel.freqs[canonicalFrequencyToken(c)] = true
		} else {
			sel.iatas[c] = true
		}
	}
	return sel
}

// matches reports whether an observer on freq with code iata is selected.
func (sel regionSelection) matches(freq, iata string) bool {
	if len(sel.freqs) > 0 && !sel.freqs[freq] {
		return false
	}
	if len(sel.iatas) > 0 && !sel.iatas[iata] {
		return false
	}
	return len(sel.freqs) > 0 || len(sel.iatas) > 0
}

// matchesKey is matches for a region key: "<frequency>|<IATA>" in frequency
// mode, a bare IATA code in iata mode.
func (sel regionSelection) matchesKey(key string) bool {
	freq, iata, composite := strings.Cut(key, "|")
	if !composite {
		freq, iata = "", key
	}
	return sel.matches(freq, iata)
}

// frequencyOnlySelection reports whether normalised codes name at least one
// frequency and no IATA code.
func frequencyOnlySelection(codes []string) bool {
	sel := newRegionSelection(codes)
	return len(sel.freqs) > 0 && len(sel.iatas) == 0
}

// frequencyOnlyKeys maps frequency-mode region keys to the frequency alone
// ("910.525|SJC" → "910.525"), leaving out observers without one.
//
// Node home regions are chosen by plurality over region keys
// (node_home_regions.go). Over "<frequency>|<IATA>" keys a node heard by
// several cities on one frequency has its hearings split between them, so a
// frequency-only selection places nodes over these keys instead, exactly as
// before regions could be combined with frequencies.
func frequencyOnlyKeys(keys map[string]string) map[string]string {
	out := make(map[string]string, len(keys))
	for id, k := range keys {
		if freq, _, _ := strings.Cut(k, "|"); freq != "" {
			out[id] = freq
		}
	}
	return out
}

// regionKeyMatches reports whether a region key (an observer's or a node's
// home region) is in the normalised selection codes. A bare IATA key, as in
// iata mode, is compared as before: equal to one of the codes. A bare
// frequency key (frequencyOnlyKeys) matches only a frequency-only selection.
func regionKeyMatches(key string, codes []string) bool {
	if key == "" || len(codes) == 0 {
		return false
	}
	if isFrequencyToken(key) {
		return frequencyOnlySelection(codes) && newRegionSelection(codes).freqs[key]
	}
	if !strings.Contains(key, "|") {
		for _, c := range codes {
			if c == key {
				return true
			}
		}
		return false
	}
	return newRegionSelection(codes).matchesKey(key)
}

// observerRegionKeys returns observer ID → region key: "<frequency>|<IATA>"
// in frequency mode, else the trimmed, upper-cased IATA code. Observers
// without one are absent. When recentSince is non-empty only observers with
// last_seen after it are returned.
func (db *DB) observerRegionKeys(recentSince string) (map[string]string, error) {
	q := "SELECT id, iata, '' FROM observers WHERE iata IS NOT NULL AND iata != ''"
	if db.regionByFrequency {
		q = "SELECT id, COALESCE(iata, ''), COALESCE(radio, '') FROM observers WHERE 1"
	}
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
		var id, iata, radio string
		if err := rows.Scan(&id, &iata, &radio); err != nil {
			return nil, err
		}
		iata = strings.TrimSpace(strings.ToUpper(iata))
		if !db.regionByFrequency {
			if iata != "" {
				out[id] = iata
			}
			continue
		}
		freq := frequencyRegionKey(radio)
		if freq != "" || iata != "" {
			out[id] = observerRegionKey(freq, iata)
		}
	}
	return out, rows.Err()
}

// regionObserverIDs returns the IDs of observers in the selection codes
// (frequency mode only; iata mode matches in SQL).
func (db *DB) regionObserverIDs(codes []string) ([]string, error) {
	keys, err := db.observerRegionKeys("")
	if err != nil {
		return nil, err
	}
	sel := newRegionSelection(codes)
	ids := make([]string, 0, len(keys))
	for id, k := range keys {
		if sel.matchesKey(k) {
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
// frequency and each IATA code a recently seen observer reports. Frequencies
// are labelled from config regions when set, else "<MHz> MHz"; codes from
// config regions when set, else the code. It also returns, for the
// frequency dropdown to narrow the region one, the codes on each frequency.
func (s *Server) regionFilterOptions() (map[string]string, map[string][]string) {
	out := make(map[string]string)
	byFreq := make(map[string][]string)
	since := time.Now().Add(-regionFrequencyListWindow).UTC().Format(time.RFC3339)
	keys, err := s.db.observerRegionKeys(since)
	if err != nil {
		return out, byFreq
	}
	labels := make(map[string]string, len(s.cfg.Regions))
	for k, v := range s.cfg.Regions {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if f := canonicalFrequencyToken(k); f != "" {
			labels[f] = v
		} else {
			labels[strings.TrimSpace(strings.ToUpper(k))] = v
		}
	}
	label := func(code, fallback string) string {
		if l, ok := labels[code]; ok {
			return l
		}
		return fallback
	}
	seen := make(map[string]bool)
	for _, k := range keys {
		freq, iata, _ := strings.Cut(k, "|")
		if freq != "" {
			out[freq] = label(freq, freq+" MHz")
		}
		if iata != "" {
			out[iata] = label(iata, iata)
		}
		if freq != "" && iata != "" && !seen[k] {
			seen[k] = true
			byFreq[freq] = append(byFreq[freq], iata)
		}
	}
	for _, codes := range byFreq {
		sort.Strings(codes)
	}
	return out, byFreq
}

// mergeFrequencyPlacements merges node region placements computed over
// frequency-only keys (byFreq) into those over "<frequency>|<IATA>" keys
// (byPair): each node's Regions hold both kinds, and its basis and hops come
// from byFreq, as before regions could be combined with frequencies.
// RegionNodePubkeys matches a frequency-only selection against the bare
// frequencies and any other against the pairs.
func mergeFrequencyPlacements(byFreq, byPair map[string]NodeRegionPlacement) map[string]NodeRegionPlacement {
	out := make(map[string]NodeRegionPlacement, len(byFreq)+len(byPair))
	for pk, p := range byPair {
		out[pk] = p
	}
	for pk, f := range byFreq {
		p, ok := byPair[pk]
		if ok {
			regions := append(append([]string{}, f.Regions...), p.Regions...)
			sort.Strings(regions)
			f.Regions = regions
		}
		out[pk] = f
	}
	return out
}
