// Package config loads the optional herdr-session-hub configuration file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Session maps a herdr session to a display label.
type Session struct {
	// Name is the herdr session directory name (case-sensitive, e.g. "CEKAT").
	Name string `toml:"name"`
	// Label is the display name (e.g. "work").
	Label string `toml:"label"`
}

// Config is the parsed configuration file. Unknown keys and sections
// (e.g. [discovery], [mac]) are ignored.
type Config struct {
	Sessions []Session `toml:"session"`
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

// Load reads the config file at path. A missing file yields an empty Config
// and a nil error; malformed TOML yields an error that names the path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
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
