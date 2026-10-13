package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	tunnelHost  = "macbook.tail1234.ts.net"
	tunnelLogin = "owner@example.com"
	loopPeer    = "127.0.0.1:50000"
	remotePeer  = "100.64.1.2:50000"
)

// withTunnel saves tunnel settings in the fake store and wires it as Settings.
func withTunnel(host, login string) func(*Config) {
	return func(c *Config) {
		fs := c.Store.(*fakeStore)
		if host != "" {
			fs.settings[store.SettingTunnelHost] = host
		}
		if login != "" {
			fs.settings[store.SettingTunnelLogin] = login
		}
		c.Settings = fs
	}
}

// idReq builds a request with an explicit Host, peer and optional login header.
func idReq(method, path, host, peer, login string, hdr ...string) *http.Request {
	r := req(method, path, "", hdr...)
	r.Host = host
	r.RemoteAddr = peer
	if login != "" {
		r.Header.Set("Tailscale-User-Login", login)
	}
	return r
}

func TestIdentity(t *testing.T) {
	bearer := []string{"Authorization", "Bearer tok-1"}
	cases := []struct {
		name              string
		host, peer, login string
		path              string
		want              int
		code              string
	}{
		{"tunnel host, matching header, loopback peer: healthz", tunnelHost, loopPeer, tunnelLogin, "/healthz", 200, ""},
		{"tunnel host, matching header, loopback peer: authed", tunnelHost, loopPeer, tunnelLogin, "/api/me", 200, ""},
		{"tunnel host with port", tunnelHost + ":443", loopPeer, tunnelLogin, "/api/me", 200, ""},
		{"login compared case-insensitively", tunnelHost, loopPeer, "Owner@Example.COM", "/api/me", 200, ""},
		{"ipv6 loopback peer", tunnelHost, "[::1]:5000", tunnelLogin, "/healthz", 200, ""},
		{"tunnel host without header", tunnelHost, loopPeer, "", "/healthz", 403, "forbidden_identity"},
		{"tunnel host wrong login", tunnelHost, loopPeer, "mallory@example.com", "/api/me", 403, "forbidden_identity"},
		{"spoofed header from non-loopback peer", tunnelHost, remotePeer, tunnelLogin, "/api/me", 403, "forbidden_identity"},
		{"spoofed header on loopback host from remote peer", testHost, remotePeer, tunnelLogin, "/healthz", 403, "forbidden_identity"},
		{"loopback host, wrong login header", testHost, loopPeer, "mallory@example.com", "/api/me", 403, "forbidden_identity"},
		{"loopback host, matching header", testHost, loopPeer, tunnelLogin, "/api/me", 200, ""},
		{"plain loopback, no header", testHost, loopPeer, "", "/api/me", 200, ""},
		{"plain loopback, no header, remote peer unchanged", testHost, remotePeer, "", "/api/me", 200, ""},
		{"other tailnet host", "other.tail1234.ts.net", loopPeer, tunnelLogin, "/healthz", 403, "forbidden_host"},
		{"foreign host", "evil.example.org", loopPeer, tunnelLogin, "/healthz", 403, "forbidden_host"},
	}
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.serve(idReq("GET", c.path, c.host, c.peer, c.login, bearer...))
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
			if c.code != "" {
				if got := errCode(t, w); got != c.code {
					t.Fatalf("code = %q, want %q", got, c.code)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("rejection lacks strict headers: %v", w.Header())
				}
			}
		})
	}
}

func TestIdentityDuplicateHeaderRejected(t *testing.T) {
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin))
	r := idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)
	r.Header.Add("Tailscale-User-Login", "mallory@example.com")
	if w := e.serve(r); w.Code != 403 {
		t.Fatalf("two login headers = %d, want 403", w.Code)
	}
	r = idReq("GET", "/healthz", testHost, loopPeer, "")
	r.Header["Tailscale-User-Login"] = []string{""}
	if w := e.serve(r); w.Code != 403 {
		t.Fatalf("empty login header = %d, want 403", w.Code)
	}
}

func TestIdentityNoLoginSaved(t *testing.T) {
	e := newEnv(t, withTunnel(tunnelHost, ""))
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 || errCode(t, w) != "forbidden_identity" {
		t.Fatalf("no saved login = %d %s", w.Code, w.Body)
	}
	if w := e.serve(idReq("GET", "/healthz", testHost, loopPeer, "")); w.Code != 200 {
		t.Fatalf("plain loopback = %d", w.Code)
	}
}

