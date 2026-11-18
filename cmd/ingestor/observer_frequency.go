package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ObserverFrequencyRule applies to the observer topic region, never the packet's
// destination or decoded node region. Frequencies are MHz. Unknown information
// is separately configurable: a missing status does not prove a mismatch.
type ObserverFrequencyRule struct {
	MHz             float64 `json:"mhz"`
	ToleranceKHz    float64 `json:"toleranceKHz,omitempty"`
	MaxStatusAgeSec int     `json:"maxStatusAgeSec,omitempty"`
	DropUnknown     bool    `json:"dropUnknown,omitempty"`
}
type observerFrequencyKey struct{ Source, Region, Observer string }
type observerFrequencyState struct {
	At     time.Time
	Radios map[string]float64
}

const maxFrequencyObservers = 8192

func (c *Config) frequencyRule(region string) (ObserverFrequencyRule, bool) {
	if c == nil {
		return ObserverFrequencyRule{}, false
	}
	r, ok := c.ObserverFrequencyRules[strings.ToUpper(strings.TrimSpace(region))]
	return r, ok
}
func (r ObserverFrequencyRule) maxAge() time.Duration {
	if r.MaxStatusAgeSec > 0 {
		return time.Duration(r.MaxStatusAgeSec) * time.Second
	}
	return 30 * time.Minute
}
func frequencyKey(tag string, parts []string, msg map[string]interface{}) observerFrequencyKey {
	return observerFrequencyKey{tag, strings.ToUpper(parts[1]), observerIDForMessage(parts, msg)}
}

// parseObserverFrequency accepts the standard radio tuple (first field MHz),
// or an explicit unit. Unlabelled Hz/kHz values are normalized by magnitude.
func parseObserverFrequency(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(strings.SplitN(s, ",", 2)[0]))
	scale := 1.0
	switch {
	case strings.HasSuffix(s, "mhz"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "mhz"))
	case strings.HasSuffix(s, "khz"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "khz"))
		scale = 1e-3
	case strings.HasSuffix(s, "hz"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "hz"))
		scale = 1e-6
	default:
		scale = 0
	}
	f, e := strconv.ParseFloat(s, 64)
	if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, false
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
	return f, f >= 100 && f <= 2500
}
func (c *Config) recordObserverFrequency(tag string, parts []string, msg map[string]interface{}, retained bool, now time.Time) {
	if len(parts) < 3 {
		return
	}
	r, ok := c.frequencyRule(parts[1])
	if !ok {
		return
	}
	key := frequencyKey(tag, parts, msg)
	state := observerFrequencyState{At: now, Radios: make(map[string]float64)}
	valid := true
	if status, _ := msg["status"].(string); status != "" && !strings.EqualFold(status, "online") {
		valid = false
	}
	raw, _ := msg["timestamp"].(string)
	if raw != "" {
		t, _, err := parseEnvelopeTime(raw)
		if err == nil {
			if t.Before(now.Add(-r.maxAge())) || t.After(now.Add(5*time.Minute)) {
				valid = false
			} else {
				state.At = t
			}
		} else if retained {
			valid = false
		}
	} else if retained {
		valid = false
	}
	if valid {
		for i, v := range extractRadioReports(msg) {
			if i >= 64 {
				break
			}
			if f, ok := parseObserverFrequency(v.Radio); ok {
				state.Radios[v.ID] = f
			}
		}
	}
	c.frequencyMu.Lock()
	defer c.frequencyMu.Unlock()
	if c.frequencyStates == nil {
		c.frequencyStates = make(map[observerFrequencyKey]observerFrequencyState)
	}
	if _, exists := c.frequencyStates[key]; !exists && len(c.frequencyStates) >= maxFrequencyObservers {
		if now.Sub(c.frequencySweep) < time.Minute {
			return
		}
		c.frequencySweep = now
		// Bounded cache: expired entries are removed only at capacity, not per packet.
		for k, v := range c.frequencyStates {
			kr, _ := c.frequencyRule(k.Region)
			if now.Sub(v.At) > kr.maxAge() {
				delete(c.frequencyStates, k)
			}
		}
		if len(c.frequencyStates) >= maxFrequencyObservers {
			return
		}
	}
	c.frequencyStates[key] = state
}
func (c *Config) observerFrequencyDecision(tag string, parts []string, msg map[string]interface{}, now time.Time) string {
	if len(parts) < 3 {
		return ""
	}
	r, ok := c.frequencyRule(parts[1])
	if !ok {
		return ""
	}
	var f float64
	known := false
	// Explicit per-reception radio metadata takes precedence over cached status.
	if raw, exists := msg["radio"]; exists {
		if s, ok := raw.(string); ok {
			f, known = parseObserverFrequency(s)
		}
	} else {
		id, _ := msg["rx_radio_id"].(string)
		c.frequencyMu.Lock()
		state, exists := c.frequencyStates[frequencyKey(tag, parts, msg)]
		c.frequencyMu.Unlock()
		if exists && !state.At.After(now.Add(5*time.Minute)) && now.Sub(state.At) <= r.maxAge() {
			f, known = state.Radios[id]
		}
	}
	if !known {
		if r.DropUnknown {
			return "unknown"
		}
		return ""
	}
	tolerance := r.ToleranceKHz
	if tolerance <= 0 {
		tolerance = 0.5
	}
	if math.Abs(f-r.MHz) > tolerance/1000+1e-9 {
		return "mismatch"
	}
	return ""
}

// One warning per configured region/minute; no unbounded observer log map.
func (c *Config) warnFrequencyDrop(region string, now time.Time) bool {
	c.frequencyMu.Lock()
	defer c.frequencyMu.Unlock()
	if c.frequencyWarnings == nil {
		c.frequencyWarnings = make(map[string]time.Time)
	}
	region = strings.ToUpper(region)
	if now.Sub(c.frequencyWarnings[region]) < time.Minute {
		return false
	}
	c.frequencyWarnings[region] = now
	return true
}

func (c *Config) validateFrequencyRules() error {
	if len(c.ObserverFrequencyRules) > 64 {
		return fmt.Errorf("observerFrequencyRules: at most 64 regions allowed")
	}
	normalized := make(map[string]ObserverFrequencyRule, len(c.ObserverFrequencyRules))
	for region, rule := range c.ObserverFrequencyRules {
		key := strings.ToUpper(strings.TrimSpace(region))
		if len(key) != 3 || key[0] < 'A' || key[0] > 'Z' || key[1] < 'A' || key[1] > 'Z' || key[2] < 'A' || key[2] > 'Z' {
			return fmt.Errorf("observerFrequencyRules: invalid region %q", region)
		}
		if rule.MHz < 100 || rule.MHz > 2500 || math.IsNaN(rule.MHz) || math.IsInf(rule.MHz, 0) || rule.ToleranceKHz < 0 || rule.ToleranceKHz > 100 || rule.MaxStatusAgeSec < 0 || rule.MaxStatusAgeSec > 86400 {
			return fmt.Errorf("observerFrequencyRules[%s]: invalid frequency, tolerance or max age", key)
		}
		if _, ok := normalized[key]; ok {
			return fmt.Errorf("observerFrequencyRules: duplicate region %s", key)
		}
		normalized[key] = rule
	}
	c.ObserverFrequencyRules = normalized
	return nil
}

func (s MQTTSource) frequencyIdentity(fallback string) string {
	if s.frequencySourceID != "" {
		return s.frequencySourceID
	}
	return fallback
}
