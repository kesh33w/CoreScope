package main

// Operator control is opt-in, separate from HTTP and read-only telemetry.
// Only a local peer with the configured OS UID can use its private Unix socket.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const operatorMaxRules = 4096

var operatorID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type operatorRequest struct {
	Action     string `json:"action"`
	RequestID  string `json:"request_id"`
	SourceID   string `json:"source_id"`
	InstanceID string `json:"instance_id"`
	Generation uint64 `json:"generation"`
	IP         string `json:"ip"`
	RuleID     string `json:"rule_id"`
}
type operatorState struct {
	Generation uint64            `json:"generation"`
	Rules      map[string]string `json:"rules"`
}
type operatorResponse struct {
	OK         bool              `json:"ok"`
	SourceID   string            `json:"source_id"`
	InstanceID string            `json:"instance_id"`
	Generation uint64            `json:"generation"`
	Rules      map[string]string `json:"rules"`
	Closed     int               `json:"closed"`
	Error      string            `json:"error,omitempty"`
}
type operatorControl struct {
	op        sync.Mutex
	admission sync.RWMutex
	hub       *Hub
	path      string
	state     operatorState
	seen      map[string]bool
	order     []string
}

func operatorIP(raw string) (string, error) {
	a, e := netip.ParseAddr(raw)
	if e != nil || a.Zone() != "" {
		return "", errors.New("invalid IP")
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() {
		return "", errors.New("non-public IP")
	}
	return a.String(), nil
}
func operatorPrivate(path string, dir bool) error {
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (owner.Uid != 0 && owner.Uid != uint32(os.Geteuid())) {
			return errors.New("operator ancestor has foreign owner")
		}
		if info.Mode().Perm()&0022 != 0 && !(owner.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return errors.New("operator ancestor is writable by others")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("operator path has unsafe ancestor")
		}
		if ancestor == filepath.Dir(ancestor) {
			break
		}
	}

	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || st.IsDir() != dir {
		return errors.New("operator path must be owned by server UID and private")
	}
	if !dir && !st.Mode().IsRegular() {
		return errors.New("operator policy must be a regular file")
	}
	return nil
}
func newOperatorControl(h *Hub, path string) (*operatorControl, error) {
	if h.telemetry == nil || !filepath.IsAbs(path) {
		return nil, errors.New("operator control requires telemetry and absolute policy path")
	}
	if e := operatorPrivate(filepath.Dir(path), true); e != nil {
		return nil, e
	}
	c := &operatorControl{hub: h, path: path, state: operatorState{Rules: map[string]string{}}, seen: map[string]bool{}}
	if _, e := os.Lstat(path); os.IsNotExist(e) {
		return c, nil
	}
	if e := operatorPrivate(path, false); e != nil {
		return nil, e
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 512*1024))
	d.DisallowUnknownFields()
	if e = d.Decode(&c.state); e != nil {
		return nil, e
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing policy data")
	}
	if c.state.Rules == nil || len(c.state.Rules) > operatorMaxRules {
		return nil, errors.New("invalid policy size")
	}
	for ip, id := range c.state.Rules {
		canonical, e := operatorIP(ip)
		if e != nil || canonical != ip || !operatorID.MatchString(id) {
			return nil, errors.New("invalid policy entry")
		}
	}
	return c, nil
}

