package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	maxEndpointLen = 2048
	maxMuteKeyLen  = 256
	p256dhLen      = 65 // uncompressed P-256 point
	authSecretLen  = 16
)

func pushUnavailable() *apiErr {
	return newErr(http.StatusServiceUnavailable, "push_unavailable", "push notifications are not available")
}

func mutesUnavailable() *apiErr {
	return newErr(http.StatusServiceUnavailable, "not_configured", "mutes are not available")
}

// mutate runs a rate-limited, audited change for the calling device. fn
// returns the audit agent id, payload and error.
func (s *server) mutate(w http.ResponseWriter, r *http.Request, action string, fn func() (agentID, payload string, aerr *apiErr)) {
	dev := deviceFrom(r)
	if ok, wait := s.actionLimit.allow(dev.ID); !ok {
		writeTooMany(w, wait)
		return
	}
	agentID, payload, aerr := fn()
	s.audit(r, dev, agentID, action, payload, aerr)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- push ----

func (s *server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if s.notifier == nil {
		writeAPIErr(w, pushUnavailable())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"public_key": s.notifier.PublicKey()})
}

// subscribeBody is the browser's PushSubscription.toJSON() wrapped in
// {"subscription": ...}. expirationTime is accepted and ignored.
type subscribeBody struct {
	Subscription *struct {
		Endpoint       string          `json:"endpoint"`
		ExpirationTime json.RawMessage `json:"expirationTime"`
		Keys           *struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	} `json:"subscription"`
}

// endpointPayload is the audit payload for a subscription: only the push
// service host, since the full endpoint URL is a delivery capability.
func endpointPayload(endpoint string) string {
	host := ""
	if u, err := url.Parse(endpoint); err == nil {
		host = u.Host
	}
	b, _ := json.Marshal(map[string]string{"endpoint_host": host})
	return string(b)
}

// Push services the hub will POST to. Only these hosts are accepted as
// subscription endpoints so a paired device cannot point the hub at arbitrary
// https servers (SSRF). Matching is case-insensitive on the parsed hostname;
// a suffix needs a non-empty label before it.
var (
	pushHostsExact = map[string]bool{
		"fcm.googleapis.com":                true, // Chrome, Edge, Android
		"updates.push.services.mozilla.com": true, // Firefox
		"web.push.apple.com":                true, // Safari
	}
	pushHostSuffixes = []string{
		".push.apple.com",
		".notify.windows.com",
		".push.services.mozilla.com",
	}
)

func pushHostAllowed(host string) bool {
	h := strings.ToLower(host)
	if pushHostsExact[h] {
		return true
	}
	for _, suf := range pushHostSuffixes {
		if strings.HasSuffix(h, suf) && len(h) > len(suf) {
			return true
		}
	}
	return false
}

// validEndpoint accepts absolute https URLs on a known push service host
// (default port or 443) with no credentials.
func validEndpoint(ep string) bool {
	if ep == "" || len(ep) > maxEndpointLen {
		return false
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return pushHostAllowed(u.Hostname())
}

// validKey checks a base64url (padding optional) value decodes to n bytes.
func validKey(v string, n int) bool {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	return err == nil && len(b) == n
}

func (s *server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		writeAPIErr(w, pushUnavailable())
		return
	}
	s.mutate(w, r, "push_subscribe", func() (string, string, *apiErr) {
		var b subscribeBody
		if e := decodeBody(w, r, &b, false); e != nil {
			return "", "", e
		}
		sub := b.Subscription
		if sub == nil || sub.Keys == nil {
			return "", "", badRequest("subscription with endpoint and keys is required")
		}
		payload := endpointPayload(sub.Endpoint)
		if !validEndpoint(sub.Endpoint) {
			return "", payload, badRequest("endpoint must be an https URL of at most 2048 characters")
		}
		if !validKey(sub.Keys.P256dh, p256dhLen) || !validKey(sub.Keys.Auth, authSecretLen) {
			return "", payload, badRequest("keys.p256dh and keys.auth must be valid base64url keys")
		}
		err := s.push.AddPushSub(r.Context(), store.PushSub{
			Endpoint:  sub.Endpoint,
			P256dh:    sub.Keys.P256dh,
			Auth:      sub.Keys.Auth,
			DeviceID:  deviceFrom(r).ID,
			CreatedAt: s.now(),
		})
		if err != nil {
			slog.Error("api: add push subscription failed", "err", err)
			e := newErr(http.StatusInternalServerError, "internal", "saving subscription failed")
			e.cause = err
			return "", payload, e
		}
		return "", payload, nil
	})
}

// handlePushUnsubscribe removes the endpoint only when the calling device
// owns it. An unknown or foreign endpoint is a no-op that still answers ok,
// so the response does not reveal other devices' subscriptions.
func (s *server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.push == nil {
		writeAPIErr(w, pushUnavailable())
		return
	}
	s.mutate(w, r, "push_unsubscribe", func() (string, string, *apiErr) {
		var b struct {
			Endpoint string `json:"endpoint"`
		}
		if e := decodeBody(w, r, &b, false); e != nil {
			return "", "", e
		}
		payload := endpointPayload(b.Endpoint)
		if b.Endpoint == "" || len(b.Endpoint) > maxEndpointLen {
			return "", payload, badRequest("endpoint is required")
		}
		internal := func(err error) *apiErr {
			slog.Error("api: remove push subscription failed", "err", err)
			e := newErr(http.StatusInternalServerError, "internal", "removing subscription failed")
			e.cause = err
			return e
		}
		subs, err := s.push.PushSubs(r.Context())
		if err != nil {
			return "", payload, internal(err)
		}
		me := deviceFrom(r).ID
		for _, sub := range subs {
			if sub.Endpoint == b.Endpoint && sub.DeviceID == me {
				if err := s.push.RemovePushSub(r.Context(), b.Endpoint); err != nil {
					return "", payload, internal(err)
				}
				break
			}
		}
		return "", payload, nil
	})
}

