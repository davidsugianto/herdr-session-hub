package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

func TestListDevices(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/devices", "")
	if w.Code != 200 {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	devs := decode[[]map[string]any](t, w)
	if len(devs) != 2 || devs[0]["id"] != "dev-1" || devs[1]["id"] != "dev-2" {
		t.Fatalf("devices = %v", devs)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "token") {
		t.Fatalf("response leaks token field: %s", w.Body)
	}
}

func TestListDevicesEmptyIsArray(t *testing.T) {
	e := newEnv(t)
	// Authenticate with tok-1, then empty the device list behind the cache.
	if w := e.do("GET", "/api/me", ""); w.Code != 200 {
		t.Fatalf("me = %d", w.Code)
	}
	e.st.mu.Lock()
	e.st.tokens = map[string]store.Device{}
	e.st.mu.Unlock()
	w := e.do("GET", "/api/devices", "")
	if w.Code != 200 {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

func TestRevokeOtherDevice(t *testing.T) {
	e := newEnv(t)
	w := e.do("DELETE", "/api/devices/dev-2", "")
	if w.Code != 200 {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	got := decode[map[string]any](t, w)
	if got["ok"] != true || got["warning"] != nil {
		t.Fatalf("body = %v", got)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("cookie must not be touched: %v", w.Result().Cookies())
	}
	if w := e.serve(req("GET", "/api/me", "", "Authorization", "Bearer tok-2")); w.Code != 401 {
		t.Fatalf("revoked device = %d", w.Code)
	}
	if w := e.do("GET", "/api/me", ""); w.Code != 200 {
		t.Fatalf("caller after revoke = %d", w.Code)
	}
}

func TestRevokeUnknownDevice(t *testing.T) {
	e := newEnv(t)
	w := e.do("DELETE", "/api/devices/nope", "")
	if w.Code != 404 || errCode(t, w) != "not_found" {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	rows := e.st.auditRows()
	if len(rows) != 1 || rows[0].Action != "revoke_device" || rows[0].Result == "ok" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestRevokeSelf(t *testing.T) {
	e := newEnv(t)
	w := e.serve(req("DELETE", "/api/devices/dev-1", "", "Cookie", "hub_token=tok-1", csrfHeader, csrfValue))
	if w.Code != 200 {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	got := decode[map[string]any](t, w)
	if got["ok"] != true || got["warning"] != "this device was revoked; you are now signed out" {
		t.Fatalf("body = %v", got)
	}
	var c *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			c = ck
		}
	}
	if c == nil || c.MaxAge != -1 || c.Value != "" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", c)
	}
	if w := e.serve(req("GET", "/api/me", "", "Authorization", "Bearer tok-1")); w.Code != 401 {
		t.Fatalf("after self revoke = %d", w.Code)
	}
}

func TestRevokeSelfCookieSecureFlag(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Secure = true })
	w := e.do("DELETE", "/api/devices/dev-1", "")
	var c *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			c = ck
		}
	}
	if c == nil || !c.Secure || c.MaxAge != -1 {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestRevokeTakesEffectImmediately(t *testing.T) {
	e := newEnv(t)
	// Warm the auth cache for tok-2.
	if w := e.serve(req("GET", "/api/me", "", "Authorization", "Bearer tok-2")); w.Code != 200 {
		t.Fatalf("warm = %d", w.Code)
	}
	if w := e.do("DELETE", "/api/devices/dev-2", ""); w.Code != 200 {
		t.Fatalf("revoke = %d %s", w.Code, w.Body)
	}
	// No clock advance: still inside the cache TTL.
	if w := e.serve(req("GET", "/api/me", "", "Authorization", "Bearer tok-2")); w.Code != 401 {
		t.Fatalf("after revoke = %d, want 401 without waiting for TTL", w.Code)
	}
}

func TestAuthCacheForget(t *testing.T) {
	e := newEnv(t)
	c := newAuthCache(e.st, e.clock.Now, time.Minute)
	for _, tok := range []string{"tok-1", "tok-2"} {
		if _, err := c.lookup(t.Context(), tok); err != nil {
			t.Fatal(err)
		}
	}
	c.forget("dev-1")
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("entries after forget = %d, want 1", n)
	}
}

func TestRevokeRequiresCSRFForCookie(t *testing.T) {
	e := newEnv(t)
	w := e.serve(req("DELETE", "/api/devices/dev-2", "", "Cookie", "hub_token=tok-1"))
	if w.Code != 403 || errCode(t, w) != "csrf" {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	if len(e.st.auditRows()) != 0 {
		t.Fatal("csrf-rejected request must not be audited")
	}
	w = e.serve(req("DELETE", "/api/devices/dev-2", "", "Cookie", "hub_token=tok-1", csrfHeader, csrfValue))
	if w.Code != 200 {
		t.Fatalf("with header = %d %s", w.Code, w.Body)
	}
}

func TestDevicesRequireAuth(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/devices"},
		{"DELETE", "/api/devices/dev-2"},
	} {
		w := e.serve(req(c.method, c.path, ""))
		if w.Code != 401 || errCode(t, w) != "unauthorized" {
			t.Fatalf("%s %s = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if len(e.st.auditRows()) != 0 {
		t.Fatal("unauthenticated request must not be audited")
	}
}

func TestRevokeAudited(t *testing.T) {
	e := newEnv(t)
	if w := e.do("DELETE", "/api/devices/dev-2", ""); w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	rows := e.st.auditRows()
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	r := rows[0]
	if r.Actor != "dev-1" || r.Surface != "api" || r.Action != "revoke_device" ||
		r.Payload != `{"id":"dev-2"}` || r.Result != "ok" || r.AgentID != "" || !r.At.Equal(e.clock.Now()) {
		t.Fatalf("audit = %+v", r)
	}
}

func TestRevokeRateLimited(t *testing.T) {
	e := newEnv(t)
	del := func() *httptest.ResponseRecorder {
		return e.serve(req("DELETE", "/api/devices/nope", "", "Authorization", "Bearer tok-1"))
	}
	for i := 0; i < 30; i++ {
		if w := del(); w.Code != 404 {
			t.Fatalf("request %d = %d", i, w.Code)
		}
	}
	w := del()
	if w.Code != 429 || errCode(t, w) != "rate_limited" {
		t.Fatalf("31st = %d %s", w.Code, w.Body)
	}
	if n := len(e.st.auditRows()); n != 30 {
		t.Fatalf("limited request must not be audited, rows = %d", n)
	}
	// GET list is not limited.
	if w := e.do("GET", "/api/devices", ""); w.Code != 200 {
		t.Fatalf("GET while limited = %d", w.Code)
	}
}
