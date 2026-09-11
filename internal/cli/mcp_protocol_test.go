package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ciphera-net/pulse-cli/internal/mcptools"
)

// TestMCPProtocolAgainstStubAPI drives the real binary over the real protocol,
// with a stub standing in for the API.
//
// This is the test that does not depend on a credential or on production being
// reachable, and it proves the two things a unit test cannot: that the stdio
// framing works end to end, and that the suppression shape survives the whole
// journey from an HTTP body to what a host hands a model.
func TestMCPProtocolAgainstStubAPI(t *testing.T) {
	const siteID = "0b57e0f2-2a51-4a9e-9a1e-1f5f4b0d9a11"

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pulse_sk_live_stub" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"unauthorized","code":"invalid_key","message":"no"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "1000")
		w.Header().Set("X-RateLimit-Remaining", "997")

		switch {
		case strings.HasSuffix(r.URL.Path, "/sites"):
			_, _ = w.Write([]byte(`{"data":[{"id":"` + siteID + `","slug":"example",
				"domain":"example.com","name":"Example","timezone":"Europe/Brussels",
				"last_event_at":null,"created_at":"2026-01-01T00:00:00Z"}],"meta":{"suppressed":false}}`))

		case strings.HasSuffix(r.URL.Path, "/stats"):
			// The floor in force: every metric null, suppressed true. This is
			// the exact body a 1-visitor slice and an empty slice both produce.
			_, _ = w.Write([]byte(`{"data":{"visitors":null,"pageviews":null,"bounce_rate":null,
				"avg_duration":null,"avg_scroll_depth":null,"avg_visible_duration":null},
				"meta":{"suppressed":true,"min_cell_size":5,
				"range":{"from":"2026-08-15","to":"2026-08-21","timezone":"Europe/Brussels","period":"7d"}}}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"not_found","code":"no_route","message":"no"}}`))
		}
	}))
	defer api.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bin := buildPulse(t)
	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(cmd.Environ(),
		"PULSE_API_KEY=pulse_sk_live_stub",
		"PULSE_API_URL="+api.URL,
	)

	c := mcp.NewClient(&mcp.Implementation{Name: "protocol-test", Version: "1"}, nil)
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	// The registry and the registration list must agree in BOTH directions.
	// One direction alone is a half-check: only listing-has-definition lets a
	// declared tool be silently forgotten at registration, and only
	// definition-is-listed lets an undeclared one reach a host with whatever
	// annotations the call site happened to pass.
	listed := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		listed[tl.Name] = tl
		if _, ok := mcptools.Lookup(tl.Name); !ok {
			t.Errorf("tool %q was registered but has no definition", tl.Name)
		}
	}
	for _, d := range mcptools.Definitions {
		tl, ok := listed[d.Name]
		if !ok {
			t.Errorf("tool %q is defined but was never registered — it cannot be called", d.Name)
			continue
		}
		// Annotations are derived from the class, so a disagreement here means
		// the derivation drifted from the table a reader trusts.
		if tl.Annotations.ReadOnlyHint != d.Class.ReadOnly() {
			t.Errorf("%s readOnlyHint=%v but class says %v",
				d.Name, tl.Annotations.ReadOnlyHint, d.Class.ReadOnly())
		}
		if tl.Annotations.DestructiveHint == nil {
			t.Errorf("%s has no destructiveHint", d.Name)
		} else if *tl.Annotations.DestructiveHint != d.Class.Destructive() {
			t.Errorf("%s destructiveHint=%v but class says %v",
				d.Name, *tl.Annotations.DestructiveHint, d.Class.Destructive())
		}
	}
	if len(listed) != len(mcptools.Definitions) {
		t.Errorf("listed %d tools, registry has %d", len(listed), len(mcptools.Definitions))
	}

	sites := callStub(t, ctx, sess, "pulse_list_sites", map[string]any{})
	list, _ := sites["sites"].([]any)
	if len(list) != 1 {
		t.Fatalf("got %d sites, want 1", len(list))
	}
	row := list[0].(map[string]any)
	if row["site_id"] != siteID {
		t.Errorf("site_id = %v", row["site_id"])
	}
	if never, _ := row["never_received_an_event"].(bool); !never {
		t.Error("a site with last_event_at null is not flagged as never reporting")
	}

	// The assertion this whole exercise exists for: a suppressed response must
	// arrive at the model with NO metric field to misread.
	stats := callStub(t, ctx, sess, "pulse_get_stats",
		map[string]any{"site_id": siteID, "period": "7d"})

	for _, metric := range []string{"visitors", "pageviews", "bounce_rate",
		"avg_duration", "avg_scroll_depth", "avg_visible_duration"} {
		if _, present := stats[metric]; present {
			t.Errorf("suppressed result reached the model carrying %q", metric)
		}
	}
	sup, ok := stats["suppression"].(map[string]any)
	if !ok {
		t.Fatal("no suppression object reached the model")
	}
	meaning, _ := sup["meaning"].(string)
	for _, phrase := range []string{"possibly none", "NEVER"} {
		if !strings.Contains(meaning, phrase) {
			t.Errorf("prose lost %q in transit: %q", phrase, meaning)
		}
	}
	if sup["min_cell_size"] != float64(5) {
		t.Errorf("min_cell_size = %v", sup["min_cell_size"])
	}

	// Quota headers were present, so they must be surfaced.
	q, ok := stats["quota"].(map[string]any)
	if !ok {
		t.Fatal("quota headers were present but not surfaced")
	}
	if q["remaining"] != float64(997) {
		t.Errorf("quota remaining = %v, want 997", q["remaining"])
	}

	// meta.range must survive for the model to quote.
	meta, _ := stats["meta"].(map[string]any)
	rng, _ := meta["range"].(map[string]any)
	if rng["from"] != "2026-08-15" || rng["timezone"] != "Europe/Brussels" {
		t.Errorf("range did not survive: %#v", rng)
	}
}

// callStub calls a tool, requires success, and requires the payload to be
// identical on both content channels.
func callStub(t *testing.T, ctx context.Context, s *mcp.ClientSession,
	name string, args map[string]any) map[string]any {
	t.Helper()

	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: %s", name, textOf(res))
	}

	var fromText map[string]any
	if err := json.Unmarshal([]byte(textOf(res)), &fromText); err != nil {
		t.Fatalf("%s: text content is not JSON: %v", name, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var fromStruct map[string]any
	if err := json.Unmarshal(raw, &fromStruct); err != nil {
		t.Fatalf("%s: structuredContent is not an object: %v", name, err)
	}

	// Hosts differ in which channel they show a model. A suppression marker
	// present on one and missing from the other is a marker that is absent for
	// half of all users.
	a, _ := json.Marshal(fromText)
	b, _ := json.Marshal(fromStruct)
	if string(a) != string(b) {
		t.Errorf("%s: the two content channels disagree\n text=%s\n struct=%s", name, a, b)
	}
	return fromStruct
}