func (s *server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.notifier == nil {
		writeAPIErr(w, pushUnavailable())
		return
	}
	s.mutate(w, r, "push_test", func() (string, string, *apiErr) {
		ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
		defer cancel()
		err := s.notifier.SendTest(ctx, deviceFrom(r).ID)
		switch {
		case err == nil:
			return "", "", nil
		case errors.Is(err, store.ErrNotFound):
			e := newErr(http.StatusConflict, "no_subscription", "this device has no push subscription")
			e.cause = err
			return "", "", e
		default:
			slog.Error("api: test push failed", "err", err)
			e := newErr(http.StatusBadGateway, "push_failed", "sending the test notification failed")
			e.cause = err
			return "", "", e
		}
	})
}

// ---- mutes ----

type muteJSON struct {
	Scope     string    `json:"scope"`
	Key       string    `json:"key"`
	Until     *string   `json:"until"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *server) handleMutes(w http.ResponseWriter, r *http.Request) {
	if s.mutes == nil {
		writeAPIErr(w, mutesUnavailable())
		return
	}
	ms, err := s.mutes.Mutes(r.Context(), s.now())
	if err != nil {
		slog.Error("api: list mutes failed", "err", err)
		writeAPIErr(w, newErr(http.StatusInternalServerError, "internal", "listing mutes failed"))
		return
	}
	out := make([]muteJSON, 0, len(ms))
	for _, m := range ms {
		j := muteJSON{Scope: m.Scope, Key: m.Key, CreatedAt: m.CreatedAt}
		if !m.Until.IsZero() {
			u := m.Until.Format(time.RFC3339)
			j.Until = &u
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mutes": out})
}

type muteKeyBody struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

// validate returns a bad-request message, or "" when scope and key are fine.
func (b *muteKeyBody) validate() string {
	if b.Scope != store.MuteAgent && b.Scope != store.MuteSession {
		return `scope must be "agent" or "session"`
	}
	if strings.TrimSpace(b.Key) == "" {
		return "key is required"
	}
	if len(b.Key) > maxMuteKeyLen {
		return "key too long"
	}
	return ""
}

// agentID is the audit agent id: the key for agent mutes.
func (b *muteKeyBody) agentID() string {
	if b.Scope == store.MuteAgent {
		return b.Key
	}
	return ""
}

func (s *server) handleSetMute(w http.ResponseWriter, r *http.Request) {
	if s.mutes == nil {
		writeAPIErr(w, mutesUnavailable())
		return
	}
	s.mutate(w, r, "mute_set", func() (string, string, *apiErr) {
		var b struct {
			muteKeyBody
			Until *string `json:"until"`
		}
		if e := decodeBody(w, r, &b, false); e != nil {
			return "", "", e
		}
		raw, _ := json.Marshal(b)
		payload, agentID := string(raw), b.agentID()
		if msg := b.validate(); msg != "" {
			return agentID, payload, badRequest(msg)
		}
		now := s.now()
		m := store.Mute{Scope: b.Scope, Key: b.Key, CreatedAt: now}
		if b.Until != nil {
			u, err := time.Parse(time.RFC3339, *b.Until)
			if err != nil {
				return agentID, payload, badRequest("until must be an RFC 3339 time or null")
			}
			if !u.After(now) {
				return agentID, payload, badRequest("until must be in the future")
			}
			m.Until = u
		}
		if err := s.mutes.SetMute(r.Context(), m); err != nil {
			slog.Error("api: set mute failed", "err", err)
			e := newErr(http.StatusInternalServerError, "internal", "saving mute failed")
			e.cause = err
			return agentID, payload, e
		}
		return agentID, payload, nil
	})
}

func (s *server) handleDeleteMute(w http.ResponseWriter, r *http.Request) {
	if s.mutes == nil {
		writeAPIErr(w, mutesUnavailable())
		return
	}
	s.mutate(w, r, "mute_delete", func() (string, string, *apiErr) {
		var b muteKeyBody
		if e := decodeBody(w, r, &b, false); e != nil {
			return "", "", e
		}
		raw, _ := json.Marshal(b)
		payload, agentID := string(raw), b.agentID()
		if msg := b.validate(); msg != "" {
			return agentID, payload, badRequest(msg)
		}
		if err := s.mutes.DeleteMute(r.Context(), b.Scope, b.Key); err != nil {
			slog.Error("api: delete mute failed", "err", err)
			e := newErr(http.StatusInternalServerError, "internal", "removing mute failed")
			e.cause = err
			return agentID, payload, e
		}
		return agentID, payload, nil
	})
}

// ---- notify settings ----

func (s *server) handleNotify(w http.ResponseWriter, r *http.Request) {
	q := s.quiet
	if q == nil {
		q = []QuietRule{}
	}
	writeJSON(w, http.StatusOK, struct {
		QuietHours         []QuietRule `json:"quiet_hours"`
		BlockedBreaksQuiet bool        `json:"blocked_breaks_quiet"`
		PushAvailable      bool        `json:"push_available"`
	}{q, s.blockedBreaksQuiet, s.notifier != nil})
}
