package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		hdr  []string
		want int
	}{
		{"none", nil, 401},
		{"bad bearer", []string{"Authorization", "Bearer nope"}, 401},
		{"good bearer", []string{"Authorization", "Bearer tok-1"}, 200},
		{"lowercase scheme", []string{"Authorization", "bearer tok-1"}, 200},
		{"basic scheme", []string{"Authorization", "Basic tok-1"}, 401},
		{"empty bearer", []string{"Authorization", "Bearer "}, 401},
		{"good cookie", []string{"Cookie", "hub_token=tok-1"}, 200},
		{"bad cookie", []string{"Cookie", "hub_token=nope"}, 401},
		{"bad bearer does not fall back to cookie", []string{"Authorization", "Bearer nope", "Cookie", "hub_token=tok-1"}, 401},
		{"oversized token", []string{"Authorization", "Bearer " + strings.Repeat("a", 600)}, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.serve(req("GET", "/api/me", "", c.hdr...))
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
			if c.want == 401 && errCode(t, w) != "unauthorized" {
				t.Fatalf("code = %q", errCode(t, w))
			}
		})
	}
}

func TestAuthAllAPIRoutesProtected(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ m, p string }{
		{"GET", "/api/sessions"}, {"GET", "/api/agents"}, {"GET", "/api/agents/labs/term_1"},
		{"GET", "/api/agents/labs/term_1/output"}, {"GET", "/api/timeline"}, {"GET", "/api/stream"},
		{"POST", "/api/agents/labs/term_1/seen"}, {"GET", "/api/nonexistent"},
	} {
		if w := e.serve(req(c.m, c.p, "")); w.Code != 401 {
			t.Errorf("%s %s = %d, want 401", c.m, c.p, w.Code)
		}
	}
	if w := e.do("GET", "/api/nonexistent", ""); w.Code != 404 || errCode(t, w) != "not_found" {
		t.Errorf("authed unknown route = %d %s", w.Code, w.Body)
	}
}

func TestHealthzNoAuth(t *testing.T) {
	e := newEnv(t)
	w := e.serve(req("GET", "/healthz", ""))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", w.Code, w.Body)
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/me", "")
	d := decode[store.Device](t, w)
	if d.ID != "dev-1" || d.Name != "phone" {
		t.Fatalf("me = %+v", d)
	}
}

func TestCSRFOnCookieRequests(t *testing.T) {
	e := newEnv(t)
	cookie := []string{"Cookie", "hub_token=tok-1"}
	post := func(extra ...string) *httptest.ResponseRecorder {
		return e.serve(req("POST", "/api/agents/labs/term_1/seen", "", append(cookie, extra...)...))
	}
	if w := post(); w.Code != 403 || errCode(t, w) != "csrf" {
		t.Fatalf("cookie POST without header = %d %s", w.Code, w.Body)
	}
	if w := post("X-Requested-With", "XMLHttpRequest"); w.Code != 403 {
		t.Fatalf("wrong header value = %d", w.Code)
	}
	if w := post("X-Requested-With", "hub"); w.Code != 200 {
		t.Fatalf("cookie POST with header = %d %s", w.Code, w.Body)
	}
	if w := e.serve(req("GET", "/api/agents", "", cookie...)); w.Code != 200 {
		t.Fatalf("cookie GET = %d", w.Code)
	}
	// Bearer is exempt.
	if w := e.serve(req("POST", "/api/agents/labs/term_1/seen", "", "Authorization", "Bearer tok-1")); w.Code != 200 {
		t.Fatalf("bearer POST = %d %s", w.Code, w.Body)
	}
	// Unauthenticated POST is 401, not 403.
	if w := e.serve(req("POST", "/api/agents/labs/term_1/seen", "")); w.Code != 401 {
		t.Fatalf("anon POST = %d", w.Code)
	}
}

func TestAuthCacheAndRevocation(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if w := e.do("GET", "/api/me", ""); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
	}
	if n := e.st.lookupCount(); n != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", n)
	}
	_ = e.st.RevokeDevice(context.Background(), "dev-1")
	if w := e.do("GET", "/api/me", ""); w.Code != 200 {
		t.Fatalf("within TTL still cached, got %d", w.Code)
	}
	e.clock.Advance(5 * time.Second) // cache is at most 5 s
	if w := e.do("GET", "/api/me", ""); w.Code != 401 {
		t.Fatalf("after TTL revoked token = %d, want 401", w.Code)
	}
	if n := e.st.lookupCount(); n != 2 {
		t.Fatalf("lookups = %d, want 2", n)
	}
	// Failed lookups are not cached.
	e.do("GET", "/api/me", "")
	if n := e.st.lookupCount(); n != 3 {
		t.Fatalf("lookups = %d, want 3", n)
	}
}

