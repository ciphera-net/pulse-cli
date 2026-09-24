package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/config"
	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/client"
)

// newBreakdownTestApp builds an App pointed at a test server, with a default
// site already configured — the same shape `--site` bypasses and every other
// command's tests would need, kept in one place so each test states only what
// it is actually exercising.
func newBreakdownTestApp(mode render.Mode, out, errBuf *bytes.Buffer, baseURL string) *App {
	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	cfg.SetSiteFor("default", "8a7cabce-4828-4663-8c77-67a9da11bc70", "ciphera.net")
	app := &App{
		Printer: render.NewPrinterTo(out, errBuf, mode),
		Config:  cfg,
		started: true,
	}
	if baseURL != "" {
		app.client = client.New(baseURL, "pulse_sk_test", "test")
	}
	return app
}

func mustExecute(t *testing.T, cmd interface{ ExecuteContext(context.Context) error }) error {
	t.Helper()
	return cmd.ExecuteContext(context.Background())
}

// * A typo'd dimension is a local mistake, not a reason to spend a request or
// * touch the keychain. The server in this test fails the test outright if it
// * is ever hit, which is the only way to prove "no request" rather than just
// * "the command happened to error before I checked".
func TestBreakdownRefusesUnknownDimensionWithoutARequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("the API must not be called for an unknown dimension; got %s", r.URL.String())
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"not_a_real_dimension"})
	err := mustExecute(t, cmd)

	if err == nil {
		t.Fatal("an unknown dimension must be refused")
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a *cliError (so `finish` cannot classify it): %v (%T)", err, err)
	}
	// * usageErr's code, the same exit a bad --filter or a bad --last already
	// * gets — "exit 2" has to mean the same thing everywhere a script checks it.
	if ce.code != client.ExitInvalidInput {
		t.Errorf("exit code = %d, want %d (bad usage)", ce.code, client.ExitInvalidInput)
	}
	if !strings.Contains(err.Error(), "not_a_real_dimension") {
		t.Errorf("refusal does not name what was typed: %v", err)
	}
}

// * An out-of-range --limit is exactly the same class of mistake and must be
// * caught the same way: locally, before a request, exit 2.
func TestBreakdownRefusesOutOfRangeLimitWithoutARequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("the API must not be called for an out-of-range --limit; got %s", r.URL.String())
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"page", "--limit", "101"})
	err := mustExecute(t, cmd)

	var ce *cliError
	if !errors.As(err, &ce) || ce.code != client.ExitInvalidInput {
		t.Fatalf("got %v, want a cliError with exit code %d", err, client.ExitInvalidInput)
	}
}

// * The request must carry dimension=, the resolved period, every --filter as
// * its own repeated filter= parameter, and limit= only when it was actually
// * set — mirrored here at the command layer because this is what proves the
// * FLAGS reach the client call correctly, which pulse-client-go's own tests
// * cannot see.
func TestBreakdownRequestShape(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"dimension":"country","rows":[]},"meta":{"suppressed":false}}`))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{
		"country", "--last", "30d", "--limit", "10",
		"--filter", "browser==Firefox", "--filter", "country==BE",
	})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if got := gotQuery.Get("dimension"); got != "country" {
		t.Errorf("dimension = %q, want %q", got, "country")
	}
	if got := gotQuery.Get("period"); got != "30d" {
		t.Errorf("period = %q, want %q", got, "30d")
	}
	if got := gotQuery.Get("limit"); got != "10" {
		t.Errorf("limit = %q, want %q", got, "10")
	}
	if got := gotQuery["filter"]; len(got) != 2 {
		t.Fatalf("got %d filter parameters %v, want 2 — filters must be repeated, not joined", len(got), got)
	}
}

// * limit=0 (never set) must be OMITTED, not sent as a literal 0 — the
// * server's own default is not this client's to assert, and a stale "0"
// * would silently ask for zero rows instead of the default 20.
func TestBreakdownOmitsLimitWhenNotSet(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"dimension":"page","rows":[]},"meta":{"suppressed":false}}`))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"page", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := gotQuery["limit"]; ok {
		t.Errorf("limit was sent (%v) when the flag was never set", gotQuery["limit"])
	}
}

