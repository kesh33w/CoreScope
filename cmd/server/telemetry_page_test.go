package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestTelemetryPageReport(t *testing.T) {
	tel := newConnectionTelemetry("test", 2, 2)
	id := tel.open(httptest.NewRequest("GET", "https://scope.example/", nil))
	if tel.snapshot().Active[0].PageID != "" {
		t.Fatal("old clients must start unknown")
	}
	tel.pageReport(id, []byte(`{"type":"page_view","page":"live"}`))
	s := tel.snapshot().Active[0]
	if s.PageID != "live" || s.PageSource != "browser_reported" || s.PageReportedAt == "" {
		t.Fatalf("bad page: %+v", s)
	}
	originalTime := s.PageReportedAt
	tel.pageReport(id, []byte(`{"type":"page_view","page":"live"}`))
	if tel.snapshot().Active[0].PageReportedAt != originalTime {
		t.Fatal("duplicate page isn't navigation")
	}
	for _, input := range []string{
		`not json`, `null`, `[]`, `{"type":"other","page":"nodes"}`,
		`{"type":"page_view","page":"nodes/secret?search=private"}`,
		`{"type":"page_view","page":"live","url":"/private"}`,
		`{"type":"page_view","page":"live"} {}`, strings.Repeat(" ", 129),
	} {
		tel.pageReport(id, []byte(input))
		got := tel.snapshot().Active[0]
		if got.PageID != "live" || got.PageReportedAt != originalTime {
			t.Fatalf("invalid changed page: %q", input)
		}
	}
	tel.pageReport(id, []byte(`{"type":"page_view","page":"unknown"}`))
	if tel.snapshot().Active[0].PageID != "unknown" {
		t.Fatal("unknown route must clear prior page")
	}
	tel.close(id, "normal")
	tel.pageReport(id, []byte(`{"type":"page_view","page":"nodes"}`))
	if tel.snapshot().Closed[0].PageID != "unknown" {
		t.Fatal("closed session altered")
	}
	(*connectionTelemetry)(nil).pageReport("x", []byte(`{}`))
}

func TestTelemetryPageReportsOverWebSocket(t *testing.T) {
	hub := NewHub()
	hub.telemetry = newConnectionTelemetry("test", 2, 2)
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte(`{"type":"page_view","page":"nodes"}`)); err != nil {
		t.Fatal(err)
	}
	binary := waitTelemetry(t, hub.telemetry, func(s telemetrySnapshot) bool { return len(s.Active) == 1 && s.Active[0].MessagesReceived == 1 })
	if binary.Active[0].PageID != "" {
		t.Fatal("binary message interpreted")
	}
	for _, page := range []string{"live", "observers", "unknown"} {
		payload, _ := json.Marshal(telemetryPageReport{Type: "page_view", Page: page})
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
		waitTelemetry(t, hub.telemetry, func(s telemetrySnapshot) bool { return len(s.Active) == 1 && s.Active[0].PageID == page })
	}
}

func TestTelemetryArrivalHostnameOnly(t *testing.T) {
	tel := newConnectionTelemetry("test", 2, 2)
	id := tel.open(httptest.NewRequest("GET", "https://scope.example/", nil))
	for _, host := range []string{"https://example.org/path?secret=1", "example.org/path", "user@example.org", "<script>", "example.org:443"} {
		payload, _ := json.Marshal(map[string]string{"type": "page_view", "page": "live", "arrival_host": host})
		tel.pageReport(id, payload)
		if tel.snapshot().Active[0].ArrivalSource != "" {
			t.Fatal("invalid hostname accepted")
		}
	}
	tel.pageReport(id, []byte(`{"type":"page_view","page":"live","arrival_host":"Example.ORG"}`))
	s := tel.snapshot().Active[0]
	if s.ArrivalHost != "example.org" || s.ArrivalSource != "browser_reported" {
		t.Fatalf("bad arrival: %+v", s)
	}
	tel.pageReport(id, []byte(`{"type":"page_view","page":"nodes","arrival_host":"other.example"}`))
	if tel.snapshot().Active[0].ArrivalHost != "example.org" {
		t.Fatal("arrival changed during navigation")
	}
	id2 := tel.open(httptest.NewRequest("GET", "https://scope.example/", nil))
	tel.pageReport(id2, []byte(`{"type":"page_view","page":"live","arrival_host":""}`))
	found := false
	for _, s := range tel.snapshot().Active {
		if s.ConnectionID == id2 {
			found = s.ArrivalHost == "" && s.ArrivalSource == "browser_reported"
		}
	}
	if !found {
		t.Fatal("blank referrer must remain distinguishable from absent reporting")
	}
}

func TestTelemetryFullURLBounds(t *testing.T) {
	tel := newConnectionTelemetry("test", 2, 2)
	id := tel.open(httptest.NewRequest("GET", "https://scope.example/", nil))
	report := func(pageURL string) {
		data, _ := json.Marshal(map[string]any{"type": "page_view", "page": "live", "arrival_host": "search.example", "arrival_url": "https://search.example/?q=radio", "page_url": pageURL, "page_url_truncated": true})
		tel.pageReport(id, data)
	}
	wanted := "https://scope.example/?region=SMF#/live?zoom=10"
	report(wanted)
	s := tel.snapshot().Active[0]
	if s.PageURL != wanted || s.ArrivalURL != "https://search.example/?q=radio" || !s.PageURLTruncated {
		t.Fatal("full reported URL lost")
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///private", "https://example.org/" + strings.Repeat("x", 4096), "https://example.org/\nprivate"} {
		report(bad)
		if tel.snapshot().Active[0].PageURL != wanted {
			t.Fatal("invalid URL accepted")
		}
	}
	report("https://scope.example/?x=1%")
	if tel.snapshot().Active[0].PageURL != "https://scope.example/?x=1%" {
		t.Fatal("truncated percent escape discarded")
	}
}

func TestTelemetryURLSnapshotBudget(t *testing.T) {
	s := telemetrySnapshot{}
	for i := 0; i < 100; i++ {
		s.Active = append(s.Active, connectionSession{ArrivalURL: strings.Repeat("<", 4096), PageURL: strings.Repeat("&", 4096)})
	}
	s.Closed = append(s.Closed, connectionSession{ArrivalURL: strings.Repeat(">", 4096)})
	limitTelemetryURLs(&s)
	if !s.Closed[0].URLDetailsOmitted {
		t.Fatal("closed URL bypassed combined budget")
	}
	total, omitted := 0, 0
	for _, row := range s.Active {
		total += 6 * (len(row.ArrivalURL) + len(row.PageURL))
		if row.URLDetailsOmitted {
			omitted++
		}
	}
	if total > 768*1024 || omitted == 0 || len(s.Active) != 100 {
		t.Fatal("URL budget discarded connections or not bounded")
	}
}
