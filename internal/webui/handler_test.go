package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html>INDEX")},
		"sw.js":                {Data: []byte("self.skipWaiting()")},
		"manifest.webmanifest": {Data: []byte(`{"name":"herd"}`)},
		"icon-192.png":         {Data: []byte("\x89PNG")},
		"logo.svg":             {Data: []byte("<svg/>")},
		"assets/app-1.js":      {Data: []byte("console.log(1)")},
		"assets/style-1.css":   {Data: []byte("body{}")},
		"secret/hidden.txt":    {Data: []byte("x")},
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestRootAndDeepLinkFallback(t *testing.T) {
	h := Handler(testFS())
	for _, p := range []string{"/", "/a", "/a/", "/a/agent/labs/term_1", "/index.html"} {
		w := get(t, h, "GET", p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "INDEX") {
			t.Errorf("GET %s = %d %q, want index.html", p, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q", p, got)
		}
		if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q", p, got)
		}
	}
}

func TestMissingPathsAre404(t *testing.T) {
	h := Handler(testFS())
	for _, p := range []string{
		"/assets/missing.js", "/missing.png", "/nope", "/assets", "/assets/", "/secret", "/secret/",
		"/../index.html", "/assets/../index.html/x", "/%2e%2e/index.html", "/assets/%2e%2e/%2e%2e/etc/passwd",
		"/assets//app-1.js", "/./index.html/", "/index.html/", "/%2fetc/passwd",
	} {
		w := get(t, h, "GET", p)
		if w.Code != 404 {
			t.Errorf("GET %s = %d, want 404 (%q)", p, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "INDEX") {
			t.Errorf("GET %s served index.html", p)
		}
	}
}

func TestNoDirectoryListing(t *testing.T) {
	h := Handler(testFS())
	for _, p := range []string{"/assets", "/assets/", "/secret/", "/secret"} {
		w := get(t, h, "GET", p)
		if w.Code != 404 || strings.Contains(w.Body.String(), "app-1.js") || strings.Contains(w.Body.String(), "hidden.txt") {
			t.Errorf("GET %s = %d %q", p, w.Code, w.Body.String())
		}
	}
}

func TestCacheHeaders(t *testing.T) {
	h := Handler(testFS())
	cases := []struct {
		path, cache string
		swAllowed   bool
	}{
		{"/", "no-cache", false},
		{"/index.html", "no-cache", false},
		{"/sw.js", "no-cache", true},
		{"/assets/app-1.js", "public, max-age=31536000, immutable", false},
		{"/assets/style-1.css", "public, max-age=31536000, immutable", false},
		{"/manifest.webmanifest", "no-cache", false},
		{"/icon-192.png", "no-cache", false},
	}
	for _, c := range cases {
		w := get(t, h, "GET", c.path)
		if w.Code != 200 {
			t.Fatalf("GET %s = %d", c.path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.cache)
		}
		got := w.Header().Get("Service-Worker-Allowed")
		if c.swAllowed && got != "/" || !c.swAllowed && got != "" {
			t.Errorf("GET %s Service-Worker-Allowed = %q", c.path, got)
		}
	}
	// A 404 for an asset must never be cached as immutable.
	if got := get(t, h, "GET", "/assets/missing.js").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("404 Cache-Control = %q", got)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := Handler(testFS())
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
		"manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	for _, c := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/assets/app-1.js"}, {"GET", "/missing"}, {"POST", "/"}, {"HEAD", "/sw.js"},
	} {
		w := get(t, h, c.method, c.path)
		if got := w.Header().Get("Content-Security-Policy"); got != csp {
			t.Errorf("%s %s CSP = %q", c.method, c.path, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s %s X-Content-Type-Options = %q", c.method, c.path, got)
		}
		if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s %s Referrer-Policy = %q", c.method, c.path, got)
		}
	}
}

func TestContentTypes(t *testing.T) {
	h := Handler(testFS())
	for path, want := range map[string]string{
		"/":                     "text/html; charset=utf-8",
		"/sw.js":                "text/javascript; charset=utf-8",
		"/assets/app-1.js":      "text/javascript; charset=utf-8",
		"/assets/style-1.css":   "text/css; charset=utf-8",
		"/manifest.webmanifest": "application/manifest+json",
		"/icon-192.png":         "image/png",
		"/logo.svg":             "image/svg+xml",
	} {
		if got := get(t, h, "GET", path).Header().Get("Content-Type"); got != want {
			t.Errorf("GET %s Content-Type = %q, want %q", path, got, want)
		}
	}
}

func TestMethods(t *testing.T) {
	h := Handler(testFS())
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		w := get(t, h, m, "/")
		if w.Code != 405 {
			t.Errorf("%s / = %d, want 405", m, w.Code)
		}
		if got := w.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s / Allow = %q", m, got)
		}
	}
	w := get(t, h, "HEAD", "/")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD / = %d body %q", w.Code, w.Body.String())
	}
}

func TestMissingIndex(t *testing.T) {
	h := Handler(fstest.MapFS{"other.txt": {Data: []byte("x")}})
	if w := get(t, h, "GET", "/"); w.Code != 404 {
		t.Errorf("GET / without index.html = %d, want 404", w.Code)
	}
}
