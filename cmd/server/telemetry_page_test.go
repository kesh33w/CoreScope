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
