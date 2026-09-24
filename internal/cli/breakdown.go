package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-api-go/publicv1"
	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/client"
)

// tableValueWidth bounds a VALUE column in table mode only.
//
// A page path, referrer or UTM value is not length-limited on the way in — a
// referrer carrying a full query string is routine — and one absurd value
// should not push every other row out of alignment or wrap the terminal.
// CSV and --json keep the full value: a spreadsheet does not have an
// alignment problem, and --json is the API's own bytes, untouched.
const tableValueWidth = 80

func newBreakdownCmd(app *App) *cobra.Command {
	var last, from, to string
	var filterExprs []string
	var limit int

	cmd := &cobra.Command{
		Use:   "breakdown <dimension>",
		Short: "Rank a site's traffic by one dimension",
		Long: "Rank a site's traffic over a range by one dimension: the top pages, referrers,\n" +
			"countries and so on, largest first.\n\n" +
			"Unlike `stats`, this endpoint has NO privacy floor: every row is returned with\n" +
			"its real counts, including rows covering fewer than five visitors. Nothing\n" +
			"here is ever withheld or printed as " + render.Withheld + ".\n\n" +
			"Supported dimensions: " + strings.Join(publicv1.BreakdownDimensions(), ", ") + ".",
		Example: "  pulse breakdown page --last 7d\n" +
			"  pulse breakdown country --last 30d --limit 10\n" +
			"  pulse breakdown referrer --filter country==BE --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dimension := strings.TrimSpace(args[0])

			// * Validated locally, before anything else in this command touches
			// * a credential or the network — a bad dimension is a typo, not a
			// * reason to spend a request or ask for a keychain unlock.
			if err := client.CheckBreakdownDimension(dimension); err != nil {
				return usageErr("%s", err.Error())
			}
			if limit != 0 && (limit < 1 || limit > 100) {
				return usageErr("--limit must be between 1 and 100 (got %d)", limit)
			}

			r, err := client.NewRange(last, from, to)
			if err != nil {
				return usageErr("%s", err.Error())
			}
			filters, err := parseFilters(filterExprs)
			if err != nil {
				return err
			}

			// * Credential BEFORE site, deliberately.
			// *
			// * A user who has never authenticated also has no default site, and
			// * resolving the site first told them to run `pulse sites use` —
			// * which cannot work, because listing sites needs a credential. It
			// * also exited 2 where the honest answer is 3. Found by the CI smoke
			// * step running the real binary in a container with neither.
			c, err := app.apiClient()
			if err != nil {
				return err
			}
			siteID, label, err := app.resolveSite(cmd.Context())
			if err != nil {
				return err
			}

			res, err := client.Breakdown(cmd.Context(), c, siteID, dimension, r, filters, limit)
			if err != nil {
				return err
			}

			p := app.Printer
			if p.Mode == render.ModeJSON {
				p.JSON(res.Raw)
				return nil
			}

			// * Region is the one dimension whose rows carry a country — a region
			// * name alone is ambiguous ("Limburg" is a province of both Belgium
			// * and the Netherlands). Keyed on the dimension the request asked
			// * for, not on whether a row happens to have one, so the column
			// * appears consistently even when the result is empty.
			hasCountry := dimension == publicv1.DimensionRegion

			if p.Mode == render.ModeCSV {
				headers := []string{"value", "visitors", "pageviews"}
				if hasCountry {
					headers = []string{"value", "country", "visitors", "pageviews"}
				}
				rows := make([][]string, 0, len(res.Data.Rows))
				for _, row := range res.Data.Rows {
					// * CSV keeps the FULL sanitised value — no truncation. A
					// * spreadsheet does not have a fixed-width alignment
					// * problem, and cutting data out of an export a script may
					// * depend on is a worse failure than a wide column.
					value := render.SanitizeControl(row.Value)
					if hasCountry {
						rows = append(rows, []string{value, sanitizedCountry(row), strconv.Itoa(row.Visitors), strconv.Itoa(row.Pageviews)})
					} else {
						rows = append(rows, []string{value, strconv.Itoa(row.Visitors), strconv.Itoa(row.Pageviews)})
					}
				}
				return p.CSVRecords(headers, rows)
			}

			// * meta.range is what the SERVER queried, not the flags typed — see
			// * stats.go for why: it differs whenever a period was resolved or
			// * the site's timezone is not the terminal's.
			p.Printf("  %s · %s · %s\n\n", p.Bold(label), dimension, client.DescribeRange(res.Meta.Range))

			if len(res.Data.Rows) == 0 {
				p.Note("No traffic for %s in this range.", dimension)
				warnQuota(app, res.Header)
				return nil
			}

			t := render.Table{
				Headers: []string{"VALUE", "VISITORS", "PAGEVIEWS"},
				Right:   []bool{false, true, true},
			}
			if hasCountry {
				t.Headers = []string{"VALUE", "COUNTRY", "VISITORS", "PAGEVIEWS"}
				t.Right = []bool{false, false, true, true}
			}
			for _, row := range res.Data.Rows {
				// * Sanitise FIRST, truncate SECOND: the length bound is on what
				// * actually gets displayed, and truncating raw input first could
				// * cut a dangerous multi-byte sequence in half instead of
				// * removing it whole.
				value := render.Truncate(render.SanitizeControl(row.Value), tableValueWidth)
				if hasCountry {
					t.Rows = append(t.Rows, []string{value, sanitizedCountry(row), render.Thousands(row.Visitors), render.Thousands(row.Pageviews)})
				} else {
					t.Rows = append(t.Rows, []string{value, render.Thousands(row.Visitors), render.Thousands(row.Pageviews)})
				}
			}
			p.Table(t)

			warnQuota(app, res.Header)
			return nil
		},
	}

	cmd.Flags().StringVar(&last, "last", "", "relative period: 7d, 30d, month, year")
	cmd.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (with --to)")
	cmd.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (with --from)")
	cmd.Flags().StringArrayVar(&filterExprs, "filter", nil,
		"narrow the query, e.g. country==BE (repeatable; max 2 dimensions)")
	cmd.Flags().IntVar(&limit, "limit", 0, "rows to return, 1-100 (default: the server's, 20)")
	return cmd
}

// sanitizedCountry reads a region row's country, which is a short ISO code
// from our own GeoIP lookup rather than visitor-supplied text — but it still
// passes through SanitizeControl, because the field is a plain string on the
// wire and defending only the columns we currently believe are risky is how a
// sanitiser goes stale the day a new source starts filling it.
func sanitizedCountry(row publicv1.BreakdownRow) string {
	if row.Country == nil {
		return ""
	}
	return render.SanitizeControl(*row.Country)
}
