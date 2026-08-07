// Package cli wires the command surface.
//
// Nouns match the API's resources: someone who has read the API docs can guess
// the commands, and someone who has used the CLI can guess the endpoints. That
// symmetry is worth more than shorter commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/config"
	"github.com/ciphera-net/pulse-cli/internal/credentials"
	"github.com/ciphera-net/pulse-cli/internal/render"
)

// Version is stamped at build time.
var Version = "dev"

// App is the state every command shares.
type App struct {
	Printer *render.Printer
	Config  *config.Config
	Profile string
	SiteRef string // --site, as typed: slug, domain or UUID

	client *client.Client

	// started records that PersistentPreRunE ran, which is what separates a
	// bad invocation from a failed one.
	//
	// Cobra's order is Find → ParseFlags → ValidateArgs → PersistentPreRunE →
	// RunE. So anything that fails before this flips is cobra rejecting what was
	// typed — an unknown command, an unknown flag, the wrong number of
	// arguments — and all of those are exit 2. Anything after it is the command
	// actually running, and errors there are already typed. Using the phase
	// rather than matching on cobra's message text means the classification does
	// not break when cobra rewords an error.
	started bool
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	app := &App{}
	var asJSON, asCSV bool

	root := &cobra.Command{
		Use:   "pulse",
		Short: "Read your Pulse analytics from the terminal",
		Long: "Pulse CLI — read-only access to your Pulse analytics.\n\n" +
			"The API is aggregates-only and so is this tool: there are no write commands,\n" +
			"and no command returns per-visitor data.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON && asCSV {
				return usageErr("--json and --csv both set; pick one")
			}
			mode := render.ModeTable
			switch {
			case asJSON:
				mode = render.ModeJSON
			case asCSV:
				mode = render.ModeCSV
			}
			app.Printer = render.NewPrinter(mode)

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			app.Config = cfg
			app.started = true
			return nil
		},
	}

	root.PersistentFlags().StringVar(&app.Profile, "profile", credentials.DefaultProfile,
		"credential profile to use")
	root.PersistentFlags().StringVar(&app.SiteRef, "site", "",
		"site to query: slug, domain or id (default: `pulse sites use`)")
	root.PersistentFlags().BoolVar(&asJSON, "json", false,
		"print the API response exactly as returned")
	root.PersistentFlags().BoolVar(&asCSV, "csv", false,
		"print as CSV")

	root.AddCommand(
		newAuthCmd(app),
		newSitesCmd(app),
		newStatsCmd(app),
		newRealtimeCmd(app),
		newExportCmd(app),
	)

	if err := root.Execute(); err != nil {
		return report(app.Printer, err, app.started)
	}
	return client.ExitOK
}

// report prints an error the way a person can act on and returns its exit code.
func report(p *render.Printer, err error, started bool) int {
	if p == nil {
		p = render.NewPrinter(render.ModeTable)
	}

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		fmt.Fprintf(p.Err, "%s %s\n", mark(p), apiErr.Error())
		if hint := hintFor(apiErr); hint != "" {
			fmt.Fprintf(p.Err, "  %s\n", p.Dim(hint))
		}
		return apiErr.ExitCode()
	}

	var ce *cliError
	if errors.As(err, &ce) {
		fmt.Fprintf(p.Err, "%s %s\n", mark(p), ce.msg)
		return ce.code
	}

	fmt.Fprintf(p.Err, "%s %v\n", mark(p), err)
	if !started {
		// * Cobra refused the invocation before the command ran: unknown
		// * command, unknown flag, wrong argument count. That is bad usage, and
		// * a script must be able to tell it apart from a server failure.
		return client.ExitInvalidInput
	}
	return client.ExitServerError
}

func mark(p *render.Printer) string {
	if p.Color {
		return "\x1b[31m✗\x1b[0m"
	}
	return "✗"
}

// hintFor turns the API's refusal into the next thing to try. The API's own
// messages explain what happened; these explain what to do about it.
func hintFor(e *client.APIError) string {
	switch e.Code {
	case "invalid_api_key":
		return "Run `pulse auth login` to store a key, or check that " + credentials.EnvVar + " is set correctly."
	case "site_not_found":
		return "Run `pulse sites ls` to see what this key can read."
	case "too_many_filter_dimensions":
		return "Drop a --filter, or widen one: repeating a single dimension is free."
	case "unknown_period":
		return "Use --from and --to for ranges the API does not name."
	case "browser_origin_not_supported":
		return "This should not happen from the CLI — please report it."
	}
	if e.Status == 429 {
		return "Your key's quota is exhausted. `pulse auth status` shows what is left."
	}
	return ""
}

