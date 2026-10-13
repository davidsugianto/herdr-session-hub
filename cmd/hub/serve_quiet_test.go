package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/api"
	"github.com/davidsugianto/herdr-session-hub/internal/config"
)

func quietConfig(t *testing.T) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[[session]]
name = "work"
quiet_hours = "Sat-Sun"

[[session]]
name = "play"

[[session]]
name = "night"
quiet_hours = "22:00-07:00"
`
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestQuietRules(t *testing.T) {
	got := quietRules(quietConfig(t))
	want := []api.QuietRule{{Session: "work", Rule: "Sat-Sun"}, {Session: "night", Rule: "22:00-07:00"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	if r := quietRules(config.Config{}); len(r) != 0 {
		t.Fatalf("empty config rules = %v", r)
	}
}

func TestQuietPolicyActive(t *testing.T) {
	q := quietPolicy{quietConfig(t)}
	sat := time.Date(2026, 10, 17, 12, 0, 0, 0, time.Local) // Saturday noon
	mon := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local) // Monday noon
	if !q.Active("work", sat) {
		t.Error("work should be quiet on Saturday")
	}
	if q.Active("work", mon) {
		t.Error("work should not be quiet on Monday")
	}
	if q.Active("play", sat) {
		t.Error("session without a rule is never quiet")
	}
	if !q.Active("night", time.Date(2026, 10, 12, 23, 0, 0, 0, time.Local)) {
		t.Error("night should be quiet at 23:00")
	}
}
