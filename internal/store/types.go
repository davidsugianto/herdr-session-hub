// Package store is the SQLite persistence layer: timeline events, audit log,
// paired devices and one-time pairing codes.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound    = errors.New("store: not found")
	ErrInvalidCode = errors.New("store: invalid or expired pairing code")
)

// Event is one timeline row: an agent state transition.
type Event struct {
	ID      int64     `json:"id"`
	Session string    `json:"session"`
	AgentID string    `json:"agent_id"`
	Name    string    `json:"name"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	At      time.Time `json:"at"`
	Source  string    `json:"source"` // "herdr" or "hub"
}

// AuditEntry records one action taken through the hub.
type AuditEntry struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`   // device id
	Surface string    `json:"surface"` // "api"
	AgentID string    `json:"agent_id"`
	Action  string    `json:"action"`
	Payload string    `json:"payload"`
	Result  string    `json:"result"` // "ok" or the error text
}

// Device is a paired client. The token itself is never stored, only its hash.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

// Store is implemented by *SQLite (see Open). All methods are safe for
// concurrent use. Two processes may open the same file (hub serve and hub pair).
type Store interface {
	AddEvent(ctx context.Context, e Event) error
	// Events returns events with At >= since, oldest first; session "" means all.
	Events(ctx context.Context, since time.Time, session string) ([]Event, error)
	AddAudit(ctx context.Context, a AuditEntry) error
	// Audit returns entries with At >= since, oldest first.
	Audit(ctx context.Context, since time.Time) ([]AuditEntry, error)

	// CreatePairCode makes a random one-time code valid for ttl.
	CreatePairCode(ctx context.Context, ttl time.Duration) (string, error)
	// ExchangePairCode consumes a valid code (exactly once), creates a device
	// and returns a new random token (shown only here). ErrInvalidCode if the
	// code is unknown, used or expired.
	ExchangePairCode(ctx context.Context, code, deviceName string) (token string, d Device, err error)
	// DeviceByToken looks a device up by token hash and bumps LastSeen.
	// ErrNotFound if the token is unknown or revoked.
	DeviceByToken(ctx context.Context, token string) (Device, error)
	Devices(ctx context.Context) ([]Device, error)
	RevokeDevice(ctx context.Context, id string) error

	// Prune deletes events and audit rows older than cutoff.
	Prune(ctx context.Context, cutoff time.Time) error
	Close() error
}

// Setting keys used with SettingsStore.
const (
	SettingVAPIDPublic  = "vapid.public"  // base64url uncompressed P-256 public key
	SettingVAPIDPrivate = "vapid.private" // base64url private key
	SettingTunnelHost   = "tunnel.host"   // tailnet DNS name, e.g. macbook.tail1234.ts.net (no trailing dot)
	SettingTunnelLogin  = "tunnel.login"  // Tailscale login name of the owner
)

// SettingsStore is a small key/value table shared by hub processes.
// Setting returns ErrNotFound for an unknown key.
type SettingsStore interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
}

// PushSub is one Web Push subscription owned by a paired device.
type PushSub struct {
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	DeviceID  string    `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}

// PushStore persists push subscriptions. Revoking a device removes its
// subscriptions.
type PushStore interface {
	// AddPushSub inserts or replaces (by Endpoint) a subscription.
	AddPushSub(ctx context.Context, s PushSub) error
	// RemovePushSub deletes by endpoint; a missing endpoint is not an error.
	RemovePushSub(ctx context.Context, endpoint string) error
	// PushSubs returns every subscription, oldest first.
	PushSubs(ctx context.Context) ([]PushSub, error)
}

// Mute scopes.
const (
	MuteAgent   = "agent"   // Key is the agent id "<session>/<terminal_id>"
	MuteSession = "session" // Key is the herdr session name
)

// Mute silences notifications for an agent or a session. A zero Until means
// until removed.
type Mute struct {
	Scope     string    `json:"scope"`
	Key       string    `json:"key"`
	Until     time.Time `json:"until"`
	CreatedAt time.Time `json:"created_at"`
}

// MuteStore persists mutes.
type MuteStore interface {
	// SetMute inserts or replaces by (Scope, Key).
	SetMute(ctx context.Context, m Mute) error
	// DeleteMute removes a mute; a missing one is not an error.
	DeleteMute(ctx context.Context, scope, key string) error
	// Mutes returns mutes that are active at now (Until zero or after now).
	// Expired rows may be deleted.
	Mutes(ctx context.Context, now time.Time) ([]Mute, error)
}
