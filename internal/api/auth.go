package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	maxTokenLen   = 512
	cookieMaxAge  = 365 * 24 * 60 * 60
	cacheSweepLen = 1024
)

type ctxKey struct{}

func deviceFrom(r *http.Request) store.Device {
	d, _ := r.Context().Value(ctxKey{}).(store.Device)
	return d
}

// authCache memoises successful DeviceByToken lookups for a short TTL so a
// burst of requests costs one database hit, while revocation still takes
// effect within the TTL. Failures are never cached. Keys are token hashes.
type authCache struct {
	st  store.Store
	now func() time.Time
	ttl time.Duration

	mu sync.Mutex
	m  map[[sha256.Size]byte]cacheEntry
}

type cacheEntry struct {
	dev     store.Device
	expires time.Time
}

func newAuthCache(st store.Store, now func() time.Time, ttl time.Duration) *authCache {
	return &authCache{st: st, now: now, ttl: ttl, m: map[[sha256.Size]byte]cacheEntry{}}
}

func (c *authCache) lookup(ctx context.Context, token string) (store.Device, error) {
	key := sha256.Sum256([]byte(token))
	t := c.now()
	c.mu.Lock()
	if e, ok := c.m[key]; ok {
		if t.Before(e.expires) {
			c.mu.Unlock()
			return e.dev, nil
		}
		delete(c.m, key)
	}
	c.mu.Unlock()

	dev, err := c.st.DeviceByToken(ctx, token)
	if err != nil {
		return store.Device{}, err
	}
	c.mu.Lock()
	if len(c.m) >= cacheSweepLen {
		for k, e := range c.m {
			if !t.Before(e.expires) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = cacheEntry{dev: dev, expires: t.Add(c.ttl)}
	c.mu.Unlock()
	return dev, nil
}

// forget drops every cached lookup for deviceID so a revocation takes effect
// on the next request instead of after the TTL.
func (c *authCache) forget(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if e.dev.ID == deviceID {
			delete(c.m, k)
		}
	}
}

// extractToken returns the bearer token or, failing that, the cookie value.
// An Authorization header that is not a Bearer credential yields no token
// (it does not fall back to the cookie).
func extractToken(r *http.Request) (token string, fromCookie bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return "", false
		}
		return strings.TrimSpace(tok), false
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value, true
	}
	return "", false
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// authed wraps h with token authentication and cookie CSRF protection.
func (s *server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, fromCookie := extractToken(r)
		if token == "" || len(token) > maxTokenLen {
			writeAPIErr(w, newErr(http.StatusUnauthorized, "unauthorized", "missing or invalid credentials"))
			return
		}
		dev, err := s.auth.lookup(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeAPIErr(w, newErr(http.StatusUnauthorized, "unauthorized", "missing or invalid credentials"))
				return
			}
			slog.Error("api: device lookup failed", "err", err)
			writeAPIErr(w, newErr(http.StatusInternalServerError, "internal", "authentication unavailable"))
			return
		}
		if fromCookie && !safeMethod(r.Method) && r.Header.Get(csrfHeader) != csrfValue {
			writeAPIErr(w, newErr(http.StatusForbidden, "csrf", "missing "+csrfHeader+": "+csrfValue+" header"))
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, dev)))
	})
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, deviceFrom(r))
}

type pairBody struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

func (s *server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if ok, wait := s.pairLimit.allow(ip); !ok {
		writeTooMany(w, wait)
		return
	}
	var b pairBody
	if e := decodeBody(w, r, &b, false); e != nil {
		writeAPIErr(w, e)
		return
	}
	b.Code = strings.TrimSpace(b.Code)
	b.DeviceName = strings.TrimSpace(b.DeviceName)
	if b.Code == "" {
		writeAPIErr(w, newErr(http.StatusBadRequest, "bad_request", "code is required"))
		return
	}
	if b.DeviceName == "" {
		b.DeviceName = "device"
	}
	if len(b.DeviceName) > 100 {
		writeAPIErr(w, newErr(http.StatusBadRequest, "bad_request", "device_name too long"))
		return
	}
	token, dev, err := s.st.ExchangePairCode(r.Context(), b.Code, b.DeviceName)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCode) {
			writeAPIErr(w, newErr(http.StatusUnauthorized, "invalid_code", "invalid or expired pairing code"))
			return
		}
		slog.Error("api: pairing failed", "err", err)
		writeAPIErr(w, newErr(http.StatusInternalServerError, "internal", "pairing failed"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "device": dev})
}