func TestIdentityNoTunnelConfigured(t *testing.T) {
	// Settings wired but empty: tunnel host is not allowed, header is rejected.
	e := newEnv(t, withTunnel("", ""))
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 || errCode(t, w) != "forbidden_host" {
		t.Fatalf("unconfigured tunnel host = %d %s", w.Code, w.Body)
	}
	if w := e.serve(idReq("GET", "/healthz", testHost, loopPeer, tunnelLogin)); w.Code != 403 || errCode(t, w) != "forbidden_identity" {
		t.Fatalf("header with no saved login = %d %s", w.Code, w.Body)
	}
	if w := e.serve(idReq("GET", "/healthz", testHost, remotePeer, "")); w.Code != 200 {
		t.Fatalf("plain loopback = %d", w.Code)
	}
}

func TestIdentityAllowedHostsWithTunnel(t *testing.T) {
	bearer := []string{"Authorization", "Bearer tok-1"}
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin), func(c *Config) {
		c.AllowedHosts = []string{"my-mac.local", "*.ts.net"}
	})
	cases := []struct {
		name              string
		host, peer, login string
		want              int
		code              string
	}{
		// A LAN allow-host keeps token-only auth even with a tunnel saved.
		{"lan allow-host, no header", "my-mac.local", remotePeer, "", 200, ""},
		{"lan allow-host with port, no header", "my-mac.local:7878", remotePeer, "", 200, ""},
		{"lan allow-host, spoofed header", "my-mac.local", remotePeer, tunnelLogin, 403, "forbidden_identity"},
		{"lan allow-host, matching header via loopback", "my-mac.local", loopPeer, tunnelLogin, 200, ""},
		// With a tunnel saved, every *.ts.net host needs the identity.
		{"other ts.net host, no header", "other.tail1234.ts.net", remotePeer, "", 403, "forbidden_identity"},
		{"other ts.net host, no header, loopback peer", "other.tail1234.ts.net", loopPeer, "", 403, "forbidden_identity"},
		{"other ts.net host, matching header via loopback", "other.tail1234.ts.net", loopPeer, tunnelLogin, 200, ""},
		{"tunnel host, no header", tunnelHost, loopPeer, "", 403, "forbidden_identity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.serve(idReq("GET", "/api/me", c.host, c.peer, c.login, bearer...))
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
			if c.code != "" && errCode(t, w) != c.code {
				t.Fatalf("code = %q, want %q", errCode(t, w), c.code)
			}
		})
	}
	// LAN allow-host still requires the token.
	if w := e.serve(idReq("GET", "/api/me", "my-mac.local", remotePeer, "")); w.Code != 401 {
		t.Fatalf("lan allow-host without token = %d, want 401", w.Code)
	}
}

func TestIdentityTSNetAllowHostWithoutTunnel(t *testing.T) {
	// Settings wired but no tunnel saved: a *.ts.net allow-host is unchanged.
	e := newEnv(t, withTunnel("", ""), func(c *Config) { c.AllowedHosts = []string{"*.ts.net"} })
	w := e.serve(idReq("GET", "/api/me", tunnelHost, remotePeer, "", "Authorization", "Bearer tok-1"))
	if w.Code != 200 {
		t.Fatalf("ts.net allow-host, no tunnel = %d %s", w.Code, w.Body)
	}
	if w := e.serve(idReq("GET", "/api/me", tunnelHost, remotePeer, "")); w.Code != 401 {
		t.Fatalf("without token = %d, want 401", w.Code)
	}
	// A login header still triggers the check.
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 || errCode(t, w) != "forbidden_identity" {
		t.Fatalf("header without saved login = %d %s", w.Code, w.Body)
	}
}

func TestIdentityStoreErrorFailsClosed(t *testing.T) {
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin))
	e.st.mu.Lock()
	e.st.settingErr = errors.New("db locked")
	e.st.mu.Unlock()
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 {
		t.Fatalf("settings error = %d, want 403", w.Code)
	}
	if w := e.serve(idReq("GET", "/healthz", testHost, loopPeer, "")); w.Code != 200 {
		t.Fatalf("plain loopback on settings error = %d", w.Code)
	}
}

func TestIdentityNilSettingsUnchanged(t *testing.T) {
	// No Settings: a header or tunnel host does not change the old rules.
	e := newEnv(t, func(c *Config) { c.AllowedHosts = []string{"*.ts.net"} })
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, remotePeer, "")); w.Code != 200 {
		t.Fatalf("allowed host = %d", w.Code)
	}
	if w := e.serve(idReq("GET", "/healthz", testHost, remotePeer, "")); w.Code != 200 {
		t.Fatalf("loopback = %d", w.Code)
	}
	d := newEnv(t)
	if w := d.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 || errCode(t, w) != "forbidden_host" {
		t.Fatalf("nil settings tunnel host = %d %s", w.Code, w.Body)
	}
}

