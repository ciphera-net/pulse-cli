package upgrade

import "testing"

// * The case that motivates having a comparator at all.
// *
// * As strings, "v1.10.0" < "v1.9.0" — so a CLI that compares tags as text stops
// * offering upgrades at the tenth minor release, forever, and reports itself as
// * current while it is nine releases behind. No error, no message.
func TestCompareOrdersNumericallyNotLexically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.10.0", "v1.9.0", 1},    // * the lexical trap
		{"v1.9.0", "v1.10.0", -1},   //
		{"v1.0.10", "v1.0.9", 1},    // * same trap, patch field
		{"v2.0.0", "v1.99.99", 1},   // * major dominates
		{"v1.1.0", "v1.0.99", 1},    // * minor dominates patch
		{"v1.0.0", "v1.0.0", 0},     // * equal
		{"1.0.0", "v1.0.0", 0},      // * the leading v is optional
		{"v1.2", "v1.2.0", 0},       // * a missing field is zero
		{"v1.0.0+abc", "v1.0.0", 0}, // * build metadata carries no ordering
		{"v1.0.1", "v1.0.0", 1},
	}
	for _, c := range cases {
		a, err := ParseVersion(c.a)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", c.a, err)
		}
		b, err := ParseVersion(c.b)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", c.b, err)
		}
		if got := Compare(a, b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// * Pre-releases exist in this repo's history (v1.0.0 took four tagging
// * attempts), and the rule that matters is counter-intuitive: v1.1.0 is NEWER
// * than v1.1.0-rc.1. Get it backwards and an rc tag permanently outranks the
// * release that followed it.
func TestCompareHandlesPreReleases(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.1.0", "v1.1.0-rc.1", 1},        // * a release beats its own rc
		{"v1.1.0-rc.1", "v1.1.0", -1},       //
		{"v1.1.0-rc.2", "v1.1.0-rc.1", 1},   // * ordered among themselves
		{"v1.1.0-rc.10", "v1.1.0-rc.9", 1},  // * numerically, not lexically
		{"v1.1.0-rc.1", "v1.1.0-rc.1", 0},   // * equal
		{"v1.1.0-alpha", "v1.1.0-beta", -1}, // * alphanumeric, ASCII order
		{"v1.1.0-rc.1", "v1.1.0-rc", 1},     // * more identifiers wins
		{"v1.1.0-1", "v1.1.0-alpha", -1},    // * numeric ranks below alphanumeric
		{"v1.1.0-rc.1", "v1.0.9", 1},        // * the numbers still decide first
	}
	for _, c := range cases {
		a, err := ParseVersion(c.a)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", c.a, err)
		}
		b, err := ParseVersion(c.b)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", c.b, err)
		}
		if got := Compare(a, b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVersionRefusesWhatItCannotOrder(t *testing.T) {
	for _, s := range []string{"", "dev", "v", "1.2.3.4", "v1.x.0", "v-1.0.0", "v1.0.0-"} {
		if v, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) = %+v, want an error — an unorderable version must not be "+
				"silently treated as 0.0.0", s, v)
		}
	}
}

// * Newer is the function the command actually branches on, and its two
// * unparseable cases point in OPPOSITE directions on purpose.
func TestNewer(t *testing.T) {
	cases := []struct {
		name            string
		latest, current string
		want            bool
	}{
		{"a newer release", "v1.1.0", "v1.0.0", true},
		{"the same release", "v1.0.0", "v1.0.0", false},
		{"an older release", "v1.0.0", "v1.1.0", false},
		{"the ten-versus-nine case", "v1.10.0", "v1.9.0", true},
		// * A development build is older than every release: someone running a
		// * binary built from a working tree should be told a release exists.
		{"a development build", "v1.0.0", "dev", true},
		// * A tag we cannot order is not an upgrade offer. Installing an
		// * artefact we cannot reason about is worse than saying nothing.
		{"an unreadable tag", "nightly", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("%s: Newer(%q, %q) = %v, want %v", c.name, c.latest, c.current, got, c.want)
		}
	}
}

func TestValid(t *testing.T) {
	if !Valid("v1.0.0") {
		t.Error("Valid(v1.0.0) = false")
	}
	if Valid("dev") {
		t.Error(`Valid("dev") = true — the CLI would print a development build as an ordered version`)
	}
}
