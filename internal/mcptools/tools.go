// Package mcptools holds the tool layer: what each tool is called, what it
// promises, what it accepts, and what it does.
//
// It imports no transport and no MCP SDK. That is enforced in CI rather than
// remembered (see .woodpecker/test.yml), and it is the seam that makes today's
// local stdio server evidence about a future hosted one: the handlers cannot
// tell which is calling them, so exercising one exercises both.
//
// Handlers take their dependencies explicitly through Deps. Nothing here reads
// an environment variable, a keychain, or a package global — a handler that did
// would work in stdio mode and be wrong in a server handling many callers.
package mcptools

import (
	"context"
	"fmt"
	"strings"

	"github.com/ciphera-net/pulse-cli/internal/mcpwrap"
	"github.com/ciphera-net/pulse-client-go/client"
)

// Deps is everything a handler is allowed to reach.
type Deps struct {
	Client *client.Client
}

// Class describes what happens if a call was not what the customer meant.
//
// Only Read exists today. The type is here because the classification is a
// property of the TOOL, and adding a write tool must mean choosing its class at
// the point of definition rather than discovering later that nobody did.
type Class int

const (
	// ClassRead does not modify anything.
	ClassRead Class = iota
	// ClassReversible is a configuration change another call can undo.
	ClassReversible
	// ClassDestructiveRecoverable destroys something a documented path restores.
	ClassDestructiveRecoverable
	// ClassIrreversible cannot be undone, or rewrites history.
	ClassIrreversible
)

// ReadOnly reports whether a class only reads.
func (c Class) ReadOnly() bool { return c == ClassRead }

// Definition is a tool's identity, independent of any protocol.
type Definition struct {
	Name        string
	Description string
	Class       Class
}

// Definitions is the registry. A registration-time test walks it and asserts
// every entry's protocol annotations match its Class, so a tool whose class and
// hints disagree fails the build rather than misleading a host.
var Definitions = []Definition{
	{Name: "pulse_whoami", Class: ClassRead, Description: "" +
		"Describe the Pulse API credential in use: which organization it belongs to and which " +
		"sites it can read. Call this first when unsure what access is available."},

	{Name: "pulse_list_sites", Class: ClassRead, Description: "" +
		"List the sites this credential can read, with each site's UUID, domain, timezone and " +
		"whether it has ever received an event. Every other tool takes the site_id (UUID) from " +
		"here; domains and slugs are not accepted as identifiers."},

	{Name: "pulse_get_stats", Class: ClassRead, Description: "" +
		"Aggregate analytics for one site over a date range: visitors, pageviews, bounce rate, " +
		"average duration, scroll depth and visible duration. " +
		"Use either period, or from and to — never both. " +
		"The server resolves the range in the SITE's timezone and echoes the resolved dates in " +
		"meta.range: quote those dates when describing the period, and never compute a range " +
		"yourself. " +
		"If the result says suppressed, the figures were withheld by a privacy floor and are " +
		"NOT zero — read the suppression object and repeat what it says."},

	{Name: "pulse_get_realtime", Class: ClassRead, Description: "" +
		"Visitors active on a site in the last five minutes, with a breakdown by page. The " +
		"site-wide number is always reported; individual page rows may be withheld by the " +
		"privacy floor, in which case the visible rows will not sum to the total. That is " +
		"correct behaviour, not a discrepancy to explain away."},

	{Name: "pulse_export_daily", Class: ClassRead, Description: "" +
		"Bulk CSV export of per-day totals for one site between two dates. Requires explicit " +
		"from and to — this endpoint does not accept a relative period. Some day buckets may " +
		"have their per-session metrics withheld, arriving as EMPTY CELLS which mean withheld, " +
		"never zero."},

	{Name: "pulse_export_pages", Class: ClassRead, Description: "" +
		"Bulk CSV export of per-page totals for one site between two dates. Requires explicit " +
		"from and to. Pages seen by fewer than the floor's visitor count are withheld entirely, " +
		"so the listed rows will not sum to the site total."},
}

// SiteArgs identifies a site.
type SiteArgs struct {
	SiteID string `json:"site_id" jsonschema:"the site's UUID, taken from pulse_list_sites"`
}

// StatsArgs is a ranged, optionally filtered query.
type StatsArgs struct {
	SiteID  string   `json:"site_id" jsonschema:"the site's UUID, taken from pulse_list_sites"`
	Period  string   `json:"period,omitempty" jsonschema:"relative period: 7d, 30d, month or year. Mutually exclusive with from/to"`
	From    string   `json:"from,omitempty" jsonschema:"start date YYYY-MM-DD, inclusive. Requires to"`
	To      string   `json:"to,omitempty" jsonschema:"end date YYYY-MM-DD, inclusive. Requires from"`
	Filters []string `json:"filters,omitempty" jsonschema:"filters as dimension==value or dimension!=value, at most 2 distinct dimensions"`
}

