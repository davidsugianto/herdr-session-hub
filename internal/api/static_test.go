package api

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/davidsugianto/herdr-session-hub/internal/webui"
)

func newStaticEnv(t *testing.T) *env {
	t.Helper()
	ui := webui.Handler(fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>herd</title>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	})
	return newEnv(t, func(c *Config) { c.Static = ui })
}

func TestStaticRootServesAppWithAppHeaders(t *testing.T) {
	e := newStaticEnv(t)
	w := e.serve(req("GET", "/", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>herd</title>") {
		t.Fatalf("GET / = %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q, want the app CSP", csp)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "no-store" {
		t.Errorf("Cache-Control = %q on static response", cc)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff")
	}
	w = e.serve(req("GET", "/assets/app-1.js", ""))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("asset = %d Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := e.serve(req("GET", "/assets/missing.js", "")); w.Code != 404 || strings.Contains(w.Body.String(), "<title>") {
		t.Errorf("missing asset = %d %q", w.Code, w.Body.String())
	}
	if w := e.serve(req("GET", "/a/agent/labs/term_1", "")); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>herd</title>") {
		t.Errorf("deep link = %d %q", w.Code, w.Body.String())
	}
}

func assertStrict(t *testing.T, what string, h http.Header) {
	t.Helper()
	if got := h.Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Errorf("%s: CSP = %q", what, got)
	}
	if got := h.Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s: Cache-Control = %q", what, got)
	}
}

func TestStaticDoesNotWeakenAPI(t *testing.T) {
	e := newStaticEnv(t)

	w := e.serve(req("GET", "/api/me", ""))
	if w.Code != 401 || errCode(t, w) != "unauthorized" {
		t.Fatalf("unauthenticated /api/me = %d %s", w.Code, w.Body)
	}
	assertStrict(t, "/api/me 401", w.Header())

	// Unknown API paths stay behind auth and never fall through to the UI.
	for _, p := range []string{"/api/zzz", "/api/", "/api/agents/x/y/z"} {
		w = e.serve(req("GET", p, ""))
		if w.Code != 401 || strings.Contains(w.Body.String(), "<title>") {
			t.Errorf("unauthenticated %s = %d %q, want 401 JSON", p, w.Code, w.Body.String())
		}
		assertStrict(t, p, w.Header())
	}
	w = e.do("GET", "/api/zzz", "")
	if w.Code != 404 || errCode(t, w) != "not_found" || strings.Contains(w.Body.String(), "<title>") {
		t.Fatalf("authenticated /api/zzz = %d %s", w.Code, w.Body)
	}
	assertStrict(t, "/api/zzz 404", w.Header())

	// Non-GET on an API path hits the API, not the UI.
	w = e.serve(req("POST", "/api/me", ""))
	if w.Code != 401 || strings.Contains(w.Body.String(), "<title>") {
		t.Errorf("POST /api/me = %d %q", w.Code, w.Body.String())
	}

	w = e.serve(req("GET", "/healthz", ""))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", w.Code, w.Body)
	}
	assertStrict(t, "/healthz", w.Header())

	// An authed API success keeps the strict headers too.
	assertStrict(t, "/api/me 200", e.do("GET", "/api/me", "").Header())
}

func TestStaticBehindHostCheck(t *testing.T) {
	e := newStaticEnv(t)
	for _, p := range []string{"/", "/assets/app-1.js", "/a/x", "/api/me", "/healthz", "/nope"} {
		r := req("GET", p, "")
		r.Host = "evil.example.com"
		w := e.serve(r)
		if w.Code != 403 || errCode(t, w) != "forbidden_host" {
			t.Errorf("GET %s with foreign Host = %d %s", p, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "<title>") {
			t.Errorf("GET %s leaked the UI to a foreign Host", p)
		}
		assertStrict(t, p+" foreign host", w.Header())
	}
}

// Without a Static handler the API keeps its strict headers everywhere and
// unknown non-API paths are not served.
func TestNoStaticKeepsStrictHeaders(t *testing.T) {
	e := newEnv(t)
	w := e.serve(req("GET", "/", ""))
	if w.Code != 404 {
		t.Fatalf("GET / without Static = %d", w.Code)
	}
	assertStrict(t, "/ without Static", w.Header())
}
