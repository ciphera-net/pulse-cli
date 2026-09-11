package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ciphera-net/pulse-cli/internal/mcptools"
)

// newMCPCmd serves the tool layer over stdio as a Model Context Protocol server.
//
// This file is the ONLY place in the repository that imports the MCP SDK, and
// CI asserts it: internal/mcptools and internal/mcpwrap must compile with the
// SDK absent from their import graph. The seam is what makes this command a
// rehearsal for a hosted server rather than a detour — the handlers below the
// seam cannot tell which transport called them.
//
// 🔴 STDOUT IS THE PROTOCOL CHANNEL HERE. Every byte written to it must be a
// JSON-RPC frame. The printer already routes notes, warnings and successes to
// stderr, which is why that discipline (built so `pulse export | head` stays
// clean) is what makes this mode possible at all — but any future handler that
// reaches for fmt.Println breaks the session in a way that reads to a host as a
// protocol error, not as a stray line.
func newMCPCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve Pulse as a Model Context Protocol server (stdio)",
		Long: "Serve the read-only Pulse tools to an AI assistant over stdio.\n\n" +
			"Add to your MCP host's configuration:\n\n" +
			"  {\"mcpServers\": {\"pulse\": {\"command\": \"pulse\", \"args\": [\"mcp\"]}}}\n\n" +
			"No API key belongs in that file. The server reads the credential\n" +
			"`pulse auth login` already stored, so the key stays in your keychain\n" +
			"rather than in a config file that tends to end up in a repository.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server := mcp.NewServer(&mcp.Implementation{
				Name:    "pulse",
				Version: Version,
			}, &mcp.ServerOptions{
				Instructions: "Read-only access to Pulse analytics. Two rules govern every " +
					"result. First: a result marked suppressed had its figures withheld by a " +
					"privacy floor — that is NEVER zero and never 'no data'; repeat what the " +
					"suppression object says. Second: quote the dates in meta.range rather than " +
					"computing a range, because ranges resolve in the site's timezone, not yours.",
			})

			register(server, app, "pulse_whoami", mcptools.Whoami)
			register(server, app, "pulse_list_sites", mcptools.ListSites)
			register(server, app, "pulse_get_stats", mcptools.GetStats)
			register(server, app, "pulse_get_realtime", mcptools.GetRealtime)
			register(server, app, "pulse_export_daily", mcptools.ExportDaily)
			register(server, app, "pulse_export_pages", mcptools.ExportPages)

			// * A host that disconnects closes our stdin, which surfaces as EOF.
			// * That is how an MCP session ENDS, not how it fails: reporting it
			// * as an error would mean every normal shutdown exits non-zero and
			// * every host that supervises the process logs a crash it did not
			// * have. A context cancellation (the user pressing Ctrl-C) is the
			// * same kind of event.
			err := server.Run(cmd.Context(), &mcp.StdioTransport{})
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	return cmd
}

// register wires one handler to the SDK, taking its identity and annotations
// from the registry rather than from the call site.
//
// Annotations are DERIVED from the tool's declared class, never hand-written
// here. A hand-written readOnlyHint is a claim that drifts from the handler it
// describes; a derived one cannot, and the class sits beside the description
// where whoever adds a tool must choose it.
func register[A any](s *mcp.Server, app *App,
	name string, fn func(context.Context, mcptools.Deps, A) (map[string]any, error)) {

	def, ok := mcptools.Lookup(name)
	if !ok {
		// Unreachable in a built binary: TestMCPProtocolAgainstStubAPI drives a
		// real session and asserts the registry and this list agree in BOTH
		// directions, so a name here with no definition fails the suite before
		// it can reach a host. Panicking rather than silently registering an
		// undescribed tool keeps that true even if somebody deletes the test.
		panic("mcp: no definition for tool " + name)
	}

	readOnly := def.Class.ReadOnly()
	destructive := def.Class.Destructive()
	openWorld := true

	mcp.AddTool(s, &mcp.Tool{
		Name:        def.Name,
		Description: def.Description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    readOnly,
			DestructiveHint: &destructive,
			IdempotentHint:  readOnly,
			OpenWorldHint:   &openWorld,
		},
	}, handlerFor(app, fn))
}

// handlerFor adapts a transport-neutral handler to the SDK's shape.
func handlerFor[A any](app *App,
	fn func(context.Context, mcptools.Deps, A) (map[string]any, error),
) func(context.Context, *mcp.CallToolRequest, A) (*mcp.CallToolResult, any, error) {

	return func(ctx context.Context, _ *mcp.CallToolRequest, args A) (*mcp.CallToolResult, any, error) {
		// * Credentials resolve per call, not at startup. A server that exits
		// * because no key is stored looks to a host like a broken install; a
		// * server that answers "run pulse auth login" is one the user can fix
		// * without leaving the assistant.
		c, err := app.apiClient()
		if err != nil {
			return toolError(err), nil, nil
		}

		out, err := fn(ctx, mcptools.Deps{Client: c}, args)
		if err != nil {
			return toolError(err), nil, nil
		}

		body, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return toolError(fmt.Errorf("could not encode the result: %w", err)), nil, nil
		}

		// * The same object in BOTH content forms. Hosts differ in whether the
		// * model is shown structuredContent, the text block, or both, and the
		// * suppression object must survive whichever path a host takes — a
		// * marker delivered on the channel the model does not read is not a
		// * marker.
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(body)}},
			StructuredContent: out,
		}, nil, nil
	}
}

// toolError reports a failure to the model rather than to the protocol.
//
// A transport-level error tells the host the server malfunctioned. A tool
// result with isError tells the MODEL what went wrong, in words it can act on
// or relay — which for "that site_id is not a UUID" or "your key is expired" is
// the difference between a fixable answer and a dead session.
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}
