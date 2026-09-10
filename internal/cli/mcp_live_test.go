package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPServerLive drives the real server the way a host does: it spawns the
// binary, speaks the protocol over stdio, and calls tools against production.
//
// It is skipped without PULSE_API_KEY. That is deliberate rather than lazy — a
// mocked transport would prove the handlers compile, and this suite already
// proves that elsewhere. What only a live run can establish is that the framing
// works, that nothing writes to stdout outside a JSON-RPC frame, and that the
// suppression shape survives a real response.
func TestMCPServerLive(t *testing.T) {
	if os.Getenv("PULSE_API_KEY") == "" {
		t.Skip("PULSE_API_KEY not set — live MCP smoke test skipped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	bin := buildPulse(t)

	c := mcp.NewClient(&mcp.Implementation{Name: "pulse-live-test", Version: "1"}, nil)
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// Every declared tool must be listed. A tool that exists in the registry
	// and never reaches a host is invisible in exactly the way a missing
	// feature is.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	want := []string{
		"pulse_whoami", "pulse_list_sites", "pulse_get_stats",
		"pulse_get_realtime", "pulse_export_daily", "pulse_export_pages",
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		got[tl.Name] = tl
	}
	for _, name := range want {
		tl, ok := got[name]
		if !ok {
			t.Fatalf("tool %q was not listed", name)
		}
		if !tl.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not annotated read-only", name)
		}
		if tl.Description == "" {
			t.Errorf("tool %q has no description", name)
		}
	}

	// A site id has to come from the product, not from a fixture: a hardcoded
	// UUID would pass on one machine and fail everywhere else.
	sitesOut := callOK(t, ctx, sess, "pulse_list_sites", map[string]any{})
	sites, _ := sitesOut["sites"].([]any)
	if len(sites) == 0 {
		t.Fatal("no sites returned — cannot exercise the site-scoped tools")
	}
	first, _ := sites[0].(map[string]any)
	siteID, _ := first["site_id"].(string)
	if siteID == "" {
		t.Fatal("first site has no site_id")
	}
	t.Logf("exercising site %v (%v)", first["domain"], siteID)

	callOK(t, ctx, sess, "pulse_whoami", map[string]any{})

	stats := callOK(t, ctx, sess, "pulse_get_stats", map[string]any{
		"site_id": siteID, "period": "7d",
	})
	assertRangeQuotable(t, stats)
	assertFloorHonoured(t, stats)

	rt := callOK(t, ctx, sess, "pulse_get_realtime", map[string]any{"site_id": siteID})
	if _, ok := rt["visitors"]; !ok {
		t.Error("realtime result has no site-wide visitors count")
	}

	// A slug must be refused before a request is built. The assertion that
	// matters is that this is a TOOL error the model can act on, not a
	// transport error that kills the session.
	bad, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "pulse_get_stats", Arguments: map[string]any{
			"site_id": "ciphera-net", "period": "7d",
		}})
	if err != nil {
		t.Fatalf("a rejected site_id must not break the session: %v", err)
	}
	if !bad.IsError {
		t.Error("a slug was accepted as a site_id")
	}
	if txt := textOf(bad); !strings.Contains(txt, "UUID") {
		t.Errorf("rejection does not tell the model what is wrong: %q", txt)
	}
}

// buildPulse compiles the binary under test once.
func buildPulse(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/pulse"
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/pulse")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// callOK calls a tool and fails on any error, returning the decoded result.
func callOK(t *testing.T, ctx context.Context, s *mcp.ClientSession,
	name string, args map[string]any) map[string]any {
	t.Helper()

	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, textOf(res))
	}

	// The same object must reach the model on BOTH channels: hosts differ in
	// which one they show, and a suppression marker delivered only on the
	// channel a host ignores is not delivered.
	if res.StructuredContent == nil {
		t.Fatalf("%s: no structuredContent", name)
	}
	txt := textOf(res)
	if txt == "" {
		t.Fatalf("%s: no text content", name)
	}

	var fromText, fromStruct map[string]any
	if err := json.Unmarshal([]byte(txt), &fromText); err != nil {
		t.Fatalf("%s: text content is not JSON: %v", name, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &fromStruct); err != nil {
		t.Fatalf("%s: structuredContent is not an object: %v", name, err)
	}
	if len(fromText) != len(fromStruct) {
		t.Errorf("%s: text and structured content disagree (%d vs %d keys)",
			name, len(fromText), len(fromStruct))
	}
	return fromStruct
}

// assertRangeQuotable checks the server's resolved range came back, since the
// model is instructed to quote it rather than compute one.
func assertRangeQuotable(t *testing.T, out map[string]any) {
	t.Helper()
	meta, ok := out["meta"].(map[string]any)
	if !ok {
		t.Fatal("stats result carries no meta")
	}
	r, ok := meta["range"].(map[string]any)
	if !ok {
		t.Fatal("stats result carries no meta.range to quote")
	}
	for _, k := range []string{"from", "to", "timezone"} {
		if s, _ := r[k].(string); s == "" {
			t.Errorf("meta.range.%s is empty", k)
		}
	}
}

// assertFloorHonoured checks the two states a stats result may be in, and that
// neither one hands a model a zero it can misreport.
func assertFloorHonoured(t *testing.T, out map[string]any) {
	t.Helper()
	suppressed, _ := out["suppressed"].(bool)

	if !suppressed {
		if _, ok := out["visitors"]; !ok {
			t.Error("unsuppressed stats result has no visitors figure")
		}
		if _, ok := out["suppression"]; ok {
			t.Error("unsuppressed result carries a suppression object")
		}
		return
	}

	for _, metric := range []string{"visitors", "pageviews", "bounce_rate",
		"avg_duration", "avg_scroll_depth", "avg_visible_duration"} {
		if _, present := out[metric]; present {
			t.Errorf("suppressed result still carries %q — a withheld metric must be "+
				"structurally absent, not null", metric)
		}
	}
	sup, ok := out["suppression"].(map[string]any)
	if !ok {
		t.Fatal("suppressed result carries no suppression object")
	}
	meaning, _ := sup["meaning"].(string)
	if !strings.Contains(meaning, "possibly none") {
		t.Error("suppression prose does not say the value may be none")
	}
}

// textOf returns the first text content block.
func textOf(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
