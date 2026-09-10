package mcpwrap_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-api-go/publicv1"
	"github.com/ciphera-net/pulse-cli/internal/mcpwrap"
	"github.com/ciphera-net/pulse-client-go/client"
)

func ptrInt(v int) *int           { return &v }
func ptrFloat(v float64) *float64 { return &v }

// suppressedStats is what the API returns for a slice under the floor: every
// metric null, suppressed true. The two callers below differ only in what the
// TRUE underlying figure was — which is the point.
func suppressedStats(minCell int) *client.Result[publicv1.Stats] {
	return &client.Result[publicv1.Stats]{
		Data: publicv1.Stats{}, // all pointers nil, exactly as the wire has it
		Meta: publicv1.Meta{
			Suppressed:  true,
			MinCellSize: minCell,
			Range: &publicv1.Range{
				From: "2026-08-15", To: "2026-08-21", Timezone: "Europe/Brussels", Period: "7d",
			},
		},
		Header: http.Header{},
	}
}

// TestOracle is the assertion the whole package exists for.
//
// The API refuses to distinguish a slice covering one visitor from a slice
// covering none: both come back with every metric null and suppressed true,
// byte-identical, because if "suppressed" meant "1 to 4" while a true zero came
// back as 0, walking a dimension's values would reveal which of them have a
// live cohort. That is an existence oracle wearing a privacy floor's clothing.
//
// This test proves the MCP layer cannot REINTRODUCE the distinction the API
// spent effort erasing. Two responses the server made identical must leave this
// package identical too.
func TestOracle_OneVisitorAndEmptyAreIndistinguishable(t *testing.T) {
	oneVisitor := suppressedStats(5)
	empty := suppressedStats(5)

	a, err := json.Marshal(mcpwrap.Stats(oneVisitor))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(mcpwrap.Stats(empty))
	if err != nil {
		t.Fatal(err)
	}

	if string(a) != string(b) {
		t.Fatalf("the wrapper distinguishes a 1-visitor slice from an empty one:\n a=%s\n b=%s", a, b)
	}
}

// TestSuppressedMetricsAreStructurallyAbsent is the layer-1 assertion.
//
// MUTATION: emit the metric keys with nil values on a suppressed result and
// this must go RED. A model shown "visitors": null does the statistically
// likely thing and writes "0 visitors".
func TestSuppressedMetricsAreStructurallyAbsent(t *testing.T) {
	out := mcpwrap.Stats(suppressedStats(5))

	for _, metric := range []string{
		"visitors", "pageviews", "bounce_rate",
		"avg_duration", "avg_scroll_depth", "avg_visible_duration",
	} {
		if _, present := out[metric]; present {
			t.Errorf("suppressed result carries %q — it must not be present at all", metric)
		}
	}
	if s, _ := out["suppressed"].(bool); !s {
		t.Error("suppressed result is not marked suppressed")
	}
}

// TestSuppressionProseSaysWhatItMeans pins the sentence a model reads.
//
// MUTATION: drop "possibly none", or drop the instruction never to report zero,
// and this goes RED. The prose is contract, not copy.
func TestSuppressionProseSaysWhatItMeans(t *testing.T) {
	out := mcpwrap.Stats(suppressedStats(5))
	sup, ok := out["suppression"].(mcpwrap.Suppression)
	if !ok {
		t.Fatalf("no suppression object: %#v", out["suppression"])
	}

	for _, phrase := range []string{"possibly none", "NEVER", "privacy floor"} {
		if !strings.Contains(sup.Meaning, phrase) {
			t.Errorf("suppression prose is missing %q: %s", phrase, sup.Meaning)
		}
	}
	if !strings.Contains(sup.Meaning, "0") {
		t.Error("suppression prose does not name zero as the wrong answer")
	}
	if sup.Reason != mcpwrap.ReasonPrivacyFloor {
		t.Errorf("reason = %q", sup.Reason)
	}
}

// TestThresholdComesFromTheServer pins layer 1's other half.
//
// MUTATION: hardcode 5 instead of reading meta.min_cell_size and this goes RED.
// A client that hardcodes the number describes the wrong floor the moment the
// server raises it, which is the whole reason min_cell_size is on the wire.
func TestThresholdComesFromTheServer(t *testing.T) {
	out := mcpwrap.Stats(suppressedStats(25))
	sup := out["suppression"].(mcpwrap.Suppression)

	if sup.MinCellSize != 25 {
		t.Errorf("min_cell_size = %d, want the server's 25", sup.MinCellSize)
	}
	if !strings.Contains(sup.Meaning, "25") {
		t.Errorf("prose does not use the server's threshold: %s", sup.Meaning)
	}
	if strings.Contains(sup.Meaning, "fewer than 5 ") {
		t.Error("prose used the compiled-in default while the server said 25")
	}
}

// TestUnsuppressedNullIsSurfacedNotSwallowed covers the contract violation.
//
// A null metric with suppressed=false should not happen. If it does, the metric
// must not silently vanish: an absent key reads to a model as "this metric does
// not exist", which is a third wrong answer alongside zero and null.
func TestUnsuppressedNullIsSurfacedNotSwallowed(t *testing.T) {
	res := &client.Result[publicv1.Stats]{
		Data:   publicv1.Stats{Visitors: ptrInt(42)}, // pageviews et al. nil
		Meta:   publicv1.Meta{Suppressed: false},
		Header: http.Header{},
	}
	out := mcpwrap.Stats(res)

	if out["visitors"] != 42 {
		t.Errorf("visitors = %v, want 42", out["visitors"])
	}
	if _, present := out["pageviews"]; present {
		t.Error("a nil metric was emitted rather than reported as absent")
	}
	absent, ok := out["unexpected_absent_fields"].([]string)
	if !ok || len(absent) == 0 {
		t.Fatalf("unexpected absences were not reported: %#v", out["unexpected_absent_fields"])
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "not treat them as zero") &&
		!strings.Contains(note, "Do not treat them as zero") {
		t.Errorf("note does not warn against reading absences as zero: %q", note)
	}
}

