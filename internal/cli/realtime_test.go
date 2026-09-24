package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/render"
)

// realtimeBody builds the wire shape of GET /sites/{id}/realtime with one
// top-path row, letting encoding/json escape the path the way the real API
// would rather than by a hand-built string literal — Go's %q would emit \x
// escapes for a control character, which is not legal JSON.
func realtimeBody(t *testing.T, visitors int, path string, pathVisitors int) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"visitors":  visitors,
			"top_paths": []map[string]any{{"path": path, "visitors": pathVisitors}},
		},
		"meta": map[string]any{"suppressed": false},
	})
	if err != nil {
		t.Fatalf("marshal realtime fixture: %v", err)
	}
	return body
}

// * The security property, for `realtime` rather than `breakdown`: a top path
// * is exactly as visitor-supplied as a breakdown page value (it is the same
// * underlying column), and this command builds its OWN rows for both the
// * table and CSV paths (internal/cli/realtime.go:56 and :75) rather than
// * reusing anything breakdown.go already sanitised. Proving it here is what
// * confirms the fix lives in the shared renderer rather than in breakdown's
// * call site alone — this command was never touched to make it pass.
func TestRealtimeSanitisesControlCharactersInTopPathInTableAndCSV(t *testing.T) {
	evil := "/checkout" + string(rune(0x1b)) + "]0;pwned" + string(rune(0x07)) + string(rune(0x202e)) + "reversed"

	for _, mode := range []render.Mode{render.ModeTable, render.ModeCSV} {
		t.Run(string(mode), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(realtimeBody(t, 4, evil, 4))
			}))
			defer srv.Close()

			out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
			app := newBreakdownTestApp(mode, out, errBuf, srv.URL)

			cmd := newRealtimeCmd(app)
			if err := mustExecute(t, cmd); err != nil {
				t.Fatalf("execute: %v", err)
			}

			got := out.String()
			for _, r := range []rune{0x1b, 0x07, 0x202e} {
				if strings.ContainsRune(got, r) {
					t.Errorf("raw U+%04X reached %s output: %q", r, mode, got)
				}
			}
			if !strings.Contains(got, `\u001B`) {
				t.Errorf("the escaped form of ESC did not appear in %s mode: %q", mode, got)
			}
			if !strings.Contains(got, "/checkout") {
				t.Errorf("the harmless part of the path was lost in %s mode: %q", mode, got)
			}
		})
	}
}