// breakdownRowJSON builds one row of the wire shape, letting encoding/json do
// the escaping — so a test value containing control characters or bidi
// overrides is encoded exactly the way the real API would encode it, rather
// than by a hand-built string literal that JSON does not actually accept
// (Go's %q, for instance, would emit \x escapes, which are not legal JSON).
func breakdownRowJSON(t *testing.T, value string, country *string, visitors, pageviews int) string {
	t.Helper()
	row := map[string]any{"value": value, "visitors": visitors, "pageviews": pageviews}
	if country != nil {
		row["country"] = *country
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return string(b)
}

func breakdownBody(t *testing.T, dimension string, rows ...string) []byte {
	t.Helper()
	body := fmt.Sprintf(`{"data":{"dimension":%q,"rows":[%s]},"meta":{"suppressed":false}}`,
		dimension, strings.Join(rows, ","))
	// * Fail the test immediately if the fixture itself is not valid JSON,
	// * rather than watching the assertions below fail for an unrelated reason.
	if !json.Valid([]byte(body)) {
		t.Fatalf("test fixture is not valid JSON: %s", body)
	}
	return []byte(body)
}

// * The decode-and-render side: a region row carries its country (a region
// * name alone is ambiguous — "Limburg" is a province of both Belgium and the
// * Netherlands), and the COUNTRY column must appear for a region breakdown
// * and nowhere else.
func TestBreakdownTableRendersRegionRowWithCountry(t *testing.T) {
	be := "BE"
	row := breakdownRowJSON(t, "Limburg", &be, 128, 340)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(breakdownBody(t, "region", row))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"region", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	for _, want := range []string{"COUNTRY", "Limburg", "BE", "128", "340"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q:\n%s", want, got)
		}
	}
}

// * The CSV counterpart of TestBreakdownTableRendersRegionRowWithCountry: the
// * header must gain the COUNTRY column in the same position CSV consumers
// * would expect it (between value and visitors), and the row must carry it —
// * pinned separately from the table case because the CSV and table code
// * paths build their headers and rows independently in breakdown.go.
func TestBreakdownCSVRendersRegionRowWithCountry(t *testing.T) {
	be := "BE"
	row := breakdownRowJSON(t, "Limburg", &be, 128, 340)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(breakdownBody(t, "region", row))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeCSV, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"region", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d CSV lines, want 2 (header + one row): %q", len(lines), out.String())
	}
	if lines[0] != "value,country,visitors,pageviews" {
		t.Errorf("CSV header = %q, want %q", lines[0], "value,country,visitors,pageviews")
	}
	if lines[1] != "Limburg,BE,128,340" {
		t.Errorf("CSV row = %q, want %q", lines[1], "Limburg,BE,128,340")
	}
}

// * A non-region dimension must NOT grow a COUNTRY column, even though the
// * wire type technically allows the field on any row.
func TestBreakdownTableOmitsCountryForNonRegionDimension(t *testing.T) {
	row := breakdownRowJSON(t, "/pricing", nil, 50, 90)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(breakdownBody(t, "page", row))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"page", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out.String(), "COUNTRY") {
		t.Errorf("a page breakdown must not have a COUNTRY column:\n%s", out.String())
	}
}

// * An empty result is a legitimate answer — a quiet site, a narrow filter —
// * not an error. It must exit 0, say so in a way a person reading the
// * terminal understands, and never crash on an empty slice.
func TestBreakdownTableHandlesEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(breakdownBody(t, "channel"))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"channel", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("an empty result must not be an error: %v", err)
	}
	if !strings.Contains(errBuf.String(), "No traffic") {
		t.Errorf("an empty result should say so on stderr, got: %q", errBuf.String())
	}
}

