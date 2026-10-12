package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// pairedDB creates a temp SQLite DB with the named devices paired and returns
// its path, the devices and their tokens (same order as names).
func pairedDB(t *testing.T, names ...string) (string, []store.Device, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var devs []store.Device
	var tokens []string
	for _, n := range names {
		code, err := st.CreatePairCode(context.Background(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		tok, d, err := st.ExchangePairCode(context.Background(), code, n)
		if err != nil {
			t.Fatal(err)
		}
		devs = append(devs, d)
		tokens = append(tokens, tok)
	}
	return path, devs, tokens
}

func runDevicesCmd(t *testing.T, input string, args ...string) (code int, out, errOut string) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(input)
	defer func() { stdin = old }()
	var o, e bytes.Buffer
	code = run(context.Background(), append([]string{"devices"}, args...), &o, &e)
	return code, o.String(), e.String()
}

func deviceIDs(t *testing.T, path string) []string {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ds, err := st.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range ds {
		ids = append(ids, d.ID)
	}
	return ids
}

func TestDevicesListEmpty(t *testing.T) {
	path, _, _ := pairedDB(t)
	code, out, errOut := runDevicesCmd(t, "", "list", "-db", path)
	if code != 0 || strings.TrimSpace(out) != "no paired devices" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestDevicesListNonEmptyHidesTokens(t *testing.T) {
	path, devs, tokens := pairedDB(t, "phone", "tablet")
	code, out, errOut := runDevicesCmd(t, "", "list", "-db", path)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, tok := range tokens {
		if strings.Contains(out, tok) {
			t.Fatalf("output leaks token %q:\n%s", tok, out)
		}
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 lines, got %d:\n%s", len(lines), out)
	}
	for _, d := range devs {
		want := d.ID + "  " + d.Name + "  created " + d.CreatedAt.UTC().Format(time.RFC3339) +
			"  last seen " + d.LastSeen.UTC().Format(time.RFC3339)
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
}

func TestDevicesRevokeYes(t *testing.T) {
	path, devs, _ := pairedDB(t, "phone", "tablet")
	code, out, errOut := runDevicesCmd(t, "", "revoke", "-db", path, "-yes", devs[0].ID)
	if code != 0 || strings.TrimSpace(out) != "revoked "+devs[0].ID || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if strings.Contains(out, "[y/N]") {
		t.Fatalf("-yes must not prompt: %q", out)
	}
	if ids := deviceIDs(t, path); len(ids) != 1 || ids[0] != devs[1].ID {
		t.Fatalf("remaining ids = %v", ids)
	}
}

func TestDevicesRevokePrompt(t *testing.T) {
	for _, in := range []string{"y\n", "YES\n", "Yes\n", "y"} {
		path, devs, _ := pairedDB(t, "phone")
		code, out, errOut := runDevicesCmd(t, in, "revoke", "-db", path, devs[0].ID)
		wantPrompt := "Revoke device " + devs[0].ID + " (phone)? This signs it out immediately. [y/N] "
		if code != 0 || !strings.HasPrefix(out, wantPrompt) || !strings.Contains(out, "revoked "+devs[0].ID) {
			t.Fatalf("input %q: code=%d out=%q err=%q", in, code, out, errOut)
		}
		if ids := deviceIDs(t, path); len(ids) != 0 {
			t.Fatalf("input %q: device not revoked: %v", in, ids)
		}
	}
}

func TestDevicesRevokeDecline(t *testing.T) {
	for _, in := range []string{"n\n", "\n", "", "maybe\n", "yep\n"} {
		path, devs, _ := pairedDB(t, "phone")
		code, out, errOut := runDevicesCmd(t, in, "revoke", "-db", path, devs[0].ID)
		if code != 1 || !strings.Contains(out, "[y/N] ") {
			t.Fatalf("input %q: code=%d out=%q", in, code, out)
		}
		if !strings.Contains(out+errOut, "aborted") {
			t.Fatalf("input %q: want aborted, out=%q err=%q", in, out, errOut)
		}
		if strings.Contains(out, "revoked ") {
			t.Fatalf("input %q: must not report revoked: %q", in, out)
		}
		if ids := deviceIDs(t, path); len(ids) != 1 {
			t.Fatalf("input %q: device was revoked: %v", in, ids)
		}
	}
}

func TestDevicesRevokeUnknownID(t *testing.T) {
	path, devs, _ := pairedDB(t, "phone")
	code, out, errOut := runDevicesCmd(t, "y\n", "revoke", "-db", path, "-yes", "nope")
	if code != 1 || !strings.Contains(errOut, "hub: no such device nope") || strings.Contains(out, "revoked") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if ids := deviceIDs(t, path); len(ids) != 1 || ids[0] != devs[0].ID {
		t.Fatalf("ids = %v", ids)
	}
	// Without -yes the lookup happens before the prompt.
	code, out, errOut = runDevicesCmd(t, "y\n", "revoke", "-db", path, "nope")
	if code != 1 || !strings.Contains(errOut, "hub: no such device nope") || strings.Contains(out, "[y/N]") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestDevicesUsageErrors(t *testing.T) {
	path, _, _ := pairedDB(t)
	cases := [][]string{
		{},
		{"bogus"},
		{"revoke", "-db", path},
		{"revoke", "-db", path, "a", "b"},
		{"list", "-db", path, "extra"},
	}
	for _, args := range cases {
		code, out, errOut := runDevicesCmd(t, "", args...)
		if code != 2 || errOut == "" || out != "" {
			t.Errorf("args %v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	if _, _, errOut := runDevicesCmd(t, ""); !strings.Contains(errOut, "usage:") {
		t.Errorf("missing subcommand should print usage, got %q", errOut)
	}
	if _, _, errOut := runDevicesCmd(t, "", "bogus"); !strings.Contains(errOut, `unknown devices subcommand "bogus"`) {
		t.Errorf("unknown subcommand message, got %q", errOut)
	}
}
