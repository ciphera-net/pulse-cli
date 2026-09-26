package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/credentials"
)

// meBody builds the wire shape of GET /me for TestAuthStatusLabelsTheTeamRow.
func meBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"organization": map[string]any{"id": "8a7cabce-4828-4663-8c77-67a9da11bc70", "name": nil},
			"key": map[string]any{
				"name":            "test",
				"last4":           "abcd",
				"expires_at":      "2026-11-05T00:00:00Z",
				"scope_all_sites": true,
				"site_ids":        []string{},
				"last_used_at":    nil,
			},
		},
		"meta": map[string]any{"suppressed": false},
	})
	if err != nil {
		t.Fatalf("marshal /me fixture: %v", err)
	}
	return body
}

// * PULSE-69 dropped "Organization" as the label for the team id row: the
// * server cannot know whether the reader works alone or on a team (PULSE-59
// * hides the word from someone who works alone), so a label that names a
// * container only a team has is wrong half the time. This pins the `auth
// * status` row so a reintroduced "Organization" fails here rather than
// * shipping. The environment variable stands in for a stored key so the test
// * never touches the real system keychain.
func TestAuthStatusLabelsTheTeamRowNotOrganization(t *testing.T) {
	t.Setenv(credentials.EnvVar, "pulse_sk_test_00000000000000000000000000")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(meBody(t))
	}))
	defer srv.Close()

	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}
	app := newBreakdownTestApp(render.ModeTable, out, errBuf, srv.URL)

	cmd := newAuthStatusCmd(app)
	if err := mustExecute(t, cmd); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "Organization") {
		t.Errorf("`auth status` still says Organization: %q", got)
	}
	if !strings.Contains(got, "Team") {
		t.Errorf("`auth status` does not label the team row: %q", got)
	}
}

// * The other half of the same change: the login prompt used to point at
// * "Settings → Organization → API Keys", a path that does not exist for
// * someone who works alone. apiKeyPrompt is what readKey's terminal branch
// * writes verbatim, so pinning the constant pins the printed text without
// * needing to fake a terminal.
func TestLoginPromptDoesNotNameAnOrganization(t *testing.T) {
	if strings.Contains(apiKeyPrompt, "Organization") {
		t.Errorf("login prompt still names Organization: %q", apiKeyPrompt)
	}
	want := "Paste your API key (create one in Settings, under API Keys):\n› "
	if apiKeyPrompt != want {
		t.Errorf("login prompt = %q, want %q", apiKeyPrompt, want)
	}
}
