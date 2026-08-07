package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-cli/internal/upgrade"
)

// feed serves a GitHub release feed. No test in this package touches the
// network: an upgrade check that reached api.github.com would make the suite
// fail on an aeroplane and, worse, spend a shared runner's unauthenticated rate
// limit.
func feed(t *testing.T, tag string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"` + tag + `",
			"html_url":"https://github.com/ciphera-net/pulse-cli/releases/tag/` + tag + `",
			"assets":[{"name":"pulse_x_linux_amd64.tar.gz","browser_download_url":"https://example.invalid/a","size":1}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runUpgradeCmd runs the command exactly as the process would, through the same
// exit-code path, and returns what a caller would see.
func runUpgradeCmd(t *testing.T, current, tag string, mode render.Mode, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	prevVersion, prevURL := Version, releasesURL
	Version, releasesURL = current, feed(t, tag)
	t.Cleanup(func() { Version, releasesURL = prevVersion, prevURL })

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{Printer: render.NewPrinterTo(out, errBuf, mode), started: true}

	cmd := newUpgradeCmd(app)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(app.Printer.Out())
	cmd.SetErr(app.Printer.Err())
	cmd.SetArgs(args)

	return finish(app, cmd.Execute()), out.String(), errBuf.String()
}

// * `--check` exits 0 whether or not an update exists.
// *
// * This is the property that makes the command safe to put in a cron job, a
// * Makefile prerequisite or an agent loop — all of which treat a non-zero exit
// * as a failure worth escalating. A check that exited non-zero BECAUSE an
// * upgrade is available would turn a routine daily check into a nightly page,
// * and the obvious "fix" is for the user to stop running it.
// *
// * Both cases in one test: an implementation that exits non-zero on the
// * up-to-date case and one that exits non-zero on the update-available case are
// * different bugs, and either alone passes a single-case test.
func TestCheckExitsZeroWhetherOrNotAnUpdateExists(t *testing.T) {
	t.Run("already current", func(t *testing.T) {
		code, stdout, _ := runUpgradeCmd(t, "v1.1.0", "v1.1.0", render.ModeTable, "--check")
		if code != client.ExitOK {
			t.Fatalf("exit code = %d, want 0 — `pulse upgrade --check` in cron must not page", code)
		}
		if !strings.Contains(stdout, "latest release") {
			t.Errorf("stdout does not say the binary is current: %q", stdout)
		}
		if strings.Contains(stdout, "Update available") {
			t.Errorf("an up-to-date binary was told to upgrade: %q", stdout)
		}
	})

	t.Run("an update is available", func(t *testing.T) {
		code, stdout, stderr := runUpgradeCmd(t, "v1.0.0", "v1.1.0", render.ModeTable, "--check")
		if code != client.ExitOK {
			t.Fatalf("exit code = %d, want 0 — an available upgrade is news, not a failure", code)
		}
		// * The verdict is the answer, so it goes to stdout where `grep -q` and
		// * a captured variable can reach it.
		if !strings.Contains(stdout, "Update available: v1.0.0 → v1.1.0") {
			t.Errorf("stdout does not name both versions: %q", stdout)
		}
		if !strings.Contains(stderr, "pulse upgrade") {
			t.Errorf("stderr does not say what to run: %q", stderr)
		}
	})

	// * Ten versus nine, through the whole command. A string comparison reports
	// * v1.10.0 as older than v1.9.0 and tells the user to "upgrade" backwards.
	t.Run("v1.10.0 is not older than v1.9.0", func(t *testing.T) {
		code, stdout, _ := runUpgradeCmd(t, "v1.10.0", "v1.9.0", render.ModeTable, "--check")
		if code != client.ExitOK {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if strings.Contains(stdout, "Update available") {
			t.Errorf("v1.10.0 was told to downgrade to v1.9.0: %q", stdout)
		}
	})
}

// * --check must never write anything, which is what lets it run unattended on a
// * machine whose binary is owned by a package manager.
func TestCheckReportsAManagedInstallWithoutTouchingIt(t *testing.T) {
	code, stdout, stderr := runUpgradeCmd(t, "v1.0.0", "v1.1.0", render.ModeTable, "--check")
	if code != client.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.Contains(stdout+stderr, "installed at") {
		t.Error("--check reported an installation")
	}
}

func TestCheckJSONIsMachineReadableInBothCases(t *testing.T) {
	for _, c := range []struct {
		name            string
		current, latest string
		want            bool
	}{
		{"update available", "v1.0.0", "v1.1.0", true},
		{"already current", "v1.1.0", "v1.1.0", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, stdout, _ := runUpgradeCmd(t, c.current, c.latest, render.ModeJSON, "--check")
			if code != client.ExitOK {
				t.Fatalf("exit code = %d, want 0", code)
			}

			var st upgrade.Status
			if err := json.Unmarshal([]byte(stdout), &st); err != nil {
				t.Fatalf("--json did not produce JSON (%v): %q", err, stdout)
			}
			if st.UpdateAvailable != c.want {
				t.Errorf("update_available = %v, want %v", st.UpdateAvailable, c.want)
			}
			if st.Current != c.current || st.Latest != c.latest {
				t.Errorf("current/latest = %q/%q, want %q/%q", st.Current, st.Latest, c.current, c.latest)
			}
			if st.Action != upgrade.ActionChecked {
				t.Errorf("action = %q, want %q", st.Action, upgrade.ActionChecked)
			}
			// * The install method is what tells a script whether it may act on
			// * update_available itself or has to call the package manager.
			if st.InstallMethod == "" {
				t.Error("install_method is empty")
			}
		})
	}
}

// * A binary built from a working tree carries the version "dev", which cannot
// * be ordered against a tag. It is treated as older than every release — and
// * said so, rather than silently rendered as an ordinary version number.
func TestCheckNamesADevelopmentBuild(t *testing.T) {
	code, stdout, _ := runUpgradeCmd(t, "dev", "v1.1.0", render.ModeTable, "--check")
	if code != client.ExitOK {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "development build") {
		t.Errorf("a dev build was reported as an ordered version: %q", stdout)
	}
}

// * A check that could not be performed is NOT a check that passed. Exit 0 is
// * reserved for having an answer; an unreachable feed has to be visible, or a
// * cron job silently stops noticing releases and nothing ever says so.
func TestAnUnreachableFeedIsAFailure(t *testing.T) {
	prevVersion, prevURL := Version, releasesURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	Version, releasesURL = "v1.0.0", srv.URL
	t.Cleanup(func() {
		Version, releasesURL = prevVersion, prevURL
		srv.Close()
	})

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{Printer: render.NewPrinterTo(out, errBuf, render.ModeTable), started: true}
	cmd := newUpgradeCmd(app)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(app.Printer.Out())
	cmd.SetErr(app.Printer.Err())
	cmd.SetArgs([]string{"--check"})

	if code := finish(app, cmd.Execute()); code == client.ExitOK {
		t.Fatal("a failed check exited 0; a cron job would never learn it stopped working")
	}
	if !strings.Contains(errBuf.String(), "500") {
		t.Errorf("the failure does not say what happened: %q", errBuf.String())
	}
}
