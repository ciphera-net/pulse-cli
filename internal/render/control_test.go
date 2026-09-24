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
// *
// * One case per class in pulse-backend's isInvisibleOrControl, so the two
// * cleaners cannot silently drift apart, plus U+2028/U+2029 (this renderer's
// * own addition — see isDangerousRune's doc comment for why the server does
// * not need them and this one does). isDangerousRune is now PROPERTY-based
// * (unicode.Cf / Other_Default_Ignorable_Code_Point / Variation_Selector)
// * rather than an enumerated range list, so these cases also cover members
// * of those properties the earlier range list never named at all: the soft
// * hyphen, the combining grapheme joiner, the Hangul filler characters, and
// * both variation-selector blocks.
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
		{"Arabic letter mark U+061C", "a" + string(rune(0x061c)) + "b", "a" + escapedForm(0x061c) + "b"},
		{"Mongolian vowel separator U+180E", "a" + string(rune(0x180e)) + "b", "a" + escapedForm(0x180e) + "b"},
		{"zero-width space U+200B", "a" + string(rune(0x200b)) + "b", "a" + escapedForm(0x200b) + "b"},
		{"ZWNJ U+200C", "a" + string(rune(0x200c)) + "b", "a" + escapedForm(0x200c) + "b"},
		{"left-to-right mark U+200E", "a" + string(rune(0x200e)) + "b", "a" + escapedForm(0x200e) + "b"},
		{"right-to-left mark U+200F", "a" + string(rune(0x200f)) + "b", "a" + escapedForm(0x200f) + "b"},
		{"LINE SEPARATOR U+2028", "a" + string(rune(0x2028)) + "b", "a" + escapedForm(0x2028) + "b"},
		{"PARAGRAPH SEPARATOR U+2029", "a" + string(rune(0x2029)) + "b", "a" + escapedForm(0x2029) + "b"},
		{"RLO bidi override", string(rune(0x202e)) + "reversed", escapedForm(0x202e) + "reversed"},
		{"word joiner U+2060", "a" + string(rune(0x2060)) + "b", "a" + escapedForm(0x2060) + "b"},
		{"invisible plus U+2064", "a" + string(rune(0x2064)) + "b", "a" + escapedForm(0x2064) + "b"},
		{"BOM U+FEFF", "a" + string(rune(0xfeff)) + "b", "a" + escapedForm(0xfeff) + "b"},
		{"PDI bidi isolate", string(rune(0x2069)) + "end", escapedForm(0x2069) + "end"},
		{"tag character U+E0001 (outside the BMP)",
			"a" + string(rune(0xe0001)) + "b", "a" + escapedForm(0xe0001) + "b"},
		{"soft hyphen U+00AD (Cf)", "co" + string(rune(0x00ad)) + "operate", "co" + escapedForm(0x00ad) + "operate"},
		{"combining grapheme joiner U+034F (Other_Default_Ignorable_Code_Point)",
			"a" + string(rune(0x034f)) + "b", "a" + escapedForm(0x034f) + "b"},
		{"Hangul filler U+3164 (Other_Default_Ignorable_Code_Point)",
			"a" + string(rune(0x3164)) + "b", "a" + escapedForm(0x3164) + "b"},
		{"halfwidth Hangul filler U+FFA0 (Other_Default_Ignorable_Code_Point)",
			"a" + string(rune(0xffa0)) + "b", "a" + escapedForm(0xffa0) + "b"},
		{"variation selector-16 U+FE0F (Variation_Selector)",
			"a" + string(rune(0xfe0f)) + "b", "a" + escapedForm(0xfe0f) + "b"},
		{"variation selector supplement U+E0100 (Variation_Selector)",
			"a" + string(rune(0xe0100)) + "b", "a" + escapedForm(0xe0100) + "b"},
		{"variation selector supplement U+E01EF (Variation_Selector)",
			"a" + string(rune(0xe01ef)) + "b", "a" + escapedForm(0xe01ef) + "b"},
		{"interlinear annotation anchor U+FFF9 (Cf)", "a" + string(rune(0xfff9)) + "b", "a" + escapedForm(0xfff9) + "b"},
		{"tag character U+E0041 (Cf, tag block)", "a" + string(rune(0xe0041)) + "b", "a" + escapedForm(0xe0041) + "b"},
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
	dangerous := []rune{
		0x1b, 0x07, 0x00, 0x7f, 0x90, 0x202e, 0x2066,
		0x061c, 0x180e, 0x200b, 0x2028, 0x2029, 0x2060, 0xfeff, 0xe0001,
		0x00ad, 0x034f, 0x3164, 0xffa0, 0xfe0f, 0xe0100, 0xe01ef, 0xfff9, 0xe0041,
	}
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
// * request would not stay on. "Zürich" and "naïve" exercise precomposed AND
// * combining accents (a combining mark is Unicode category M, not Cf/ODICP/
// * Variation_Selector, so it must not be caught by the property-based rule);
// * "日本語" is CJK; "emoji 😀" is outside the BMP but is Symbol (So), not a
// * format character.
func TestSanitizeControlLeavesOrdinaryTextUnchanged(t *testing.T) {
	for _, s := range []string{
		"/blog/où-nous-allons",
		"日本語のページ",
		"/pricing?plan=pro&ref=twitter",
		"",
		"plain ascii, spaces, punctuation!",
		"/pricing",
		"Zürich",
		"日本語",
		"naïve",
		"emoji 😀",
	} {
		if got := SanitizeControl(s); got != s {
			t.Errorf("SanitizeControl(%q) = %q, want it unchanged", s, got)
		}
	}
}

// * Precomposed vs. decomposed accents are a real-world variant of the same
// * "naïve" string above, worth pinning separately: a combining acute accent
// * (U+0301) is category Mn (Mark, nonspacing), which is not Cf, not
// * Other_Default_Ignorable_Code_Point, and not Variation_Selector, so it must
// * survive — it is not a variation selector just because it also modifies the
// * character before it.
func TestSanitizeControlLeavesDecomposedAccentsUnchanged(t *testing.T) {
	decomposed := "nai" + string(rune(0x0308)) + "ve" // n a i <combining diaeresis> v e
	if got := SanitizeControl(decomposed); got != decomposed {
		t.Errorf("SanitizeControl(%q) = %q, want it unchanged", decomposed, got)
	}
}

// * The specific behaviour the review named: an emoji followed by a variation
// * selector (VS16, U+FE0F — here selecting the emoji-style presentation of a
// * character that also has a text-style form) loses ONLY the selector, never
// * the base character it was modifying. This is what proves the rule reaches
// * into VS16 without over-reaching into the emoji itself, which is category
// * So (Symbol, other), not Cf/ODICP/Variation_Selector.
func TestSanitizeControlOnEmojiPlusVariationSelectorDropsOnlyTheSelector(t *testing.T) {
	emoji := "😀"
	in := emoji + string(rune(0xfe0f))
	want := emoji + escapedForm(0xfe0f)
	got := SanitizeControl(in)
	if got != want {
		t.Errorf("SanitizeControl(%q) = %q, want %q (emoji preserved, only VS16 escaped)", in, got, want)
	}
	if !strings.Contains(got, emoji) {
		t.Errorf("the base emoji was lost, not just the selector: %q", got)
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
