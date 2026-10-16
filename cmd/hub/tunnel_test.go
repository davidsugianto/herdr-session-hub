package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

type tunRunner struct {
	out   map[string]string // keyed by first arg ("status", "serve")
	err   map[string]error
	calls [][]string
}

func (r *tunRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.out[args[0]]), r.err[args[0]]
}

type tunStore struct {
	settings map[string]string
	closed   bool
	ttl      time.Duration
	order    *[]string
}

func (s *tunStore) Setting(_ context.Context, k string) (string, error) {
	v, ok := s.settings[k]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}
func (s *tunStore) SetSetting(_ context.Context, k, v string) error {
	*s.order = append(*s.order, "set:"+k)
	s.settings[k] = v
	return nil
}
func (s *tunStore) DeleteSetting(_ context.Context, k string) error {
	*s.order = append(*s.order, "del:"+k)
	delete(s.settings, k)
	return nil
}
func (s *tunStore) CreatePairCode(_ context.Context, ttl time.Duration) (string, error) {
	*s.order = append(*s.order, "paircode")
	s.ttl = ttl
	return "CODE123", nil
}
func (s *tunStore) Close() error { s.closed = true; return nil }

const tunStatus = `{"Self":{"DNSName":"mac.tail1.ts.net.","UserID":5},"User":{"5":{"LoginName":"me@example.com"}}}`

func setupTunnel(t *testing.T, r *tunRunner, st *tunStore) {
	t.Helper()
	oldO, oldR, oldF := openTunnelStore, tunnelRunner, tunnelFindCLI
	t.Cleanup(func() { openTunnelStore, tunnelRunner, tunnelFindCLI = oldO, oldR, oldF })
	openTunnelStore = func(string) (tunnelStore, error) { return st, nil }
	tunnelRunner = r
	tunnelFindCLI = func() (string, error) { return "/bin/ts", nil }
}

func runTun(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(context.Background(), append([]string{"tunnel"}, args...), &o, &e)
	return code, o.String(), e.String()
}

func TestTunnelUp(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{}, order: &order}
	r := &tunRunner{out: map[string]string{"status": tunStatus}}
	setupTunnel(t, r, st)

	code, out, errOut := runTun("-no-qr", "-ttl", "5m", "-listen", "127.0.0.1:9000")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	wantCalls := [][]string{
		{"/bin/ts", "status", "--json"},
		{"/bin/ts", "serve", "--bg", "--https=443", "http://127.0.0.1:9000"},
	}
	if !reflect.DeepEqual(r.calls, wantCalls) {
		t.Fatalf("calls = %v", r.calls)
	}
	wantOrder := []string{"set:" + store.SettingTunnelHost, "set:" + store.SettingTunnelLogin, "paircode"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %v", order)
	}
	if st.settings[store.SettingTunnelHost] != "mac.tail1.ts.net" || st.settings[store.SettingTunnelLogin] != "me@example.com" {
		t.Fatalf("settings = %v", st.settings)
	}
	if st.ttl != 5*time.Minute || !st.closed {
		t.Fatalf("ttl %v closed %v", st.ttl, st.closed)
	}
	for _, want := range []string{"remote URL: https://mac.tail1.ts.net ", "https://mac.tail1.ts.net/#pair=CODE123", "CODE123"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "▀") || strings.Contains(out, "█") || strings.Contains(out, "▄") {
		t.Fatalf("-no-qr printed a QR:\n%s", out)
	}
}

func TestTunnelUpPrintsQR(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{}, order: &order}
	setupTunnel(t, &tunRunner{out: map[string]string{"status": tunStatus}}, st)
	code, out, errOut := runTun()
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.ContainsAny(out, "▀█▄") {
		t.Fatalf("no QR in output:\n%s", out)
	}
}

func TestTunnelUpErrors(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{}, order: &order}
	r := &tunRunner{out: map[string]string{"status": `{"Self":null}`}}
	setupTunnel(t, r, st)
	code, _, errOut := runTun()
	if code != 1 || !strings.Contains(errOut, "not logged in") {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if len(st.settings) != 0 || len(r.calls) != 1 {
		t.Fatalf("side effects after failed status: %v %v", st.settings, r.calls)
	}

	tunnelFindCLI = func() (string, error) { return "", errors.New("tailscale CLI not found") }
	code, _, errOut = runTun()
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("code %d err %q", code, errOut)
	}
}

func TestTunnelOff(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{
		store.SettingTunnelHost: "h", store.SettingTunnelLogin: "l", store.SettingVAPIDPublic: "keep",
	}, order: &order}
	r := &tunRunner{}
	setupTunnel(t, r, st)
	code, _, errOut := runTun("off")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	want := [][]string{{"/bin/ts", "serve", "--https=443", "off"}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v", r.calls)
	}
	if !reflect.DeepEqual(st.settings, map[string]string{store.SettingVAPIDPublic: "keep"}) {
		t.Fatalf("settings = %v", st.settings)
	}
	// Nothing saved: still fine.
	code, _, errOut = runTun("off")
	if code != 0 {
		t.Fatalf("second off: code %d: %s", code, errOut)
	}
}

func TestTunnelOffDownFailureKeepsSettings(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{store.SettingTunnelHost: "h"}, order: &order}
	r := &tunRunner{err: map[string]error{"serve": errors.New("exit 1")}}
	setupTunnel(t, r, st)
	code, _, errOut := runTun("off")
	if code != 1 || !strings.Contains(errOut, "exit 1") || st.settings[store.SettingTunnelHost] != "h" {
		t.Fatalf("code %d err %q settings %v", code, errOut, st.settings)
	}
}

func TestTunnelOffMissingCLIClearsSettings(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{
		store.SettingTunnelHost: "h", store.SettingTunnelLogin: "l", store.SettingVAPIDPublic: "keep",
	}, order: &order}
	r := &tunRunner{}
	setupTunnel(t, r, st)
	tunnelFindCLI = func() (string, error) { return "", errors.New("tailscale CLI not found") }
	code, _, errOut := runTun("off")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.Contains(errOut, "hub: warning: tailscale CLI not found; cleared the saved tunnel settings") {
		t.Fatalf("stderr = %q", errOut)
	}
	if len(r.calls) != 0 {
		t.Fatalf("Down invoked: %v", r.calls)
	}
	if !reflect.DeepEqual(st.settings, map[string]string{store.SettingVAPIDPublic: "keep"}) {
		t.Fatalf("settings = %v", st.settings)
	}
}

func TestTunnelUpMissingCLIStillFails(t *testing.T) {
	var order []string
	st := &tunStore{settings: map[string]string{}, order: &order}
	setupTunnel(t, &tunRunner{}, st)
	tunnelFindCLI = func() (string, error) { return "", errors.New("tailscale CLI not found") }
	if code, _, _ := runTun(); code != 1 {
		t.Fatalf("code %d", code)
	}
}