// * The CSV counterpart: still a header row and nothing else, never an error
// * and never a stray data row.
func TestBreakdownCSVHandlesEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(breakdownBody(t, "page"))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeCSV, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"page", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.TrimSpace(out.String()) != "value,visitors,pageviews" {
		t.Errorf("empty CSV = %q, want just the header row", out.String())
	}
}

// * --json is the API's own bytes, unmodified — an empty rows:[] must reach
// * stdout exactly as the server sent it, not re-encoded.
func TestBreakdownJSONPassesRawBytesUnmodified(t *testing.T) {
	body := `{"data":{"dimension":"os","rows":[]},"meta":{"suppressed":false,"unknown_future_field":1}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeJSON, out, errBuf, srv.URL)

	cmd := newBreakdownCmd(app)
	cmd.SetArgs([]string{"os", "--last", "7d"})
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := strings.TrimRight(out.String(), "\n"); got != body {
		t.Errorf("JSON output was altered.\n got: %s\nwant: %s", got, body)
	}
}

// * The security property: a value carrying an ESC-driven terminal escape
// * sequence and a bidi override must never reach the terminal (or a CSV cell)
// * raw, in EITHER output mode. `breakdown` is the one endpoint with no
// * privacy floor, so page paths, referrers and UTM values are unfiltered
// * visitor input by the time they get here.
func TestBreakdownSanitisesControlCharactersInTableAndCSV(t *testing.T) {
	evil := "/pricing\x1b]0;pwned\x07\u202ereversed"
	row := breakdownRowJSON(t, evil, nil, 12, 30)

	for _, mode := range []render.Mode{render.ModeTable, render.ModeCSV} {
		t.Run(string(mode), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(breakdownBody(t, "page", row))
			}))
			defer srv.Close()

			out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
			app := newBreakdownTestApp(mode, out, errBuf, srv.URL)

			cmd := newBreakdownCmd(app)
			cmd.SetArgs([]string{"page", "--last", "7d"})
			if err := mustExecute(t, cmd); err != nil {
				t.Fatalf("execute: %v", err)
			}

			got := out.String()
			for _, r := range []rune{0x1b, 0x07, 0x202e} {
				if strings.ContainsRune(got, r) {
					t.Errorf("raw U+%04X reached stdout: %q", r, got)
				}
			}
			if !strings.Contains(got, `\u001B`) {
				t.Errorf("the escaped form of ESC did not appear: %q", got)
			}
			if !strings.Contains(got, "/pricing") {
				t.Errorf("the harmless part of the value was lost: %q", got)
			}
		})
	}
}

// * Table mode truncates an absurdly long value so one row cannot wreck every
// * column's alignment; CSV must NOT — a script piping the export wants the
// * full value, sanitised but not shortened.
func TestBreakdownTruncatesLongValuesInTableOnlyNotCSV(t *testing.T) {
	long := "/blog/" + strings.Repeat("a", 200)
	row := breakdownRowJSON(t, long, nil, 5, 8)

	t.Run("table truncates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(breakdownBody(t, "page", row))
		}))
		defer srv.Close()

		out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
		app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)
		cmd := newBreakdownCmd(app)
		cmd.SetArgs([]string{"page", "--last", "7d"})
		if err := mustExecute(t, cmd); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if strings.Contains(out.String(), long) {
			t.Errorf("table output was not truncated: %q", out.String())
		}
		if !strings.Contains(out.String(), "…") {
			t.Errorf("truncated table output has no ellipsis: %q", out.String())
		}
	})

	t.Run("csv keeps the full value", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(breakdownBody(t, "page", row))
		}))
		defer srv.Close()

		out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
		app := newBreakdownTestApp(render.ModeCSV, out, errBuf, srv.URL)
		cmd := newBreakdownCmd(app)
		cmd.SetArgs([]string{"page", "--last", "7d"})
		if err := mustExecute(t, cmd); err != nil {
			t.Fatalf("execute: %v", err)
		}
		if !strings.Contains(out.String(), long) {
			t.Errorf("CSV output truncated a value it must keep in full: %q", out.String())
		}
	})
}
