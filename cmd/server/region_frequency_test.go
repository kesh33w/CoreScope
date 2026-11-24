package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestFrequencyRegionKey(t *testing.T) {
	for in, want := range map[string]string{
		"910.5250061,62.5,7,5": "910.525",
		"909.875,62.50,7,5":    "909.875",
		"927.875,250,11,5":     "927.875",
		"":                     "",
		"garbage":              "",
		"0,62.5,7,5":           "",
		"910525000,62.5,7,5":   "910.525",
		"909875 kHz":           "909.875",
		"911.0,62.5,7,5":       "911",
	} {
		if got := frequencyRegionKey(in); got != want {
			t.Errorf("frequencyRegionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegionKeyMatches(t *testing.T) {
	for _, c := range []struct {
		key, region string
		want        bool
	}{
		{"SJC", "SJC", true},
		{"SJC", "SFO,SJC", true},
		{"SJC", "910.525", false},
		{"910.525|SJC", "910.525", true},
		{"910.525|SJC", "SJC", true},
		{"910.525|SJC", "910.525,SJC", true},
		{"910.525|SJC", "910.525,SFO", false},
		{"910.525|SJC", "909.875,SJC", false},
		{"910.525|SJC", "909.875,910.525,OAK,SJC", true},
		{"|SJC", "SJC", true},
		{"|SJC", "910.525,SJC", false},
		{"910.525|", "910.525", true},
		{"910.525|", "910.525,SJC", false},
		{"", "SJC", false},
		// Bare frequencies (frequency-only placements) match only a
		// frequency-only selection.
		{"910.525", "910.525", true},
		{"910.525", "909.875,910.525", true},
		{"910.525", "910.525,SJC", false},
		{"910.525", "SJC", false},
	} {
		if got := regionKeyMatches(c.key, normalizeRegionCodes(c.region)); got != c.want {
			t.Errorf("regionKeyMatches(%q, %q) = %v, want %v", c.key, c.region, got, c.want)
		}
	}
}

func TestNormalizeRegionCodesFrequencyTokens(t *testing.T) {
	got := normalizeRegionCodes(" 910.5250 ,smf,All,927.875")
	want := []string{"910.525", "SMF", "927.875"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// seedFrequencyRadios puts the seed observers on two networks: obs1 (SJC)
// on 910.525 MHz, reported as an unrounded float, and obs2 (SFO) on 909.875.
func seedFrequencyRadios(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.conn.Exec(`UPDATE observers SET radio = '910.5250061,62.5,7,5' WHERE id = 'obs1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE observers SET radio = '909.875,62.5,7,5' WHERE id = 'obs2'`); err != nil {
		t.Fatal(err)
	}
}

func TestRegionObserversByFrequency(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	seedFrequencyRadios(t, db)

	// IATA mode is unchanged: a frequency matches nothing, an IATA code matches.
	if ids, err := db.GetObserverIdsForRegion("910.525"); err != nil || len(ids) != 0 {
		t.Fatalf("iata mode, frequency: ids=%v err=%v, want none", ids, err)
	}
	if ids, err := db.GetObserverIdsForRegion("SJC"); err != nil || len(ids) != 1 || ids[0] != "obs1" {
		t.Fatalf("iata mode, SJC: ids=%v err=%v, want [obs1]", ids, err)
	}

	db.SetRegionByFrequency(true)
	if ids, err := db.GetObserverIdsForRegion("910.5250"); err != nil || len(ids) != 1 || ids[0] != "obs1" {
		t.Fatalf("frequency mode, 910.525: ids=%v err=%v, want [obs1]", ids, err)
	}
	ids, err := db.GetObserverIdsForRegion("910.525,909.875")
	sort.Strings(ids)
	if err != nil || len(ids) != 2 || ids[0] != "obs1" || ids[1] != "obs2" {
		t.Fatalf("frequency mode, both: ids=%v err=%v", ids, err)
	}
	// IATA codes still select, alone or together with a frequency.
	for region, want := range map[string]string{
		"SJC":                 "obs1",
		"910.525,SJC":         "obs1",
		"910.525,SFO":         "",
		"909.875,910.525,SFO": "obs2",
		"sjc,sfo,909.875":     "obs2",
	} {
		ids, err := db.GetObserverIdsForRegion(region)
		if err != nil || strings.Join(ids, ",") != want {
			t.Fatalf("frequency mode, %s: ids=%v err=%v, want %q", region, ids, err, want)
		}
	}

	// The channel message (transmission 2) was heard only by obs1.
	ch, err := db.GetChannels("910.525")
	if err != nil || len(ch) != 1 {
		t.Fatalf("GetChannels(910.525) = %v, %v; want the #test channel", ch, err)
	}
	if ch, err := db.GetChannels("909.875"); err != nil || len(ch) != 0 {
		t.Fatalf("GetChannels(909.875) = %v, %v; want none", ch, err)
	}
	if _, total, err := db.GetChannelMessages("#test", 10, 0, "910.525"); err != nil || total != 1 {
		t.Fatalf("GetChannelMessages(910.525) total=%d err=%v, want 1", total, err)
	}
	if _, total, err := db.GetChannelMessages("#test", 10, 0, "909.875"); err != nil || total != 0 {
		t.Fatalf("GetChannelMessages(909.875) total=%d err=%v, want 0", total, err)
	}
}

func setupFrequencyServer(t *testing.T, cfg *Config) *mux.Router {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	seedFrequencyRadios(t, db)
	db.SetRegionByFrequency(cfg.RegionByFrequency())
	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatalf("background indexes never became ready")
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router
}

func getFreqJSON(t *testing.T, router *mux.Router, url string, v interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 200 {
		t.Fatalf("%s: status %d: %s", url, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v", url, err)
	}
}

func TestConfigRegionsFrequencyMode(t *testing.T) {
	cfg := &Config{
		Port:             3000,
		RegionFilter:     &RegionFilterConfig{Mode: "frequency"},
		Regions:          map[string]string{"910.5250": "US 910", "SJC": "San Jose"},
		RegionQuickPicks: []RegionQuickPick{{Name: "Bay", Regions: []string{"SJC"}}},
	}
	router := setupFrequencyServer(t, cfg)

	// obs2 was last seen yesterday and obs1 an hour ago: both are recent.
	var regions map[string]string
	getFreqJSON(t, router, "/api/config/regions", &regions)
	want := map[string]string{"910.525": "US 910", "909.875": "909.875 MHz", "SJC": "San Jose", "SFO": "SFO"}
	if !reflect.DeepEqual(regions, want) {
		t.Fatalf("regions = %v, want %v", regions, want)
	}

	var picks RegionQuickPicksResponse
	getFreqJSON(t, router, "/api/config/region-quick-picks", &picks)
	if picks.Mode != "frequency" || len(picks.QuickPicks) != 1 || picks.QuickPicks[0].Name != "Bay" {
		t.Fatalf("quick picks = %+v, want frequency mode with the Bay pick", picks)
	}
	wantByFreq := map[string][]string{"910.525": {"SJC"}, "909.875": {"SFO"}}
	if !reflect.DeepEqual(picks.FrequencyRegions, wantByFreq) {
		t.Fatalf("frequencyRegions = %v, want %v", picks.FrequencyRegions, wantByFreq)
	}

	var nodes struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	getFreqJSON(t, router, "/api/nodes?region=910.525", &nodes)
	found := false
	for _, n := range nodes.Nodes {
		if n["public_key"] == "aabbccdd11223344" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/api/nodes?region=910.525 lacks the repeater both observers heard: %v", nodes.Nodes)
	}
}

func TestConfigRegionsIATAModeUnchanged(t *testing.T) {
	router := setupFrequencyServer(t, &Config{Port: 3000})
	var regions map[string]string
	getFreqJSON(t, router, "/api/config/regions", &regions)
	if _, ok := regions["SJC"]; !ok || len(regions) != 2 {
		t.Fatalf("regions = %v, want the IATA codes", regions)
	}
	var picks map[string]interface{}
	getFreqJSON(t, router, "/api/config/region-quick-picks", &picks)
	if _, ok := picks["mode"]; ok {
		t.Fatalf("iata mode must not send mode: %v", picks)
	}
}

func TestMergeFrequencyPlacements(t *testing.T) {
	two := 2
	byFreq := map[string]NodeRegionPlacement{
		"a": {Basis: RegionBasisZeroHop, Regions: []string{"910.525"}},
		"b": {Basis: RegionBasisFewestHops, Regions: []string{"927.875"}, Hops: &two},
	}
	byPair := map[string]NodeRegionPlacement{
		"a": {Basis: RegionBasisFewestHops, Regions: []string{"910.525|SJC"}, Hops: &two},
		"c": {Basis: RegionBasisZeroHop, Regions: []string{"|SMF"}},
	}
	got := mergeFrequencyPlacements(byFreq, byPair)
	if a := got["a"]; a.Basis != RegionBasisZeroHop || a.Hops != nil ||
		!reflect.DeepEqual(a.Regions, []string{"910.525", "910.525|SJC"}) {
		t.Fatalf("a = %+v", a)
	}
	if b := got["b"]; !reflect.DeepEqual(b.Regions, []string{"927.875"}) {
		t.Fatalf("b = %+v", b)
	}
	if c := got["c"]; !reflect.DeepEqual(c.Regions, []string{"|SMF"}) {
		t.Fatalf("c = %+v", c)
	}
}
