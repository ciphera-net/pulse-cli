// Package mcpwrap turns an API result into the shape a language model reads.
//
// It exists because the CLI's renderer and a model's reader need different
// things from the same response. A human sees an em dash and a footnote and
// understands that a number was withheld. A model sees a gap and completes it:
// shown `"visitors": null`, the statistically likely continuation is "0
// visitors", which reconstructs — in the customer's own transcript — precisely
// the claim the server refused to make.
//
// So this package does not forward the envelope. It rebuilds it, and the rule
// it enforces is that a withheld metric is STRUCTURALLY ABSENT rather than
// null. There is no field for a summariser to round to zero, because the field
// a summariser would misread is not there to misread.
//
// It imports no transport. Nothing here knows what MCP is.
package mcpwrap

import (
	"fmt"

	"github.com/ciphera-net/pulse-api-go/publicv1"
	"github.com/ciphera-net/pulse-client-go/client"
)

// ReasonPrivacyFloor is the machine-readable cause of a suppression.
const ReasonPrivacyFloor = "privacy_floor"

// Suppression is what replaces withheld metrics.
//
// Meaning carries the instruction in the PAYLOAD rather than only in the tool
// description, because descriptions are read once at list-tools time and fall
// out of long contexts. This sentence sits adjacent to the data at the moment
// of summarisation, which is the position where an instruction actually
// survives.
type Suppression struct {
	Reason      string `json:"reason"`
	Meaning     string `json:"meaning"`
	MinCellSize int    `json:"min_cell_size"`

	// RowsWithheld and RowsWithheldTotal describe partial suppression: rows
	// dropped from a breakdown while the aggregate survived. Pointers because
	// zero is a real answer ("nothing was withheld") and must not read as
	// "this response has no rows".
	RowsWithheld      *int `json:"rows_withheld,omitempty"`
	RowsWithheldTotal *int `json:"rows_withheld_total,omitempty"`
}

// fullMeaning is the sentence for a wholly suppressed result.
//
// The threshold is interpolated from the server's own meta.min_cell_size, never
// from a compiled-in constant: a client that hardcodes the number describes the
// wrong floor the moment the server raises it, which is the entire reason
// min_cell_size travels on the wire.
func fullMeaning(threshold int) string {
	return fmt.Sprintf(
		"This slice covers fewer than %d visitors — possibly none. The Pulse API withholds "+
			"every metric below its privacy floor, including a true zero, so this MUST NEVER be "+
			"reported as 0, as \"no visitors\", or as \"no data\". The only accurate statement is "+
			"that the figures were withheld by the privacy floor (fewer than %d visitors, possibly "+
			"none).", threshold, threshold)
}

// rowsMeaning is the sentence for partial suppression.
func rowsMeaning(threshold int) string {
	return fmt.Sprintf(
		"Some rows covered fewer than %d visitors and were withheld — possibly none, possibly up "+
			"to %d each. Their combined total is reported because an aggregate is safe to state "+
			"while the individual rows are not. Withheld rows MUST NEVER be described as having "+
			"zero traffic, and the visible rows do not sum to the total.", threshold, threshold-1)
}

// threshold reads the floor the server applied, falling back to the published
// constant only when the server did not say.
func threshold(m publicv1.Meta) int {
	if m.MinCellSize > 0 {
		return m.MinCellSize
	}
	return publicv1.MinCellSize
}

// forFull builds the suppression object for a wholly withheld result.
func forFull(m publicv1.Meta) Suppression {
	t := threshold(m)
	return Suppression{Reason: ReasonPrivacyFloor, Meaning: fullMeaning(t), MinCellSize: t}
}

// forRows builds the suppression object for a partially withheld result.
func forRows(m publicv1.Meta) Suppression {
	t := threshold(m)
	s := Suppression{Reason: ReasonPrivacyFloor, Meaning: rowsMeaning(t), MinCellSize: t}
	s.RowsWithheld = m.SuppressedRows
	s.RowsWithheldTotal = m.SuppressedTotal
	return s
}

// meta renders the response metadata a model is allowed to rely on.
//
// The resolved range is echoed verbatim so the model can quote it. It must
// never compute or restate a range of its own: `period=7d` resolves in the
// SITE's timezone, and a model that expands "last 7 days" into dates itself
// lands a day out whenever the site is not in the reader's zone.
func meta(m publicv1.Meta) map[string]any {
	out := map[string]any{}
	if m.Range != nil {
		r := map[string]any{
			"from":     m.Range.From,
			"to":       m.Range.To,
			"timezone": m.Range.Timezone,
			"note": "Resolved by the server in the site's timezone. Quote these dates when " +
				"describing the period; do not compute or restate a range.",
		}
		if m.Range.Period != "" {
			r["period"] = m.Range.Period
		}
		out["range"] = r
	}
	return out
}

// quota surfaces the rate-limit headers when they were present.
//
// Omitted entirely when absent, because absent is not zero: reporting a missing
// header as "0 remaining" invents an exhausted quota.
func quota[T any](res *client.Result[T]) (map[string]any, bool) {
	limit, remaining, ok := res.Quota()
	if !ok {
		return nil, false
	}
	return map[string]any{"limit": limit, "remaining": remaining}, true
}

// envelope assembles the common shell every wrapped result shares.
func envelope[T any](res *client.Result[T]) map[string]any {
	out := map[string]any{}
	if m := meta(res.Meta); len(m) > 0 {
		out["meta"] = m
	}
	if q, ok := quota(res); ok {
		out["quota"] = q
	}
	return out
}

