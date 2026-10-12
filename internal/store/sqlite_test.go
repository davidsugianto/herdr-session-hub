package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var base = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func openTemp(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestEventsOrderingSinceAndSession(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	evs := []Event{
		{Session: "a", AgentID: "1", Name: "x", From: "idle", To: "working", At: base.Add(2 * time.Second), Source: "herdr"},
		{Session: "b", AgentID: "2", Name: "y", From: "working", To: "idle", At: base.Add(1 * time.Second), Source: "hub"},
		{Session: "a", AgentID: "1", Name: "x", From: "working", To: "idle", At: base.Add(3 * time.Second), Source: "herdr"},
	}
	for _, e := range evs {
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Events(ctx, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Session != "b" || all[1].To != "working" || all[2].To != "idle" {
		t.Fatalf("unexpected order: %+v", all)
	}
	if all[0].ID == 0 || all[0].At.Location() != time.UTC || !all[0].At.Equal(base.Add(time.Second)) {
		t.Fatalf("bad id/time: %+v", all[0])
	}
	got, err := s.Events(ctx, base.Add(2*time.Second), "")
	if err != nil || len(got) != 2 {
		t.Fatalf("since filter: %v %+v", err, got)
	}
	got, err = s.Events(ctx, base, "a")
	if err != nil || len(got) != 2 || got[0].Session != "a" {
		t.Fatalf("session filter: %v %+v", err, got)
	}
	got, err = s.Events(ctx, base, "nope")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty session: %v %+v", err, got)
	}
}

func TestAuditRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := AuditEntry{At: base, Actor: "dev1", Surface: "api", AgentID: "ag", Action: "send", Payload: `{"text":"hi"}`, Result: "ok"}
	if err := s.AddAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAudit(ctx, AuditEntry{At: base.Add(-time.Hour), Actor: "old"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Audit(ctx, base)
	if err != nil || len(got) != 1 {
		t.Fatalf("audit: %v %+v", err, got)
	}
	a.ID = got[0].ID
	if got[0] != a {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got[0], a)
	}
	both, _ := s.Audit(ctx, base.Add(-2*time.Hour))
	if len(both) != 2 || both[0].Actor != "old" {
		t.Fatalf("ordering: %+v", both)
	}
}

func TestPrune(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.now = func() time.Time { return base }
	_ = s.AddEvent(ctx, Event{Session: "a", At: base.Add(-time.Hour)})
	_ = s.AddEvent(ctx, Event{Session: "a", At: base.Add(time.Hour)})
	_ = s.AddAudit(ctx, AuditEntry{At: base.Add(-time.Hour)})
	_ = s.AddAudit(ctx, AuditEntry{At: base.Add(time.Hour)})
	expired, _ := s.CreatePairCode(ctx, time.Minute)
	used, _ := s.CreatePairCode(ctx, time.Hour)
	live, _ := s.CreatePairCode(ctx, time.Hour)
	if _, _, err := s.ExchangePairCode(ctx, used, "d"); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := s.Prune(ctx, base); err != nil {
		t.Fatal(err)
	}
	if ev, _ := s.Events(ctx, base.Add(-24*time.Hour), ""); len(ev) != 1 {
		t.Fatalf("events after prune: %+v", ev)
	}
	if au, _ := s.Audit(ctx, base.Add(-24*time.Hour)); len(au) != 1 {
		t.Fatalf("audit after prune: %+v", au)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pair_codes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pair codes left = %d (%v), want 1", n, err)
	}
	if _, _, err := s.ExchangePairCode(ctx, expired, "d"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired: %v", err)
	}
	if _, _, err := s.ExchangePairCode(ctx, live, "d"); err != nil {
		t.Fatalf("live code should survive prune: %v", err)
	}
}

func TestPairCodeFormat(t *testing.T) {
	s, _ := openTemp(t)
	for i := 0; i < 50; i++ {
		c, err := s.CreatePairCode(context.Background(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 8 {
			t.Fatalf("len(%q) != 8", c)
		}
		if strings.ContainsAny(c, "0O1Il") {
			t.Fatalf("ambiguous char in %q", c)
		}
	}
}

func TestPairCodeHappyPathAndReuse(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.now = func() time.Time { return base }
	code, err := s.CreatePairCode(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Case-insensitive, whitespace trimmed.
	token, d, err := s.ExchangePairCode(ctx, "  "+strings.ToLower(code)+" ", "phone")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if token == "" || strings.ContainsAny(token, "=+/") || len(token) != 43 {
		t.Fatalf("bad token %q", token)
	}
	if len(d.ID) != 16 || d.Name != "phone" || !d.CreatedAt.Equal(base) {
		t.Fatalf("bad device %+v", d)
	}
	if _, _, err := s.ExchangePairCode(ctx, code, "again"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("reuse: %v", err)
	}
	if _, _, err := s.ExchangePairCode(ctx, "ZZZZZZZZ", "x"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("unknown: %v", err)
	}
	if _, _, err := s.ExchangePairCode(ctx, "  ", "x"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("empty: %v", err)
	}
	// Neither the code nor the token are stored in plain text.
	var leaked int
	_ = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM pair_codes WHERE code_hash = ?) + (SELECT COUNT(*) FROM devices WHERE token_hash = ?)`, code, token).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("plaintext secret stored")
	}
}

func TestPairCodeExpiry(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.now = func() time.Time { return base }
	code, _ := s.CreatePairCode(ctx, time.Minute)
	s.now = func() time.Time { return base.Add(time.Minute) } // exactly at expiry
	if _, _, err := s.ExchangePairCode(ctx, code, "late"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired: %v", err)
	}
	s.now = func() time.Time { return base }
	code2, _ := s.CreatePairCode(ctx, time.Minute)
	s.now = func() time.Time { return base.Add(59 * time.Second) }
	if _, _, err := s.ExchangePairCode(ctx, code2, "ok"); err != nil {
		t.Fatalf("within ttl: %v", err)
	}
}

func TestConcurrentExchangeExactlyOnce(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	code, err := s.CreatePairCode(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, invalid int
	var other []error
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := s.ExchangePairCode(ctx, code, "dev")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrInvalidCode):
				invalid++
			default:
				other = append(other, err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || invalid != workers-1 || len(other) != 0 {
		t.Fatalf("ok=%d invalid=%d other=%v", ok, invalid, other)
	}
	if devs, _ := s.Devices(ctx); len(devs) != 1 {
		t.Fatalf("devices = %d, want 1", len(devs))
	}
}

func TestTokenLookupRevokeAndLastSeen(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	s.now = func() time.Time { return base }
	code, _ := s.CreatePairCode(ctx, time.Hour)
	token, dev, err := s.ExchangePairCode(ctx, code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(5 * time.Minute) }
	got, err := s.DeviceByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != dev.ID || got.Name != "laptop" || !got.CreatedAt.Equal(base) || !got.LastSeen.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("lookup: %+v", got)
	}
	devs, _ := s.Devices(ctx)
	if len(devs) != 1 || !devs[0].LastSeen.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("devices: %+v", devs)
	}
	if _, err := s.DeviceByToken(ctx, "bogus"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := s.RevokeDevice(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token: %v", err)
	}
	if err := s.RevokeDevice(ctx, dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double revoke: %v", err)
	}
}

func TestFileModes(t *testing.T) {
	s, path := openTemp(t)
	_ = s.AddEvent(context.Background(), Event{Session: "a", At: base})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Fatalf("db mode = %o, want 600", m)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if m := di.Mode().Perm(); m != 0o700 {
		t.Fatalf("dir mode = %o, want 700", m)
	}
}

func TestTwoOpensSameFileConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- a.AddEvent(ctx, Event{Session: "a", At: base})
		}()
		go func() {
			defer wg.Done()
			errs <- b.AddAudit(ctx, AuditEntry{At: base, Actor: "b"})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	// serve + pair: code made through one handle is exchanged through the other.
	code, err := b.CreatePairCode(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.ExchangePairCode(ctx, code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeviceByToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if ev, _ := b.Events(ctx, base, ""); len(ev) != 20 {
		t.Fatalf("events = %d, want 20", len(ev))
	}
}

func TestReopenIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = s.AddEvent(ctx, Event{Session: "a", At: base})
	code, _ := s.CreatePairCode(ctx, time.Hour)
	token, _, err := s.ExchangePairCode(ctx, code, "d")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s.Close() }()
	if ev, _ := s.Events(ctx, base, ""); len(ev) != 1 {
		t.Fatalf("data lost on reopen: %+v", ev)
	}
	if _, err := s.DeviceByToken(ctx, token); err != nil {
		t.Fatalf("device lost on reopen: %v", err)
	}
	var n, v int
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(version) FROM schema_version`).Scan(&n, &v); err != nil || n != 1 || v != schemaVersion {
		t.Fatalf("schema_version rows=%d version=%d err=%v", n, v, err)
	}
}

func TestOpenMemory(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.AddEvent(context.Background(), Event{Session: "a", At: base}); err != nil {
		t.Fatal(err)
	}
	if ev, _ := s.Events(context.Background(), base, ""); len(ev) != 1 {
		t.Fatalf("events: %+v", ev)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg")
	if got, want := DefaultPath(), "/xdg/herdr-session-hub/hub.db"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := DefaultPath(), "/home/u/.local/share/herdr-session-hub/hub.db"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if DefaultRetention != 14*24*time.Hour {
		t.Fatal("DefaultRetention")
	}
}
