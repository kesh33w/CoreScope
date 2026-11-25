package main

import (
	"reflect"
	"strings"
	"testing"
)

func presetConfig(presets ...RegionPreset) *Config {
	return &Config{Port: 3000, RegionFilter: &RegionFilterConfig{Mode: "frequency", Presets: presets}}
}

func TestRegionPresetsNormalised(t *testing.T) {
	cfg := presetConfig(
		RegionPreset{Code: " bay910 ", Label: "Bay Area 910", Regions: []string{"910.5250", "sjc", " OAK", "SJC"}},
		RegionPreset{Code: "BAY910", Regions: []string{"SFO"}},         // duplicate code
		RegionPreset{Code: "910.525", Regions: []string{"SJC"}},        // numeric code
		RegionPreset{Code: "A,B", Regions: []string{"SJC"}},            // separator
		RegionPreset{Code: "all", Regions: []string{"SJC"}},            // reserved
		RegionPreset{Code: "EMPTY", Regions: []string{" ", "all"}},     // no regions
		RegionPreset{Code: "NEST", Regions: []string{"BAY910", "SMF"}}, // presets do not nest
	)
	got := cfg.RegionPresets()
	want := []RegionPreset{
		{Code: "BAY910", Label: "Bay Area 910", Regions: []string{"910.525", "SJC", "OAK"}},
		{Code: "NEST", Label: "NEST", Regions: []string{"SMF"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RegionPresets() = %+v, want %+v", got, want)
	}

	iata := &Config{Port: 3000, RegionFilter: &RegionFilterConfig{Presets: cfg.RegionFilter.Presets}}
	if p := iata.RegionPresets(); p != nil {
		t.Fatalf("iata mode RegionPresets() = %+v, want nil", p)
	}
	if p := (&Config{}).RegionPresets(); p != nil {
		t.Fatalf("no regionFilter RegionPresets() = %+v, want nil", p)
	}
}

func TestExpandRegionPresets(t *testing.T) {
	presets := []RegionPreset{
		{Code: "BAY910", Regions: []string{"910.525", "SJC", "OAK"}},
		{Code: "SAC909", Regions: []string{"909.875", "SMF"}},
	}
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"SJC", "SJC"},
		{"910.525,SFO", "910.525,SFO"},
		{"BAY910", "910.525,SJC,OAK"},
		{"bay910", "910.525,SJC,OAK"},
		{" BAY910 ,SFO", "910.525,SJC,OAK,SFO"},
		{"BAY910,SJC,SAC909", "910.525,SJC,OAK,909.875,SMF"},
	} {
		if got := expandRegionPresets(tc.in, presets); got != tc.want {
			t.Errorf("expandRegionPresets(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A preset code in ?region= gives the same answer as its tokens, and the
// config endpoints list it.
func TestRegionPresetsEndToEnd(t *testing.T) {
	cfg := presetConfig(RegionPreset{Code: "SJ910", Label: "San Jose 910", Regions: []string{"910.525", "SJC"}})
	router := setupFrequencyServer(t, cfg)

	var regions map[string]string
	getFreqJSON(t, router, "/api/config/regions", &regions)
	if regions["SJ910"] != "San Jose 910" || regions["SJC"] != "SJC" {
		t.Fatalf("regions = %v, want the preset beside the codes", regions)
	}
	var picks RegionQuickPicksResponse
	getFreqJSON(t, router, "/api/config/region-quick-picks", &picks)
	if len(picks.Presets) != 1 || picks.Presets[0].Code != "SJ910" ||
		!reflect.DeepEqual(picks.Presets[0].Regions, []string{"910.525", "SJC"}) {
		t.Fatalf("quick-picks presets = %+v", picks.Presets)
	}

	for _, path := range []string{"/api/nodes", "/api/packets?limit=50", "/api/observers"} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		var viaPreset, viaTokens map[string]interface{}
		getFreqJSON(t, router, path+sep+"region=sj910", &viaPreset)
		getFreqJSON(t, router, path+sep+"region=910.525,SJC", &viaTokens)
		delete(viaPreset, "server_time")
		delete(viaTokens, "server_time")
		if !reflect.DeepEqual(viaPreset, viaTokens) {
			t.Errorf("%s: preset answer differs from its tokens\npreset: %v\ntokens: %v", path, viaPreset, viaTokens)
		}
	}
}
