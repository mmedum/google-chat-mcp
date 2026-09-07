// Package server wires the MCP SDK to the tools and dumps the tool
// schemas through an in-memory client session.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/service"
	"github.com/mmedum/google-chat-mcp/internal/tools"
)

// Name is the MCP server name.
const Name = "google-chat-mcp"

// SDKVersion is recorded in a schema dump so a diff caused by an SDK
// upgrade can be told from a change to the tool surface.
const SDKVersion = "v1.7.0"

// Description is the one-line summary of the server. The Claude Desktop
// bundle and the MCP registry entry both carry it, and both are built
// from this constant rather than from a sentence typed twice. The
// registry caps a description at 100 characters, which a test holds.
const Description = "Google Chat as MCP tools: read, search and write to your spaces, direct messages and sidebar."

// The instructions are the first thing a client shows the model, so
// they describe the tools that are actually registered. A pointer to a
// tool that is not there costs a turn and teaches the model nothing.
//
// Built from the config rather than fixed, because read-only mode drops
// every write tool: a constant string named send_message and
// find_direct_message to a server that had neither, which is the exact
// failure the paragraph above warns about. TestInstructionsOnlyNameTools
// ThatAreRegistered holds it for every configuration.
const readInstructions = "Google Chat tools. Start with list_spaces to find a space's resource name; whoami says " +
	"which account is signed in. Read with get_messages, get_thread and get_message; search_messages scans one " +
	"space, and search_people turns a name into an email address."

// writeInstructions are dropped under GCM_READ_ONLY, along with the
// tools they name.
const writeInstructions = "Write with send_message, which posts the text exactly as given, and find_direct_message " +
	"to reach one person. Most write tools take dry_run, which returns the request body without sending it."

// readOnlyInstructions replace them, because "no write tool is here" is
// worth a sentence: without it the model discovers the same absence one
// failed call at a time.
const readOnlyInstructions = "This server is read-only: no tool here can post, edit or delete anything in Chat."

const tailInstructions = "Every space, message and thread is addressed by its resource name, never by position. A " +
	"listing that reports unparsed rows is incomplete rather than short. Keep paging while next_page_token is " +
	"non-null, and note that AN EMPTY PAGE IS NOT THE END: Google applies the page size before it filters, so a " +
	"page can come back with no rows and a token still on it. Stopping there reports a space that has messages " +
	"in it as empty. Resources gchat://spaces/{id} and its messages and threads carry the same content as the " +
	"matching get_ tools."

// instructionsFor is the instruction string for one configuration.
func instructionsFor(cfg config.Config) string {
	middle := writeInstructions
	if cfg.ReadOnly {
		middle = readOnlyInstructions
	}
	return readInstructions + " " + middle + " " + tailInstructions
}

// Deps are what the server needs.
type Deps struct {
	Service *service.Service
	Config  config.Config
	Logger  *slog.Logger
	Version string
}

// New builds the MCP server with every tool registered.
func New(d Deps) *mcp.Server {
	opts := &mcp.ServerOptions{Instructions: instructionsFor(d.Config)}
	if d.Logger != nil && d.Logger.Enabled(context.Background(), slog.LevelDebug) {
		opts.Logger = d.Logger
	}
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: d.Version}, opts)
	tools.Register(s, tools.Deps{Service: d.Service, Config: d.Config, Logger: d.Logger})
	return s
}

// Dump is the shape --dump-schemas writes. The schema-diff gate reads
// it, and testdata/schemas-baseline.json is the same shape from the
// last release.
type Dump struct {
	Server            string                  `json:"server"`
	Version           string                  `json:"version"`
	SDK               string                  `json:"sdk"`
	Tools             []*mcp.Tool             `json:"tools"`
	ResourceTemplates []*mcp.ResourceTemplate `json:"resourceTemplates"`
}

// DumpSchemas writes the tool list and the resource templates as the
// wire carries them. The SDK has no public enumerator, so an in-memory
// client asks the server, which is also what the client sees.
func DumpSchemas(ctx context.Context, s *mcp.Server, w io.Writer, version string) error {
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		return fmt.Errorf("connect server: %w", err)
	}
	defer func() { _ = ss.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "schema-dump", Version: version}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return fmt.Errorf("connect client: %w", err)
	}
	defer func() { _ = cs.Close() }()

	toolList, err := cs.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	sort.Slice(toolList.Tools, func(i, j int) bool { return toolList.Tools[i].Name < toolList.Tools[j].Name })

	templates, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resource templates: %w", err)
	}
	sort.Slice(templates.ResourceTemplates, func(i, j int) bool {
		return templates.ResourceTemplates[i].URITemplate < templates.ResourceTemplates[j].URITemplate
	})

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(Dump{
		Server:            Name,
		Version:           version,
		SDK:               SDKVersion,
		Tools:             toolList.Tools,
		ResourceTemplates: templates.ResourceTemplates,
	})
}