// client builds an authenticated client, resolving the credential once.
func (a *App) apiClient() (*client.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	cred, err := credentials.Load(a.Profile)
	if err != nil {
		if errors.Is(err, credentials.ErrNoCredential) {
			return nil, notAuthenticated(err)
		}
		return nil, err
	}

	base := strings.TrimSpace(os.Getenv("PULSE_API_URL"))
	c := client.New(base, cred.Key, "pulse-cli/"+Version)
	c.Notify = func(msg string) { a.Printer.Note("%s", msg) }
	a.client = c
	return c, nil
}

// resolveSite turns --site (or the stored default) into a site id.
//
// A UUID is used as-is, which costs nothing. Anything else is matched against
// /sites by slug, domain or name — one request, and worth it: the live slugs are
// derived from the domain (`ciphera-net`, `pulse-ciphera-net`), so the name a
// user reaches for first is usually the domain rather than the slug.
//
// Paths stay UUID-only on the wire. A slug is user-editable, and putting a
// mutable string in a URL means every stored command breaks the day someone
// renames a site.
func (a *App) resolveSite(ctx context.Context) (id, label string, err error) {
	ref := strings.TrimSpace(a.SiteRef)
	if ref == "" {
		id, label = a.Config.SiteFor(a.Profile)
		if id == "" {
			return "", "", usageErr("no site selected\n\n  Run `pulse sites ls` to see your sites, then `pulse sites use <slug>`,\n  or pass --site <slug|domain|id>.")
		}
		return id, label, nil
	}
	if uuidRE.MatchString(ref) {
		return ref, ref, nil
	}

	c, err := a.apiClient()
	if err != nil {
		return "", "", err
	}
	res, err := client.Sites(ctx, c)
	if err != nil {
		return "", "", err
	}

	needle := strings.ToLower(ref)
	for _, s := range res.Data {
		if strings.ToLower(s.Slug) == needle ||
			strings.ToLower(s.Domain) == needle ||
			strings.ToLower(s.Name) == needle {
			return s.ID, s.Domain, nil
		}
	}

	available := make([]string, 0, len(res.Data))
	for _, s := range res.Data {
		available = append(available, s.Slug)
	}
	return "", "", notFoundErr("no site %q, or this key is not scoped to it\n\n  This key can read: %s",
		ref, strings.Join(available, ", "))
}

// cliError is a failure detected locally, carrying the exit code it deserves.
//
// The code is part of the error rather than assumed at the top, because a
// locally-detected failure has to exit with the SAME code the API would have
// produced for the same condition. A missing credential is exit 3 whether the
// CLI noticed it or the server did, and a site that does not exist is exit 4
// either way — otherwise `pulse` saving a round trip silently changes what a
// script sees, and the optimisation becomes a behaviour change.
type cliError struct {
	msg  string
	code int
}

func (e *cliError) Error() string { return e.msg }

// usageErr is a mistake in what the user typed: exit 2.
func usageErr(format string, args ...any) error {
	return &cliError{msg: fmt.Sprintf(format, args...), code: client.ExitInvalidInput}
}

// notAuthenticated is the one message for "there is no usable credential".
//
// It always exits 3, including when the keychain could not be consulted at all.
// A CI container has no Secret Service on the session bus, so go-keyring fails
// with "dbus-launch: executable file not found" — measured by running the real
// linux/amd64 binary in an Alpine container, where it exited 1 and told a script
// the tool was broken rather than that it was unauthenticated.
//
// The keychain's own reason is appended when there is one, because "there is no
// keychain on this machine" and "your keychain is locked" call for different
// responses and only the reason distinguishes them.
func notAuthenticated(cause error) error {
	msg := "not authenticated\n\n  Run `pulse auth login` to store a key in your keychain,\n" +
		"  or export " + credentials.EnvVar + " for a CI environment."

	var unavailable *credentials.UnavailableError
	if errors.As(cause, &unavailable) {
		// * The reason already reads as a sentence ("could not read the system
		// * keychain: …"), so it is appended bare rather than introduced again.
		msg += "\n\n  " + unavailable.Reason.Error()
	}
	return authErr("%s", msg)
}

// authErr is a missing or unusable credential: exit 3, the same as the API's
// unauthorized, so `[ $? -eq 3 ] && pulse auth login` works either way.
func authErr(format string, args ...any) error {
	return &cliError{msg: fmt.Sprintf(format, args...), code: client.ExitUnauthorized}
}

// notFoundErr is a site that does not exist or is out of this key's scope:
// exit 4, matching the API's not_found for the same condition.
func notFoundErr(format string, args ...any) error {
	return &cliError{msg: fmt.Sprintf(format, args...), code: client.ExitNotFound}
}
