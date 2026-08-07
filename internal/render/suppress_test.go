package render

import (
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-api-go/publicv1"
)

// * The single easiest thing to get wrong in a Pulse client.
// *
// * A suppressed response returns null for EVERY metric, and a genuine zero is
// * suppressed too — so nil means "fewer than five, possibly none" and the
// * caller cannot tell which. Printing 0 publishes a number the server
// * deliberately refused to state, and Go makes it the path of least
// * resistance: any accidental dereference-with-default yields exactly 0.
// *
// * Asserted as "the output contains no digit at all" rather than "the output
// * is not 0", because 0, 0.0, 0%, and 0s are four different ways to make the
// * same claim.
func TestWithheldMetricsNeverRenderAsANumber(t *testing.T) {
	cases := map[string]string{
		"visitors":     Metric(nil),
		"pageviews":    Metric(nil),
		"bounce rate":  Percent(nil),
		"avg duration": Duration(nil),
	}

	for name, got := range cases {
		if got != Withheld {
			t.Errorf("withheld %s rendered as %q, want %q", name, got, Withheld)
		}
		if strings.ContainsAny(got, "0123456789") {
			t.Errorf("withheld %s rendered %q, which contains a digit — a withheld value must never look like a measurement", name, got)
		}
	}
}

// * The complement, and it matters just as much: a real zero must still print
// * as zero. If every falsy value rendered as "—" the CLI would report a
// * privacy refusal for every quiet period, and users would stop believing the
// * marker on the occasions it is real.
func TestRealZeroStillRendersAsZero(t *testing.T) {
	zero := 0
	if got := Metric(&zero); got != "0" {
		t.Errorf("a real zero rendered as %q, want \"0\" — zero and withheld are different facts", got)
	}
	zeroF := 0.0
	if got := Percent(&zeroF); got != "0.0%" {
		t.Errorf("a real 0%% rendered as %q", got)
	}
}

// * The explanation has to come from the server's meta.min_cell_size, not from
// * publicv1.MinCellSize. Both are 5 today, so a client that hardcodes the
// * constant looks correct — right up until the server raises the floor, at
// * which point it confidently explains the wrong threshold to every user. The
// * whole reason min_cell_size travels on the wire is so a client can describe
// * the gap without knowing our policy.
func TestSuppressionNoteUsesTheServersThreshold(t *testing.T) {
	note := SuppressionNote(publicv1.Meta{Suppressed: true, MinCellSize: 25})

	if !strings.Contains(note, "25") {
		t.Errorf("note did not use the server's min_cell_size of 25: %q", note)
	}
	if strings.Contains(note, " 5 ") || strings.Contains(note, "than 5") {
		t.Errorf("note quoted the hardcoded default instead of the server's figure: %q", note)
	}
}

// * A response that withheld nothing must produce no note at all. A permanent
// * privacy footer is noise, and noise is how a real warning gets ignored.
func TestNoNoteWhenNothingWasSuppressed(t *testing.T) {
	if note := SuppressionNote(publicv1.Meta{Suppressed: false}); note != "" {
		t.Errorf("unsuppressed response produced a note: %q", note)
	}
}

// * A row-shaped result reports how many rows went and what they summed to, and
// * the note must say so. Without it a user sees visible rows that do not add
// * up to the total, reports it as a bug, and no answer is satisfying unless the
// * response said so itself.
func TestRowSuppressionReportsTheWithheldRemainder(t *testing.T) {
	rows, total := 14, 31
	note := SuppressionNote(publicv1.Meta{
		Suppressed:      true,
		MinCellSize:     5,
		SuppressedRows:  &rows,
		SuppressedTotal: &total,
	})

	for _, want := range []string{"14", "31"} {
		if !strings.Contains(note, want) {
			t.Errorf("row-suppression note is missing %s — the numbers must reconcile: %q", want, note)
		}
	}
}

// * "fewer than five, POSSIBLY NONE" is the honest reading, and the note has to
// * carry it. A message that says only "withheld" invites the reader to assume
// * there is something behind it, which is exactly the inference the floor
// * refuses to support — an empty slice and a one-visitor slice return
// * byte-identical bodies.
func TestSuppressionNoteDoesNotImplyDataExists(t *testing.T) {
	note := strings.ToLower(SuppressionNote(publicv1.Meta{Suppressed: true, MinCellSize: 5}))

	if !strings.Contains(note, "possibly none") {
		t.Errorf("note must say the slice may be empty, not merely withheld: %q", note)
	}
	if !strings.Contains(note, "not mean zero") && !strings.Contains(note, "does not mean zero") {
		t.Errorf("note must say it is not zero either: %q", note)
	}
}

// * The export floor is applied to VISITORS but reports withheld PAGEVIEWS —
// * nine pageviews can be one person reloading, so a pageview-based floor would
// * pass exactly the row it exists to withhold. The note has to name the right
// * quantity or it teaches the reader the wrong rule.
func TestExportNoteNamesVisitorsAsTheThresholdAndPageviewsAsTheTotal(t *testing.T) {
	note := ExportSuppressionNote(9, 21, 5)

	if !strings.Contains(note, "5 visitors") {
		t.Errorf("export note must state the threshold in visitors: %q", note)
	}
	if !strings.Contains(note, "21 pageviews") {
		t.Errorf("export note must state the withheld total in pageviews: %q", note)
	}
	if ExportSuppressionNote(0, 0, 5) != "" {
		t.Error("an export that withheld nothing must produce no note")
	}
}
