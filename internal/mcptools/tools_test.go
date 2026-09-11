package mcptools_test

import (
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/mcptools"
)

// TestSiteIDMustBeAUUID is a security assertion as much as a correctness one.
//
// site_id is interpolated into a URL path. Anything that is not a UUID must be
// refused BEFORE a request is built — a traversal attempt should cost zero API
// calls and never reach the boundary at all.
//
// MUTATION: remove the check and this goes RED.
func TestSiteIDMustBeAUUID(t *testing.T) {
	rejected := []struct{ name, id string }{
		{"slug", "ciphera-net"},
		{"domain", "ciphera.net"},
		{"traversal", "../../internal/admin"},
		{"encoded traversal", "..%2f..%2fadmin"},
		{"empty", ""},
		{"uuid with trailing path", "0b57e0f2-2a51-4a9e-9a1e-1f5f4b0d9a11/../x"},
		{"too short", "0b57e0f2-2a51-4a9e-9a1e-1f5f4b0d9a1"},
		{"non-hex", "zzzzzzzz-2a51-4a9e-9a1e-1f5f4b0d9a11"},
		{"wrong separators", "0b57e0f2_2a51_4a9e_9a1e_1f5f4b0d9a11x"},
	}

	// A handler is never reached, so a nil client is safe here: if any of these
	// inputs got past validation the test would panic rather than pass, which
	// is the failure mode we want.
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			_, err := mcptools.GetRealtime(t.Context(), mcptools.Deps{},
				mcptools.SiteArgs{SiteID: c.id})
			if err == nil {
				t.Fatalf("%q was accepted as a site_id", c.id)
			}
			if !strings.Contains(err.Error(), "UUID") {
				t.Errorf("rejection does not say what is required: %v", err)
			}
			if !strings.Contains(err.Error(), "pulse_list_sites") {
				t.Errorf("rejection does not say where to get a valid id: %v", err)
			}
		})
	}
}

// TestExportsRequireExplicitDates — these endpoints take no relative period, so
// saying so locally beats letting the server answer 400 to a model that will
// then guess.
func TestExportsRequireExplicitDates(t *testing.T) {
	id := "0b57e0f2-2a51-4a9e-9a1e-1f5f4b0d9a11"
	for _, c := range []mcptools.ExportArgs{
		{SiteID: id},
		{SiteID: id, From: "2026-08-01"},
		{SiteID: id, To: "2026-08-07"},
	} {
		if _, err := mcptools.ExportDaily(t.Context(), mcptools.Deps{}, c); err == nil {
			t.Errorf("export accepted an incomplete range: %+v", c)
		}
	}
}

// TestStatsRejectsBothRangeForms mirrors the API, which answers 400 rather than
// letting one form silently win. A caller sending both has a bug, and answering
// one of them hides it behind plausible numbers.
func TestStatsRejectsBothRangeForms(t *testing.T) {
	_, err := mcptools.GetStats(t.Context(), mcptools.Deps{}, mcptools.StatsArgs{
		SiteID: "0b57e0f2-2a51-4a9e-9a1e-1f5f4b0d9a11",
		Period: "7d", From: "2026-08-01", To: "2026-08-07",
	})
	if err == nil {
		t.Fatal("both a period and explicit dates were accepted")
	}
	if !strings.Contains(err.Error(), "period") {
		t.Errorf("error does not name the conflicting parameters: %v", err)
	}
}

// TestEveryDefinitionIsUsable pins the registry itself. A tool with no
// description is invisible to a model in the way an undocumented flag is
// invisible to a person.
func TestEveryDefinitionIsUsable(t *testing.T) {
	if len(mcptools.Definitions) == 0 {
		t.Fatal("no tools defined")
	}
	seen := map[string]bool{}
	for _, d := range mcptools.Definitions {
		if seen[d.Name] {
			t.Errorf("duplicate tool name %q", d.Name)
		}
		seen[d.Name] = true

		if !strings.HasPrefix(d.Name, "pulse_") {
			t.Errorf("tool %q is not namespaced — it shares a name space with every other "+
				"server the host has loaded", d.Name)
		}
		if len(d.Description) < 40 {
			t.Errorf("tool %q has a description too short to guide a model: %q", d.Name, d.Description)
		}
		if _, ok := mcptools.Lookup(d.Name); !ok {
			t.Errorf("tool %q is not findable by name", d.Name)
		}
	}
}

// TestEveryToolIsReadOnlyForNow is the standing rule made mechanical.
//
// The API this server calls is aggregates-only, so every tool here reads. When
// a write tool is added this test must be changed DELIBERATELY, by someone who
// has read why the classification exists — which is the entire point of making
// it fail rather than leaving the rule in a document.
func TestEveryToolIsReadOnlyForNow(t *testing.T) {
	for _, d := range mcptools.Definitions {
		if !d.Class.ReadOnly() {
			t.Errorf("tool %q is not read-only. A write tool needs a blast-radius class and "+
				"a confirmation path before it ships, so changing this test is the deliberate "+
				"act that says both exist", d.Name)
		}
	}
}

// TestSuppressionIsExplainedInDescriptions — the payload carries the binding
// instruction, but a description is where a model decides whether to call a
// tool at all, and the floor is surprising enough to name up front.
func TestSuppressionIsExplainedInDescriptions(t *testing.T) {
	for _, name := range []string{"pulse_get_stats", "pulse_get_realtime",
		"pulse_export_daily", "pulse_export_pages"} {
		d, ok := mcptools.Lookup(name)
		if !ok {
			t.Fatalf("%s is not defined", name)
		}
		lower := strings.ToLower(d.Description)
		if !strings.Contains(lower, "withheld") && !strings.Contains(lower, "privacy floor") {
			t.Errorf("%s does not mention the privacy floor: %q", name, d.Description)
		}
	}
}

// TestDestructiveIsNotTheNegationOfReadOnly pins the distinction that a future
// write tool depends on.
//
// A class-1 tool writes — it creates a goal, connects an integration — and is
// explicitly recoverable by another call. Deriving destructiveHint as
// !ReadOnly() would annotate it identically to permanently deleting a site, and
// a host that shows that hint to a person would warn about both in the same
// words. This test is the reason that derivation cannot quietly regress.
func TestDestructiveIsNotTheNegationOfReadOnly(t *testing.T) {
	cases := []struct {
		class       mcptools.Class
		readOnly    bool
		destructive bool
	}{
		{mcptools.ClassRead, true, false},
		{mcptools.ClassReversible, false, false}, // writes, but is not destructive
		{mcptools.ClassDestructiveRecoverable, false, true},
		{mcptools.ClassIrreversible, false, true},
	}
	for _, c := range cases {
		if got := c.class.ReadOnly(); got != c.readOnly {
			t.Errorf("class %d ReadOnly()=%v, want %v", c.class, got, c.readOnly)
		}
		if got := c.class.Destructive(); got != c.destructive {
			t.Errorf("class %d Destructive()=%v, want %v", c.class, got, c.destructive)
		}
	}
	if mcptools.ClassReversible.Destructive() == !mcptools.ClassReversible.ReadOnly() {
		t.Error("Destructive() has collapsed back into !ReadOnly() — a reversible " +
			"write would now be annotated as destructive")
	}
}
