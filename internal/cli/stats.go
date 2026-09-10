package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/client"
)

func newStatsCmd(app *App) *cobra.Command {
	var last, from, to string
	var filterExprs []string

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate metrics for a site over a range",
		Long: "Aggregate metrics for a site over a range.\n\n" +
			"Filters narrow the query, and any filter engages the privacy floor: a slice\n" +
			"covering fewer than five visitors is withheld entirely, including a genuine\n" +
			"zero. A withheld metric prints as " + render.Withheld + " and never as 0.",
		Example: "  pulse stats --last 7d\n" +
			"  pulse stats --last 30d --filter country==BE\n" +
			"  pulse stats --from 2026-08-01 --to 2026-08-07 --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			res, err := client.Stats(cmd.Context(), c, siteID, r, filters)
			if err != nil {
				return err
			}

			p := app.Printer
			if p.Mode == render.ModeJSON {
				p.JSON(res.Raw)
				return nil
			}

			// * CSV carries the API's field names and raw values; the table
			// * carries human labels and formatted ones. A spreadsheet cannot
			// * compute on "1m 47s" or "62.4%", and a reader does not want
			// * 107.3333. Same numbers, two audiences.
			// *
			// * A withheld metric is an EMPTY cell in CSV — not 0, and not "—",
			// * which a spreadsheet would import as text and refuse to sum. Empty
			// * is the one value every tool already reads as "no data".
			if p.Mode == render.ModeCSV {
				return p.CSVRecords([]string{"metric", "value"}, [][]string{
					{"visitors", csvInt(res.Data.Visitors)},
					{"pageviews", csvInt(res.Data.Pageviews)},
					{"bounce_rate", csvFloat(res.Data.BounceRate)},
					{"avg_duration", csvFloat(res.Data.AvgDuration)},
					{"avg_scroll_depth", csvFloat(res.Data.AvgScrollDepth)},
					{"avg_visible_duration", csvFloat(res.Data.AvgVisibleDuration)},
				})
			}

			rows := [][]string{
				{"Visitors", render.Metric(res.Data.Visitors)},
				{"Pageviews", render.Metric(res.Data.Pageviews)},
				{"Bounce rate", render.Percent(res.Data.BounceRate)},
				{"Avg duration", render.Duration(res.Data.AvgDuration)},
				{"Avg scroll depth", render.Percent(res.Data.AvgScrollDepth)},
				{"Avg visible time", render.Duration(res.Data.AvgVisibleDuration)},
			}

			// * The header prints meta.range — what the SERVER queried — not the
			// * flags that were typed. They differ whenever a period was resolved
			// * or the site's timezone is not the terminal's, and printing the
			// * local guess is the drift server-side resolution exists to remove.
			p.Printf("  %s · %s\n\n", p.Bold(label), client.DescribeRange(res.Meta.Range))

			pairs := make([][2]string, 0, len(rows))
			for _, r := range rows {
				pairs = append(pairs, [2]string{r[0], r[1]})
			}
			p.KeyValue(pairs)

			if note := render.SuppressionNote(res.Meta); note != "" {
				p.Printf("\n")
				p.Note("%s", note)
			}
			warnQuota(app, res.Header)
			return nil
		},
	}

	cmd.Flags().StringVar(&last, "last", "", "relative period: 7d, 30d, month, year")
	cmd.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (with --to)")
	cmd.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (with --from)")
	cmd.Flags().StringArrayVar(&filterExprs, "filter", nil,
		"narrow the query, e.g. country==BE (repeatable; max 2 dimensions)")
	return cmd
}

// parseFilters converts the repeated --filter flags and checks the dimension cap
// before a request is spent.
func parseFilters(exprs []string) ([]client.Filter, error) {
	if len(exprs) == 0 {
		return nil, nil
	}
	filters := make([]client.Filter, 0, len(exprs))
	for _, expr := range exprs {
		f, err := client.ParseFilter(expr)
		if err != nil {
			return nil, usageErr("%s", err.Error())
		}
		filters = append(filters, f)
	}
	if err := client.CheckFilterDimensions(filters); err != nil {
		return nil, usageErr("%s", err.Error())
	}
	return filters, nil
}

// csvInt and csvFloat render a possibly-withheld metric for CSV.
//
// A withheld value is an EMPTY cell. Not 0, which is a number the server refused
// to state; and not the em dash the table uses, which a spreadsheet imports as
// text and then refuses to sum — turning a privacy marker into a broken column.
// Empty is what every tool already reads as "no data".
func csvInt(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func csvFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
