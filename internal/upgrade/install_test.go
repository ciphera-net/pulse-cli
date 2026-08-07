package upgrade

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// * The detection that decides whether pulse may write to its own path.
// *
// * The Homebrew row is the one with teeth: the Cellar is writable by the user
// * who installed brew, so a self-replacing updater succeeds there and leaves
// * `brew list --versions pulse` describing a binary that is gone. Nothing
// * errors, and the next `brew upgrade` quietly reverts the user.
// *
// * Both Homebrew layouts are asserted. The tap published a FORMULA up to
// * v1.1.0 (Cellar) and publishes a CASK from v1.1.1 (Caskroom) — and the two
// * land in different directories, so a guard written against one stops firing
// * the day the other ships. The Caskroom paths below are not invented: they are
// * what `brew install --cask` actually produced on Homebrew 6.0.15 when the
// * generated cask was installed from a local tap, resolved through the
// * /opt/homebrew/bin symlink.
func TestDetectIdentifiesTheInstallMethod(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Method
	}{
		{
			name: "homebrew formula on apple silicon",
			path: "/opt/homebrew/Cellar/pulse/1.0.0/bin/pulse",
			want: MethodHomebrew,
		},
		{
			name: "homebrew formula on intel macos",
			path: "/usr/local/Cellar/pulse/1.0.0/bin/pulse",
			want: MethodHomebrew,
		},
		{
			name: "homebrew formula on linux",
			path: "/home/linuxbrew/.linuxbrew/Cellar/pulse/1.0.0/bin/pulse",
			want: MethodHomebrew,
		},
		{
			name: "homebrew cask on apple silicon",
			path: "/opt/homebrew/Caskroom/pulse/1.1.1/pulse",
			want: MethodHomebrew,
		},
		{
			name: "homebrew cask on intel macos",
			path: "/usr/local/Caskroom/pulse/1.1.1/pulse",
			want: MethodHomebrew,
		},
		{
			name: "homebrew cask on linux",
			path: "/home/linuxbrew/.linuxbrew/Caskroom/pulse/1.1.1/pulse",
			want: MethodHomebrew,
		},
		{
			name: "go install, default GOPATH",
			path: "/Users/usman/go/bin/pulse",
			want: MethodGoInstall,
		},
		{
			name: "go install on linux",
			path: "/root/go/bin/pulse",
			want: MethodGoInstall,
		},
		{
			name: "go install on windows",
			path: `C:\Users\usman\go\bin\pulse.exe`,
			want: MethodGoInstall,
		},
		{
			name: "a downloaded archive in /usr/local/bin",
			path: "/usr/local/bin/pulse",
			want: MethodBinary,
		},
		{
			name: "a downloaded archive under the user's home",
			path: "/home/usman/.local/bin/pulse",
			want: MethodBinary,
		},
		{
			name: "a build in the working tree",
			path: "/Users/usman/src/pulse-cli/pulse",
			want: MethodBinary,
		},
		{
			name: "no path at all",
			path: "",
			want: MethodUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Detect(c.path); got != c.want {
				t.Errorf("Detect(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// * Detection is only useful if it produces the command to run instead. A
// * managed install that prints "cannot upgrade" and stops is a dead end.
func TestInstructionNamesWhatToRunInstead(t *testing.T) {
	if got := MethodHomebrew.Instruction(); !strings.Contains(got, "brew upgrade") {
		t.Errorf("Homebrew instruction = %q, want it to name `brew upgrade`", got)
	}
	if got := MethodGoInstall.Instruction(); !strings.Contains(got, "go install github.com/ciphera-net/pulse-cli/cmd/pulse@latest") {
		t.Errorf("go-install instruction = %q, want the full module path", got)
	}
	if got := MethodBinary.Instruction(); got != "" {
		t.Errorf("plain binary instruction = %q, want empty — pulse upgrades this case itself", got)
	}
}

// * The replacement itself: the new bytes land, they are executable, and the
// * staging file does not survive.
func TestReplaceSwapsTheBinaryAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pulse")
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Replace(target, []byte("the new binary")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the replaced binary: %v", err)
	}
	if string(got) != "the new binary" {
		t.Errorf("content = %q, want the new binary", got)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		// * CreateTemp makes files 0600. Without the explicit chmod the upgrade
		// * "succeeds" and the next `pulse` is a permission denied.
		if perm := info.Mode().Perm(); perm != 0o755 {
			t.Errorf("mode = %v, want 0755 — the replaced binary must be executable", perm)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "pulse" {
			t.Errorf("left %q behind in the install directory", e.Name())
		}
	}
}

// * The staging file has to be created in the TARGET's directory. Across
// * filesystems os.Rename fails outright — /tmp is tmpfs on Linux and a separate
// * volume in most containers — so a temp file in os.TempDir turns every upgrade
// * on those machines into "invalid cross-device link".
func TestReplaceStagesInsideTheTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pulse")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// * A directory the process cannot write to has to fail BEFORE anything is
	// * renamed, which is what proves the staging file is created there and not
	// * somewhere else that happens to be writable.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not gate root, and do not work this way on Windows")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	err := Replace(target, []byte("new"))
	if err == nil {
		t.Fatal("Replace succeeded against a read-only directory; it is not staging there")
	}
	// * The failure has to be the STAGING step. If it were the rename, the temp
	// * file was created somewhere else — os.TempDir — and the upgrade would
	// * still fail on any machine where that is a different filesystem, which is
	// * every Linux box with a tmpfs /tmp and every container.
	if !strings.Contains(err.Error(), "stage the new binary in "+dir) {
		t.Errorf("Replace failed at the wrong step (%v); it is not staging in the install directory", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("a failed upgrade modified the existing binary: %q", got)
	}
}

// * Refusing early, and naming the path. The alternative is discovering the
// * directory is read-only after a 3 MB download — same outcome, worse
// * experience — or, worse, telling the user to re-run under sudo.
func TestCheckWritableRefusesAReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not gate root, and do not work this way on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "pulse")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	err := CheckWritable(target)
	if err == nil {
		t.Fatal("CheckWritable accepted a directory this user cannot write to")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("the refusal does not name the path: %q", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "sudo") &&
		!strings.Contains(err.Error(), "never use sudo") {
		t.Errorf("the refusal suggests sudo: %q", err)
	}
}

// * The happy path, and the check that CheckWritable does not leave probe files
// * scattered in people's bin directories.
func TestCheckWritableAcceptsAWritableDirectoryAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pulse")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(target); err != nil {
		t.Fatalf("CheckWritable refused a writable directory: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("CheckWritable left %d files behind, want only the binary", len(entries)-1)
	}
}

// * The resolution step Detect depends on. On a Homebrew install
// * /usr/local/bin/pulse is a SYMLINK into the Cellar (formula) or the Caskroom
// * (cask): without EvalSymlinks the path looks like a plain binary, detection
// * says "mine to replace", and the replacement overwrites the symlink with a
// * regular file.
// *
// * Both layouts are exercised because the link target is the ONLY thing that
// * differs between them — the symlink itself sits at the same
// * <prefix>/bin/pulse in either case, so the unresolved path is equally
// * uninformative and equally dangerous.
func TestExecutablePathResolvesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege this test should not assume on Windows")
	}
	layouts := []struct {
		name    string
		target  []string // path segments below the brew prefix
		explain string
	}{
		{
			name:    "formula, into the Cellar",
			target:  []string{"Cellar", "pulse", "1.0.0", "bin"},
			explain: "Cellar",
		},
		{
			name:    "cask, into the Caskroom",
			target:  []string{"Caskroom", "pulse", "1.1.1"},
			explain: "Caskroom",
		},
	}

	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			dir := t.TempDir()
			staged := filepath.Join(append([]string{dir}, l.target...)...)
			if err := os.MkdirAll(staged, 0o755); err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(staged, "pulse")
			if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "pulse")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}

			resolved, err := filepath.EvalSymlinks(link)
			if err != nil {
				t.Fatal(err)
			}
			if Detect(link) != MethodBinary {
				t.Fatal("precondition: the unresolved symlink should look like a plain binary")
			}
			if got := Detect(resolved); got != MethodHomebrew {
				t.Errorf("Detect(%q) = %q, want %q — resolving the symlink is what exposes the %s",
					resolved, got, MethodHomebrew, l.explain)
			}
		})
	}
}
