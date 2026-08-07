package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a released version, ordered the way semver orders them.
//
// String comparison is not an ordering: "v1.10.0" < "v1.9.0" lexically, so a
// CLI that compares tags as text stops offering upgrades at the tenth minor
// release and never says why. Hand-rolled because the alternative is a
// dependency, and this tool ships six of those on purpose.
type Version struct {
	Major, Minor, Patch int

	// Pre is the pre-release suffix without its leading '-', empty for a final
	// release. Build metadata (+sha) is parsed off and discarded: semver says it
	// carries no ordering, so two tags differing only there are the same version.
	Pre string
}

// ParseVersion reads a tag. The leading "v" is optional, a missing minor or
// patch is zero, and anything else is refused rather than guessed at — a
// version we cannot order is not a version we should compare.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = s[i+1:]
		s = s[:i]
		if v.Pre == "" {
			return Version{}, fmt.Errorf("empty pre-release in %q", s)
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("%q has %d numeric fields, want at most 3", s, len(parts))
	}
	out := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, part := range parts {
		n, err := parseNumber(part)
		if err != nil {
			return Version{}, err
		}
		*out[i] = n
	}
	return v, nil
}

// Valid reports whether a version string can be ordered. `pulse` built from a
// working tree carries version "dev", which cannot.
func Valid(s string) bool {
	_, err := ParseVersion(s)
	return err == nil
}

// Compare orders two versions: -1 if a sorts before b, 0 if they are equal, +1
// if a sorts after b. Both must parse; callers that may hold an unparseable
// version use Newer, which states what it does with one.
func Compare(a, b Version) int {
	for _, pair := range [][2]int{
		{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch},
	} {
		if c := cmpInt(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return comparePre(a.Pre, b.Pre)
}

// Newer reports whether the released tag is worth installing over what is
// running.
//
// An unparseable CURRENT version means "development build" — someone running
// `go run ./cmd/pulse`, where Version is the literal string "dev" — and every
// published release is newer than that. An unparseable LATEST is the opposite
// case: the API handed us something we cannot order, and offering to install an
// artefact we cannot reason about is worse than saying nothing.
func Newer(latest, current string) bool {
	l, err := ParseVersion(latest)
	if err != nil {
		return false
	}
	c, err := ParseVersion(current)
	if err != nil {
		return true
	}
	return Compare(l, c) > 0
}

// comparePre orders pre-release suffixes by semver §11.
//
// The rule that matters and is easiest to get wrong: a version WITHOUT a
// pre-release outranks the same version with one. v1.1.0 is newer than
// v1.1.0-rc.1, so an rc must not stop the release that follows it from being
// offered.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}

	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aNum := parseNumber(as[i])
		bn, bNum := parseNumber(bs[i])
		switch {
		case aNum == nil && bNum == nil:
			// * Both numeric: compare as numbers, so rc.10 follows rc.9.
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aNum == nil:
			// * Numeric identifiers always rank below alphanumeric ones.
			return -1
		case bNum == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	// * Everything shared is equal, so the one with more identifiers is larger:
	// * rc.1.1 > rc.1.
	return cmpInt(len(as), len(bs))
}

// parseNumber accepts only digits. strconv.Atoi would happily read "-1" and
// "+1", which are not version fields and would order in surprising places.
func parseNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty numeric field")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a number", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return n, nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
