package render

import (
	"strings"
	"unicode/utf8"
)

// SanitizeControl escapes runes a terminal could act on rather than display, so
// a value we did not choose can never reach the terminal raw.
//
// Every string that flows through this — a page path, a referrer, a UTM value,
// a language tag — is supplied by a visitor's browser, and `breakdown` is the
// one endpoint with no privacy floor: nothing upstream filters it before it
// reaches this process. A path of "/\x1b]0;pwned\x07" is a terminal
// escape-sequence injection, not a hypothetical one, and a bidi override
// character can make a value read backwards or hide part of it — printing
// either raw hands a visitor's browser a small amount of control over the
// operator's terminal.
//
// Escaped as \uXXXX, never stripped: stripping silently changes what the value
// WAS, which is the wrong trade for a security control whose output someone may
// paste into a bug report or a support ticket. \uXXXX is unambiguous, greppable,
// and the same notation Go and JSON already use for an unprintable rune.
func SanitizeControl(s string) string {
	if !strings.ContainsFunc(s, isDangerousRune) {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if isDangerousRune(r) {
			sb.WriteString(escapeRune(r))
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// isDangerousRune reports whether r is a control character or a bidi override
// / isolate — something a terminal interprets rather than displays.
//
// The C0 range (0x00-0x1F) is escaped WHOLE, including \t and \n: this
// renderer's contract is one logical row per line, in every mode, and a raw
// newline or tab inside a value breaks that as surely as ESC does — the two
// concerns share one fix.
func isDangerousRune(r rune) bool {
	switch {
	case r <= 0x1f: // C0 controls, including ESC (0x1b)
		return true
	case r == 0x7f: // DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}

const hexDigits = "0123456789ABCDEF"

// escapeRune renders r as \uXXXX (or, for a rune outside the Basic Multilingual
// Plane, a \UXXXXXXXX of eight digits) — Go source-string notation, so the same
// bytes are unambiguous whether they end up in a terminal, a CSV cell, or pasted
// into a report.
func escapeRune(r rune) string {
	if r > 0xffff {
		b := [10]byte{'\\', 'U', '0', '0', '0', '0', '0', '0', '0', '0'}
		for i := 9; i >= 2; i-- {
			b[i] = hexDigits[r&0xf]
			r >>= 4
		}
		return string(b[:])
	}
	b := [6]byte{'\\', 'u', '0', '0', '0', '0'}
	for i := 5; i >= 2; i-- {
		b[i] = hexDigits[r&0xf]
		r >>= 4
	}
	return string(b[:])
}

// Truncate shortens s to at most max runes, replacing the tail with an ellipsis
// when it does not fit.
//
// Rune-aware, so a multi-byte character never gets cut mid-sequence. Callers
// that also need SanitizeControl should sanitise FIRST and truncate the
// result: truncating first could cut a raw multi-byte escape target in half,
// and the point of a length bound here is the DISPLAYED width, which is the
// sanitised string's.
func Truncate(s string, max int) string {
	if max <= 1 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}