// Stats wraps an aggregate result.
//
// When the floor is in force NO metric key is emitted at all — not the key with
// a null, not the key with a zero. That absence is the whole design: see the
// package comment.
func Stats(res *client.Result[publicv1.Stats]) map[string]any {
	out := envelope(res)

	if res.Meta.Suppressed {
		out["suppressed"] = true
		out["suppression"] = forFull(res.Meta)
		return out
	}

	s := res.Data
	metrics := map[string]any{}
	var withheld []string

	putInt := func(name string, v *int) {
		if v == nil {
			withheld = append(withheld, name)
			return
		}
		metrics[name] = *v
	}
	putFloat := func(name string, v *float64) {
		if v == nil {
			withheld = append(withheld, name)
			return
		}
		metrics[name] = *v
	}

	putInt("visitors", s.Visitors)
	putInt("pageviews", s.Pageviews)
	putFloat("bounce_rate", s.BounceRate)
	putFloat("avg_duration", s.AvgDuration)
	putFloat("avg_scroll_depth", s.AvgScrollDepth)
	putFloat("avg_visible_duration", s.AvgVisibleDuration)

	for k, v := range metrics {
		out[k] = v
	}
	out["suppressed"] = false

	// A null metric on an UNSUPPRESSED response contradicts the API contract.
	// It is surfaced rather than swallowed: a silently missing key would read
	// to a model as "this metric does not exist", which is a third wrong
	// answer alongside zero and null.
	if len(withheld) > 0 {
		out["unexpected_absent_fields"] = withheld
		out["note"] = "The server reported no suppression yet omitted the fields listed in " +
			"unexpected_absent_fields. Do not treat them as zero; report them as unavailable."
	}
	return out
}

// Realtime wraps the live view.
//
// Site-wide Visitors is never suppressed — one number describing a whole site
// is a population, not a person. TopPaths rows are subject to the floor, so a
// partial suppression object rides alongside them.
func Realtime(res *client.Result[publicv1.Realtime]) map[string]any {
	out := envelope(res)
	out["visitors"] = res.Data.Visitors

	paths := make([]map[string]any, 0, len(res.Data.TopPaths))
	for _, p := range res.Data.TopPaths {
		paths = append(paths, map[string]any{"path": p.Path, "visitors": p.Visitors})
	}
	out["top_paths"] = paths

	if res.Meta.Suppressed {
		out["suppressed"] = true
		out["suppression"] = forRows(res.Meta)
	} else {
		out["suppressed"] = false
	}
	return out
}

// Sites wraps the site list. No metrics, so no floor applies.
func Sites(res *client.Result[[]publicv1.Site]) map[string]any {
	out := envelope(res)
	list := make([]map[string]any, 0, len(res.Data))
	for _, s := range res.Data {
		row := map[string]any{
			"site_id":  s.ID,
			"domain":   s.Domain,
			"name":     s.Name,
			"timezone": s.Timezone,
		}
		// last_event_at is null when a site has never reported. Rendered as an
		// explicit statement rather than an absent key, because "never" is the
		// answer to the first question anyone asks of a site list.
		if s.LastEventAt != nil {
			row["last_event_at"] = s.LastEventAt.UTC().Format("2006-01-02T15:04:05Z")
		} else {
			row["last_event_at"] = nil
			row["never_received_an_event"] = true
		}
		list = append(list, row)
	}
	out["sites"] = list
	out["note"] = "Use site_id (a UUID) for every other tool. Slugs and domains are not accepted."
	return out
}

// Me wraps the key description.
func Me(res *client.Result[publicv1.Me]) map[string]any {
	out := envelope(res)
	out["me"] = res.Data
	return out
}

// Export wraps a bulk CSV export.
//
// The CSV is passed through verbatim. Its suppression arrives in headers rather
// than a meta object, because CSV has nowhere to put one, and it is reported
// here in the same shape as everywhere else so a model meets one vocabulary.
func Export(csv string, s client.Suppression, seen bool) map[string]any {
	out := map[string]any{"format": "csv", "csv": csv}

	if !seen || (s.Rows == 0 && s.DayMetrics == 0) {
		out["suppressed"] = false
		return out
	}

	t := s.MinCellSize
	if t == 0 {
		t = publicv1.MinCellSize
	}
	sup := Suppression{Reason: ReasonPrivacyFloor, MinCellSize: t}

	switch {
	case s.Rows > 0:
		rows := s.Rows
		total := s.Pageviews
		sup.RowsWithheld = &rows
		sup.RowsWithheldTotal = &total
		sup.Meaning = fmt.Sprintf(
			"%d row(s) covering fewer than %d visitors were withheld from this CSV — possibly "+
				"none each, possibly up to %d. Their combined %d pageviews are not listed. "+
				"Withheld rows MUST NEVER be described as having zero traffic, and the listed "+
				"rows do not sum to the site total.", rows, t, t-1, total)
	default:
		n := s.DayMetrics
		sup.RowsWithheld = &n
		sup.Meaning = fmt.Sprintf(
			"%d day bucket(s) covered fewer than %d visitors, so their per-session metrics are "+
				"withheld and arrive as EMPTY CELLS. An empty cell here means withheld — possibly "+
				"none — and MUST NEVER be read as 0. The day counts themselves are present.", n, t)
	}

	out["suppressed"] = true
	out["suppression"] = sup
	return out
}