// Persist before publication. A post-rename fsync failure is uncertain, so the
// caller still publishes the renamed policy and reports the durability error.
func (c *operatorControl) persist(s operatorState) (committed bool, err error) {
	if e := operatorPrivate(filepath.Dir(c.path), true); e != nil {
		return false, e
	}
	if _, e := os.Lstat(c.path); e == nil {
		if e = operatorPrivate(c.path, false); e != nil {
			return false, e
		}
	} else if !os.IsNotExist(e) {
		return false, e
	}
	f, e := os.CreateTemp(filepath.Dir(c.path), ".operator-policy-")
	if e != nil {
		return false, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = json.NewEncoder(f).Encode(s); e != nil {
		return false, e
	}
	if e = f.Sync(); e != nil {
		return false, e
	}
	if e = f.Close(); e != nil {
		return false, e
	}
	if e = os.Rename(f.Name(), c.path); e != nil {
		return false, e
	}
	d, e := os.Open(filepath.Dir(c.path))
	if e != nil {
		return true, e
	}
	defer d.Close()
	return true, d.Sync()
}
func (c *operatorControl) response() operatorResponse {
	rules := make(map[string]string, len(c.state.Rules))
	for ip, id := range c.state.Rules {
		rules[ip] = id
	}
	return operatorResponse{OK: true, SourceID: c.hub.telemetry.source, InstanceID: c.hub.telemetry.instance, Generation: c.state.Generation, Rules: rules}
}
func (c *operatorControl) apply(r operatorRequest) (operatorResponse, error) {
	c.op.Lock()
	defer c.op.Unlock()
	out := c.response()
	if !operatorID.MatchString(r.RequestID) {
		return out, errors.New("invalid request ID")
	}
	if r.Action == "status" {
		return out, nil
	}
	if c.seen[r.RequestID] {
		return out, errors.New("request ID already used; reconcile with status")
	}
	if r.SourceID != out.SourceID || r.InstanceID != out.InstanceID || r.Generation != out.Generation {
		return out, errors.New("source, instance, or generation changed")
	}
	if r.Action != "block" && r.Action != "unblock" {
		return out, errors.New("invalid action")
	}
	ip, e := operatorIP(r.IP)
	if e != nil {
		return out, e
	}
	if !operatorID.MatchString(r.RuleID) {
		return out, errors.New("invalid owned rule ID")
	}
	old := c.state.Rules[ip]
	if (r.Action == "unblock" && old != r.RuleID) || (old != "" && old != r.RuleID) {
		return out, errors.New("rule owner mismatch")
	}
	if r.Action == "block" && old == "" && len(c.state.Rules) >= operatorMaxRules {
		return out, errors.New("policy full")
	}
	if c.state.Generation == ^uint64(0) {
		return out, errors.New("policy generation exhausted")
	}
	next := operatorState{Generation: c.state.Generation + 1, Rules: out.Rules}
	if r.Action == "block" {
		next.Rules[ip] = r.RuleID
	} else {
		delete(next.Rules, ip)
	}
	committed, persistErr := c.persist(next)
	if !committed {
		return c.response(), persistErr
	}
	// New admissions may complete during disk I/O. Publish under the admission
	// lock, then collect every matching connection, including those admissions.
	c.admission.Lock()
	c.state = next
	targets := []*Client{}
	c.hub.mu.RLock()
	for client := range c.hub.clients {
		if client.operatorIP == ip && r.Action == "block" {
			targets = append(targets, client)
		}
	}
	c.hub.mu.RUnlock()
	c.admission.Unlock()
	c.seen[r.RequestID] = true
	c.order = append(c.order, r.RequestID)
	if len(c.order) > 64 {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	closeDeadline := time.Now().Add(100 * time.Millisecond)
	for _, client := range targets {
		client.setCloseReason("operator_block")
		_ = client.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "operator block"), closeDeadline)
		_ = client.conn.Close()
	}
	out = c.response()
	out.Closed = len(targets)
	if persistErr != nil {
		return out, fmt.Errorf("policy applied; durability uncertain: %w", persistErr)
	}
	return out, nil
}
func (c *operatorControl) admit(r *http.Request) (string, bool, func()) {
	c.admission.RLock()
	_, ip, source := telemetryAddress(r, c.hub.telemetry.trusted)
	canonical, e := operatorIP(ip)
	if e != nil || source == "proxy_unresolved" {
		// Preserve existing admission semantics for unattributed/private peers.
		// They are never eligible for operator targeting.
		return "", true, c.admission.RUnlock
	}
	if c.state.Rules[canonical] != "" {
		c.admission.RUnlock()
		return "", false, func() {}
	}
	return canonical, true, c.admission.RUnlock
}

type operatorPeerKey struct{}

func operatorPeerUID(conn net.Conn) (uint32, error) {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("Unix peer required")
	}
	raw, e := u.SyscallConn()
	if e != nil {
		return 0, e
	}
	var cred *syscall.Ucred
	var inner error
	e = raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if e != nil {
		return 0, e
	}
	if inner != nil {
		return 0, inner
	}
	return cred.Uid, nil
}
func startOperatorControl(path string, c *operatorControl, uid uint32) (*http.Server, error) {
	if !filepath.IsAbs(path) || filepath.Dir(path) != filepath.Dir(c.path) {
		return nil, errors.New("socket and policy require same private directory")
	}
	if e := operatorPrivate(filepath.Dir(path), true); e != nil {
		return nil, e
	}
	if st, e := os.Lstat(path); e == nil {
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 {
			return nil, errors.New("unsafe existing control socket")
		}
		peer, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			peer.Close()
			return nil, errors.New("control socket already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, errors.New("control socket state uncertain")
		}
		now, err := os.Lstat(path)
		if err != nil || !os.SameFile(st, now) {
			return nil, errors.New("control socket changed")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		ln.Close()
		return nil, e
	}
	server := &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096}
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		peer, e := operatorPeerUID(conn)
		return context.WithValue(ctx, operatorPeerKey{}, e == nil && peer == uid)
	}
	slots := make(chan struct{}, 4)
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "control busy", 503)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		if r.Context().Value(operatorPeerKey{}) != true {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/control" || r.URL.RawQuery != "" {
			http.Error(w, "not found", 404)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var request operatorRequest
		if e := d.Decode(&request); e != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if d.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		out, e := c.apply(request)
		if e != nil {
			out.OK = false
			out.Error = e.Error()
			w.WriteHeader(409)
		}
		json.NewEncoder(w).Encode(out)
	})
	go server.Serve(ln)
	return server, nil
}
