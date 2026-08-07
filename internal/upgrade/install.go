package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Method is how this copy of pulse got onto the machine.
//
// It decides whether `pulse upgrade` may touch the binary at all. Package
// managers keep their own record of what they installed and what version it is;
// replacing the file underneath one leaves that record describing a binary that
// no longer exists.
type Method string

const (
	// MethodBinary is a downloaded archive, or anything else with no manager
	// behind it: the only case pulse replaces itself.
	MethodBinary Method = "binary"

	// MethodHomebrew is a Homebrew install, cask or formula.
	MethodHomebrew Method = "homebrew"

	// MethodGoInstall is `go install`.
	MethodGoInstall Method = "go-install"

	// MethodUnknown is a binary whose own path could not be resolved.
	MethodUnknown Method = "unknown"
)

// Detect classifies an executable path.
//
// Homebrew is the case that makes this mandatory rather than a nicety. The
// Cellar is owned by the user on a default macOS install, so a self-replacing
// updater SUCCEEDS there — and leaves `brew list --versions pulse` reporting the
// version brew installed, `brew upgrade` a no-op that thinks it is current, and
// the next `brew upgrade pulse` silently reverting the user to an older binary.
// Nothing errors; the tool just lies about its own version from then on.
//
// BOTH Homebrew layouts are matched, because pulse has shipped through both. A
// formula stages into .../Cellar/pulse/<version>/bin/pulse; a cask — which is
// what the tap publishes from v1.1.1 on — stages into
// .../Caskroom/pulse/<version>/pulse. Matching only the Cellar would have made
// the Homebrew guard silently stop firing on the release that switched, which is
// the worst possible shape for this bug: no error, no failing build, just every
// brew user's install quietly desynchronised the first time they run
// `pulse upgrade`.
//
// A GOPATH/GOBIN install has the same shape: the file is writable, and replacing
// it desynchronises what `go version -m` reports about the module that built it.
func Detect(exe string) Method {
	if exe == "" {
		return MethodUnknown
	}
	// * Compared as slash paths so the same two rules hold on Windows, where
	// * %USERPROFILE%\go\bin\pulse.exe is the go-install case.
	// *
	// * Backslashes are folded unconditionally rather than through
	// * filepath.ToSlash alone, which is a no-op off Windows. That makes the
	// * classification independent of the host it runs on — the rules can then be
	// * tested for every platform from one machine, instead of the Windows rows
	// * being asserted only by a CI job we do not run.
	p := strings.ReplaceAll(filepath.ToSlash(exe), `\`, "/")
	switch {
	case strings.Contains(p, "/Cellar/"), strings.Contains(p, "/Caskroom/"):
		// * Homebrew on Linux lands at /home/linuxbrew/.linuxbrew/{Cellar,Caskroom}/…
		// * and matches the same way, which is deliberate: the tap covers Linux too.
		// *
		// * Cellar is the formula layout (shipped up to v1.1.0), Caskroom the cask
		// * layout (v1.1.1 on). Both stay matched forever — a machine that
		// * installed the formula years ago still has a Cellar path, and
		// * `brew upgrade pulse` is the right answer for it either way.
		return MethodHomebrew
	case strings.Contains(p, "/go/bin/"):
		return MethodGoInstall
	default:
		return MethodBinary
	}
}

// Instruction is what to run instead, for the methods pulse refuses to handle
// itself. Empty for a plain binary, which pulse upgrades directly.
func (m Method) Instruction() string {
	switch m {
	case MethodHomebrew:
		return "brew upgrade pulse"
	case MethodGoInstall:
		return "go install github.com/ciphera-net/pulse-cli/cmd/pulse@latest"
	default:
		return ""
	}
}

// ExecutablePath resolves the running binary to a real file.
//
// EvalSymlinks matters twice over: /usr/local/bin/pulse is a symlink into the
// Cellar on a Homebrew install, so without it Detect sees a plain binary and
// happily replaces the SYMLINK with a regular file — and the same resolution is
// what makes the atomic rename land in the directory the file actually lives in.
func ExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not locate the running pulse binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("could not resolve %s: %w", exe, err)
	}
	return resolved, nil
}

// CheckWritable reports whether the running binary can be replaced, before
// anything is downloaded.
//
// The DIRECTORY is what gets tested, not the file: replacement is a rename, and
// on Linux opening a running executable for writing fails with ETXTBSY however
// permissive its mode is. Renaming into place is what avoids that, and renaming
// needs write permission on the directory.
//
// Never resolved by escalating. A tool that offers to re-run itself under sudo
// teaches its users to type sudo in front of a network-fed binary replacement,
// which is the last habit a signed-release story should be building.
func CheckWritable(target string) error {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".pulse-upgrade-probe-*")
	if err != nil {
		return fmt.Errorf("cannot replace %s: %s is not writable by this user (%w)\n\n"+
			"  Reinstall through a package manager, or install pulse somewhere you own\n"+
			"  (for example ~/.local/bin) and upgrade there. pulse will never use sudo.",
			target, dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Replace swaps new bytes in for the running binary, atomically.
//
// The temp file is created in the TARGET's directory, not os.TempDir: rename is
// only atomic within one filesystem, and /tmp is routinely a different one
// (tmpfs on Linux, a separate volume in a container). Across filesystems the
// rename fails outright — or, in an implementation that falls back to copying,
// leaves a half-written binary where a working one used to be.
//
// The caller verifies the signature before calling. Nothing here re-checks it,
// so nothing here may be called with unverified bytes.
func Replace(target string, binary []byte) (err error) {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".pulse-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot stage the new binary in %s: %w", dir, err)
	}
	staged := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(staged)
		}
	}()

	if _, err = tmp.Write(binary); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", staged, err)
	}
	// * Flushed to disk before it is renamed into place. Without it a crash
	// * between the rename and the flush can leave the target present, named
	// * correctly, and empty — an unbootable `pulse` with no old copy left.
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flushing %s: %w", staged, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", staged, err)
	}
	// * CreateTemp makes the file 0600. An executable nobody can execute is a
	// * broken install that only shows up on the next invocation.
	if err = os.Chmod(staged, 0o755); err != nil {
		return fmt.Errorf("making %s executable: %w", staged, err)
	}

	if runtime.GOOS == "windows" {
		return replaceWindows(target, staged)
	}
	if err = os.Rename(staged, target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}

// replaceWindows works around the one platform where a running executable
// cannot be overwritten.
//
// Windows holds an image lock on a running .exe, so the rename in Replace fails
// with ERROR_ACCESS_DENIED. Moving the file is allowed while it is locked, so
// the running binary is renamed aside first and the new one takes its name. The
// displaced file cannot be deleted until the process exits — it is left behind
// on purpose, and the next upgrade removes it.
func replaceWindows(target, staged string) error {
	old := target + ".old"
	os.Remove(old) // * left by a previous upgrade; failure here is not fatal.

	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("moving the running %s aside: %w", target, err)
	}
	if err := os.Rename(staged, target); err != nil {
		// * Put the working binary back. Leaving the machine with no pulse at
		// * all is a far worse outcome than a failed upgrade.
		if restoreErr := os.Rename(old, target); restoreErr != nil {
			return fmt.Errorf("replacing %s failed (%w) and the previous binary could not be "+
				"restored from %s (%v) — move it back by hand", target, err, old, restoreErr)
		}
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}
