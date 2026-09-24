package render

import (
	"strings"
	"unicode"
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

// isDangerousRune reports whether r is a control character or one Unicode
// marks as invisible, format, or a variation selector — something a terminal
// interprets, hides, reorders, or silently swallows rather than displaying
// plainly.
//
// This mirrors the rule pulse-backend's MCP cleaner
// (internal/api/mcp_clean.go, isInvisibleOrControl) uses, PROPERTY-based
// rather than an enumerated list of ranges, so the two cannot silently drift
// apart as Unicode adds members to a category neither side remembered to
// list by hand:
//
//   - C0 controls, DEL, C1 controls — the classic terminal-escape range.
//   - unicode.Cf (Cf: Other, format): soft hyphen (U+00AD), the Arabic letter
//     mark and Mongolian vowel separator, every zero-width space/joiner and
//     LRM/RLM, every bidi embedding/override/isolate, the word joiner and
//     invisible math operators, the BOM, the interlinear annotation
//     characters, and the tag characters (U+E0000 block) — invisible text
//     smuggling.
//   - unicode.Other_Default_Ignorable_Code_Point: characters Unicode says a
//     renderer may treat as invisible even though they are not formally
//     Cf — the combining grapheme joiner (U+034F), the Hangul filler
//     characters, and the rest of the "default ignorable" set.
//   - unicode.Variation_Selector: VS1-16 (U+FE00-FE0F) and the supplement
//     VS17-256 (U+E0100-E01EF), plus the Mongolian free variation selectors
//     — a known byte-smuggling channel (data hidden as a "rendering hint"
//     attached to the preceding character) and, incidentally, why an emoji
//     followed by VS16 shows as the emoji alone here: only the selector is
//     escaped, never the base character it was modifying.
//
// Plus one addition neither side needs to make from the same property, kept
// as an explicit case for exactly that reason: U+2028 and U+2029
// (LINE/PARAGRAPH SEPARATOR). Unicode does not put them in Cf, and the
// server's cleaner does not need them escaped — it collapses all whitespace,
// and those two code points are Unicode whitespace — but this renderer does
// not collapse whitespace, and a raw one of either would print as an actual
// line break in a terminal, which is exactly the "one logical row per line"
// break ESC already gets escaped for.
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
	case r == 0x2028, r == 0x2029: // LINE SEPARATOR, PARAGRAPH SEPARATOR
		return true
	case unicode.Is(unicode.Cf, r): // format characters (soft hyphen, bidi controls, ZW*, tags, ...)
		return true
	case unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r): // CGJ, Hangul fillers, ...
		return true
	case unicode.Is(unicode.Variation_Selector, r): // VS1-16, VS17-256, Mongolian FVS
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
// Rune-aware, so a multi-byte character never gets cut mid-sequence — and a
// dangerous rune is exactly one rune, so a cut at the boundary keeps it whole
// or drops it whole, never half of it. Callers do not need to sanitise before
// calling this: Table (table.go) sanitises every cell it is given, after
// truncation, which is the shared choke point now rather than a per-caller
// step — see Table's doc comment.
func Truncate(s string, max int) string {
	if max <= 1 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}
