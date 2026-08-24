package render

import (
	"fmt"

	"github.com/ciphera-net/pulse-api-go/publicv1"
)

// Withheld is what a suppressed value prints as.
//
// An em dash, not "0", not "-", not an empty cell. Zero is a claim the API did
// not make; an empty cell reads as a rendering bug and gets reported as one.
const Withheld = "—"

// Metric renders one possibly-withheld number.
//
// EVERY metric on a suppressed /stats response is null, including a genuine
// zero — the API withholds a true zero on purpose, because if `suppressed` meant
// "1 to 4" while a real zero came back as 0, walking a dimension's values would
// reveal exactly which of them have a live cohort. That is an existence oracle
// wearing a privacy floor's clothing.
//
// So nil means "fewer than five, possibly none", and the caller cannot tell
// which. Printing 0 would publish a number the server deliberately refused to
// state, and it is the single easiest thing to get wrong in a client: Go's zero
// value for a dereferenced-then-defaulted *int is exactly the wrong answer.
func Metric(v *int) string {
	if v == nil {
		return Withheld
	}
	return Thousands(*v)
}

// Percent renders a possibly-withheld percentage.
func Percent(v *float64) string {
	if v == nil {
		return Withheld
	}
	return fmt.Sprintf("%.1f%%", *v)
}

// Duration renders a possibly-withheld duration in seconds.
func Duration(v *float64) string {
	if v == nil {
		return Withheld
	}
	return HumanDuration(*v)
}

// SuppressionNote explains a suppressed response in one line, or returns "" when
// nothing was withheld.
//
// The threshold comes from meta.min_cell_size — the server's own figure —
// rather than from publicv1.MinCellSize. Both are 5 today, but a client that
// hardcodes the number describes the wrong floor the moment the server raises
// it, and the whole reason min_cell_size is on the wire is so a client can
// explain the gap without knowing our policy.
func SuppressionNote(m publicv1.Meta) string {
	if !m.Suppressed {
		return ""
	}
	threshold := m.MinCellSize
	if threshold == 0 {
		threshold = publicv1.MinCellSize
	}

	if m.SuppressedRows != nil && *m.SuppressedRows > 0 {
		total := 0
		if m.SuppressedTotal != nil {
			total = *m.SuppressedTotal
		}
		return fmt.Sprintf(
			"%d %s below the reporting threshold of %d visitors %s withheld (%s in total). "+
				"The combined figure is shown because it is aggregated; the individual rows are not.",
			*m.SuppressedRows, plural(*m.SuppressedRows, "row", "rows"), threshold,
			verb(*m.SuppressedRows), Thousands(total))
	}

	return fmt.Sprintf(
		"Withheld: this slice covers fewer than %d visitors, so every metric is reported as %s. "+
			"That means \"fewer than %d, possibly none\" — it does not mean zero.",
		threshold, Withheld, threshold)
}

// ExportSuppressionNote is the same explanation for the CSV exports, whose
// suppression arrives in X-Pulse-Suppressed-* headers because CSV has nowhere to
// put a meta object.
//
// Note it reports withheld PAGEVIEWS while the floor itself is applied to
// VISITORS — nine pageviews can be one person reloading, so a pageview-based
// floor would pass exactly the row it exists to withhold. The two figures are
// deliberately different quantities.
func ExportSuppressionNote(rows, pageviews, minCell int) string {
	if rows == 0 {
		return ""
	}
	if minCell == 0 {
		minCell = publicv1.MinCellSize
	}
	return fmt.Sprintf("%d %s seen by fewer than %d visitors %s withheld (%s pageviews in total, not listed above).",
		rows, plural(rows, "page", "pages"), minCell, verb(rows), Thousands(pageviews))
}

// ExportDayMetricsNote is the daily export's counterpart: the floor there
// withholds the four per-session metrics of any day bucket covering fewer
// than min_cell_size visitors (day COUNTS stay — a whole-site day count is an
// aggregate). Those cells arrive empty, and an empty cell with no explanation
// reads as a rendering bug and gets reported as one.
func ExportDayMetricsNote(dayMetrics, minCell int) string {
	if dayMetrics == 0 {
		return ""
	}
	if minCell == 0 {
		minCell = publicv1.MinCellSize
	}
	return fmt.Sprintf("%d day %s fewer than %d visitors; their per-session metrics are withheld (empty cells, not zero).",
		dayMetrics, plural(dayMetrics, "bucket covers", "buckets cover"), minCell)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func verb(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}
