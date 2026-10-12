package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/config"
	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
)

func TestFormatUpdate(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 5, 7, 0, time.UTC)
	label := func(s string) string { return map[string]string{"CEKAT": "work"}[s] }
	a := sessions.Agent{Kind: "claude", Name: "upsera", PaneID: "w2:p1", Status: "blocked"}
	cases := []struct {
		u    sessions.Update
		want string
	}{
		{sessions.Update{Session: "CEKAT", Type: sessions.UpdateOnline, At: at}, "09:05:07 [work] online"},
		{sessions.Update{Session: "CEKAT", Type: sessions.UpdateTransition, Agent: a, From: "working", To: "blocked", At: at}, "09:05:07 [work] claude upsera (w2:p1)  working -> blocked"},
		{sessions.Update{Session: "CEKAT", Type: sessions.UpdateAdded, Agent: sessions.Agent{Kind: "pi", PaneID: "w1:p1", Status: "idle"}, At: at}, "09:05:07 [work] + pi w1:p1  idle"},
		{sessions.Update{Session: "CEKAT", Type: sessions.UpdateRemoved, Agent: a, At: at}, "09:05:07 [work] - claude upsera (w2:p1)"},
	}
	for _, c := range cases {
		if got := formatUpdate(c.u, label); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestFindSessionByNameOrLabel(t *testing.T) {
	found := []sessions.Found{{Name: "CEKAT", SocketPath: "/a"}, {Name: "FRIDAY", SocketPath: "/b"}}
	cfg := config.Config{Sessions: []config.Session{{Name: "CEKAT", Label: "work"}, {Name: "FRIDAY", Label: "labs"}}}
	for _, want := range []string{"CEKAT", "work"} {
		if f, ok := findSession(found, cfg, want); !ok || f.SocketPath != "/a" {
			t.Errorf("findSession(%q) = %+v, %v", want, f, ok)
		}
	}
	if _, ok := findSession(found, cfg, "cekat"); ok {
		t.Error("session names are case-sensitive")
	}
}

func TestCaptureName(t *testing.T) {
	got := captureName("work", "w1:p2", time.Date(2026, 10, 12, 9, 5, 7, 0, time.UTC))
	if want := "work-w1-p2-20261012T090507Z.txt"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRunUsageAndUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage:") {
		t.Fatalf("no args: code=%d stderr=%q", code, errb.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"nope"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), `unknown command "nope"`) {
		t.Fatalf("unknown: code=%d stderr=%q", code, errb.String())
	}
}

func TestCaptureNameSanitizesLabel(t *testing.T) {
	got := captureName("../../evil/x", "w1:p2", time.Date(2026, 10, 12, 9, 5, 7, 0, time.UTC))
	if strings.ContainsAny(got, "/\\") {
		t.Fatalf("file name %q must not contain a path separator", got)
	}
}