// TestRangeIsEchoedForQuoting pins the timezone discipline.
//
// MUTATION: omit meta.range and this goes RED. A model that computes its own
// range lands a day out whenever the site's timezone is not the reader's.
func TestRangeIsEchoedForQuoting(t *testing.T) {
	out := mcpwrap.Stats(suppressedStats(5))
	meta, ok := out["meta"].(map[string]any)
	if !ok {
		t.Fatal("no meta")
	}
	r, ok := meta["range"].(map[string]any)
	if !ok {
		t.Fatal("no meta.range")
	}
	if r["from"] != "2026-08-15" || r["to"] != "2026-08-21" {
		t.Errorf("range not echoed verbatim: %#v", r)
	}
	if r["timezone"] != "Europe/Brussels" {
		t.Errorf("timezone not echoed: %#v", r["timezone"])
	}
	if note, _ := r["note"].(string); !strings.Contains(note, "do not compute") {
		t.Errorf("range carries no instruction to quote rather than compute: %q", note)
	}
}

// TestQuotaOmittedWhenHeadersAbsent — absent is not zero.
//
// MUTATION: report a missing header as 0 remaining and this goes RED. Inventing
// an exhausted quota tells a model to stop working for no reason.
func TestQuotaOmittedWhenHeadersAbsent(t *testing.T) {
	out := mcpwrap.Stats(suppressedStats(5))
	if _, present := out["quota"]; present {
		t.Error("quota reported although no rate-limit headers were present")
	}

	withHeaders := suppressedStats(5)
	withHeaders.Header = http.Header{
		"X-Ratelimit-Limit":     []string{"1000"},
		"X-Ratelimit-Remaining": []string{"0"},
	}
	out = mcpwrap.Stats(withHeaders)
	q, ok := out["quota"].(map[string]any)
	if !ok {
		t.Fatal("quota not reported although headers were present")
	}
	if q["limit"] != 1000 || q["remaining"] != 0 {
		t.Errorf("quota = %#v", q)
	}
}

// TestRealtimeKeepsTheSiteTotalAndFlagsWithheldRows.
//
// The site-wide count is never suppressed — one number describing a whole site
// is a population, not a person. Rows are. The visible rows will not sum to the
// total, and the suppression object has to say so or a model reports a bug.
func TestRealtimeKeepsTheSiteTotalAndFlagsWithheldRows(t *testing.T) {
	res := &client.Result[publicv1.Realtime]{
		Data: publicv1.Realtime{
			Visitors: 12,
			TopPaths: []publicv1.PathVisitors{{Path: "/", Visitors: 7}},
		},
		Meta: publicv1.Meta{
			Suppressed: true, MinCellSize: 5,
			SuppressedRows: ptrInt(3), SuppressedTotal: ptrInt(5),
		},
		Header: http.Header{},
	}
	out := mcpwrap.Realtime(res)

	if out["visitors"] != 12 {
		t.Errorf("site-wide visitors = %v, want 12 — this figure is never suppressed", out["visitors"])
	}
	sup, ok := out["suppression"].(mcpwrap.Suppression)
	if !ok {
		t.Fatal("withheld rows were not reported")
	}
	if sup.RowsWithheld == nil || *sup.RowsWithheld != 3 {
		t.Errorf("rows_withheld = %v, want 3", sup.RowsWithheld)
	}
	if !strings.Contains(sup.Meaning, "do not sum") {
		t.Errorf("prose does not warn that visible rows undercount: %s", sup.Meaning)
	}
}

// TestExportDayMetricsEmptyCellsAreNotZero — the daily export's own trap.
//
// Withheld per-session metrics arrive as EMPTY CSV CELLS. An empty cell is the
// single most zero-looking thing in a spreadsheet, so the prose has to name it.
func TestExportDayMetricsEmptyCellsAreNotZero(t *testing.T) {
	out := mcpwrap.Export("date,visitors\n2026-08-20,3\n",
		client.Suppression{DayMetrics: 2, MinCellSize: 5}, true)

	sup, ok := out["suppression"].(mcpwrap.Suppression)
	if !ok {
		t.Fatal("day-metric suppression was not reported")
	}
	if !strings.Contains(sup.Meaning, "EMPTY CELLS") {
		t.Errorf("prose does not name the empty cell: %s", sup.Meaning)
	}
	if !strings.Contains(sup.Meaning, "NEVER be read as 0") {
		t.Errorf("prose does not forbid reading the cell as zero: %s", sup.Meaning)
	}
}

// TestExportZeroSuppressionIsNotSuppression — presence is the signal, and an
// explicit zero means "nothing was withheld", not "something was".
func TestExportZeroSuppressionIsNotSuppression(t *testing.T) {
	out := mcpwrap.Export("a,b\n", client.Suppression{Rows: 0, MinCellSize: 5}, true)
	if s, _ := out["suppressed"].(bool); s {
		t.Error("an explicit zero suppression header was reported as suppression")
	}
	if _, present := out["suppression"]; present {
		t.Error("suppression object emitted although nothing was withheld")
	}
}
