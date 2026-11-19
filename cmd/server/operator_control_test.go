package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func controlFixture(t *testing.T) (*operatorControl, *Hub) {
	t.Helper()
	d := t.TempDir()
	os.Chmod(d, 0700)
	h := NewHub()
	h.telemetry = newConnectionTelemetry("test", 512, 1024)
	h.telemetry.trusted, _ = parseTelemetryTrust("127.0.0.1/32")
	c, e := newOperatorControl(h, filepath.Join(d, "policy.json"))
	if e != nil {
		t.Fatal(e)
	}
	h.control = c
	return c, h
}
func operatorTestRequest(c *operatorControl, action, ip string) operatorRequest {
	return operatorRequest{Action: action, RequestID: fmt.Sprintf("%032x", time.Now().UnixNano()), SourceID: "test", InstanceID: c.hub.telemetry.instance, Generation: c.state.Generation, IP: ip, RuleID: strings.Repeat("a", 32)}
}
func TestOperatorTargetAndPersistence(t *testing.T) {
	c, h := controlFixture(t)
	s := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	defer s.Close()
	dial := func(ip string) *websocket.Conn {
		w, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {ip}})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { w.Close() })
		return w
	}
	a := dial("8.8.8.8")
	b := dial("1.1.1.1")
	r := operatorTestRequest(c, "block", "8.8.8.8")
	out, e := c.apply(r)
	if e != nil || out.Closed != 1 {
		t.Fatalf("%+v %v", out, e)
	}
	a.SetReadDeadline(time.Now().Add(time.Second))
	_, _, e = a.ReadMessage()
	if !websocket.IsCloseError(e, 1008) {
		t.Fatal(e)
	}
	h.Broadcast("still here")
	b.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = b.ReadMessage(); e != nil {
		t.Fatal(e)
	}
	_, resp, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"::ffff:8.8.8.8"}})
	if e == nil || resp.StatusCode != 403 {
		t.Fatalf("%v %v", resp, e)
	}
	loaded, e := newOperatorControl(h, c.path)
	if e != nil || loaded.state.Rules["8.8.8.8"] != r.RuleID {
		t.Fatalf("%v", e)
	}
	if _, e = c.apply(r); e == nil {
		t.Fatal("replay accepted")
	}
	r = operatorTestRequest(c, "unblock", "8.8.8.8")
	r.RuleID = strings.Repeat("b", 32)
	if _, e = c.apply(r); e == nil {
		t.Fatal("wrong owner accepted")
	}
	r = operatorTestRequest(c, "unblock", "8.8.8.8")
	if _, e = c.apply(r); e != nil {
		t.Fatal(e)
	}
}
func TestOperatorGuards(t *testing.T) {
	c, _ := controlFixture(t)
	for _, ip := range []string{"::ffff:127.0.0.1", "::ffff:10.0.0.1", "fe80::1", "224.0.0.1", "0.0.0.0", "::1"} {
		if _, e := operatorIP(ip); e == nil {
			t.Fatalf("accepted %s", ip)
		}
	}
	r := operatorTestRequest(c, "block", "8.8.8.8")
	r.SourceID = "other"
	if _, e := c.apply(r); e == nil {
		t.Fatal("wrong source")
	}
	r = operatorTestRequest(c, "block", "8.8.8.8")
	r.InstanceID = "old"
	if _, e := c.apply(r); e == nil {
		t.Fatal("wrong boot")
	}
	r = operatorTestRequest(c, "block", "8.8.8.8")
	r.Generation = 99
	if _, e := c.apply(r); e == nil {
		t.Fatal("wrong generation")
	}
	c.path = filepath.Join(c.path, "invalid")
	r = operatorTestRequest(c, "block", "8.8.8.8")
	if _, e := c.apply(r); e == nil || len(c.state.Rules) != 0 {
		t.Fatal("save failure mutated policy")
	}
}
func TestOperatorUnixAuthorization(t *testing.T) {
	allowedUIDs := []uint32{uint32(os.Getuid()), uint32(os.Getuid() + 1)}
	if os.Getuid() != 0 {
		allowedUIDs = append(allowedUIDs, 0)
	}
	for _, allowed := range allowedUIDs {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			c, _ := controlFixture(t)
			path := filepath.Join(filepath.Dir(c.path), "control.sock")
			server, e := startOperatorControl(path, c, allowed)
			if e != nil {
				t.Fatal(e)
			}
			defer server.Close()
			st, _ := os.Stat(path)
			if st.Mode().Perm() != 0600 {
				t.Fatal(st.Mode())
			}
			client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			}}}
			defer client.CloseIdleConnections()
			data, _ := json.Marshal(operatorRequest{Action: "status", RequestID: strings.Repeat("a", 32)})
			res, e := client.Post("http://unix/v1/control", "application/json", strings.NewReader(string(data)))
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			want := 200
			if allowed != uint32(os.Getuid()) {
				want = 403
			}
			if res.StatusCode != want {
				t.Fatalf("got%d want%d", res.StatusCode, want)
			}
		})
	}
}

