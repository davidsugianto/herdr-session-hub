package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// DefaultRetention is how long events and audit rows are kept by default.
const DefaultRetention = 14 * 24 * time.Hour

const (
	schemaVersion = 2

	// pairCodeLen is the length of a pairing code.
	pairCodeLen = 8
	// pairCodeAlphabet leaves out 0/O/1/I/L so codes can be read aloud or typed.
	pairCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session    TEXT    NOT NULL,
	agent_id   TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	from_state TEXT    NOT NULL,
	to_state   TEXT    NOT NULL,
	at         INTEGER NOT NULL,
	source     TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS events_at ON events (at);
CREATE INDEX IF NOT EXISTS events_session_at ON events (session, at);
CREATE TABLE IF NOT EXISTS audit (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	at       INTEGER NOT NULL,
	actor    TEXT    NOT NULL,
	surface  TEXT    NOT NULL,
	agent_id TEXT    NOT NULL,
	action   TEXT    NOT NULL,
	payload  TEXT    NOT NULL,
	result   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_at ON audit (at);
CREATE TABLE IF NOT EXISTS devices (
	id         TEXT    PRIMARY KEY,
	name       TEXT    NOT NULL,
	token_hash TEXT    NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS pair_codes (
	code_hash  TEXT    PRIMARY KEY,
	expires_at INTEGER NOT NULL,
	used       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push_subs (
	endpoint   TEXT    PRIMARY KEY,
	device_id  TEXT    NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
	p256dh     TEXT    NOT NULL,
	auth       TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS push_subs_device ON push_subs (device_id);
CREATE TABLE IF NOT EXISTS mutes (
	scope      TEXT    NOT NULL,
	key        TEXT    NOT NULL,
	until      INTEGER NOT NULL DEFAULT 0, -- unix ms; 0 = until removed
	created_at INTEGER NOT NULL,
	PRIMARY KEY (scope, key)
);
`

// SQLite is the SQLite-backed Store.
type SQLite struct {
	db  *sql.DB
	now func() time.Time // injectable clock for tests
}

var (
	_ Store         = (*SQLite)(nil)
	_ SettingsStore = (*SQLite)(nil)
	_ PushStore     = (*SQLite)(nil)
	_ MuteStore     = (*SQLite)(nil)
)

// DefaultPath returns $XDG_DATA_HOME/herdr-session-hub/hub.db, or
// ~/.local/share/herdr-session-hub/hub.db when XDG_DATA_HOME is unset or empty.
func DefaultPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "herdr-session-hub", "hub.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "herdr-session-hub", "hub.db")
}

// Open opens (creating if needed) the database at path, applying pragmas and
// the idempotent schema. The parent directory is created 0700 and the file 0600.
// The special path ":memory:" gives a private in-memory database.
func Open(path string) (*SQLite, error) {
	memory := path == ":memory:"
	if !memory {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create store dir for %s: %w", path, err)
		}
		// Create the file ourselves so it gets 0600 (SQLite's WAL/shm files
		// inherit the main file's mode).
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create store file %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("create store file %s: %w", path, err)
		}
	}

	// busy_timeout goes first so the later pragmas already wait on locks.
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	var dsn string
	if memory {
		dsn = "file::memory:?" + q.Encode()
	} else {
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	if memory {
		// Every connection to :memory: is a separate database.
		db.SetMaxOpenConns(1)
	}
	s := &SQLite{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate store %s: %w", path, err)
	}
	return s, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, schemaVersion); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ? WHERE version < ?`, schemaVersion, schemaVersion); err != nil {
		// Older databases only lack the tables created above; bump the marker.
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (s *SQLite) Close() error { return s.db.Close() }

func toMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func (s *SQLite) AddEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (session, agent_id, name, from_state, to_state, at, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Session, e.AgentID, e.Name, e.From, e.To, toMillis(e.At), e.Source)
	if err != nil {
		return fmt.Errorf("add event: %w", err)
	}
	return nil
}

func (s *SQLite) Events(ctx context.Context, since time.Time, session string) ([]Event, error) {
	query := `SELECT id, session, agent_id, name, from_state, to_state, at, source FROM events WHERE at >= ?`
	args := []any{toMillis(since)}
	if session != "" {
		query += ` AND session = ?`
		args = append(args, session)
	}
	query += ` ORDER BY at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &e.Session, &e.AgentID, &e.Name, &e.From, &e.To, &at, &e.Source); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.At = fromMillis(at)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	return out, nil
}

func (s *SQLite) AddAudit(ctx context.Context, a AuditEntry) error {
	if a.At.IsZero() {
		a.At = s.now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (at, actor, surface, agent_id, action, payload, result)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		toMillis(a.At), a.Actor, a.Surface, a.AgentID, a.Action, a.Payload, a.Result)
	if err != nil {
		return fmt.Errorf("add audit: %w", err)
	}
	return nil
}

func (s *SQLite) Audit(ctx context.Context, since time.Time) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, actor, surface, agent_id, action, payload, result
		 FROM audit WHERE at >= ? ORDER BY at, id`, toMillis(since))
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		var at int64
		if err := rows.Scan(&a.ID, &at, &a.Actor, &a.Surface, &a.AgentID, &a.Action, &a.Payload, &a.Result); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		a.At = fromMillis(at)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	return out, nil
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func randomCode() (string, error) {
	max := big.NewInt(int64(len(pairCodeAlphabet)))
	b := make([]byte, pairCodeLen)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = pairCodeAlphabet[n.Int64()]
	}
	return string(b), nil
}

func (s *SQLite) CreatePairCode(ctx context.Context, ttl time.Duration) (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}
	expires := s.now().Add(ttl)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO pair_codes (code_hash, expires_at, used) VALUES (?, ?, 0)`,
		hashHex(code), toMillis(expires)); err != nil {
		return "", fmt.Errorf("store pairing code: %w", err)
	}
	return code, nil
}

func (s *SQLite) ExchangePairCode(ctx context.Context, code, deviceName string) (string, Device, error) {
	code = normalizeCode(code)
	if code == "" {
		return "", Device{}, ErrInvalidCode
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", Device{}, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", Device{}, fmt.Errorf("generate device id: %w", err)
	}
	now := s.now().UTC()
	d := Device{ID: hex.EncodeToString(idBytes), Name: deviceName, CreatedAt: fromMillis(toMillis(now))}
	d.LastSeen = d.CreatedAt

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Device{}, fmt.Errorf("exchange pairing code: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The first statement is a write, so the transaction takes the write lock
	// immediately and the single-row UPDATE consumes the code atomically.
	res, err := tx.ExecContext(ctx,
		`UPDATE pair_codes SET used = 1 WHERE code_hash = ? AND used = 0 AND expires_at > ?`,
		hashHex(code), toMillis(now))
	if err != nil {
		return "", Device{}, fmt.Errorf("exchange pairing code: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", Device{}, fmt.Errorf("exchange pairing code: %w", err)
	}
	if n == 0 {
		return "", Device{}, ErrInvalidCode
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO devices (id, name, token_hash, created_at, last_seen) VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.Name, hashHex(token), toMillis(d.CreatedAt), toMillis(d.LastSeen)); err != nil {
		return "", Device{}, fmt.Errorf("create device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", Device{}, fmt.Errorf("exchange pairing code: %w", err)
	}
	return token, d, nil
}

func (s *SQLite) DeviceByToken(ctx context.Context, token string) (Device, error) {
	var d Device
	var created, seen int64
	err := s.db.QueryRowContext(ctx,
		`UPDATE devices SET last_seen = ? WHERE token_hash = ?
		 RETURNING id, name, created_at, last_seen`,
		toMillis(s.now()), hashHex(token)).Scan(&d.ID, &d.Name, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("lookup device: %w", err)
	}
	d.CreatedAt, d.LastSeen = fromMillis(created), fromMillis(seen)
	return d, nil
}

func (s *SQLite) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, created_at, last_seen FROM devices ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Device
	for rows.Next() {
		var d Device
		var created, seen int64
		if err := rows.Scan(&d.ID, &d.Name, &created, &seen); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		d.CreatedAt, d.LastSeen = fromMillis(created), fromMillis(seen)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return out, nil
}

func (s *SQLite) RevokeDevice(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Explicit as well as ON DELETE CASCADE, in case foreign keys are off.
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subs WHERE device_id = ?`, id); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	return nil
}

// Prune deletes events and audit rows with At < cutoff, plus used or expired
// pairing codes.
func (s *SQLite) Prune(ctx context.Context, cutoff time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	c := toMillis(cutoff)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM events WHERE at < ?`, []any{c}},
		{`DELETE FROM audit WHERE at < ?`, []any{c}},
		{`DELETE FROM pair_codes WHERE used = 1 OR expires_at <= ?`, []any{toMillis(s.now())}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.q, stmt.args...); err != nil {
			return fmt.Errorf("prune: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	return nil
}
