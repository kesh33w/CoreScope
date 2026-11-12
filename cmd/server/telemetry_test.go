package main

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelemetryIdentityPrivacy(t *testing.T) {
	r := httptest.NewRequest("GET", "https://scope.example/ws?token=SECRET", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	r.Header.Set("CF-Connecting-IP", "1.0.0.1")
	r.Header.Set("Origin", "https://scope.example/private?secret=SECRET")
	r.Header.Set("User-Agent", "Mozilla/5.0 Chrome/123 SECRET")
	r.Header.Set("Authorization", "SECRET")
	reg := newConnectionTelemetry("test", 2, 2)
	id := reg.open(r)
	s := reg.snapshot().Active[0]
	if s.ClientIP != "" || s.AddressSource != "proxy_unresolved" {
		t.Fatalf("trusted spoof: %+v", s)
	}
	b, _ := json.Marshal(reg.snapshot())
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "private") {
		t.Fatal(string(b))
	}
	if s.UserAgent != "Chrome" || s.ClientKind != "likely_browser" {
		t.Fatalf("classification: %+v", s)
	}
	reg.close(id, "attacker close SECRET")
	if reg.snapshot().Closed[0].CloseReason != "unknown" {
		t.Fatal("unbounded close reason")
	}
}
func TestTelemetryLifecycleBounds(t *testing.T) {
	reg := newConnectionTelemetry("test", 1, 2)
	r := httptest.NewRequest("GET", "https://scope.example/ws", nil)
	r.RemoteAddr = "8.8.8.8:123"
	id := reg.open(r)
	omitted := reg.open(r)
	if omitted != "" || reg.snapshot().Limits.ActiveOmitted != 1 {
		t.Fatal("active bound")
	}
	reg.activity(id, "read", 5)
	reg.activity(id, "write", 10)
	reg.activity(id, "pong", 0)
	reg.activity(id, "drop", 0)
	reg.close(id, "normal")
	reg.close(id, "read_error")
	s := reg.snapshot()
	if s.Sequence != 1 || s.Closed[0].MessagesSent != 1 || s.Closed[0].PayloadBytesReceived != 5 || s.Closed[0].QueueDrops != 1 || s.Closed[0].LastPongAt == "" {
		t.Fatalf("lifecycle %+v", s)
	}
	for i := 0; i < 3; i++ {
		id = reg.open(r)
		reg.close(id, "normal")
	}
	s = reg.snapshot()
	if len(s.Closed) != 2 || s.Sequence != 4 || s.EarliestSequence != 3 || s.DroppedEvents != 2 {
		t.Fatalf("ring %+v", s)
	}
}

func TestTelemetryForwarding(t *testing.T) {
	trusted, err := parseTelemetryTrust("10.0.0.2/32,192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ peer, xff, want, source string }{
		{"8.8.8.8:12", "1.1.1.1", "8.8.8.8", "direct"},
		{"10.0.0.3:12", "1.1.1.1", "", "proxy_unresolved"},
		{"10.0.0.2:12", "1.1.1.1, 192.0.2.4", "1.1.1.1", "trusted_forwarded"},
		{"10.0.0.2:12", "9.9.9.9, 1.1.1.1, 192.0.2.4", "1.1.1.1", "trusted_forwarded"},
		{"10.0.0.2:12", "bad, 192.0.2.4", "", "proxy_unresolved"},
		{"10.0.0.2:12", "192.0.2.4", "", "proxy_unresolved"},
		{"10.0.0.2:12", "", "", "proxy_unresolved"},
		{"10.0.0.2:12", "0.0.0.0", "", "proxy_unresolved"},
		{"10.0.0.2:12", "ff02::1", "", "proxy_unresolved"},
		{"10.0.0.2:12", "224.0.0.1", "", "proxy_unresolved"},
	} {
		r := httptest.NewRequest("GET", "https://scope.example/ws", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.xff)
		reg := newConnectionTelemetry("test", 1, 1)
		reg.trusted = trusted
		reg.open(r)
		s := reg.snapshot().Active[0]
		if s.ClientIP != tc.want || s.AddressSource != tc.source {
			t.Fatalf("%+v: %+v", tc, s)
		}
	}
	if _, err := parseTelemetryTrust("broken"); err == nil {
		t.Fatal("invalid trust accepted")
	}
}

func TestTelemetrySocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "telemetry.sock")
	reg := newConnectionTelemetry("test", 2, 2)
	srv, err := startTelemetry(path, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions %v %v", info, err)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: time.Second}
	resp, err := client.Get("http://localhost/v1/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snap telemetrySnapshot
	err = json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if err != nil || snap.SchemaVersion != 1 {
		t.Fatalf("snapshot %v %+v", err, snap)
	}
	for _, p := range []string{"/api/telemetry", "/v1/snapshot?secret=x"} {
		resp, err = client.Get("http://localhost" + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatal(resp.Status)
		}
	}
	if _, err = startTelemetry(path, reg); err == nil {
		t.Fatal("replaced existing socket")
	}
	unsafe := t.TempDir()
	os.Chmod(unsafe, 0755)
	if _, err = startTelemetry(filepath.Join(unsafe, "x.sock"), reg); err == nil {
		t.Fatal("public directory accepted")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(dir, link)
	if _, err = startTelemetry(filepath.Join(link, "x.sock"), reg); err == nil {
		t.Fatal("symlink accepted")
	}
}
func waitTelemetry(t *testing.T, reg *connectionTelemetry, predicate func(telemetrySnapshot) bool) telemetrySnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := reg.snapshot()
		if predicate(s) {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("telemetry condition timed out: %+v", reg.snapshot())
	return telemetrySnapshot{}
}
func TestTelemetryWebSocket(t *testing.T) {
	hub := NewHub()
	hub.telemetry = newConnectionTelemetry("test", 10, 10)
	hub.pingInterval = 10 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer server.Close()
	defer hub.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	hub.Broadcast("payload")
	conn.SetReadDeadline(time.Now().Add(time.Second))
	for i := 0; i < 3; i++ {
		if _, _, err = conn.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	s := waitTelemetry(t, hub.telemetry, func(s telemetrySnapshot) bool { return len(s.Active) == 1 && s.Active[0].LastPongAt != "" })
	if s.Active[0].MessagesReceived != 1 || s.Active[0].PayloadBytesReceived != 5 || s.Active[0].MessagesSent < 3 {
		t.Fatalf("counters %+v", s)
	}
	conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "SECRET"))
	s = waitTelemetry(t, hub.telemetry, func(s telemetrySnapshot) bool { return len(s.Closed) == 1 })
	if s.Closed[0].CloseReason != "normal" {
		t.Fatal(s.Closed[0].CloseReason)
	}
}
func TestTelemetryConcurrentSnapshots(t *testing.T) {
	reg := newConnectionTelemetry("test", 20, 20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r := httptest.NewRequest("GET", "https://scope.example/ws", nil)
				id := reg.open(r)
				reg.activity(id, "write", 20)
				reg.snapshot()
				reg.close(id, "normal")
			}
		}()
	}
	wg.Wait()
	s := reg.snapshot()
	if s.Sequence != 800 || s.DroppedEvents != 780 {
		t.Fatalf("concurrent ring %+v", s)
	}
}

