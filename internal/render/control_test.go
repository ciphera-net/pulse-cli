package render

import (
	"fmt"
	"strings"
	"testing"
)

// escapedForm is the independent oracle TestSanitizeControlEscapesEverythingDangerous
// checks SanitizeControl against: built with plain hex formatting rather than
// by calling the package's own escapeRune, so a bug in that function cannot
// make this test agree with it by construction.
func escapedForm(r rune) string {
	if r > 0xffff {
		return fmt.Sprintf("\\U%08X", r)
	}
	return fmt.Sprintf("\\u%04X", r)
}

// * `breakdown` is the one endpoint with no privacy floor, so its VALUES —
// * page paths, referrers, UTM parameters, language tags — are visitor-supplied
// * strings that reach the terminal with nothing upstream having filtered them.
// * Every C0/C1 control character, ESC, and every bidi override/isolate must be
// * escaped, never written raw — a raw ESC is a terminal escape-sequence
// * injection and a raw bidi override can make the printed line read backwards.
func TestSanitizeControlEscapesEverythingDangerous(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ESC", "/pricing" + string(rune(0x1b)) + "]0;pwned" + string(rune(0x07)),
			"/pricing" + escapedForm(0x1b) + "]0;pwned" + escapedForm(0x07)},
		{"NUL", "a" + string(rune(0x00)) + "b", "a" + escapedForm(0x00) + "b"},
		{"DEL", "a" + string(rune(0x7f)) + "b", "a" + escapedForm(0x7f) + "b"},
		{"C1 control", "a" + string(rune(0x85)) + "b", "a" + escapedForm(0x85) + "b"},
		{"tab and newline", "a\tb\nc\rd",
			"a" + escapedForm('\t') + "b" + escapedForm('\n') + "c" + escapedForm('\r') + "d"},
		{"RLO bidi override", string(rune(0x202e)) + "reversed", escapedForm(0x202e) + "reversed"},
		{"PDI bidi isolate", string(rune(0x2069)) + "end", escapedForm(0x2069) + "end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeControl(tc.in)
			if got != tc.want {
				t.Errorf("SanitizeControl(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// * Every dangerous byte named above must be gone, individually, from the
// * output — not merely absent in aggregate. A test that only checks the whole
// * string against a fixture can pass by accident if the escaping logic drops a
// * byte instead of escaping it.
func TestSanitizeControlLeavesNoRawDangerousByte(t *testing.T) {
	dangerous := []rune{0x1b, 0x07, 0x00, 0x7f, 0x90, 0x202e, 0x2066}
	var sb strings.Builder
	sb.WriteString("/a")
	for _, r := range dangerous {
		sb.WriteRune(r)
	}
	sb.WriteString("b")

	got := SanitizeControl(sb.String())
	for _, r := range dangerous {
		if strings.ContainsRune(got, r) {
			t.Errorf("SanitizeControl left raw U+%04X in the output: %q", r, got)
		}
	}
	if !strings.HasPrefix(got, "/a") || !strings.HasSuffix(got, "b") {
		t.Errorf("ordinary text around the dangerous runes was not preserved: %q", got)
	}
}

// * The complement: ordinary text, including non-ASCII, must survive untouched
// * — a security control that mangles a French or Japanese page title on every
// * request would not stay on.
func TestSanitizeControlLeavesOrdinaryTextUnchanged(t *testing.T) {
	for _, s := range []string{
		"/blog/où-nous-allons",
		"日本語のページ",
		"/pricing?plan=pro&ref=twitter",
		"",
		"plain ascii, spaces, punctuation!",
	} {
		if got := SanitizeControl(s); got != s {
			t.Errorf("SanitizeControl(%q) = %q, want it unchanged", s, got)
		}
	}
}

// * The no-op fast path must not allocate a different string identity in a way
// * that breaks equality — a cheap sanity check that ContainsFunc actually
// * gates the rewrite.
func TestSanitizeControlIsNoOpWhenNothingIsDangerous(t *testing.T) {
	s := "/perfectly/ordinary/path"
	if got := SanitizeControl(s); got != s {
		t.Errorf("clean input was altered: %q -> %q", s, got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 80); got != "short" {
		t.Errorf("a string under the limit must be unchanged, got %q", got)
	}
	if got := Truncate("exactly10!", 10); got != "exactly10!" {
		t.Errorf("a string exactly at the limit must be unchanged, got %q", got)
	}

	long := strings.Repeat("a", 100)
	got := Truncate(long, 80)
	if got != strings.Repeat("a", 79)+"…" {
		t.Errorf("Truncate did not cut to 79 runes plus an ellipsis: len=%d, got %q", len([]rune(got)), got)
	}
	if n := len([]rune(got)); n != 80 {
		t.Errorf("truncated length = %d runes, want 80", n)
	}

	// * Rune-safe: cutting mid multi-byte character would produce invalid
	// * UTF-8 or a mangled glyph. Each of these runes is 3 bytes.
	multibyte := strings.Repeat("日", 50)
	got = Truncate(multibyte, 10)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated multibyte string has no ellipsis: %q", got)
	}
	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncated multibyte length = %d runes, want 10", n)
	}
	for _, r := range got {
		if r != '日' && r != '…' {
			t.Fatalf("truncation produced a corrupted rune: %q", got)
		}
	}
}
