// Package api implements the hub's HTTP API: REST, SSE and device auth.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	maxBodyBytes  = 64 << 10
	ringSize      = 500
	authCacheTTL  = 3 * time.Second
	actionTimeout = 15 * time.Second
	cookieName    = "hub_token"
	csrfHeader    = "X-Requested-With"
	csrfValue     = "hub"

	defaultHeartbeat = 15 * time.Second
)

// Engine is the state engine as the API needs it (implemented by *model.Engine).
type Engine interface {
	Sessions() []model.SessionInfo
	Agents(session string) []model.Agent
	Agent(id string) (model.Agent, bool)
	MarkSeen(id string) bool
	Subscribe(ctx context.Context) <-chan model.Change
}

// Config wires the API to its collaborators.
type Config struct {
	Engine  Engine
	Store   store.Store
	Actions actions.Service
	Now     func() time.Time // default time.Now
	// Secure sets the Secure flag on the auth cookie (false on plain-http localhost).
	Secure bool
	// AllowedHosts lists extra Host header names accepted besides loopback
	// (localhost, 127.0.0.0/8, ::1). Entries are exact names or "*.suffix"
	// patterns such as "*.ts.net". Ports are ignored. This blunts DNS rebinding.
	AllowedHosts []string
	// HeartbeatInterval is the SSE keep-alive comment period (default 15s).
	HeartbeatInterval time.Duration
}

type server struct {
	ctx       context.Context
	eng       Engine
	st        store.Store
	act       actions.Service
	now       func() time.Time
	secure    bool
	hosts     hostMatcher
	heartbeat time.Duration

	auth        *authCache
	actionLimit *limiter // per device: burst 30, refill 2/s
	pairLimit   *limiter // per remote IP: 10/minute
	ring        *ring
}

// New starts a goroutine (stopped by ctx) that reads Engine.Subscribe into a
// ring buffer (500 entries, monotonically increasing event ids) for SSE
// replay, and returns the handler.
func New(ctx context.Context, c Config) http.Handler {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	hb := c.HeartbeatInterval
	if hb <= 0 {
		hb = defaultHeartbeat
	}
	s := &server{
		ctx:         ctx,
		eng:         c.Engine,
		st:          c.Store,
		act:         c.Actions,
		now:         now,
		secure:      c.Secure,
		hosts:       newHostMatcher(c.AllowedHosts),
		heartbeat:   hb,
		auth:        newAuthCache(c.Store, now, authCacheTTL),
		actionLimit: newLimiter(30, 2, now),
		pairLimit:   newLimiter(10, 10.0/60.0, now),
		ring:        newRing(ringSize, now()),
	}
	changes := c.Engine.Subscribe(ctx)
	go s.pump(changes)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/pair", s.handlePair)
	mux.Handle("GET /api/me", s.authed(s.handleMe))
	mux.Handle("GET /api/sessions", s.authed(s.handleSessions))
	mux.Handle("GET /api/agents", s.authed(s.handleAgents))
	mux.Handle("GET /api/agents/{rest...}", s.authed(s.handleAgentGet))
	mux.Handle("POST /api/agents/{rest...}", s.authed(s.handleAgentPost))
	mux.Handle("GET /api/devices", s.authed(s.handleDevices))
	mux.Handle("DELETE /api/devices/{id}", s.authed(s.handleRevokeDevice))
	mux.Handle("GET /api/timeline", s.authed(s.handleTimeline))
	mux.Handle("GET /api/stream", s.authed(s.handleStream))
	mux.Handle("/api/", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeAPIErr(w, newErr(http.StatusNotFound, "not_found", "no such route"))
	}))
	return s.base(mux)
}

// base rejects foreign Host headers and sets the API's security headers.
func (s *server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if !s.hosts.allowed(r.Host) {
			writeAPIErr(w, newErr(http.StatusForbidden, "forbidden_host", "host not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- host matching ----

type hostMatcher struct {
	exact    map[string]bool
	suffixes []string // ".ts.net"
}

func normHost(hostport string) string {
	var h string
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

func newHostMatcher(extra []string) hostMatcher {
	m := hostMatcher{exact: map[string]bool{}}
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(e, "*."); ok {
			m.suffixes = append(m.suffixes, "."+normHost(rest))
			continue
		}
		m.exact[normHost(e)] = true
	}
	return m
}

func (m hostMatcher) allowed(hostport string) bool {
	h := normHost(hostport)
	if h == "" {
		return false
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
		return true
	}
	if m.exact[h] {
		return true
	}
	for _, s := range m.suffixes {
		if strings.HasSuffix(h, s) && len(h) > len(s) {
			return true
		}
	}
	return false
}

// ---- JSON helpers and errors ----

type apiErr struct {
	status int
	code   string
	msg    string
	cause  error
}

func newErr(status int, code, msg string) *apiErr {
	return &apiErr{status: status, code: code, msg: msg}
}

func (e *apiErr) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.code + ": " + e.msg
}

func (e *apiErr) Unwrap() error { return e.cause }

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b = []byte(`{"error":{"code":"internal","message":"encode failed"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeAPIErr(w http.ResponseWriter, e *apiErr) {
	writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.msg}})
}

func writeTooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeAPIErr(w, newErr(http.StatusTooManyRequests, "rate_limited", "too many requests"))
}

// fromActionErr maps an actions error to an HTTP error.
func fromActionErr(err error) *apiErr {
	e := newErr(http.StatusBadGateway, "action_failed", err.Error())
	switch {
	case errors.Is(err, actions.ErrNotBlocked):
		e.status, e.code = http.StatusConflict, "not_blocked"
	case errors.Is(err, actions.ErrPromptChanged):
		e.status, e.code = http.StatusConflict, "prompt_changed"
	case errors.Is(err, actions.ErrNoPrompt):
		e.status, e.code = http.StatusConflict, "no_prompt"
	case errors.Is(err, actions.ErrActionNotAvail):
		e.status, e.code = http.StatusUnprocessableEntity, "action_unavailable"
	case errors.Is(err, actions.ErrAgentBlocked):
		e.status, e.code = http.StatusConflict, "agent_blocked"
	case errors.Is(err, actions.ErrInvalidInput):
		e.status, e.code = http.StatusBadRequest, "bad_request"
	case errors.Is(err, actions.ErrAgentGone):
		e.status, e.code = http.StatusNotFound, "agent_not_found"
	case errors.Is(err, actions.ErrSessionOffline):
		e.status, e.code = http.StatusServiceUnavailable, "session_offline"
	case errors.Is(err, context.DeadlineExceeded):
		e.status, e.code = http.StatusGatewayTimeout, "timeout"
	}
	e.cause = err
	return e
}
