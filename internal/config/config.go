// Package config loads the optional herdr-session-hub configuration file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Session maps a herdr session to a display label.
type Session struct {
	// Name is the herdr session directory name (case-sensitive, e.g. "CEKAT").
	Name string `toml:"name"`
	// Label is the display name (e.g. "work").
	Label string `toml:"label"`
	// QuietHours optionally silences push notifications, e.g.
	// "22:00-07:00, Sat-Sun" (see ParseQuietHours).
	QuietHours string `toml:"quiet_hours"`
}

// Notify configures push notifications.
type Notify struct {
	// BlockedBreaksQuiet lets "blocked" notifications through quiet hours.
	BlockedBreaksQuiet bool `toml:"blocked_breaks_quiet"`
	// Contact is the Web Push VAPID subject (a mailto: or https: URL).
	Contact string `toml:"contact"`
}

// Mac configures macOS-only behaviour.
type Mac struct {
	// KeepAwake is "off", "while-working" or "always".
	KeepAwake string `toml:"keep_awake"`
}

// Keep-awake modes.
const (
	KeepAwakeOff          = "off"
	KeepAwakeWhileWorking = "while-working"
	KeepAwakeAlways       = "always"
)

// Config is the parsed configuration file. Unknown keys and sections
// (e.g. [discovery]) are ignored.
type Config struct {
	Sessions []Session `toml:"session"`
	Notify   Notify    `toml:"notify"`
	Mac      Mac       `toml:"mac"`
}

// defaults returns the configuration used for keys the file does not set.
func defaults() Config {
	return Config{
		Notify: Notify{BlockedBreaksQuiet: true, Contact: "mailto:hub@example.com"},
		Mac:    Mac{KeepAwake: KeepAwakeWhileWorking},
	}
}

// DefaultPath returns $XDG_CONFIG_HOME/herdr-session-hub/config.toml, or
// ~/.config/herdr-session-hub/config.toml when XDG_CONFIG_HOME is unset or empty.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr-session-hub", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "herdr-session-hub", "config.toml")
}

// Load reads the config file at path. A missing file yields the defaults and
// a nil error; malformed TOML, a bad keep_awake mode or a bad quiet_hours
// yields an error that names the path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return defaults(), nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := defaults()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	switch cfg.Mac.KeepAwake {
	case KeepAwakeOff, KeepAwakeWhileWorking, KeepAwakeAlways:
	default:
		return Config{}, fmt.Errorf("config %s: [mac] keep_awake %q must be %q, %q or %q",
			path, cfg.Mac.KeepAwake, KeepAwakeOff, KeepAwakeWhileWorking, KeepAwakeAlways)
	}
	for _, s := range cfg.Sessions {
		if _, err := ParseQuietHours(s.QuietHours); err != nil {
			return Config{}, fmt.Errorf("config %s: session %q quiet_hours: %w", path, s.Name, err)
		}
	}
	return cfg, nil
}

// QuietActive reports whether the session's quiet_hours rule is active at t
// (in t's local time). A session with no rule, or an unparseable one, is never
// quiet. If several entries share a name, the last one wins.
func (c Config) QuietActive(session string, t time.Time) bool {
	rule := ""
	for _, s := range c.Sessions {
		if s.Name == session {
			rule = s.QuietHours
		}
	}
	r, err := ParseQuietHours(rule)
	if err != nil {
		return false
	}
	return r.Active(t)
}

// Label returns the configured label for the session name, or name itself when
// there is no entry or the label is empty. If several entries share a name, the
// last one wins.
func (c Config) Label(name string) string {
	label := ""
	for _, s := range c.Sessions {
		if s.Name == name {
			label = s.Label
		}
	}
	if label == "" {
		return name
	}
	return label
}
