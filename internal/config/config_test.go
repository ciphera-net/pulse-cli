package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// * The reason this package and internal/credentials are separate.
// *
// * The config file is meant to be readable, syncable and committable by
// * accident. If a key could ever reach it, every one of those becomes a leak —
// * and the CLI's whole argument is that a keychain beats the .env file people
// * already commit. This walks the written bytes rather than trusting the struct
// * definition, so a field added later with a json/toml tag is caught too.
func TestNoCredentialEverReachesTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c := &Config{Profiles: map[string]Profile{}}
	c.SetSiteFor("default", "8a7cabce-4828-4663-8c77-67a9da11bc70", "ciphera.net")
	c.SetSiteFor("work", "3dcf0448-ef33-407a-b1a9-92503d11f083", "pulse.ciphera.net")
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pulse", "config.toml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	body := strings.ToLower(string(data))

	for _, forbidden := range []string{"pulse_sk", "secret", "token", "password", "api_key", "apikey", "bearer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the config file contains %q — credentials belong in the keychain, never here:\n%s", forbidden, data)
		}
	}
}

// * 0600, and 0700 on the directory. It holds no secret today, but a
// * world-readable preferences file is a habit rather than a decision, and the
// * stricter mode costs nothing.
func TestConfigIsWrittenPrivate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c := &Config{}
	c.SetSiteFor("default", "id", "example.com")
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "pulse", "config.toml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode is %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Join(dir, "pulse"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode is %o, want 700", perm)
	}
}

// * First run has no file, and that is not an error. A CLI that fails before it
// * can be configured cannot be configured.
func TestMissingConfigIsAnEmptyConfigNotAnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("a missing config must load cleanly: %v", err)
	}
	if id, _ := c.SiteFor("default"); id != "" {
		t.Errorf("empty config reported a default site %q", id)
	}
}

// * Profiles keep their own default site, so --profile work and --profile
// * personal do not overwrite each other's selection.
func TestProfilesKeepSeparateDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	c := &Config{Profiles: map[string]Profile{}}
	c.SetSiteFor("default", "id-a", "a.example")
	c.SetSiteFor("work", "id-b", "b.example")
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if id, _ := reloaded.SiteFor("default"); id != "id-a" {
		t.Errorf("default profile site = %q, want id-a", id)
	}
	if id, _ := reloaded.SiteFor("work"); id != "id-b" {
		t.Errorf("work profile site = %q, want id-b", id)
	}
	// * A profile with nothing of its own falls back to the top-level default,
	// * so introducing profiles does not break an existing setup.
	if id, _ := reloaded.SiteFor("unconfigured"); id != "id-a" {
		t.Errorf("unknown profile should fall back to the default site, got %q", id)
	}
}
