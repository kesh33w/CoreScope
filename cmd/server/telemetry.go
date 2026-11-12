package main

// Private, opt-in connection telemetry. This never reads or writes a database.
import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type connectionSession struct {
	PageID               string `json:"page_id"`
	PageSource           string `json:"page_source"`
	PageReportedAt       string `json:"page_reported_at"`
	ConnectionID         string `json:"connection_id"`
	SourceID             string `json:"source_id"`
	InstanceID           string `json:"instance_id"`
	Domain               string `json:"domain"`
	PeerIP               string `json:"peer_ip"`
	ClientIP             string `json:"client_ip"`
	AddressSource        string `json:"address_source"`
	Origin               string `json:"origin"`
	UserAgent            string `json:"user_agent"`
	ClientKind           string `json:"client_kind"`
	ClientLabel          string `json:"client_label"`
	Confidence           string `json:"confidence"`
	ConnectedAt          string `json:"connected_at"`
	LastReadAt           string `json:"last_read_at"`
	LastWriteAt          string `json:"last_write_at"`
	LastPongAt           string `json:"last_pong_at"`
	MessagesSent         uint64 `json:"messages_sent"`
	MessagesReceived     uint64 `json:"messages_received"`
	PayloadBytesSent     uint64 `json:"payload_bytes_sent"`
	PayloadBytesReceived uint64 `json:"payload_bytes_received"`
	QueueDrops           uint64 `json:"queue_drops"`
	ClosedAt             string `json:"closed_at"`
	CloseReason          string `json:"close_reason"`
	EndSequence          uint64 `json:"end_sequence"`
}
type telemetryLimits struct {
	MaxActive     int    `json:"max_active"`
	MaxClosed     int    `json:"max_closed"`
	MaxHTTPSeries int    `json:"max_http_series"`
	ActiveOmitted uint64 `json:"active_omitted"`
	HTTPOmitted   uint64 `json:"http_omitted"`
}
type telemetryHTTP struct {
	Method        string  `json:"method"`
	Route         string  `json:"route"`
	Status        int     `json:"status"`
	Requests      uint64  `json:"requests"`
	ResponseBytes uint64  `json:"response_bytes"`
	DurationTotal float64 `json:"duration_ms_total"`
	DurationMax   float64 `json:"duration_ms_max"`
}
type telemetrySnapshot struct {
	SchemaVersion    int                 `json:"schema_version"`
	SourceID         string              `json:"source_id"`
	InstanceID       string              `json:"instance_id"`
	CapturedAt       string              `json:"captured_at"`
	Sequence         uint64              `json:"sequence"`
	EarliestSequence uint64              `json:"earliest_sequence"`
	DroppedEvents    uint64              `json:"dropped_events"`
	Active           []connectionSession `json:"active"`
	Closed           []connectionSession `json:"closed"`
	HTTP             []telemetryHTTP     `json:"http"`
	Limits           telemetryLimits     `json:"limits"`
}
type connectionTelemetry struct {
	trusted                   []*net.IPNet
	mu                        sync.Mutex
	source, instance          string
	nextID, sequence, dropped uint64
	active                    map[string]*connectionSession
	closed                    []connectionSession
	head                      int
	limits                    telemetryLimits
}

var telemetryLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func newConnectionTelemetry(source string, maxActive, maxClosed int) *connectionTelemetry {
	if !telemetryLabel.MatchString(source) {
		source = "corescope"
	}
	if maxActive < 1 {
		maxActive = 1
	}
	if maxClosed < 1 {
		maxClosed = 1
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("cannot generate telemetry instance ID")
	}
	return &connectionTelemetry{source: source, instance: hex.EncodeToString(b), active: make(map[string]*connectionSession), limits: telemetryLimits{MaxActive: maxActive, MaxClosed: maxClosed}}
}
func telemetryTime() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func telemetryHost(raw string) string {
	if len(raw) > 300 || strings.ContainsAny(raw, "/@?#\\ \t\r\n") {
		return ""
	}
	host := raw
	if h, _, e := net.SplitHostPort(raw); e == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if net.ParseIP(host) != nil {
		return host
	}
	if len(host) == 0 || len(host) > 253 {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return ""
			}
		}
	}
	return host
}
func telemetryOrigin(raw string) string {
	if len(raw) > 2048 {
		return ""
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || len(u.Host) > 300 || (u.Scheme != "http" && u.Scheme != "https") || telemetryHost(u.Host) == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
	}
	// The path, query, and fragment never leave the process.
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
func (t *connectionTelemetry) open(r *http.Request) string {
	if t == nil {
		return ""
	}
	s := connectionSession{SourceID: t.source, InstanceID: t.instance, Domain: telemetryHost(r.Host), Origin: telemetryOrigin(r.Header.Get("Origin")), AddressSource: "proxy_unresolved", ClientKind: "unknown", Confidence: "unknown", ConnectedAt: telemetryTime()}
	s.PeerIP, s.ClientIP, s.AddressSource = telemetryAddress(r, t.trusted)
	ua := r.UserAgent()
	if len(ua) <= 1024 {
		switch {
		case strings.HasPrefix(ua, "NodeScope/"):
			s.UserAgent = "NodeScope"
			s.ClientLabel = "NodeScope"
			s.ClientKind = "self_identified_app"
			s.Confidence = "self_reported"
		case strings.Contains(ua, "Firefox/"):
			s.UserAgent = "Firefox"
		case strings.Contains(ua, "Edg/"):
			s.UserAgent = "Edge"
		case strings.Contains(ua, "Chrome/"):
			s.UserAgent = "Chrome"
		case strings.Contains(ua, "Safari/"):
			s.UserAgent = "Safari"
		}
	}
	if s.ClientKind == "unknown" && s.UserAgent != "" && s.Origin != "" {
		u, _ := url.Parse(s.Origin)
		if strings.EqualFold(u.Host, r.Host) {
			s.ClientKind = "likely_browser"
			s.ClientLabel = s.UserAgent
			s.Confidence = "inferred"
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.active) >= t.limits.MaxActive {
		t.limits.ActiveOmitted++
		return ""
	}
	t.nextID++
	s.ConnectionID = fmt.Sprintf("%s-%d", t.instance, t.nextID)
	t.active[s.ConnectionID] = &s
	return s.ConnectionID
}

// Page reports are optional browser claims, never proof of attention. Only
// coarse, fixed labels enter private telemetry; raw paths and parameters do not.
var telemetryPageNames = map[string]string{
	"home": "Home", "packets": "Packets", "packet-detail": "Packet details",
	"nodes": "Nodes", "node-analytics": "Node analytics", "node-reach": "Node reach",
	"map": "Map", "live": "Live map", "channels": "Channels",
	"observers": "Observers", "observer-detail": "Observer details",
	"analytics": "Analytics", "tools": "Tools", "traces": "Traces",
	"path-inspector": "Path inspector", "audio-lab": "Audio lab", "perf": "Performance",
	"compare": "Compare", "rx-coverage": "Reception coverage", "scope-audit": "Scope audit",
	"account": "Account", "admin": "Administration", "unknown": "Unknown",
}

type telemetryPageReport struct {
	Type string `json:"type"`
	Page string `json:"page"`
}

func (t *connectionTelemetry) pageReport(id string, data []byte) {
	if t == nil || id == "" || len(data) == 0 || len(data) > 128 {
		return
	}
	var report telemetryPageReport
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&report) != nil || report.Type != "page_view" {
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return
	}
	_, ok := telemetryPageNames[report.Page]
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.active[id]; s != nil && s.PageID != report.Page {
		s.PageID = report.Page
		s.PageSource = "browser_reported"
		s.PageReportedAt = telemetryTime()
	}
}

func (t *connectionTelemetry) activity(id, kind string, n int) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.active[id]
	if s == nil {
		return
	}
	switch kind {
	case "read":
		s.MessagesReceived++
		s.PayloadBytesReceived += uint64(n)
		s.LastReadAt = telemetryTime()
	case "write":
		s.MessagesSent++
		s.PayloadBytesSent += uint64(n)
		s.LastWriteAt = telemetryTime()
	case "pong":
		s.LastPongAt = telemetryTime()
	case "drop":
		s.QueueDrops++
	}
}
func (t *connectionTelemetry) close(id, reason string) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.active[id]
	if s == nil {
		return
	}
	switch reason {
	case "normal", "peer_closed", "read_timeout", "read_error", "write_error", "server_shutdown":
	default:
		reason = "unknown"
	}
	delete(t.active, id)
	t.sequence++
	s.EndSequence = t.sequence
	s.ClosedAt = telemetryTime()
	s.CloseReason = reason
	if len(t.closed) < t.limits.MaxClosed {
		t.closed = append(t.closed, *s)
	} else {
		t.closed[t.head] = *s
		t.head = (t.head + 1) % len(t.closed)
		t.dropped++
	}
}
func (t *connectionTelemetry) snapshot() telemetrySnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := telemetrySnapshot{SchemaVersion: 1, SourceID: t.source, InstanceID: t.instance, CapturedAt: telemetryTime(), Sequence: t.sequence, EarliestSequence: t.sequence + 1, DroppedEvents: t.dropped, Limits: t.limits, Active: make([]connectionSession, 0, len(t.active)), Closed: make([]connectionSession, 0, len(t.closed)), HTTP: []telemetryHTTP{}}
	for _, v := range t.active {
		s.Active = append(s.Active, *v)
	}
	for i := 0; i < len(t.closed); i++ {
		s.Closed = append(s.Closed, t.closed[(t.head+i)%len(t.closed)])
	}
	if len(s.Closed) > 0 {
		s.EarliestSequence = s.Closed[0].EndSequence
	}
	return s
}