func TestOperatorTransportRejectsMalformed(t *testing.T) {
	c, _ := controlFixture(t)
	path := filepath.Join(filepath.Dir(c.path), "control.sock")
	server, e := startOperatorControl(path, c, uint32(os.Getuid()))
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	defer client.CloseIdleConnections()
	for _, body := range []string{`{"action":"status","unknown":true}`, `{} {}`, strings.Repeat("x", 2049)} {
		res, e := client.Post("http://unix/v1/control", "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal(res.StatusCode)
		}
	}
	res, e := client.Get("http://unix/v1/control")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
	if _, e = startOperatorControl(path, c, uint32(os.Getuid())); e == nil {
		t.Fatal("replaced existing socket")
	}
}
func TestOperatorPreservesUnattributedAdmission(t *testing.T) {
	c, _ := controlFixture(t)
	for _, remote := range []string{"10.0.0.1:1234", "127.0.0.1:1234"} {
		r := httptest.NewRequest("GET", "http://test/ws", nil)
		r.RemoteAddr = remote
		ip, ok, done := c.admit(r)
		done()
		if !ok || ip != "" {
			t.Fatalf("unattributed admission changed: %s %v", ip, ok)
		}
	}
}
func TestOperatorPathSafety(t *testing.T) {
	c, h := controlFixture(t)
	if e := os.WriteFile(c.path, []byte(`{}`), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := newOperatorControl(h, c.path); e == nil {
		t.Fatal("accepted world-readable policy")
	}
	root := t.TempDir()
	os.Chmod(root, 0700)
	link := filepath.Join(root, "link")
	os.Symlink(filepath.Dir(c.path), link)
	if _, e := newOperatorControl(h, filepath.Join(link, "policy.json")); e == nil {
		t.Fatal("accepted symlink parent")
	}
}
func TestOperatorConcurrentAdmission(t *testing.T) {
	c, h := controlFixture(t)
	s := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	defer s.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 10; j++ {
				w, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"8.8.8.8"}})
				if e == nil {
					w.Close()
				}
			}
		}()
	}
	close(start)
	if _, e := c.apply(operatorTestRequest(c, "block", "8.8.8.8")); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	for j := 0; j < 5; j++ {
		w, res, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"8.8.8.8"}})
		if e == nil {
			w.Close()
			t.Fatal("admitted after block")
		}
		if res == nil || res.StatusCode != 403 {
			t.Fatalf("%v %v", res, e)
		}
	}
}
func TestOperatorStaleSocketRecovery(t *testing.T) {
	c, _ := controlFixture(t)
	path := filepath.Join(filepath.Dir(c.path), "control.sock")
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	ln.SetUnlinkOnClose(false)
	os.Chmod(path, 0600)
	ln.Close()
	s, e := startOperatorControl(path, c, uint32(os.Getuid()))
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}
func TestOperatorWritableAncestor(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0777)
	d := filepath.Join(root, "private")
	os.Mkdir(d, 0700)
	if e := operatorPrivate(d, true); e == nil {
		t.Fatal("accepted writable ancestor")
	}
}
func TestOperatorPythonHelperIntegration(t *testing.T) {
	dir := os.Getenv("OPERATOR_HELPER_DIR")
	if dir == "" {
		t.Skip("set OPERATOR_HELPER_DIR to test actual operator CLI helper")
	}
	c, h := controlFixture(t)
	path := filepath.Join(filepath.Dir(c.path), "control.sock")
	control, e := startOperatorControl(path, c, uint32(os.Getuid()))
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	s := httptest.NewServer(http.HandlerFunc(h.ServeWS))
	defer s.Close()
	run := func(action string) {
		cmd := exec.Command("python3", "-c", `import sys,json
sys.path.insert(0,sys.argv[1])
from operator_control import execute
cfg={'sources':{'test':{'socket':sys.argv[2]}},'protected_ips':[],'proxy_networks':[],'cloudflare':{}}
a=sys.argv[3]
r=execute(cfg,'test',a,'8.8.8.8','a'*32,a+' test 8.8.8.8',verify=lambda *args:None)
assert r['ok'] and r['generation']==(1 if a=='block' else 2)
print(json.dumps(r))`, dir, path, action)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("helper %v: %s", e, out)
		}
	}
	run("block")
	_, resp, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"8.8.8.8"}})
	if e == nil || resp.StatusCode != 403 {
		t.Fatalf("%v %v", resp, e)
	}
	run("unblock")
	w, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"8.8.8.8"}})
	if e != nil {
		t.Fatal(e)
	}
	w.Close()
}
func TestOperatorReloadDeniesAndRejectsOldBoot(t *testing.T) {
	c, h := controlFixture(t)
	old := operatorTestRequest(c, "block", "8.8.8.8")
	if _, e := c.apply(old); e != nil {
		t.Fatal(e)
	}
	next := NewHub()
	next.telemetry = newConnectionTelemetry("test", 512, 1024)
	next.telemetry.trusted = h.telemetry.trusted
	loaded, e := newOperatorControl(next, c.path)
	if e != nil {
		t.Fatal(e)
	}
	next.control = loaded
	s := httptest.NewServer(http.HandlerFunc(next.ServeWS))
	defer s.Close()
	_, resp, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http"), http.Header{"X-Forwarded-For": {"8.8.8.8"}})
	if e == nil || resp.StatusCode != 403 {
		t.Fatalf("%v %v", resp, e)
	}
	old.Generation = loaded.state.Generation
	old.RequestID = strings.Repeat("b", 32)
	if _, e := loaded.apply(old); e == nil {
		t.Fatal("old boot accepted after reload")
	}
}