// Run explicitly with TELEMETRY_HARNESS_SOCKET=/absolute/private/fixture.sock
// and go test -run '^TestTelemetryHarness$' -v. No radio DB is opened. The
// private directory must already exist and have mode 0700. All data is synthetic.
func TestTelemetryHarness(t *testing.T) {
	path := os.Getenv("TELEMETRY_HARNESS_SOCKET")
	if path == "" {
		t.Skip("opt-in synthetic integration harness")
	}
	reg := newConnectionTelemetry("fixture-corescope", 20, 20)
	srv, err := startTelemetry(path, reg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hub := NewHub()
	hub.telemetry = reg
	hub.pingInterval = 50 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(hub.ServeWS))
	defer server.Close()
	defer hub.Close()
	for i := 0; i < 3; i++ {
		headers := http.Header{}
		if i < 2 {
			headers.Set("Origin", server.URL)
			headers.Set("User-Agent", "Mozilla/5.0 Chrome/123")
		} else {
			headers.Set("User-Agent", "NodeScope/1.0")
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", headers)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("synthetic fixture"))
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}
	t.Logf("Fixture ready: %s; WS %s; three synthetic clients", path, server.URL)
	duration := 20 * time.Second
	if raw := os.Getenv("TELEMETRY_HARNESS_DURATION"); raw != "" {
		duration, err = time.ParseDuration(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(duration / 2)
	hub.Broadcast("synthetic fixture")
	hub.Close()
	time.Sleep(duration / 2)
}

func TestTelemetryQueueDrops(t *testing.T) {
	h := NewHub()
	reg := newConnectionTelemetry("test", 2, 2)
	r := httptest.NewRequest("GET", "https://scope.example/ws", nil)
	c := &Client{send: make(chan []byte, 1), telemetry: reg, telemetryID: reg.open(r)}
	h.Register(c)
	h.Broadcast("first")
	h.Broadcast("dropped")
	s := reg.snapshot().Active[0]
	if s.QueueDrops != 1 || s.MessagesSent != 0 || s.PayloadBytesSent != 0 {
		t.Fatalf("queued is not sent %+v", s)
	}
	h.Unregister(c)
}
func TestTelemetryShutdownAndDisappearance(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		h := NewHub()
		h.telemetry = newConnectionTelemetry("test", 2, 2)
		srv := httptest.NewServer(http.HandlerFunc(h.ServeWS))
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		waitTelemetry(t, h.telemetry, func(s telemetrySnapshot) bool { return len(s.Active) == 1 })
		want := "read_error"
		if shutdown {
			h.Close()
			want = "server_shutdown"
		} else {
			c.Close()
		}
		s := waitTelemetry(t, h.telemetry, func(s telemetrySnapshot) bool { return len(s.Closed) == 1 })
		if s.Closed[0].CloseReason != want {
			t.Fatalf("want %s: %+v", want, s)
		}
		c.Close()
		srv.Close()
	}
}
func TestTelemetryReadTimeout(t *testing.T) {
	h := NewHub()
	h.telemetry = newConnectionTelemetry("test", 2, 2)
	h.readTimeout = 20 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	defer srv.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := waitTelemetry(t, h.telemetry, func(s telemetrySnapshot) bool { return len(s.Closed) == 1 })
	if s.Closed[0].CloseReason != "read_timeout" {
		t.Fatalf("timeout %+v", s)
	}
}

func TestTelemetrySnapshotSizeBound(t *testing.T) {
	reg := newConnectionTelemetry(strings.Repeat("a", 64), 512, 1024)
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	r := httptest.NewRequest("GET", "https://"+host+"/ws", nil)
	r.RemoteAddr = "2001:db8:abcd:abcd:abcd:abcd:abcd:abcd:123"
	r.Header.Set("Origin", "https://"+host+":65535")
	r.Header.Set("User-Agent", "NodeScope/1")
	for i := 0; i < 1024; i++ {
		reg.close(reg.open(r), "server_shutdown")
	}
	for i := 0; i < 512; i++ {
		reg.open(r)
	}
	b, err := json.Marshal(reg.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 4*1024*1024 {
		t.Fatalf("snapshot too big: %d", len(b))
	}
	t.Logf("maximum string fixture: %d bytes", len(b))
}

// delayedSuccessfulWrite lets the underlying connection complete a real write,
// then holds its successful return while the reader observes peer closure.
// This deterministically exercises the final-frame accounting race.
type delayedSuccessfulWrite struct {
	net.Conn
	armed       atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	closed      chan struct{}
	enteredOnce sync.Once
	closedOnce  sync.Once
}

func (c *delayedSuccessfulWrite) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && c.armed.Load() {
		c.enteredOnce.Do(func() { close(c.entered) })
		<-c.release
	}
	return n, err
}
func (c *delayedSuccessfulWrite) Close() error {
	err := c.Conn.Close()
	c.closedOnce.Do(func() { close(c.closed) })
	return err
}

type telemetryHijackWriter struct {
	http.ResponseWriter
	connections chan *delayedSuccessfulWrite
}

func (w telemetryHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	wrapped := &delayedSuccessfulWrite{Conn: conn, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	w.connections <- wrapped
	return wrapped, rw, nil
}
func TestTelemetryInFlightSuccessfulWriteAtReaderClose(t *testing.T) {
	h := NewHub()
	h.telemetry = newConnectionTelemetry("test", 2, 2)
	connections := make(chan *delayedSuccessfulWrite, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeWS(telemetryHijackWriter{w, connections}, r) }))
	defer srv.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wrapped := <-connections
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(wrapped.release) }) }
	defer release()
	waitSignal := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal(what)
		}
	}
	waitTelemetry(t, h.telemetry, func(s telemetrySnapshot) bool { return len(s.Active) == 1 })
	wrapped.armed.Store(true)
	h.Broadcast("last successful payload")
	waitSignal(wrapped.entered, "writer did not reach successful-return barrier")
	client.Close()
	// Reader closes the server socket before waiting for writerDone. Seeing this
	// event proves reader termination has reached the finalization barrier.
	waitSignal(wrapped.closed, "reader did not close socket")
	before := h.telemetry.snapshot()
	if len(before.Active) != 1 || len(before.Closed) != 0 || before.Active[0].MessagesSent != 0 {
		t.Fatalf("session froze before writer completed: %+v", before)
	}
	release()
	after := waitTelemetry(t, h.telemetry, func(s telemetrySnapshot) bool { return len(s.Closed) == 1 })
	payload, _ := json.Marshal("last successful payload")
	if after.Closed[0].MessagesSent != 1 || after.Closed[0].PayloadBytesSent != uint64(len(payload)) || after.Closed[0].LastWriteAt == "" {
		t.Fatalf("successful final frame lost: %+v", after)
	}
	if after.Closed[0].CloseReason != "read_error" {
		t.Fatalf("unexpected cause: %+v", after)
	}
}