func TestIdentitySettingsCached(t *testing.T) {
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin))
	for i := 0; i < 5; i++ {
		if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
	}
	first := e.st.settingCalls()
	if first == 0 || first > 2 {
		t.Fatalf("setting calls = %d, want 1-2 (cached)", first)
	}
	// A new tunnel login takes effect after the cache TTL.
	_ = e.st.SetSetting(t.Context(), store.SettingTunnelLogin, "new@example.com")
	e.clock.Advance(6 * time.Second)
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, tunnelLogin)); w.Code != 403 {
		t.Fatalf("old login after refresh = %d", w.Code)
	}
	if w := e.serve(idReq("GET", "/healthz", tunnelHost, loopPeer, "new@example.com")); w.Code != 200 {
		t.Fatalf("new login = %d", w.Code)
	}
}

func pairCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range w.Result().Cookies() {
		if ck.Name == cookieName {
			return ck
		}
	}
	t.Fatalf("no %s cookie (%d %s)", cookieName, w.Code, w.Body)
	return nil
}

func TestPairCookieSecureFromForwardedProto(t *testing.T) {
	pair := func(e *env, peer string, hdr ...string) *http.Cookie {
		r := req("POST", "/api/pair", `{"code":"good-code"}`, hdr...)
		r.RemoteAddr = peer
		return pairCookie(t, e.serve(r))
	}
	if c := pair(newEnv(t), loopPeer, "X-Forwarded-Proto", "https"); !c.Secure {
		t.Fatal("https via loopback proxy must set Secure")
	}
	if c := pair(newEnv(t), remotePeer, "X-Forwarded-Proto", "https"); c.Secure {
		t.Fatal("X-Forwarded-Proto from a non-loopback peer must not be trusted")
	}
	if c := pair(newEnv(t), loopPeer, "X-Forwarded-Proto", "http"); c.Secure {
		t.Fatal("http proto must not set Secure")
	}
	if c := pair(newEnv(t), loopPeer); c.Secure {
		t.Fatal("no proto header must not set Secure")
	}
	if c := pair(newEnv(t, func(c *Config) { c.Secure = true }), remotePeer); !c.Secure {
		t.Fatal("Config.Secure must set Secure")
	}
}

func TestRevokeSelfCookieSecureFromForwardedProto(t *testing.T) {
	e := newEnv(t)
	r := req("DELETE", "/api/devices/dev-1", "", "Authorization", "Bearer tok-1", "X-Forwarded-Proto", "https")
	r.RemoteAddr = loopPeer
	if c := pairCookie(t, e.serve(r)); !c.Secure {
		t.Fatalf("cookie = %+v", c)
	}
}

func TestIdentityForwardedHeaders(t *testing.T) {
	bearer := []string{"Authorization", "Bearer tok-1"}
	e := newEnv(t, withTunnel(tunnelHost, tunnelLogin))
	cases := []struct {
		name  string
		login string
		hdr   []string
		want  int
		code  string
	}{
		{"XFF, no login", "", []string{"X-Forwarded-For", "100.64.1.2"}, 403, "forbidden_identity"},
		{"XFP, no login", "", []string{"X-Forwarded-Proto", "https"}, 403, "forbidden_identity"},
		{"XFF, matching login", tunnelLogin, []string{"X-Forwarded-For", "100.64.1.2"}, 200, ""},
		{"XFP, matching login", tunnelLogin, []string{"X-Forwarded-Proto", "https"}, 200, ""},
		{"XFF, wrong login", "mallory@example.com", []string{"X-Forwarded-For", "100.64.1.2"}, 403, "forbidden_identity"},
		{"plain loopback, no forwarded headers", "", nil, 200, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.serve(idReq("GET", "/api/me", "localhost", loopPeer, c.login, append(append([]string{}, bearer...), c.hdr...)...))
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
			if c.code != "" && errCode(t, w) != c.code {
				t.Fatalf("code = %q, want %q", errCode(t, w), c.code)
			}
		})
	}
	// Without a saved tunnel the forwarded headers change nothing.
	n := newEnv(t, withTunnel("", ""))
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto"} {
		w := n.serve(idReq("GET", "/api/me", "localhost", loopPeer, "", "Authorization", "Bearer tok-1", h, "x"))
		if w.Code != 200 {
			t.Errorf("no tunnel + %s = %d %s", h, w.Code, w.Body)
		}
	}
}
