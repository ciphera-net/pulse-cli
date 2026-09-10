package cli

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/render"
	"github.com/ciphera-net/pulse-client-go/client"
)

func newSitesCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sites",
		Short:   "List sites and choose a default",
		Aliases: []string{"site"},
	}
	cmd.AddCommand(newSitesListCmd(app), newSitesUseCmd(app))
	return cmd
}

func newSitesListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the sites this key can read",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.apiClient()
			if err != nil {
				return err
			}
			res, err := client.Sites(cmd.Context(), c)
			if err != nil {
				return err
			}

			p := app.Printer
			if p.Mode == render.ModeJSON {
				p.JSON(res.Raw)
				return nil
			}

			if len(res.Data) == 0 {
				p.Note("This key is not scoped to any site.")
				return nil
			}

			// * CSV gets machine columns, not the display table.
			// *
			// * The table shows "13 hr ago" because that is what a person reads at
			// * a glance; a spreadsheet needs the timestamp. And the column names
			// * are the API's own field names, so a CSV and a --json body describe
			// * the same site with the same words — the alternative is a user
			// * mapping DOMAIN to domain by eye and guessing at LAST EVENT.
			if p.Mode == render.ModeCSV {
				rows := make([][]string, 0, len(res.Data))
				for _, s := range res.Data {
					lastEvent := ""
					if s.LastEventAt != nil {
						lastEvent = s.LastEventAt.UTC().Format(time.RFC3339)
					}
					rows = append(rows, []string{
						s.ID, s.Slug, s.Domain, s.Name, s.Timezone,
						lastEvent, s.CreatedAt.UTC().Format(time.RFC3339),
					})
				}
				return p.CSVRecords(
					[]string{"id", "slug", "domain", "name", "timezone", "last_event_at", "created_at"},
					rows)
			}

			defaultID, _ := app.Config.SiteFor(app.Profile)
			t := render.Table{
				Headers: []string{"SLUG", "DOMAIN", "TIMEZONE", "LAST EVENT"},
				Right:   []bool{false, false, false, false},
			}
			for _, s := range res.Data {
				slug := s.Slug
				if s.ID == defaultID && p.Mode == render.ModeTable {
					// * Marking the default in the list is what makes `sites use`
					// * discoverable — otherwise the setting is invisible until
					// * something reads the wrong site.
					slug = "* " + slug
				}
				t.Rows = append(t.Rows, []string{
					slug,
					s.Domain,
					s.Timezone,
					// * LastEventAt is null when a site has never received an
					// * event, which is the first thing anyone wants to know about
					// * a list of sites. Rendered as "never", not as a zero date.
					render.Relative(s.LastEventAt),
				})
			}
			p.Table(t)

			if p.Mode == render.ModeTable && defaultID != "" {
				p.Note("* default site — change it with `pulse sites use <slug>`")
			}
			return nil
		},
	}
}

func newSitesUseCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "use <slug|domain|id>",
		Short: "Set the default site for later commands",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// * Resolved through --site's own path so that `sites use` accepts
			// * exactly what --site accepts. Two resolvers would drift, and the
			// * drift would show up as "--site ciphera.net works but
			// * `sites use ciphera.net` does not".
			app.SiteRef = strings.TrimSpace(args[0])
			id, label, err := app.resolveSite(cmd.Context())
			if err != nil {
				return err
			}

			app.Config.SetSiteFor(app.Profile, id, label)
			if err := app.Config.Save(); err != nil {
				return err
			}
			app.Printer.Success("Default site set to %s.", label)
			return nil
		},
	}
}
