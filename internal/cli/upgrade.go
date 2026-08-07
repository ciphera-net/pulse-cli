package cli

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-cli/internal/upgrade"
)

// releasesURL is where this command looks for the newest release.
//
// A package-level var so that a test can point it at an httptest server, and
// nothing more: there is deliberately no flag and no environment override.
// PULSE_API_URL exists because redirecting a READ is harmless; redirecting where
// a replacement binary is fetched from is a supply-chain switch, and one that no
// support answer should ever begin by suggesting. (The embedded signing key
// would still refuse a foreign archive — this is the second lock, not the only
// one.)
var releasesURL = upgrade.DefaultReleasesURL

func newUpgradeCmd(app *App) *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Install the newest release",
		Long: "Install the newest release.\n\n" +
			"The archive is downloaded from GitHub's public release feed — no Pulse API key is\n" +
			"sent, and no quota is spent — and its cosign signature is checked against the key\n" +
			"compiled into this binary before anything on disk is touched.\n\n" +
			"A pulse installed by Homebrew or `go install` is left alone: the command prints\n" +
			"what to run instead. Replacing such a binary in place works, and then leaves the\n" +
			"package manager reporting a version that is no longer installed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpgrade(cmd.Context(), app, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false,
		"report whether a newer release exists, change nothing, and exit 0 either way")
	return cmd
}

// runUpgrade is the whole command.
//
// The order is load-bearing: resolve where we are, ask what exists, and only
// then decide whether this binary is ours to replace.
func runUpgrade(ctx context.Context, app *App, checkOnly bool) error {
	p := app.Printer

	// * A path that will not resolve is not fatal for a check — "is there a
	// * newer version" has an answer either way — so the error is carried and
	// * raised only where it actually blocks work.
	exe, exeErr := upgrade.ExecutablePath()

	c := upgrade.NewClient("pulse-cli/" + Version)
	c.ReleasesURL = releasesURL

	st, rel, err := upgrade.Check(ctx, c, Version, exe)
	if err != nil {
		return err
	}

	if checkOnly {
		// * Exit 0 whether or not an update exists.
		// *
		// * `pulse upgrade --check` is meant to be safe in a cron job, a
		// * Makefile and an agent loop, and every one of those treats a non-zero
		// * exit as a failure to escalate. Exiting non-zero because an upgrade
		// * EXISTS would turn a routine check into a nightly page. The answer is
		// * the output; the exit code stays reserved for the check itself
		// * failing.
		st.Action = upgrade.ActionChecked
		return reportCheck(p, st)
	}

	if !st.UpdateAvailable {
		st.Action = upgrade.ActionUpToDate
		if emitted, err := emitJSON(p, st); emitted {
			return err
		}
		p.Success("pulse %s is the latest release.", displayVersion(st.Current))
		return nil
	}

	method := upgrade.Method(st.InstallMethod)
	if instruction := method.Instruction(); instruction != "" {
		st.Action = upgrade.ActionDeferred
		if emitted, err := emitJSON(p, st); emitted {
			return err
		}
		p.Printf("pulse %s → %s is available; installed via %s, so run: %s\n",
			displayVersion(st.Current), st.Latest, managerName(method), instruction)
		p.Note("Replacing the binary here would leave %s reporting a version that is no longer installed.",
			managerName(method))
		return nil
	}

	if exeErr != nil {
		return &cliError{msg: exeErr.Error(), code: client.ExitServerError}
	}
	// * Checked before a byte is downloaded. Discovering that the target is
	// * read-only after a 3 MB download and a signature check is a worse
	// * experience for exactly the same outcome.
	if err := upgrade.CheckWritable(exe); err != nil {
		return &cliError{msg: err.Error(), code: client.ExitServerError}
	}

	binary, err := upgrade.Prepare(ctx, c, rel, func(msg string) { p.Note("%s", msg) })
	if err != nil {
		return err
	}
	if err := upgrade.Replace(exe, binary); err != nil {
		return &cliError{msg: err.Error(), code: client.ExitServerError}
	}

	st.Action = upgrade.ActionInstalled
	st.Current = st.Latest
	st.UpdateAvailable = false
	if emitted, err := emitJSON(p, st); emitted {
		return err
	}
	p.Success("pulse %s installed at %s.", st.Latest, exe)
	if st.ReleaseURL != "" {
		p.Note("Release notes: %s", st.ReleaseURL)
	}
	return nil
}

// reportCheck prints what a check found.
//
// The verdict goes to stdout, because for this command the verdict IS the
// answer — `pulse upgrade --check | grep -q 'Update available'` has to work.
// The URL and the suggested command are context, and context goes to stderr like
// everywhere else in this CLI.
func reportCheck(p *render.Printer, st upgrade.Status) error {
	if emitted, err := emitJSON(p, st); emitted {
		return err
	}

	if !st.UpdateAvailable {
		p.Printf("pulse %s is the latest release.\n", displayVersion(st.Current))
		return nil
	}

	p.Printf("Update available: %s → %s\n", displayVersion(st.Current), st.Latest)
	if st.ReleaseURL != "" {
		p.Note("%s", st.ReleaseURL)
	}
	if instruction := upgrade.Method(st.InstallMethod).Instruction(); instruction != "" {
		p.Note("Installed via %s — run: %s", managerName(upgrade.Method(st.InstallMethod)), instruction)
	} else {
		p.Note("Run `pulse upgrade` to install it.")
	}
	return nil
}

// emitJSON writes the status when --json is set, reporting whether it handled
// the output so the caller can skip its prose.
func emitJSON(p *render.Printer, st upgrade.Status) (bool, error) {
	if p.Mode != render.ModeJSON {
		return false, nil
	}
	// * Marshalled rather than passed through: unlike every other --json in this
	// * CLI, this object is ours and not an API response, so there is no upstream
	// * body whose exact bytes have to survive.
	b, err := json.Marshal(st)
	if err != nil {
		return true, err
	}
	p.JSON(b)
	return true, nil
}

// displayVersion names the running build, marking one that cannot be ordered.
//
// A binary built from a working tree carries "dev", and comparing "dev" to a
// release tag has no answer — so it is treated as older than everything and
// SAID so, rather than silently reported as out of date.
func displayVersion(v string) string {
	if upgrade.Valid(v) {
		return v
	}
	return v + " (development build)"
}

func managerName(m upgrade.Method) string {
	switch m {
	case upgrade.MethodHomebrew:
		return "Homebrew"
	case upgrade.MethodGoInstall:
		return "go install"
	default:
		return string(m)
	}
}
