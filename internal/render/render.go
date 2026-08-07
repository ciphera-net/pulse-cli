// Package render turns API responses into terminal output.
//
// One rule dominates everything here: a withheld metric is never printed as a
// number. See suppress.go.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Mode is the output format.
type Mode string

const (
	ModeTable Mode = "table"
	ModeJSON  Mode = "json"
	ModeCSV   Mode = "csv"
)

// Printer writes command output.
//
// Out is the data stream and Err is everything else: colour, progress, warnings
// and the resolved-range note. The split is what makes `pulse stats --json | jq`
// always clean — anything that is not the answer goes to stderr, so a pipe
// receives bytes and a human still gets context.
//
// Both streams are checked (see stream.go), which is why none of the methods
// below return an error: a failed write is remembered on the stream and read
// once, by WriteError, before the process exits. A Printer must therefore be
// built by NewPrinter or NewPrinterTo — a zero Printer has no streams to check.
type Printer struct {
	Mode  Mode
	Color bool

	out *stream
	err *stream
}

// NewPrinter builds a printer for the real process.
//
// Colour follows stdout, not stderr: the question colour answers is "will the
// thing reading my output render escape codes", and the thing reading it is
// whatever stdout points at. NO_COLOR is honoured because it is the convention
// users already have configured.
func NewPrinter(mode Mode) *Printer {
	color := isTerminal(os.Stdout)
	if _, set := os.LookupEnv("NO_COLOR"); set {
		color = false
	}
	if os.Getenv("TERM") == "dumb" {
		color = false
	}
	p := NewPrinterTo(os.Stdout, os.Stderr, mode)
	p.Color = color
	return p
}

// NewPrinterTo builds a printer over streams the caller supplies, without
// colour: the shape a test needs, and the one thing that guarantees a Printer
// is never assembled field by field with unchecked writers.
func NewPrinterTo(out, err io.Writer, mode Mode) *Printer {
	return &Printer{
		Mode: mode,
		out:  &stream{name: "stdout", w: out},
		err:  &stream{name: "stderr", w: err},
	}
}

// Out is the data stream, for the few callers that need to write bytes we did
// not format — cobra's own help output, and nothing else so far.
func (p *Printer) Out() io.Writer { return p.out }

// Err is the stream everything that is not the answer goes to.
func (p *Printer) Err() io.Writer { return p.err }

// WriteError reports the first write that failed, naming the stream, or nil
// when every byte this printer was given reached its destination.
//
// stdout is checked before stderr: if both broke, the lost answer is the one
// worth naming. A broken pipe is deliberately not reported — see isBrokenPipe.
func (p *Printer) WriteError() error {
	if err := p.out.failure(); err != nil {
		return err
	}
	return p.err.failure()
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// Printf writes data to stdout.
func (p *Printer) Printf(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
}

// Note writes context to stderr. Never to stdout: a note in a piped stream
// becomes a corrupt CSV row or a jq parse error.
func (p *Printer) Note(format string, args ...any) {
	fmt.Fprintf(p.err, "%s\n", p.dim(fmt.Sprintf(format, args...)))
}

// Warn writes a warning to stderr.
func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.yellow("!"), fmt.Sprintf(format, args...))
}

// Success writes a confirmation to stderr, so that even `pulse sites use`
// leaves stdout empty for anything that wants to capture it.
func (p *Printer) Success(format string, args ...any) {
	fmt.Fprintf(p.err, "%s %s\n", p.green("✓"), fmt.Sprintf(format, args...))
}

// JSON writes an API response body through unmodified.
//
// The raw bytes, not a re-marshalling of the decoded struct. --json promises
// "exactly the API response", which makes the CLI a debugging tool for the API
// and stops it from becoming a second, subtly different contract. Re-encoding
// would reorder keys and silently drop any field this build does not know about
// — and v1 is additive, so an old CLI meeting a new field is the expected case,
// not the exceptional one.
func (p *Printer) JSON(raw []byte) {
	out := strings.TrimRight(string(raw), "\n")
	fmt.Fprintln(p.out, out)
}

// Raw writes a body to stdout byte for byte, adding nothing — the bulk exports,
// whose CSV is rendered server-side.
func (p *Printer) Raw(b []byte) {
	_, _ = p.out.Write(b)
}

// ANSI helpers. Hand-rolled rather than pulled in: three escape sequences do not
// justify a dependency in a binary people install to read four numbers.
func (p *Printer) wrap(code, s string) string {
	if !p.Color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p *Printer) dim(s string) string    { return p.wrap("2", s) }
func (p *Printer) bold(s string) string   { return p.wrap("1", s) }
func (p *Printer) green(s string) string  { return p.wrap("32", s) }
func (p *Printer) yellow(s string) string { return p.wrap("33", s) }

// Dim exposes dimming for callers assembling their own lines.
func (p *Printer) Dim(s string) string { return p.dim(s) }

// Bold exposes emphasis for callers assembling their own lines.
func (p *Printer) Bold(s string) string { return p.bold(s) }
