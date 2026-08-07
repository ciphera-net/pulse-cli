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
type Printer struct {
	Out   io.Writer
	Err   io.Writer
	Mode  Mode
	Color bool
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
	return &Printer{Out: os.Stdout, Err: os.Stderr, Mode: mode, Color: color}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// Printf writes data to stdout.
func (p *Printer) Printf(format string, args ...any) {
	fmt.Fprintf(p.Out, format, args...)
}

// Note writes context to stderr. Never to stdout: a note in a piped stream
// becomes a corrupt CSV row or a jq parse error.
func (p *Printer) Note(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s\n", p.dim(fmt.Sprintf(format, args...)))
}

// Warn writes a warning to stderr.
func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.yellow("!"), fmt.Sprintf(format, args...))
}

// Success writes a confirmation to stderr, so that even `pulse sites use`
// leaves stdout empty for anything that wants to capture it.
func (p *Printer) Success(format string, args ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.green("✓"), fmt.Sprintf(format, args...))
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
	fmt.Fprintln(p.Out, out)
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