func TestPairSuccess(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Secure = true })
	w := e.serve(req("POST", "/api/pair", `{"code":"good-code","device_name":"my phone"}`))
	if w.Code != 200 {
		t.Fatalf("pair = %d %s", w.Code, w.Body)
	}
	resp := decode[struct {
		Token  string       `json:"token"`
		Device store.Device `json:"device"`
	}](t, w)
	if resp.Token != "paired-token-1" || resp.Device.Name != "my phone" {
		t.Fatalf("resp = %+v", resp)
	}
	var c *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "hub_token" {
			c = ck
		}
	}
	if c == nil {
		t.Fatal("no hub_token cookie")
	}
	if c.Value != resp.Token || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 365*24*3600 {
		t.Fatalf("cookie = %+v", c)
	}
	// The cookie authenticates subsequent requests.
	if w := e.serve(req("GET", "/api/me", "", "Cookie", "hub_token="+resp.Token)); w.Code != 200 {
		t.Fatalf("me via cookie = %d %s", w.Code, w.Body)
	}
	// Code is one-time.
	if w := e.serve(req("POST", "/api/pair", `{"code":"good-code"}`)); w.Code != 401 {
		t.Fatalf("reused code = %d", w.Code)
	}
}

func TestPairCookieNotSecureByDefault(t *testing.T) {
	e := newEnv(t)
	w := e.serve(req("POST", "/api/pair", `{"code":"good-code"}`))
	for _, ck := range w.Result().Cookies() {
		if ck.Secure {
			t.Fatal("cookie must not be Secure when Config.Secure is false")
		}
	}
	if d := decode[struct{ Device store.Device }](t, w).Device; d.Name != "device" {
		t.Fatalf("default device name = %q", d.Name)
	}
}

func TestPairInvalid(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"code":"wrong"}`, `{"code":"good-code","extra":1}`, `{}`, `nope`, ``} {
		w := e.serve(req("POST", "/api/pair", body))
		want := 401
		if body != `{"code":"wrong"}` {
			want = 400
		}
		if w.Code != want {
			t.Errorf("body %q = %d, want %d (%s)", body, w.Code, want, w.Body)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("body %q set a cookie", body)
		}
	}
	if w := e.serve(req("POST", "/api/pair", `{"code":"wrong"}`)); errCode(t, w) != "invalid_code" {
		t.Fatalf("code = %q", errCode(t, w))
	}
}

func TestPairRateLimit(t *testing.T) {
	e := newEnv(t)
	try := func(addr string) *httptest.ResponseRecorder {
		r := req("POST", "/api/pair", `{"code":"wrong"}`)
		r.RemoteAddr = addr
		return e.serve(r)
	}
	for i := 0; i < 10; i++ {
		if w := try("203.0.113.9:1000" + strconv.Itoa(i)); w.Code != 401 {
			t.Fatalf("attempt %d = %d", i, w.Code)
		}
	}
	w := try("203.0.113.9:5555") // port differs; same IP
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("11th attempt = %d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	if w := try("203.0.113.10:1"); w.Code != 401 {
		t.Fatalf("other IP = %d, want 401", w.Code)
	}
	e.clock.Advance(7 * time.Second) // one token back (10/min)
	if w := try("203.0.113.9:5555"); w.Code != 401 {
		t.Fatalf("after refill = %d", w.Code)
	}
}

func TestHostHeader(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AllowedHosts = []string{"hub.example.com", "*.ts.net"} })
	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:7878", 200}, {"localhost", 200}, {"LOCALHOST:80", 200}, {"[::1]:7878", 200},
		{"127.1.2.3", 200}, {"app.localhost", 200},
		{"evil.example.org", 403}, {"evil.example.org:7878", 403}, {"192.168.1.5:7878", 403},
		{"hub.example.com", 200}, {"hub.example.com:8443", 200},
		{"macbook.tail1234.ts.net", 200}, {"ts.net", 403}, {"evil-ts.net", 403}, {"x.ts.net.evil.com", 403},
	}
	for _, c := range cases {
		r := req("GET", "/healthz", "")
		r.Host = c.host
		w := e.serve(r)
		if w.Code != c.want {
			t.Errorf("Host %q = %d, want %d", c.host, w.Code, c.want)
		}
		if c.want == 403 && errCode(t, w) != "forbidden_host" {
			t.Errorf("Host %q code = %q", c.host, errCode(t, w))
		}
	}
	// Default config: loopback only.
	d := newEnv(t)
	r := req("GET", "/healthz", "")
	r.Host = "macbook.tail1234.ts.net"
	if w := d.serve(r); w.Code != 403 {
		t.Fatalf("default config ts.net = %d, want 403", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	for _, w := range []*httptest.ResponseRecorder{
		e.do("GET", "/api/sessions", ""),
		e.serve(req("GET", "/api/sessions", "")), // 401 too
		e.serve(req("GET", "/healthz", "")),
	} {
		h := w.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("headers = %v", h)
		}
	}
}
