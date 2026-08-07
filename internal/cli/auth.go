package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ciphera-net/pulse-api-go/publicv1"
	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/credentials"
	"github.com/ciphera-net/pulse-cli/internal/render"
)

func newAuthCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage the API key in your system keychain",
	}
	cmd.AddCommand(newAuthLoginCmd(app), newAuthLogoutCmd(app), newAuthStatusCmd(app))
	return cmd
}

func newAuthLoginCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Store an API key in the system keychain",
		Long: "Store an API key in the operating system keychain.\n\n" +
			"The key is read from the terminal without echoing and written to the macOS\n" +
			"Keychain, libsecret, or the Windows Credential Manager. This tool never\n" +
			"writes a key to a file.\n\n" +
			"Create a key at Settings → Organization → API Keys.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, err := readKey(app)
			if err != nil {
				return err
			}
			if key == "" {
				return usageErr("no key entered")
			}
			if !credentials.LooksLikeKey(key) {
				return usageErr("that does not look like a Pulse API key (they start with `pulse_sk_`)\n\n" +
					"  Check for a stray quote or prompt character in what was pasted.")
			}

			// * Validate BEFORE storing. Writing an unusable key to the keychain
			// * and reporting success turns the next command's 401 into a puzzle
			// * about the wrong thing.
			probe := client.New(strings.TrimSpace(os.Getenv("PULSE_API_URL")), key, "pulse-cli/"+Version)
			me, err := client.Me(cmd.Context(), probe)
			if err != nil {
				return err
			}

			if err := credentials.Store(app.Profile, key); err != nil {
				return err
			}

			p := app.Printer
			// * Not `Authenticated as "Acme"`: organization.name is null, and
			// * stays null until Ciphera ID pushes a name on the member-sync path.
			// * The key's own name is both true and more useful — it is what
			// * distinguishes two keys in the same organization.
			// *
			// * last4 comes from the API, not from a local slice of the
			// * credential: measured against a live key those are different
			// * strings, and the API's is the one the dashboard shows.
			p.Success("Stored key %q (…%s) in the %s.",
				me.Data.Key.Name, me.Data.Key.Last4, keychainName())
			p.Note("Organization %s · %s · expires %s",
				me.Data.Organization.ID, scopeDescription(me.Data.Key), render.Expiry(me.Data.Key.ExpiresAt))

			if id, _ := app.Config.SiteFor(app.Profile); id == "" {
				p.Note("Next: `pulse sites ls`, then `pulse sites use <slug>`.")
			}
			return nil
		},
	}
}

func newAuthLogoutCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored API key",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := credentials.Delete(app.Profile); err != nil {
				return err
			}
			app.Printer.Success("Removed the stored key for profile %q.", app.Profile)
			if os.Getenv(credentials.EnvVar) != "" {
				// * The keychain is empty but the environment is not, so the next
				// * command still authenticates. Reporting "logged out" without
				// * saying so would be false.
				app.Printer.Warn("%s is still set in this shell and takes precedence — you are not logged out here.",
					credentials.EnvVar)
			}
			return nil
		},
	}
}

func newAuthStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which key is in use and what it can read",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cred, err := credentials.Load(app.Profile)
			if err != nil {
				if errors.Is(err, credentials.ErrNoCredential) {
					return notAuthenticated(err)
				}
				return err
			}

			c, err := app.apiClient()
			if err != nil {
				return err
			}
			res, err := client.Me(cmd.Context(), c)
			if err != nil {
				return err
			}

			p := app.Printer
			if p.Mode == render.ModeJSON {
				p.JSON(res.Raw)
				return nil
			}

			rows := [][2]string{
				{"Organization", res.Data.Organization.ID},
				{"Key", fmt.Sprintf("%s  ·  …%s", res.Data.Key.Name, res.Data.Key.Last4)},
				{"Expires", render.Expiry(res.Data.Key.ExpiresAt)},
				{"Scope", scopeDescription(res.Data.Key)},
				{"Last used", render.Relative(res.Data.Key.LastUsedAt)},
				{"Stored in", string(cred.Source)},
			}
			if id, label := app.Config.SiteFor(app.Profile); id != "" {
				rows = append(rows, [2]string{"Default site", label})
			}
			if limit, remaining, ok := res.Quota(); ok {
				rows = append(rows, [2]string{"Quota",
					fmt.Sprintf("%s of %s units remaining", render.Thousands(remaining), render.Thousands(limit))})
			}
			p.KeyValue(rows)

			if cred.Source == credentials.SourceEnv {
				p.Note("Using %s from the environment, which takes precedence over the keychain.", credentials.EnvVar)
			}
			return nil
		},
	}
}

// readKey prompts for a key without echoing it.
//
// When stdin is not a terminal the key is read from the pipe instead — which is
// what makes `pass show pulse/api | pulse auth login` work — and no prompt is
// printed, because a prompt written into a pipe is noise.
func readKey(app *App) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading key from stdin: %w", err)
		}
		return strings.TrimSpace(line), nil
	}

	fmt.Fprint(app.Printer.Err(), "Paste your API key (create one at Settings → Organization → API Keys):\n› ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(app.Printer.Err())
	if err != nil {
		return "", fmt.Errorf("reading key: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// scopeDescription says what a key may read.
//
// site_ids is empty when scope_all_sites is true — the schema enforces that the
// two are never both set — so the count is only meaningful in the scoped case.
func scopeDescription(k publicv1.Key) string {
	if k.ScopeAllSites {
		return "all sites"
	}
	if len(k.SiteIDs) == 1 {
		return "1 site"
	}
	return fmt.Sprintf("%d sites", len(k.SiteIDs))
}

func keychainName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS Keychain"
	case "windows":
		return "Windows Credential Manager"
	default:
		return "system keyring"
	}
}
