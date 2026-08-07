package cli

import (
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/client"
	"github.com/ciphera-net/pulse-cli/internal/render"
)

func newRealtimeCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "realtime",
		Short: "Visitors active on a site right now",
		Long: "Visitors active on a site in the last five minutes.\n\n" +
			"The whole-site count is never withheld — one number describing an entire site\n" +
			"is a population, however small. The per-page breakdown is subject to the\n" +
			"privacy floor, so quiet sites show a count with no pages, which is expected\n" +
			"rather than a failure.\n\n" +
			"This endpoint accepts no filters, permanently: a filtered five-minute window\n" +
			"describes one person's current session.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			siteID, label, err := app.resolveSite(cmd.Context())
			if err != nil {
				return err
			}
			c, err := app.apiClient()
			if err != nil {
				return err
			}

			res, err := client.Realtime(cmd.Context(), c, siteID)
			if err != nil {
				return err
			}

			p := app.Printer
			if p.Mode == render.ModeJSON {
				p.JSON(res.Raw)
				return nil
			}

			if p.Mode == render.ModeCSV {
				rows := make([][]string, 0, len(res.Data.TopPaths))
				for _, tp := range res.Data.TopPaths {
					rows = append(rows, []string{tp.Path, strconv.Itoa(tp.Visitors)})
				}
				return p.CSVRecords([]string{"path", "visitors"}, rows)
			}

			noun := "visitors"
			if res.Data.Visitors == 1 {
				noun = "visitor"
			}
			p.Printf("  %s · %s %s right now\n",
				p.Bold(label), p.Bold(render.Thousands(res.Data.Visitors)), noun)

			if len(res.Data.TopPaths) > 0 {
				p.Printf("\n")
				t := render.Table{
					Headers: []string{"PAGE", "VISITORS"},
					Right:   []bool{false, true},
				}
				for _, tp := range res.Data.TopPaths {
					t.Rows = append(t.Rows, []string{tp.Path, render.Thousands(tp.Visitors)})
				}
				p.Table(t)
			}

			if note := render.SuppressionNote(res.Meta); note != "" {
				p.Printf("\n")
				p.Note("%s", note)
			}
			warnQuota(app, res.Header)
			return nil
		},
	}
	return cmd
}

// warnQuota surfaces an exhausted-ish budget before it becomes a 429.
//
// Only near the end, and only to stderr: a quota line on every command is noise
// that gets filtered out mentally, and then the one that mattered is filtered
// out too.
func warnQuota(app *App, h http.Header) {
	limit, err1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, err2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err1 != nil || err2 != nil || limit <= 0 {
		return
	}
	if remaining*20 <= limit { // 5% or less
		app.Printer.Warn("%s of %s quota units left this month.",
			render.Thousands(remaining), render.Thousands(limit))
	}
}
