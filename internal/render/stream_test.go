package render

import (
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

// failingWriter refuses every write, the way a full disk (ENOSPC) or a
// `ulimit -f` file-size cap (EFBIG) does.
type failingWriter struct {
	err    error
	writes int
}

func (w *failingWriter) Write(b []byte) (int, error) {
	w.writes++
	return 0, w.err
}

// shortWriter accepts the write and reports success, but keeps only some of the
// bytes — output lost without an error to show for it.
type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return len(b) - 1, nil
}

// * The whole point of the type: a write that failed is still a failure after
// * the function that made it has returned.
// *
// * `pulse export daily > week.csv` under a file-size limit wrote 1,024 bytes,
// * dropped the EFBIG on the floor and exited 0 — so the script that ran it
// * kept a CSV ending mid-row and never learned. The error has to survive as
// * far as the exit code, and it has to name what broke.
func TestAFailedWriteSurvivesAsAnError(t *testing.T) {
	out := &failingWriter{err: syscall.ENOSPC}
	p := NewPrinterTo(out, &failingWriter{err: syscall.ENOSPC}, ModeCSV)

	p.Raw([]byte("date,visitors\n2026-08-01,1284\n"))

	err := p.WriteError()
	if err == nil {
		t.Fatal("a write that failed with ENOSPC was reported as complete output")
	}
	if !strings.Contains(err.Error(), "stdout") {
		t.Errorf("the failure does not name the stream that broke: %q", err)
	}
	if !strings.Contains(err.Error(), syscall.ENOSPC.Error()) {
		t.Errorf("the failure does not name the reason: %q", err)
	}
}

// * Every method writes through the same two streams, so none of them can be
// * the one that forgets. Checked one at a time because a printer that only
// * notices failures on stdout would exit 0 on a lost warning.
func TestEveryPrinterMethodIsChecked(t *testing.T) {
	toStdout := map[string]func(*Printer){
		"Printf":     func(p *Printer) { p.Printf("visitors: %d\n", 1284) },
		"JSON":       func(p *Printer) { p.JSON([]byte(`{"data":{}}`)) },
		"Raw":        func(p *Printer) { p.Raw([]byte("date,visitors\n")) },
		"Table":      func(p *Printer) { p.Table(Table{Headers: []string{"A"}, Rows: [][]string{{"1"}}}) },
		"KeyValue":   func(p *Printer) { p.KeyValue([][2]string{{"Visitors", "1,284"}}) },
		"CSVRecords": func(p *Printer) { _ = p.CSVRecords([]string{"a"}, [][]string{{"1"}}) },
	}
	for name, write := range toStdout {
		for _, mode := range []Mode{ModeTable, ModeCSV} {
			p := NewPrinterTo(&failingWriter{err: syscall.ENOSPC}, &failingWriter{err: syscall.ENOSPC}, mode)
			write(p)
			if err := p.WriteError(); err == nil {
				t.Errorf("%s in %s mode lost its output silently", name, mode)
			}
		}
	}

	toStderr := map[string]func(*Printer){
		"Note":    func(p *Printer) { p.Note("resolved 7d in the site's timezone") },
		"Warn":    func(p *Printer) { p.Warn("quota is nearly exhausted") },
		"Success": func(p *Printer) { p.Success("default site set") },
	}
	for name, write := range toStderr {
		p := NewPrinterTo(&failingWriter{err: syscall.ENOSPC}, &failingWriter{err: syscall.ENOSPC}, ModeTable)
		write(p)
		if err := p.WriteError(); err == nil {
			t.Errorf("%s lost its output silently", name)
		}
	}
}

// * A closed pipe is the pipeline working.
// *
// * `pulse sites ls | head -3` closes the pipe as soon as head has its three
// * lines. If that became a non-zero exit, piping into head, less or `grep -q`
// * would start failing for everyone using a CLI whose v1.0.0 behaviour is
// * already published — a far worse regression than the bug being fixed.
func TestABrokenPipeIsNotAFailure(t *testing.T) {
	// * EPIPE bare, EPIPE wrapped the way os.File wraps it, and the two
	// * Windows errnos, which are not EPIPE and only reach us as text.
	for _, cause := range []error{
		syscall.EPIPE,
		&fs.PathError{Op: "write", Path: "/dev/stdout", Err: syscall.EPIPE},
		errString("write /dev/stdout: The pipe has been ended."),
		errString("write /dev/stdout: The pipe is being closed."),
	} {
		p := NewPrinterTo(&failingWriter{err: cause}, &failingWriter{err: cause}, ModeTable)
		p.Printf("ciphera-net  ciphera.net  UTC\n")

		if err := p.WriteError(); err != nil {
			t.Errorf("a closed pipe was reported as a failure (%v): %v", cause, err)
		}
	}
}

// errString is an error that is nothing but its message — the shape a Windows
// errno reaches this package in on a build that cannot name it.
type errString string

func (e errString) Error() string { return string(e) }

// * Once a stream has refused, there is nothing to gain from pushing the rest of
// * a 98-day export at it — and under `ulimit -f` every further write raises
// * SIGXFSZ again.
func TestAFailedStreamStopsWriting(t *testing.T) {
	out := &failingWriter{err: syscall.EFBIG}
	p := NewPrinterTo(out, &failingWriter{err: syscall.EFBIG}, ModeTable)

	for i := 0; i < 5; i++ {
		p.Printf("row %d\n", i)
	}

	if out.writes != 1 {
		t.Errorf("kept writing to a stream that had already failed: %d writes", out.writes)
	}
}

// * A short write with no error loses bytes just as completely as a refused one,
// * and is the failure mode a caller is least likely to imagine.
func TestAShortWriteIsAFailure(t *testing.T) {
	p := NewPrinterTo(shortWriter{}, shortWriter{}, ModeTable)
	p.Printf("date,visitors\n")

	if err := p.WriteError(); err == nil {
		t.Fatal("output was truncated by a short write and reported as complete")
	}
}

// * The complement to all of the above: output that arrives must not be
// * reported as broken, or every successful command exits 1.
func TestCompleteOutputIsNotAFailure(t *testing.T) {
	p, _, _ := testPrinter(ModeTable)
	p.Printf("visitors: %d\n", 1284)
	p.Note("resolved 7d in the site's timezone")

	if err := p.WriteError(); err != nil {
		t.Errorf("output that was written in full was reported as a failure: %v", err)
	}
}
