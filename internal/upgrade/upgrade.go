// Package upgrade implements `pulse upgrade`: find the newest published
// release, prove it was signed by the key that shipped with this binary, and
// replace the running executable with it.
//
// Two rules shape everything here.
//
// The first is that a signature is checked before anything is written. The
// bytes arrive over the network from a host that is not ours, and the only
// thing standing between "GitHub served a file" and "the user runs it as
// themselves" is the embedded release key. Verification therefore happens on
// the downloaded archive, in memory, before a single byte reaches the
// filesystem — see Prepare, which returns the binary or an error and has no
// third outcome.
//
// The second is that pulse does not fight a package manager for ownership of
// its own file. See Detect.
package upgrade

import (
	"context"
	"fmt"
	"runtime"
)

// Status is what a check found. The json tags are the --json contract.
type Status struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	InstallMethod   string `json:"install_method"`
	Path            string `json:"path"`
	ReleaseURL      string `json:"release_url"`

	// Action is what the command did about it. Reported so that a script
	// reading --json can distinguish "nothing to do" from "a package manager
	// owns this binary and you must run something else" — both of which exit 0
	// and would otherwise be indistinguishable.
	Action Action `json:"action"`
}

// Action is the outcome of one invocation.
type Action string

const (
	// ActionChecked is --check: a comparison, and nothing else.
	ActionChecked Action = "checked"
	// ActionUpToDate is an upgrade that had nothing to install.
	ActionUpToDate Action = "up-to-date"
	// ActionDeferred is an upgrade a package manager has to perform.
	ActionDeferred Action = "deferred"
	// ActionInstalled is a binary that was replaced.
	ActionInstalled Action = "installed"
)

// Check compares the running version against the newest release.
//
// It never writes anything and never needs a credential, which is what makes it
// safe to run on a timer.
func Check(ctx context.Context, c *Client, current, exe string) (Status, *Release, error) {
	rel, err := c.Latest(ctx)
	if err != nil {
		return Status{}, nil, err
	}
	return Status{
		Current:         current,
		Latest:          rel.Tag,
		UpdateAvailable: Newer(rel.Tag, current),
		InstallMethod:   string(Detect(exe)),
		Path:            exe,
		ReleaseURL:      rel.URL,
	}, rel, nil
}

// Prepare downloads the release build for this platform and returns the
// verified executable.
//
// Nothing it returns has skipped verification: the archive and its detached
// signature are fetched, checked against the embedded key, and only then opened.
// A caller that gets bytes back can write them; a caller that gets an error must
// not write anything.
func Prepare(ctx context.Context, c *Client, rel *Release, progress func(string)) ([]byte, error) {
	archive, signature, err := PickAssets(rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	pub, err := PublicKey()
	if err != nil {
		return nil, err
	}

	note(progress, fmt.Sprintf("Downloading %s (%s)…", archive.Name, humanSize(archive.Size)))
	blob, err := c.Download(ctx, archive)
	if err != nil {
		return nil, err
	}
	sig, err := c.Download(ctx, signature)
	if err != nil {
		return nil, err
	}

	note(progress, "Verifying the release signature…")
	if err := Verify(pub, blob, sig); err != nil {
		return nil, fmt.Errorf("%w\n\n"+
			"  %s did not come from a Pulse release, or it was modified in transit.\n"+
			"  Nothing has been changed on disk. Do not install it.", err, archive.Name)
	}

	bin, err := ExtractBinary(archive.Name, blob, BinaryName(runtime.GOOS))
	if err != nil {
		return nil, err
	}
	return bin, nil
}

func note(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}

// humanSize renders a download size the way a person reads one.
func humanSize(n int64) string {
	const mb = 1 << 20
	if n <= 0 {
		return "unknown size"
	}
	if n < mb {
		return fmt.Sprintf("%d KB", n/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/mb)
}
