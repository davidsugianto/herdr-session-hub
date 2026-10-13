package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *SQLite) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get setting %s: %w", key, err)
	}
	return v, nil
}

func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

func (s *SQLite) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete setting %s: %w", key, err)
	}
	return nil
}

// AddPushSub inserts or replaces by endpoint. A zero CreatedAt means now. An
// unknown DeviceID fails on the foreign key.
func (s *SQLite) AddPushSub(ctx context.Context, sub PushSub) error {
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = s.now()
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO push_subs (endpoint, device_id, p256dh, auth, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (endpoint) DO UPDATE SET
		   device_id = excluded.device_id, p256dh = excluded.p256dh,
		   auth = excluded.auth, created_at = excluded.created_at`,
		sub.Endpoint, sub.DeviceID, sub.P256dh, sub.Auth, toMillis(sub.CreatedAt)); err != nil {
		return fmt.Errorf("add push subscription: %w", err)
	}
	return nil
}

func (s *SQLite) RemovePushSub(ctx context.Context, endpoint string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM push_subs WHERE endpoint = ?`, endpoint); err != nil {
		return fmt.Errorf("remove push subscription: %w", err)
	}
	return nil
}

func (s *SQLite) PushSubs(ctx context.Context) ([]PushSub, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint, device_id, p256dh, auth, created_at FROM push_subs ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		var created int64
		if err := rows.Scan(&p.Endpoint, &p.DeviceID, &p.P256dh, &p.Auth, &created); err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		p.CreatedAt = fromMillis(created)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list push subscriptions: %w", err)
	}
	return out, nil
}

// SetMute inserts or replaces by (Scope, Key). A zero CreatedAt means now.
func (s *SQLite) SetMute(ctx context.Context, m Mute) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.now()
	}
	var until int64
	if !m.Until.IsZero() {
		until = toMillis(m.Until)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO mutes (scope, key, until, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (scope, key) DO UPDATE SET until = excluded.until, created_at = excluded.created_at`,
		m.Scope, m.Key, until, toMillis(m.CreatedAt)); err != nil {
		return fmt.Errorf("set mute: %w", err)
	}
	return nil
}

func (s *SQLite) DeleteMute(ctx context.Context, scope, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mutes WHERE scope = ? AND key = ?`, scope, key); err != nil {
		return fmt.Errorf("delete mute: %w", err)
	}
	return nil
}

// Mutes returns the mutes active at now, oldest first, and deletes expired rows.
func (s *SQLite) Mutes(ctx context.Context, now time.Time) ([]Mute, error) {
	ms := toMillis(now)
	// Best effort: a failed cleanup must not hide the active mutes.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM mutes WHERE until != 0 AND until <= ?`, ms)
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, key, until, created_at FROM mutes
		 WHERE until = 0 OR until > ? ORDER BY created_at, scope, key`, ms)
	if err != nil {
		return nil, fmt.Errorf("list mutes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Mute
	for rows.Next() {
		var m Mute
		var until, created int64
		if err := rows.Scan(&m.Scope, &m.Key, &until, &created); err != nil {
			return nil, fmt.Errorf("scan mute: %w", err)
		}
		if until != 0 {
			m.Until = fromMillis(until)
		}
		m.CreatedAt = fromMillis(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mutes: %w", err)
	}
	return out, nil
}
