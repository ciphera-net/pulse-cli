// Package config reads and writes the CLI's preferences file.
//
// Preferences only. No credential ever reaches this file — those live in the OS
// keychain (see internal/credentials). The split is the point: this file is safe
// to read, sync, and commit by accident.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/pulse/config.toml.
type Config struct {
	// DefaultSite is what `pulse sites use` sets. Stored as the site's UUID
	// plus the label it was chosen by: the UUID is the stable identifier the
	// API takes, and the label is what a human recognises in `auth status`
	// without a lookup.
	DefaultSite  string `toml:"default_site,omitempty"`
	DefaultLabel string `toml:"default_site_label,omitempty"`

	// Profiles maps a profile name to its default site, so `--profile work`
	// and `--profile personal` do not fight over one setting.
	Profiles map[string]Profile `toml:"profiles,omitempty"`
}

// Profile is per-profile state.
type Profile struct {
	DefaultSite  string `toml:"default_site,omitempty"`
	DefaultLabel string `toml:"default_site_label,omitempty"`
}

// Path returns the config file location, honouring XDG_CONFIG_HOME.
func Path() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "pulse", "config.toml"), nil
	}
	home, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the config directory: %w", err)
	}
	return filepath.Join(home, "pulse", "config.toml"), nil
}

// Load reads the config. A missing file is an empty config, not an error — the
// CLI has to work on first run.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	c := &Config{Profiles: map[string]Profile{}}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

// Save writes the config, creating the directory if needed.
//
// 0600 on the file and 0700 on the directory. It holds no secret today, but a
// preferences file that is world-readable is a habit rather than a decision,
// and the cost of the stricter mode is nothing.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	var sb strings.Builder
	sb.WriteString("# Pulse CLI preferences. API keys are NOT stored here — they live in\n")
	sb.WriteString("# the operating system keychain. Safe to read, sync and share.\n\n")
	if err := toml.NewEncoder(&sb).Encode(c); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// SiteFor returns the default site for a profile, falling back to the top-level
// default so an existing setup keeps working when a profile is introduced.
func (c *Config) SiteFor(profile string) (id, label string) {
	if p, ok := c.Profiles[profile]; ok && p.DefaultSite != "" {
		return p.DefaultSite, p.DefaultLabel
	}
	return c.DefaultSite, c.DefaultLabel
}

// SetSiteFor records the default site for a profile.
func (c *Config) SetSiteFor(profile, id, label string) {
	if profile == "" || profile == "default" {
		c.DefaultSite, c.DefaultLabel = id, label
		return
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	c.Profiles[profile] = Profile{DefaultSite: id, DefaultLabel: label}
}
