// Package server wires the MCP SDK to the tools and dumps the tool
// schemas through an in-memory client session.
package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v9/internal/config"
	"github.com/mmedum/google-chat-mcp/v9/internal/service"
	"github.com/mmedum/google-chat-mcp/v9/internal/tools"
)

// Name is the MCP server name.
const Name = "google-chat-mcp"

// sdkModule is the MCP Go SDK's module path.
const sdkModule = "github.com/modelcontextprotocol/go-sdk"

// sdkVersion is recorded in a schema dump so a diff caused by an SDK
// upgrade can be told from a change to the tool surface. It is read
// from the build, since a typed copy fell behind go.mod once, and a
// replace directive wins, since that is the code built in.
func sdkVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path != sdkModule {
				continue
			}
			if r := d.Replace; r != nil {
				return cmp.Or(r.Version, r.Path)
			}
			return d.Version
		}
	}
	return "unknown"
}

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
	"to reach one person. A write to a space takes space_display_name beside space_id, so the prompt that approves " +
	"it names the space: a named space by the name list_spaces gives, a direct message by the other person's " +
	"email, a group chat by the other members' emails. It is checked, and a call naming another space is refused " +
	"[stale]. Most write tools take dry_run, which returns the request body without sending it."

// destructiveInstructions follow them when GCM_ENABLE_DESTRUCTIVE
// registers the tools they describe.
const destructiveInstructions = "The writes that cannot be taken back take confirm: true and repeat what they act " +
	"on, a message's text or a space's or an emoji's name. The target is read first, and a call that names it " +
	"otherwise is refused [stale]."

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

// mode is what decides the instructions a client is told: the only part
// of a configuration they read, so a dump that holds every mode holds
// every instruction string.
type mode string

const (
	// modeDefault is writes without the destructive tools.
	modeDefault mode = "default"
	// modeDestructive is writes with them, under GCM_ENABLE_DESTRUCTIVE.
	modeDestructive mode = "destructive"
	// modeReadOnly is GCM_READ_ONLY.
	modeReadOnly mode = "readOnly"
)

// allModes is every mode, as the dump carries them.
var allModes = []mode{modeDefault, modeDestructive, modeReadOnly}

// modeOf is the mode a configuration runs in.
func modeOf(cfg config.Config) mode {
	switch {
	case cfg.ReadOnly:
		return modeReadOnly
	case cfg.DestructiveRegisters():
		return modeDestructive
	}
	return modeDefault
}

// inMode is cfg set to run in m, its toolsets kept.
func inMode(cfg config.Config, m mode) config.Config {
	cfg.ReadOnly, cfg.EnableDestructive = m == modeReadOnly, m == modeDestructive
	return cfg
}

// instructionsFor is the instruction string for one configuration.
func instructionsFor(cfg config.Config) string {
	middle := writeInstructions
	switch modeOf(cfg) {
	case modeReadOnly:
		middle = readOnlyInstructions
	case modeDestructive:
		middle += " " + destructiveInstructions
	case modeDefault:
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
	Server  string `json:"server"`
	Version string `json:"version"`
	SDK     string `json:"sdk"`
	// Instructions are what a client is told before any tool, by the mode
	// that decides them, so a change to any of them is a change to the
	// surface.
	Instructions      map[string]string       `json:"instructions,omitempty"`
	Tools             []*mcp.Tool             `json:"tools"`
	ResourceTemplates []*mcp.ResourceTemplate `json:"resourceTemplates"`
	Resources         []*mcp.Resource         `json:"resources,omitempty"`
	Prompts           []*mcp.Prompt           `json:"prompts,omitempty"`
}

// DumpSchemas writes what a client of the server d builds is told, as
// the wire carries it: the tools, the resources, the resource templates
// and the prompts, and the instructions in every mode. The SDK has no
// public enumerator, so an in-memory client asks the server, which is
// also what the client sees.
func DumpSchemas(ctx context.Context, d Deps, w io.Writer) error {
	modes := map[mode]*mcp.Server{}
	for _, m := range allModes {
		in := d
		in.Config = inMode(d.Config, m)
		modes[m] = New(in)
	}
	return dumpServer(ctx, New(d), modes, w, d.Version)
}

// dumpServer is DumpSchemas over servers already built, so a test can
// dump one with a prompt, which this server does not register.
func dumpServer(ctx context.Context, s *mcp.Server, modes map[mode]*mcp.Server, w io.Writer, version string) error {
	d := Dump{Server: Name, Version: version, SDK: sdkVersion()}
	err := withSession(ctx, s, version, func(cs *mcp.ClientSession) error {
		tools, err := cs.ListTools(ctx, nil)
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		templates, err := cs.ListResourceTemplates(ctx, nil)
		if err != nil {
			return fmt.Errorf("list resource templates: %w", err)
		}
		resources, err := cs.ListResources(ctx, nil)
		if err != nil {
			return fmt.Errorf("list resources: %w", err)
		}
		prompts, err := cs.ListPrompts(ctx, nil)
		if err != nil {
			return fmt.Errorf("list prompts: %w", err)
		}
		d.Tools, d.ResourceTemplates = tools.Tools, templates.ResourceTemplates
		d.Resources, d.Prompts = resources.Resources, prompts.Prompts
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(d.Tools, func(i, j int) bool { return d.Tools[i].Name < d.Tools[j].Name })
	sort.Slice(d.ResourceTemplates, func(i, j int) bool {
		return d.ResourceTemplates[i].URITemplate < d.ResourceTemplates[j].URITemplate
	})
	sort.Slice(d.Resources, func(i, j int) bool { return d.Resources[i].URI < d.Resources[j].URI })
	sort.Slice(d.Prompts, func(i, j int) bool { return d.Prompts[i].Name < d.Prompts[j].Name })
	if len(modes) > 0 {
		d.Instructions = map[string]string{}
	}
	for m, ms := range modes {
		err := withSession(ctx, ms, version, func(cs *mcp.ClientSession) error {
			d.Instructions[string(m)] = cs.InitializeResult().Instructions
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s instructions: %w", m, err)
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// withSession runs f with a client session on s, over an in-memory
// transport, and closes both ends after.
func withSession(ctx context.Context, s *mcp.Server, version string, f func(*mcp.ClientSession) error) error {
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
	return f(cs)
}
