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
