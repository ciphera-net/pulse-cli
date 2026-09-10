package cli

import (
	"bytes"
	"strings"
	"syscall"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/client"
)

// failingWriter refuses every write, the way a full disk (ENOSPC) or a
// `ulimit -f` file-size cap (EFBIG) does.
type failingWriter struct{ err error }

func (w failingWriter) Write(b []byte) (int, error) { return 0, w.err }

// * A command that produced truncated output did not succeed.
// *
// * Measured before the fix: `ulimit -f 1; pulse export daily --from 2026-01-01
// * --to 2026-08-06 > out.csv` exited 0 with a file cut off at 1,024 bytes. A
// * script branching on `$?` kept the broken file and moved on, which is the
// * worst possible outcome for a tool whose entire job is producing data other
// * programs read.
func TestFailedOutputExitsNonZeroAndSaysWhy(t *testing.T) {
	errBuf := &bytes.Buffer{}
	app := &App{
		Printer: render.NewPrinterTo(failingWriter{err: syscall.EFBIG}, errBuf, render.ModeCSV),
		started: true,
	}

	app.Printer.Raw([]byte("date,visitors\n2026-08-01,1284\n"))

	// * nil: the command itself succeeded. Only the output failed, which is
	// * exactly the case that used to exit 0.
	code := finish(app, nil)

	if code == client.ExitOK {
		t.Fatal("exited 0 after stdout refused the export; a script cannot tell the file is truncated")
	}
	if code != client.ExitServerError {
		t.Errorf("exit code = %d, want %d — a failed write is an unexpected local failure",
			code, client.ExitServerError)
	}
	if !strings.Contains(errBuf.String(), "stdout") {
		t.Errorf("the failure does not name the stream that broke: %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), syscall.EFBIG.Error()) {
		t.Errorf("the failure does not name the reason: %q", errBuf.String())
	}
}

// * The other half, and the one that makes the first half safe to ship.
// *
// * `pulse sites ls | head -3` closes the pipe the moment head has what it
// * wants, and every remaining write fails with EPIPE. That is a pipeline
// * working. Turning it into a non-zero exit would break piping into head, less
// * and `grep -q` for every user of a released, brew-installed CLI — so it stays
// * exit 0, and it stays silent: an error message about a closed pipe is noise
// * printed at someone who did nothing wrong.
func TestBrokenPipeStaysExitZeroAndSilent(t *testing.T) {
	errBuf := &bytes.Buffer{}
	app := &App{
		Printer: render.NewPrinterTo(failingWriter{err: syscall.EPIPE}, errBuf, render.ModeTable),
		started: true,
	}

	app.Printer.Printf("ciphera-net  ciphera.net  UTC  2 min ago\n")

	if code := finish(app, nil); code != client.ExitOK {
		t.Fatalf("`| head` broke the pipe and the CLI exited %d, want %d", code, client.ExitOK)
	}
	if errBuf.Len() != 0 {
		t.Errorf("a closed pipe printed a complaint: %q", errBuf.String())
	}
}

// * A command that failed on its own terms keeps its own exit code. The write
// * check must not overwrite exit 3 with exit 1 just because the error message
// * it was printing also failed to land — a script keyed on 3 to re-run
// * `pulse auth login` would stop working.
func TestCommandErrorKeepsItsExitCode(t *testing.T) {
	app := &App{
		Printer: render.NewPrinterTo(failingWriter{err: syscall.ENOSPC}, failingWriter{err: syscall.ENOSPC},
			render.ModeTable),
		started: true,
	}

	if code := finish(app, authErr("not authenticated")); code != client.ExitUnauthorized {
		t.Errorf("exit code = %d, want %d", code, client.ExitUnauthorized)
	}
}
