package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigLoadMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(cfg.Sessions) != 0 {
		t.Fatalf("expected no sessions, got %+v", cfg)
	}
	assertDefaults(t, cfg)
}

func TestConfigLoadValid(t *testing.T) {
	p := writeFile(t, `
[[session]]
name  = "CEKAT"
label = "work"
[[session]]
name  = "FRIDAY"
label = "labs"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{{Name: "CEKAT", Label: "work"}, {Name: "FRIDAY", Label: "labs"}}
	if len(cfg.Sessions) != len(want) {
		t.Fatalf("got %+v", cfg.Sessions)
	}
	for i, s := range want {
		if cfg.Sessions[i] != s {
			t.Errorf("session %d = %+v, want %+v", i, cfg.Sessions[i], s)
		}
	}
	if got := cfg.Label("CEKAT"); got != "work" {
		t.Errorf("Label(CEKAT) = %q", got)
	}
}

func TestConfigUnknownSectionsIgnored(t *testing.T) {
	p := writeFile(t, `
[discovery]
auto = true

[future]
keep_awake = true

[[session]]
name  = "CEKAT"
label = "work"
color = "blue"
quiet_hours = "Sat-Sun"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("unknown keys should be ignored: %v", err)
	}
	if len(cfg.Sessions) != 1 || cfg.Label("CEKAT") != "work" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestConfigMalformedErrorMentionsPath(t *testing.T) {
	p := writeFile(t, "[[session\nname = \n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), p) {
		t.Fatalf("error %q should mention path %q", err, p)
	}
}

func TestConfigLabelFallback(t *testing.T) {
	cfg := Config{Sessions: []Session{{Name: "CEKAT", Label: "work"}}}
	if got := cfg.Label("OTHER"); got != "OTHER" {
		t.Errorf("Label(OTHER) = %q, want OTHER", got)
	}
	// Names are case-sensitive.
	if got := cfg.Label("cekat"); got != "cekat" {
		t.Errorf("Label(cekat) = %q, want cekat", got)
	}
}

func TestConfigEmptyLabelFallback(t *testing.T) {
	cfg := Config{Sessions: []Session{{Name: "CEKAT", Label: ""}}}
	if got := cfg.Label("CEKAT"); got != "CEKAT" {
		t.Errorf("Label(CEKAT) = %q, want CEKAT", got)
	}
}

func TestConfigDuplicateNamesLastWins(t *testing.T) {
	p := writeFile(t, `
[[session]]
name  = "CEKAT"
label = "first"
[[session]]
name  = "CEKAT"
label = "second"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Label("CEKAT"); got != "second" {
		t.Errorf("Label(CEKAT) = %q, want second", got)
	}
}

func TestConfigDuplicateNamesLastEmptyLabel(t *testing.T) {
	cfg := Config{Sessions: []Session{{Name: "A", Label: "x"}, {Name: "A", Label: ""}}}
	if got := cfg.Label("A"); got != "A" {
		t.Errorf("last entry has empty label, want name fallback, got %q", got)
	}
}

func TestConfigDefaultPathXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	want := filepath.Join("/xdg", "herdr-session-hub", "config.toml")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestConfigDefaultPathUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "herdr-session-hub", "config.toml")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
