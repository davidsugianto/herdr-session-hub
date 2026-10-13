package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestInterfacesImplemented(t *testing.T) {
	var s *SQLite
	var _ SettingsStore = s
	var _ PushStore = s
	var _ MuteStore = s
}

func TestSettings(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.Setting(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Setting(ctx, "k"); err != nil || v != "v2" {
		t.Fatalf("got %q %v", v, err)
	}
	if err := s.DeleteSetting(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSetting(ctx, "k"); err != nil {
		t.Fatalf("deleting a missing key must not error: %v", err)
	}
	if _, err := s.Setting(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func pairDevice(t *testing.T, s *SQLite, name string) Device {
	t.Helper()
	ctx := context.Background()
	code, err := s.CreatePairCode(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, d, err := s.ExchangePairCode(ctx, code, name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPushSubsLifecycle(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	d1, d2 := pairDevice(t, s, "a"), pairDevice(t, s, "b")

	if err := s.AddPushSub(ctx, PushSub{Endpoint: "https://x/1", DeviceID: "nope"}); err == nil {
		t.Fatal("unknown device must error")
	}
	t1 := base.Add(time.Second)
	subs := []PushSub{
		{Endpoint: "https://x/2", P256dh: "p", Auth: "a", DeviceID: d1.ID, CreatedAt: base.Add(2 * time.Second)},
		{Endpoint: "https://x/1", P256dh: "p", Auth: "a", DeviceID: d1.ID, CreatedAt: t1},
		{Endpoint: "https://x/3", P256dh: "p", Auth: "a", DeviceID: d2.ID, CreatedAt: base.Add(3 * time.Second)},
	}
	for _, sub := range subs {
		if err := s.AddPushSub(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.PushSubs(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Endpoint != "https://x/1" || !got[0].CreatedAt.Equal(t1) || got[2].Endpoint != "https://x/3" {
		t.Fatalf("bad order/time: %+v", got)
	}

	// Replace by endpoint.
	if err := s.AddPushSub(ctx, PushSub{Endpoint: "https://x/1", P256dh: "new", Auth: "a", DeviceID: d2.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PushSubs(ctx)
	if len(got) != 3 {
		t.Fatalf("replace changed count: %+v", got)
	}
	for _, g := range got {
		if g.Endpoint == "https://x/1" && (g.P256dh != "new" || g.DeviceID != d2.ID || g.CreatedAt.IsZero()) {
			t.Fatalf("not replaced: %+v", g)
		}
	}

	if err := s.RemovePushSub(ctx, "https://x/3"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePushSub(ctx, "https://missing"); err != nil {
		t.Fatalf("missing endpoint must not error: %v", err)
	}

	// Revoking a device removes its subscriptions.
	if err := s.RevokeDevice(ctx, d2.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PushSubs(ctx)
	if len(got) != 1 || got[0].Endpoint != "https://x/2" {
		t.Fatalf("after revoke: %+v", got)
	}
}

func TestMutes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := base
	s.now = func() time.Time { return now }

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetMute(ctx, Mute{Scope: MuteAgent, Key: "w/t1"}))                                // forever
	must(s.SetMute(ctx, Mute{Scope: MuteSession, Key: "w", Until: now.Add(time.Hour)}))      // active
	must(s.SetMute(ctx, Mute{Scope: MuteSession, Key: "old", Until: now.Add(-time.Minute)})) // expired

	got, err := s.Mutes(ctx, now)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %+v", err, got)
	}
	for _, m := range got {
		switch m.Key {
		case "w/t1":
			if !m.Until.IsZero() || m.CreatedAt.IsZero() {
				t.Fatalf("forever mute: %+v", m)
			}
		case "w":
			if !m.Until.Equal(now.Add(time.Hour)) {
				t.Fatalf("until: %+v", m)
			}
		default:
			t.Fatalf("unexpected %+v", m)
		}
	}

	// Replace by (scope,key).
	must(s.SetMute(ctx, Mute{Scope: MuteAgent, Key: "w/t1", Until: now.Add(time.Minute)}))
	got, _ = s.Mutes(ctx, now)
	if len(got) != 2 {
		t.Fatalf("replace changed count: %+v", got)
	}
	// Later, it expires.
	got, _ = s.Mutes(ctx, now.Add(2*time.Hour))
	if len(got) != 0 {
		t.Fatalf("all should be expired: %+v", got)
	}

	must(s.SetMute(ctx, Mute{Scope: MuteAgent, Key: "z"}))
	must(s.DeleteMute(ctx, MuteAgent, "z"))
	must(s.DeleteMute(ctx, MuteAgent, "z"))
	got, _ = s.Mutes(ctx, now)
	for _, m := range got {
		if m.Key == "z" {
			t.Fatal("not deleted")
		}
	}
}

func TestMigratesV1InPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// A v1 database: the old schema only, version 1, with a device in it.
	if _, err := raw.Exec(`
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version (version) VALUES (1);
CREATE TABLE devices (
	id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL);
INSERT INTO devices VALUES ('d1', 'phone', 'h', 1, 1);
`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 db: %v", err)
	}
	defer func() { _ = s.Close() }()
	var v int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("version = %d, %v", v, err)
	}
	ctx := context.Background()
	if err := s.AddPushSub(ctx, PushSub{Endpoint: "https://x", DeviceID: "d1"}); err != nil {
		t.Fatalf("existing device must accept subs: %v", err)
	}
	if err := s.SetSetting(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	// Reopening is idempotent.
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if subs, _ := s2.PushSubs(ctx); len(subs) != 1 {
		t.Fatalf("%+v", subs)
	}
}
