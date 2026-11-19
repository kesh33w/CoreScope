package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

// Saved policy, not a claim about the ingestor's currently loaded configuration.
type observerFrequencyRule struct {
	MHz             float64 `json:"mhz"`
	ToleranceKHz    float64 `json:"toleranceKHz,omitempty"`
	MaxStatusAgeSec int     `json:"maxStatusAgeSec,omitempty"`
	DropUnknown     bool    `json:"dropUnknown,omitempty"`
}
type observerFrequencyConfigResponse struct {
	Rules           map[string]observerFrequencyRule `json:"rules"`
	RestartRequired bool                             `json:"restartRequired"`
	WriteEnabled    bool                             `json:"writeEnabled"`
}

var frequencyRegionPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func validateObserverFrequencyRules(rules map[string]observerFrequencyRule) error {
	if len(rules) > 64 {
		return fmt.Errorf("at most 64 region rules are allowed")
	}
	for region, r := range rules {
		if !frequencyRegionPattern.MatchString(region) {
			return fmt.Errorf("region must use exactly three uppercase letters")
		}
		if math.IsNaN(r.MHz) || math.IsInf(r.MHz, 0) || r.MHz < 100 || r.MHz > 2500 {
			return fmt.Errorf("MHz must be between 100 and 2500")
		}
		if math.IsNaN(r.ToleranceKHz) || math.IsInf(r.ToleranceKHz, 0) || r.ToleranceKHz < 0 || r.ToleranceKHz > 100 {
			return fmt.Errorf("toleranceKHz must be between 0 and 100 (0 uses default)")
		}
		if r.MaxStatusAgeSec < 0 || r.MaxStatusAgeSec > 86400 {
			return fmt.Errorf("maxStatusAgeSec must be between 0 and 86400 (0 uses default)")
		}
	}
	return nil
}
func frequencyConfigPath(dir string) (string, error) {
	for _, p := range []string{filepath.Join(dir, "config.json"), filepath.Join(dir, "data", "config.json")} {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
				return resolved, nil
			}
		}
	}
	return "", fmt.Errorf("config.json not found")
}
func readFrequencyConfig(dir string) (string, map[string]json.RawMessage, error) {
	p, err := frequencyConfigPath(dir)
	if err != nil {
		return "", nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", nil, err
	}
	var raw map[string]json.RawMessage
	err = json.Unmarshal(b, &raw)
	if raw == nil && err == nil {
		err = fmt.Errorf("config must be an object")
	}
	return p, raw, err
}
func (s *Server) handleGetObserverFrequency(w http.ResponseWriter, r *http.Request) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	_, raw, err := readFrequencyConfig(s.configDir)
	if err != nil {
		writeError(w, 500, "cannot read frequency policy")
		return
	}
	rules := make(map[string]observerFrequencyRule)
	if b := raw["observerFrequencyRules"]; len(b) > 0 {
		if err = json.Unmarshal(b, &rules); err != nil {
			writeError(w, 500, "invalid frequency policy")
			return
		}
	}
	if rules == nil {
		rules = make(map[string]observerFrequencyRule)
	}
	writeJSON(w, observerFrequencyConfigResponse{Rules: rules, RestartRequired: true, WriteEnabled: s.cfg != nil && s.cfg.APIKey != "" && !IsWeakAPIKey(s.cfg.APIKey)})
}
func (s *Server) handlePutObserverFrequency(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	var body struct {
		Rules map[string]observerFrequencyRule `json:"rules"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, 400, "invalid frequency policy JSON")
		return
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(w, 400, "expected one JSON object")
		return
	}
	if body.Rules == nil {
		writeError(w, 400, "rules object required; use {} to disable all rules")
		return
	}
	if err := validateObserverFrequencyRules(body.Rules); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	p, raw, err := readFrequencyConfig(s.configDir)
	if err != nil {
		writeError(w, 500, "cannot read frequency policy")
		return
	}
	raw["observerFrequencyRules"], err = json.Marshal(body.Rules)
	if err != nil {
		writeError(w, 500, "cannot encode frequency policy")
		return
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		writeError(w, 500, "cannot encode config")
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		writeError(w, 500, "cannot read config permissions")
		return
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".frequency-config-*")
	if err != nil {
		writeError(w, 500, "cannot save config")
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	err = f.Chmod(info.Mode().Perm())
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		copyOwner(tmp, info)
		err = os.Rename(tmp, p)
	}
	if err != nil {
		writeError(w, 500, "cannot save config")
		return
	}
	writeJSON(w, observerFrequencyConfigResponse{Rules: body.Rules, RestartRequired: true, WriteEnabled: true})
}
