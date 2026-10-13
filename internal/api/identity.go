package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	tunnelCacheTTL    = 5 * time.Second
	tunnelLoadTimeout = 2 * time.Second
	loginHeader       = "Tailscale-User-Login"
)

// tunnelInfo is the saved `hub tunnel` state. Empty fields mean "not
// configured"; both are normalized (host via normHost, login trimmed).
type tunnelInfo struct {
	host  string
	login string
}

// isHost reports whether hostport names the configured tunnel host.
func (t tunnelInfo) isHost(hostport string) bool {
	return t.host != "" && normHost(hostport) == t.host
}

// tunnelCache memoises the tunnel settings for a short TTL so every request
// does not hit the database, while `hub tunnel` / `hub tunnel off` in another
// process still take effect within the TTL. A failed read is cached as "no
// tunnel", which fails closed: the tunnel host is refused and every request
// carrying a login header is rejected.
type tunnelCache struct {
	st  store.SettingsStore
	now func() time.Time
	ttl time.Duration

	mu      sync.Mutex
	info    tunnelInfo
	expires time.Time
	loaded  bool
}

func newTunnelCache(st store.SettingsStore, now func() time.Time, ttl time.Duration) *tunnelCache {
	return &tunnelCache{st: st, now: now, ttl: ttl}
}

func (c *tunnelCache) get() tunnelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.now()
	if c.loaded && t.Before(c.expires) {
		return c.info
	}
	// Detached from any request: one client hanging up must not poison the
	// cache for everyone else.
	ctx, cancel := context.WithTimeout(context.Background(), tunnelLoadTimeout)
	defer cancel()
	host, herr := c.read(ctx, store.SettingTunnelHost)
	login, lerr := c.read(ctx, store.SettingTunnelLogin)
	if herr != nil || lerr != nil {
		host, login = "", ""
	}
	c.info = tunnelInfo{host: normHost(host), login: strings.TrimSpace(login)}
	c.expires = t.Add(c.ttl)
	c.loaded = true
	return c.info
}

// read returns "" for a missing key and logs any other error.
func (c *tunnelCache) read(ctx context.Context, key string) (string, error) {
	v, err := c.st.Setting(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		slog.Error("api: reading tunnel setting failed", "key", key, "err", err)
		return "", err
	}
	return v, nil
}

// identityOK applies the tunnel identity rule. The check is needed when the
// request carries the login header (even empty), or when a tunnel is saved
// and the Host is the tunnel host or any *.ts.net name, or the request has
// X-Forwarded-For / X-Forwarded-Proto. tailscale serve adds those forwarded
// headers, so a proxied request (even one without a login header, such as
// from a tagged node, or with a spoofed Host: localhost) cannot skip the
// check; plain local use sends neither. Other requests (loopback, or a
// non-tailnet AllowedHosts entry such as a LAN name) are unchanged: token
// auth only. When needed, the request must come from a loopback peer (the
// local tailscale serve proxy, which sets the header for tailnet users and
// strips client copies), carry exactly one login header, and that login must
// equal the saved owner login, case-insensitively.
func identityOK(r *http.Request, tun tunnelInfo) bool {
	vals, present := r.Header[loginHeader]
	if !present && !tunnelRequest(r, tun) {
		return true
	}
	if !remoteIsLoopback(r) || tun.login == "" || len(vals) != 1 {
		return false
	}
	got := strings.TrimSpace(vals[0])
	return got != "" && strings.EqualFold(got, tun.login)
}

// tunnelRequest reports whether, with a tunnel saved, r looks like it came
// through it: tunnel or *.ts.net Host, or headers added by a forwarding proxy.
func tunnelRequest(r *http.Request, tun tunnelInfo) bool {
	if tun.host == "" {
		return false
	}
	if tun.isHost(r.Host) || isTSNetHost(r.Host) {
		return true
	}
	_, xff := r.Header["X-Forwarded-For"]
	_, xfp := r.Header["X-Forwarded-Proto"]
	return xff || xfp
}

// isTSNetHost reports whether hostport is a tailnet MagicDNS name (*.ts.net).
func isTSNetHost(hostport string) bool {
	return strings.HasSuffix(normHost(hostport), ".ts.net")
}

// remoteIsLoopback reports whether the TCP peer is a loopback address.
func remoteIsLoopback(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// secureCookie reports whether auth cookies set on this response get the
// Secure flag: always with Config.Secure, otherwise only when a loopback
// reverse proxy (tailscale serve) says the client used https.
// X-Forwarded-Proto from any other peer is ignored.
func (s *server) secureCookie(r *http.Request) bool {
	if s.secure {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") && remoteIsLoopback(r)
}