// startTelemetry refuses existing paths and requires a private directory. It
// never deletes a pre-existing socket or changes directory permissions.
func startTelemetry(path string, t *connectionTelemetry) (*http.Server, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("telemetry socket requires clean absolute path")
	}
	dir := filepath.Dir(path)
	resolved, e := filepath.EvalSymlinks(dir)
	if e != nil || resolved != dir {
		return nil, fmt.Errorf("telemetry directory must exist without symlinks")
	}
	info, e := os.Stat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("telemetry directory must be private (0700)")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("telemetry directory must be owned by the current user")
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		return nil, fmt.Errorf("telemetry socket path already exists or is inaccessible")
	}
	l, e := net.Listen("unix", path)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		l.Close()
		return nil, e
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" || r.URL.Path != "/v1/snapshot" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(t.snapshot())
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096}
	go func() {
		if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
			log.Print("[telemetry] private exporter stopped unexpectedly")
		}
	}()
	return srv, nil
}

// Trust is telemetry-only and must describe a verified, header-sanitizing ingress
// chain. Trusting the final hop alone cannot repair spoofable upstream headers.
func parseTelemetryTrust(raw string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	if raw == "" {
		return out, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 64 {
		return nil, fmt.Errorf("too many telemetry trusted proxies")
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if ip := net.ParseIP(part); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid telemetry trusted proxy")
		}
		out = append(out, network)
	}
	return out, nil
}
func telemetryAddress(r *http.Request, trusted []*net.IPNet) (peer, client, source string) {
	source = "proxy_unresolved"
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return
	}
	peer = ip.String()
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !isTrusted(ip) {
		if ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() {
			client = peer
			source = "direct"
		}
		return
	}
	raw := r.Header.Values("X-Forwarded-For")
	if len(raw) != 1 || len(raw[0]) > 2048 {
		return
	}
	parts := strings.Split(raw[0], ",")
	if len(parts) > 32 {
		return
	}
	chain := make([]net.IP, len(parts))
	for i, p := range parts {
		chain[i] = net.ParseIP(strings.TrimSpace(p))
		if chain[i] == nil || chain[i].IsUnspecified() || chain[i].IsMulticast() {
			return
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !isTrusted(chain[i]) {
			client = chain[i].String()
			source = "trusted_forwarded"
			return
		}
	}
	return
}