// ExportArgs is a bulk export request.
type ExportArgs struct {
	SiteID string `json:"site_id" jsonschema:"the site's UUID, taken from pulse_list_sites"`
	From   string `json:"from" jsonschema:"start date YYYY-MM-DD, inclusive. Required"`
	To     string `json:"to" jsonschema:"end date YYYY-MM-DD, inclusive. Required"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum rows for the pages export"`
}

// NoArgs is an empty parameter set.
type NoArgs struct{}

// validSiteID accepts a canonical UUID and nothing else.
//
// This is a correctness control and a security one at the same time. site_id is
// interpolated into a URL path, so anything that is not a UUID — a slug, a
// domain, "../../admin" — must be refused BEFORE a request is built, not after
// the server answers. Rejecting locally also means a traversal attempt costs
// zero API calls and leaves no trace of an attempt at the boundary.
func validSiteID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// requireSiteID validates and explains, naming the tool that supplies the value.
func requireSiteID(s string) error {
	if validSiteID(s) {
		return nil
	}
	if strings.Contains(s, ".") || strings.Contains(s, "/") {
		return fmt.Errorf("site_id must be a UUID, not a domain or path (got %q) — "+
			"call pulse_list_sites and use the site_id field", s)
	}
	return fmt.Errorf("site_id must be a UUID (got %q) — call pulse_list_sites and use the "+
		"site_id field", s)
}

// buildRange reuses the CLI's validation so there is one definition of a valid
// range, and restates the parameter names in this surface's vocabulary.
func buildRange(period, from, to string) (client.Range, error) {
	r, err := client.NewRange(period, from, to)
	if err != nil {
		return client.Range{}, fmt.Errorf("invalid range — supply either period, or both from "+
			"and to, never both forms: %w", err)
	}
	return r, nil
}

// buildFilters parses and caps the filter expressions.
func buildFilters(exprs []string) ([]client.Filter, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	out := make([]client.Filter, 0, len(exprs))
	for _, e := range exprs {
		f, err := client.ParseFilter(e)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := client.CheckFilterDimensions(out); err != nil {
		return nil, err
	}
	return out, nil
}

// Whoami describes the calling credential.
func Whoami(ctx context.Context, d Deps, _ NoArgs) (map[string]any, error) {
	res, err := client.Me(ctx, d.Client)
	if err != nil {
		return nil, err
	}
	return mcpwrap.Me(res), nil
}

// ListSites lists readable sites.
func ListSites(ctx context.Context, d Deps, _ NoArgs) (map[string]any, error) {
	res, err := client.Sites(ctx, d.Client)
	if err != nil {
		return nil, err
	}
	return mcpwrap.Sites(res), nil
}

// GetStats returns aggregates for a range.
func GetStats(ctx context.Context, d Deps, a StatsArgs) (map[string]any, error) {
	if err := requireSiteID(a.SiteID); err != nil {
		return nil, err
	}
	r, err := buildRange(a.Period, a.From, a.To)
	if err != nil {
		return nil, err
	}
	filters, err := buildFilters(a.Filters)
	if err != nil {
		return nil, err
	}
	res, err := client.Stats(ctx, d.Client, a.SiteID, r, filters)
	if err != nil {
		return nil, err
	}
	return mcpwrap.Stats(res), nil
}

// GetRealtime returns the live view.
func GetRealtime(ctx context.Context, d Deps, a SiteArgs) (map[string]any, error) {
	if err := requireSiteID(a.SiteID); err != nil {
		return nil, err
	}
	res, err := client.Realtime(ctx, d.Client, a.SiteID)
	if err != nil {
		return nil, err
	}
	return mcpwrap.Realtime(res), nil
}

// ExportDaily returns per-day totals as CSV.
func ExportDaily(ctx context.Context, d Deps, a ExportArgs) (map[string]any, error) {
	return export(ctx, d, a, client.ExportDaily)
}

// ExportPages returns per-page totals as CSV.
func ExportPages(ctx context.Context, d Deps, a ExportArgs) (map[string]any, error) {
	return export(ctx, d, a, client.ExportPages)
}

func export(ctx context.Context, d Deps, a ExportArgs, kind client.ExportKind) (map[string]any, error) {
	if err := requireSiteID(a.SiteID); err != nil {
		return nil, err
	}
	// These endpoints take no period, so from/to are mandatory rather than
	// merely paired. Saying so here beats letting the server answer 400.
	if a.From == "" || a.To == "" {
		return nil, fmt.Errorf("from and to are both required for an export — this endpoint " +
			"does not accept a relative period")
	}
	if _, err := buildRange("", a.From, a.To); err != nil {
		return nil, err
	}

	body, header, err := client.Export(ctx, d.Client, a.SiteID, kind, a.From, a.To,
		client.ExportCSV, a.Limit, nil)
	if err != nil {
		return nil, err
	}
	sup, seen := client.Suppressed(header)
	return mcpwrap.Export(string(body), sup, seen), nil
}

// Lookup finds a definition by name.
func Lookup(name string) (Definition, bool) {
	for _, d := range Definitions {
		if d.Name == name {
			return d, true
		}
	}
	return Definition{}, false
}
