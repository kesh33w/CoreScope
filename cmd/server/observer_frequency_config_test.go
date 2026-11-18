package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveObserverFrequencyRules(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, []byte(`{"apiKey":"keep-secret","other":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{configDir: d}
	body := `{"rules":{"SMF":{"mhz":909.875,"toleranceKHz":0.5,"maxStatusAgeSec":1800,"dropUnknown":true}}}`
	w := httptest.NewRecorder()
	s.handlePutObserverFrequency(w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var reply observerFrequencyConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.RestartRequired || reply.Rules["SMF"].MHz != 909.875 {
		t.Fatal(reply)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"keep-secret"`) || !strings.Contains(string(b), `"other": true`) {
		t.Fatal(string(b))
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	for _, body := range []string{`{"rules":{"SMF":{"mhz":0}}}`, `{"rules":{"smf":{"mhz":909.875}}}`, `{"rules":{"SMF":{"mhz":909.875,"maxStatusAgeSec":-1}}}`, `{"rules":{"SMF":{"mhz":909.875,"toleranceKHz":9000}}}`, `{"rules":{}} {}`} {
		w = httptest.NewRecorder()
		s.handlePutObserverFrequency(w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestObserverFrequencyRequiresAdmin(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.json")
	os.WriteFile(p, []byte(`{}`), 0600)
	s := &Server{configDir: d, cfg: &Config{APIKey: strings.Repeat("A", 40)}}
	w := httptest.NewRecorder()
	s.requireAdmin(http.HandlerFunc(s.handlePutObserverFrequency)).ServeHTTP(w, httptest.NewRequest("PUT", "/", strings.NewReader(`{"rules":{}}`)))
	if w.Code != 401 {
		t.Fatalf("unauthenticated write status=%d", w.Code)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "{}" {
		t.Fatal("unauthorized config change")
	}
}

func TestObserverFrequencySaveFollowsSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "data.json")
	os.WriteFile(target, []byte(`{"keep":1}`), 0600)
	link := filepath.Join(d, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	s := &Server{configDir: d}
	w := httptest.NewRecorder()
	s.handlePutObserverFrequency(w, httptest.NewRequest("PUT", "/", strings.NewReader(`{"rules":{}}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	info, _ := os.Lstat(link)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced symlink")
	}
	b, _ := os.ReadFile(target)
	if !strings.Contains(string(b), "observerFrequencyRules") {
		t.Fatal("target not updated")
	}
}
