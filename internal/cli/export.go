package cli

import (
	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/render"
)

func newExportCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Bulk export as CSV or JSON",
		Long: "Bulk export, rendered server-side.\n\n" +
			"These endpoints predate the {data, meta} envelope and keep their own contract:\n" +
			"CSV by default, or a bare JSON array with --json. Suppression arrives in\n" +
			"X-Pulse-Suppressed-* headers, because CSV has nowhere to put a meta object —\n" +
			"the CLI reports it on stderr so a redirected file stays valid CSV.\n\n" +
			"One export costs 20 quota units per week of range.",
	}
	cmd.AddCommand(
		newExportSubCmd(app, client.ExportDaily, "daily", "Daily totals, one row per day"),
		newExportSubCmd(app, client.ExportPages, "pages", "Top pages by pageviews"),
	)
	return cmd
}

func newExportSubCmd(app *App, kind client.ExportKind, use, short string) *cobra.Command {
	var last, from, to string
	var filterExprs []string
	var limit int

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := client.NewRange(last, from, to)
			if err != nil {
				return usageErr("%s", err.Error())
			}
			filters, err := parseFilters(filterExprs)
			if err != nil {
				return err
			}
			if kind == client.ExportDaily && len(filters) > 0 {
				// * export/daily takes no dimension filters. Refusing locally
				// * beats sending them and having them silently ignored, which
				// * would return whole-site figures under a heading that claims
				// * to be a slice.
				return usageErr("`export daily` does not accept --filter; use `export pages` or `stats` for a filtered view")
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

			// * The export endpoints require explicit from/to — they do not accept
			// * period=. Resolving a relative period locally would use the USER's
			// * timezone to answer a question about the SITE's, so the server
			// * resolves it: one extra request, and the dates come back as the
			// * ones the server itself would have used.
			fromDate, toDate, tz, err := client.ResolveExportRange(cmd.Context(), c, siteID, r)
			if err != nil {
				return err
			}

			format := client.ExportCSV
			if app.Printer.Mode == render.ModeJSON {
				format = client.ExportJSON
			}

			body, header, err := client.Export(cmd.Context(), c, siteID, kind,
				fromDate, toDate, format, limit, filters)
			if err != nil {
				return err
			}

			// * The body goes to stdout byte-for-byte. Re-parsing and re-emitting
			// * it would make the CLI a second formatter of a contract that is
			// * already published, and would drop any column added later.
			// *
			// * A short or refused write is kept by the stream and turns into a
			// * non-zero exit — an export is the one command where a truncated
			// * result is both most likely and least visible.
			app.Printer.Raw(body)

			// * Always say which site and which dates, on stderr. A CSV
			// * redirected to a file has no header identifying either, and
			// * "which site was this export from?" is the question asked of a
			// * month-old file. The timezone is only known when the server
			// * resolved the range for us; with explicit dates there is nothing
			// * to disambiguate, because the caller supplied the dates.
			p := app.Printer
			if tz != "" {
				p.Note("%s · %s to %s (%s)", label, fromDate, toDate, tz)
			} else {
				p.Note("%s · %s to %s", label, fromDate, toDate)
			}
			// * Both notes return "" for an explicit zero — the header said
			// * "nothing withheld", and a permanent privacy footer is noise.
			if s, ok := client.Suppressed(header); ok {
				if note := render.ExportSuppressionNote(s.Rows, s.Pageviews, s.MinCellSize); note != "" {
					p.Note("%s", note)
				}
				if note := render.ExportDayMetricsNote(s.DayMetrics, s.MinCellSize); note != "" {
					p.Note("%s", note)
				}
			}
			warnQuota(app, header)
			return nil
		},
	}

	cmd.Flags().StringVar(&last, "last", "", "relative period: 7d, 30d, month, year")
	cmd.Flags().StringVar(&from, "from", "", "start date, YYYY-MM-DD (with --to)")
	cmd.Flags().StringVar(&to, "to", "", "end date, YYYY-MM-DD (with --from)")
	if kind == client.ExportPages {
		cmd.Flags().IntVar(&limit, "limit", 0, "maximum rows to return")
		cmd.Flags().StringArrayVar(&filterExprs, "filter", nil,
			"narrow the query, e.g. country==BE (repeatable; max 2 dimensions)")
	}
	return cmd
}
