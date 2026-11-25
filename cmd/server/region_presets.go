// Package main: region presets (frequency mode only).
//
// A preset names a fixed selection of frequencies and IATA codes under one
// code, e.g. "BAY910" = 910.525 + OAK, SFO, SJC. Clients that send a single
// ?region= value (the NodeScope app) can then pick "910.525 in the Bay
// Area", which otherwise takes two tokens. Presets are configured under
// regionFilter.presets and apply only in frequency mode.
//
// The server expands preset codes in ?region= before any handler reads it
// (regionPresetMiddleware), so every endpoint, cache and SQL filter sees the
// plain tokens. /api/config/regions lists each preset code with its label,
// and /api/config/region-quick-picks returns the presets so the web UI can
// show them and expand them itself for its client-side filters.
//
// Tokens from several presets are combined like any other selection (OR
// within frequencies, OR within codes, AND across the two), so choosing
// BAY910 and SOCAL927 together also matches 927.875 observers in the Bay.
package main

import (
	"log"
	"net/http"
	"strings"
)

// RegionPreset is one entry of config regionFilter.presets.
type RegionPreset struct {
	Code    string   `json:"code"`
	Label   string   `json:"label,omitempty"`
	Regions []string `json:"regions"`
}

// RegionPresets returns the usable presets, normalised: codes trimmed and
// upper-cased, regions canonicalised like ?region= tokens. It skips a preset
// (logRegionPresets reports it at startup) whose code is empty, "ALL", a number,
// contains ',' or '|', repeats an earlier code, or has no regions; a region
// naming another preset is dropped, since presets do not nest. Nil outside
// frequency mode.
func (c *Config) RegionPresets() []RegionPreset {
	if !c.RegionByFrequency() || len(c.RegionFilter.Presets) == 0 {
		return nil
	}
	presetCode := func(raw string) string {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code == "" || code == "ALL" || canonicalFrequencyToken(code) != "" ||
			strings.ContainsAny(code, ",|") {
			return ""
		}
		return code
	}
	codes := make(map[string]bool, len(c.RegionFilter.Presets))
	for _, p := range c.RegionFilter.Presets {
		if code := presetCode(p.Code); code != "" {
			codes[code] = true
		}
	}
	out := make([]RegionPreset, 0, len(c.RegionFilter.Presets))
	seen := make(map[string]bool)
	for _, p := range c.RegionFilter.Presets {
		code := presetCode(p.Code)
		if code == "" || seen[code] {
			continue
		}
		var regions []string
		have := make(map[string]bool)
		for _, r := range normalizeRegionCodes(strings.Join(p.Regions, ",")) {
			if codes[r] || have[r] {
				continue
			}
			have[r] = true
			regions = append(regions, r)
		}
		if len(regions) == 0 {
			continue
		}
		seen[code] = true
		label := strings.TrimSpace(p.Label)
		if label == "" {
			label = code
		}
		out = append(out, RegionPreset{Code: code, Label: label, Regions: regions})
	}
	return out
}

// expandRegionPresets replaces each preset code in a ?region= value with the
// preset's tokens, dropping repeats. It returns the value unchanged when it
// names no preset.
func expandRegionPresets(param string, presets []RegionPreset) string {
	if param == "" || len(presets) == 0 {
		return param
	}
	byCode := make(map[string][]string, len(presets))
	for _, p := range presets {
		byCode[p.Code] = p.Regions
	}
	tokens := strings.Split(param, ",")
	out := make([]string, 0, len(tokens))
	seen := make(map[string]bool)
	expanded := false
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if regions, ok := byCode[strings.ToUpper(t)]; ok {
			expanded = true
			for _, r := range regions {
				add(r)
			}
			continue
		}
		add(t)
	}
	if !expanded {
		return param
	}
	return strings.Join(out, ",")
}

// regionPresetMiddleware rewrites ?region= with preset codes expanded, so
// handlers never see a preset code.
func (s *Server) regionPresetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presets := s.cfg.RegionPresets()
		if len(presets) > 0 {
			q := r.URL.Query()
			if v := q.Get("region"); v != "" {
				if nv := expandRegionPresets(v, presets); nv != v {
					q.Set("region", nv)
					r.URL.RawQuery = q.Encode()
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// logRegionPresets reports, at startup, presets that RegionPresets skipped.
func logRegionPresets(c *Config) {
	if !c.RegionByFrequency() || len(c.RegionFilter.Presets) == 0 {
		if c != nil && c.RegionFilter != nil && len(c.RegionFilter.Presets) > 0 {
			log.Printf("[config] regionFilter.presets ignored: they need regionFilter.mode \"frequency\"")
		}
		return
	}
	kept := make(map[string]bool)
	for _, p := range c.RegionPresets() {
		kept[p.Code] = true
	}
	for _, p := range c.RegionFilter.Presets {
		if !kept[strings.ToUpper(strings.TrimSpace(p.Code))] {
			log.Printf("[config] regionFilter.presets: skipping %q (empty, numeric, duplicate or reserved code, or no regions)", p.Code)
		}
	}
	log.Printf("[config] regionFilter.presets: %d active", len(kept))
}
