package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v8/internal/config"
	"github.com/mmedum/google-chat-mcp/v8/internal/gchat"
)

type probeIn struct {
	Name string `json:"name"`
}

type probeOut struct {
	When time.Time `json:"when"`
}

func (probeOut) Render() string { return "probe" }

// registered lists the tools a spec produces under a configuration.
// Every rule the wrapper enforces is checked here rather than per tool,
// which is the point of having a wrapper: fifty-eight tools is too many
// for four rules kept by hand.
func registered(t *testing.T, cfg config.Config, specs ...spec) map[string]*mcp.Tool {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	for _, sp := range specs {
		// A destructive tool takes confirm and nothing else may.
		if sp.Kind == Destructive {
			register(s, Deps{Config: cfg}, sp,
				func(context.Context, *mcp.CallToolRequest, deleteProbe) (*mcp.CallToolResult, probeOut, error) {
					return nil, probeOut{}, nil
				})
			continue
		}
		register(s, Deps{Config: cfg}, sp,
			func(context.Context, *mcp.CallToolRequest, probeIn) (*mcp.CallToolResult, probeOut, error) {
				return nil, probeOut{}, nil
			})
	}

	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		out[tool.Name] = tool
	}
	return out
}

func allToolsets() config.Config { return config.Config{Toolsets: config.AllToolsets} }

// GCM_READ_ONLY is the flag a person sets when they want this server
// nowhere near their Chat history. It has to hold for every kind of
// write, not only the destructive ones.
func TestReadOnlyLeavesOutEveryWrite(t *testing.T) {
	cfg := allToolsets()
	cfg.ReadOnly = true
	cfg.EnableDestructive = true
	got := registered(t, cfg,
		spec{Name: "a_read", Description: "d", Kind: Read},
		spec{Name: "a_write", Description: "d", Kind: Write},
		spec{Name: "an_idempotent_write", Description: "d", Kind: WriteIdempotent},
		spec{Name: "a_delete", Description: "d", Kind: Destructive},
	)
	if _, ok := got["a_read"]; !ok {
		t.Error("a read tool should survive read-only mode")
	}
	for _, name := range []string{"a_write", "an_idempotent_write", "a_delete"} {
		if _, ok := got[name]; ok {
			t.Errorf("%q is registered in read-only mode", name)
		}
	}
}

// Read-only mode asks for read-only scopes, so a read that Google takes
// only through a scope that can also write would fail on every call.
func TestReadOnlyLeavesOutAReadThroughAWriteScope(t *testing.T) {
	cfg := allToolsets()
	cfg.ReadOnly = true
	got := registered(t, cfg,
		spec{Name: "a_read", Description: "d", Kind: Read},
		spec{Name: "a_read_through_a_write_scope", Description: "d", Kind: Read, WriteScope: true},
	)
	if _, ok := got["a_read"]; !ok {
		t.Error("a read tool should survive read-only mode")
	}
	if _, ok := got["a_read_through_a_write_scope"]; ok {
		t.Error("a read that needs a write scope is registered in read-only mode, which asks for none")
	}
}

// A destructive tool is not registered unless GCM_ENABLE_DESTRUCTIVE is
// on: some mode of every widely used client runs a listed tool with
// nobody approving the call, so a tool that must not run unattended is
// not listed.
func TestADestructiveToolIsRegisteredOnlyWhenAllowed(t *testing.T) {
	specs := []spec{
		{Name: "a_read", Description: "d", Kind: Read},
		{Name: "a_write", Description: "d", Kind: Write},
		{Name: "an_idempotent_write", Description: "d", Kind: WriteIdempotent},
		{Name: "a_delete", Description: "d", Kind: Destructive},
	}
	refused := registered(t, allToolsets(), specs...)
	for _, name := range []string{"a_read", "a_write", "an_idempotent_write"} {
		if _, ok := refused[name]; !ok {
			t.Errorf("%q was taken out with the destructive tools", name)
		}
	}
	if _, ok := refused["a_delete"]; ok {
		t.Error("a destructive tool is registered without GCM_ENABLE_DESTRUCTIVE")
	}

	cfg := allToolsets()
	cfg.EnableDestructive = true
	if _, ok := registered(t, cfg, specs...)["a_delete"]; !ok {
		t.Error("a destructive tool is missing with GCM_ENABLE_DESTRUCTIVE on")
	}
}

// Registration refuses three mistakes outright, at start, rather than
// leaving them to be found in a call: a destructive tool that cannot be
// told confirm, a confirm on a tool that is not destructive, which would
// refuse every call made without it, and the mark on a tool the
// destructive flag does not gate.
func TestRegistrationRefusesAnUnguardedDestructiveTool(t *testing.T) {
	// The default, which registers no destructive tool: the spec is
	// checked anyway.
	cfg := allToolsets()
	for _, tc := range []struct {
		name string
		add  func(s *mcp.Server)
		want string
	}{
		{"a delete with no confirm", func(s *mcp.Server) {
			register(s, Deps{Config: cfg}, spec{Name: "a_delete", Description: "d", Kind: Destructive},
				func(context.Context, *mcp.CallToolRequest, dryRunProbe) (*mcp.CallToolResult, probeOut, error) {
					return nil, probeOut{}, nil
				})
		}, "tools: a_delete: a confirm input goes with the Destructive kind and nowhere else"},
		{"a write with confirm", func(s *mcp.Server) {
			register(s, Deps{Config: cfg}, spec{Name: "a_write", Description: "d", Kind: Write},
				func(context.Context, *mcp.CallToolRequest, deleteProbe) (*mcp.CallToolResult, probeOut, error) {
					return nil, probeOut{}, nil
				})
		}, "tools: a_write: a confirm input goes with the Destructive kind and nowhere else"},
		{"a marked write", func(s *mcp.Server) {
			register(s, Deps{Config: cfg}, spec{Name: "a_write", Description: "d", Kind: Write, Marked: true},
				func(context.Context, *mcp.CallToolRequest, dryRunProbe) (*mcp.CallToolResult, probeOut, error) {
					return nil, probeOut{}, nil
				})
		}, "tools: a_write carries the mark and is not Destructive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if got := recover(); got != tc.want {
					t.Errorf("panic = %v, want %q", got, tc.want)
				}
			}()
			tc.add(mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil))
		})
	}
}

// A toolset a person left out takes its tools with it, and core is
// always there.
func TestAToolsetCanBeLeftOut(t *testing.T) {
	cfg := config.Config{Toolsets: []config.Toolset{config.ToolsetCore}}
	got := registered(t, cfg,
		spec{Name: "a_core_tool", Description: "d", Kind: Read},
		spec{Name: "a_section_tool", Description: "d", Kind: Read, Toolset: config.ToolsetSections},
	)
	if _, ok := got["a_core_tool"]; !ok {
		t.Error("core is always registered")
	}
	if _, ok := got["a_section_tool"]; ok {
		t.Error("a tool whose toolset was left out is still registered")
	}
}

// The released surface annotates its timestamps and jsonschema-go does
// not do it on its own, so the wrapper builds every output schema.
func TestEveryToolGetsAnAnnotatedOutputSchema(t *testing.T) {
	got := registered(t, allToolsets(), spec{Name: "a_read", Description: "d", Kind: Read})
	schema, ok := got["a_read"].OutputSchema.(map[string]any)
	if !ok {
		t.Fatalf("output schema = %T, want an object", got["a_read"].OutputSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	when, _ := props["when"].(map[string]any)
	if when["format"] != "date-time" {
		t.Errorf("when = %v, want a date-time format", when)
	}
}

// dryRunProbe stands in for a tool that offers dry_run and forgets to
// act on it, which is the mistake the guard exists to catch.
type dryRunProbe struct {
	Name   string `json:"name"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// The wrapper finds the flag by reflection, so a tool cannot declare
// dry_run and also have to remember to honor it.
func TestTheDryRunFlagIsFoundOnTheInputType(t *testing.T) {
	if got := boolField[dryRunProbe]("dry_run"); got != 1 {
		t.Errorf("dry_run field = %d, want the second field", got)
	}
	if got := boolField[probeIn]("dry_run"); got != -1 {
		t.Errorf("an input with no dry_run reported field %d", got)
	}
	if got := boolField[string]("dry_run"); got != -1 {
		t.Errorf("a non-struct input reported field %d", got)
	}
}

// The guarantee a dry run makes is kept underneath the handler: the
// call runs on a context internal/gchat will not write under, so a
// handler that forgot its own preview branch fails loudly.
func TestADryRunPutsTheCallOnANoWriteContext(t *testing.T) {
	var sawPreview, sawPlain bool
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	register(s, Deps{Config: allToolsets()}, spec{Name: "a_write", Description: "d", Kind: Write},
		func(ctx context.Context, _ *mcp.CallToolRequest, in dryRunProbe) (*mcp.CallToolResult, probeOut, error) {
			// Deliberately no dry-run branch: this probe is the tool
			// whose author forgot one.
			err := gchat.New(gchat.Options{Tokens: noToken{}}).DeleteMessage(ctx, "spaces/A/messages/B", false)
			if in.DryRun {
				sawPreview = errors.Is(err, gchat.ErrWriteForbidden)
			} else {
				sawPlain = errors.Is(err, gchat.ErrWriteForbidden)
			}
			return nil, probeOut{}, nil
		})

	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	for _, dryRun := range []bool{true, false} {
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "a_write", Arguments: map[string]any{"name": "x", "dry_run": dryRun},
		}); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	if !sawPreview {
		t.Error("a dry run reached the write path")
	}
	if sawPlain {
		t.Error("a real call was refused its write")
	}
}

// noToken stands in for credentials in a test that must never reach
// the network: the write guard fires before the token is resolved, and
// anything that gets past it fails here instead of calling Google.
type noToken struct{}

func (noToken) Token(context.Context) (string, error) { return "", errors.New("no credentials") }

// deleteProbe is a destructive tool's input: confirm and dry_run.
type deleteProbe struct {
	Name    string `json:"name"`
	Confirm bool   `json:"confirm,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// A destructive call says confirm: true, or it never reaches its
// handler. The refusal has to reach the model as a tool error it can
// read, saying what to pass: one the SDK reports as a protocol failure
// teaches the model nothing, and a guard that ran the handler first has
// already deleted the thing. A dry run changes nothing and needs no
// confirm.
func TestADeleteRunsOnlyWithConfirm(t *testing.T) {
	cfg := allToolsets()
	cfg.EnableDestructive = true

	ran := 0
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	register(s, Deps{Config: cfg}, spec{Name: "a_delete", Description: "d", Kind: Destructive},
		func(context.Context, *mcp.CallToolRequest, deleteProbe) (*mcp.CallToolResult, probeOut, error) {
			ran++
			return nil, probeOut{}, nil
		})
	cs := connectTo(t, s)

	const refusal = "[invalid] a_delete cannot be taken back, so every call says so: pass confirm: true. " +
		"Nothing was changed."
	for _, tc := range []struct {
		name     string
		args     map[string]any
		wantRun  bool
		wantText string
	}{
		{"no confirm", map[string]any{"name": "x"}, false, refusal},
		{"confirm false", map[string]any{"name": "x", "confirm": false}, false, refusal},
		{"a dry run without confirm", map[string]any{"name": "x", "dry_run": true}, true, ""},
		{"confirm true", map[string]any{"name": "x", "confirm": true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := ran
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "a_delete", Arguments: tc.args})
			if err != nil {
				t.Fatalf("the refusal must be a tool error the model can read, not a protocol failure: %v", err)
			}
			if got := ran > before; got != tc.wantRun {
				t.Errorf("handler ran = %v, want %v", got, tc.wantRun)
			}
			if tc.wantText == "" {
				if res.IsError {
					t.Errorf("refused: %s", errorText(t, res))
				}
				return
			}
			if got := errorText(t, res); got != tc.wantText {
				t.Errorf("refusal = %q, want %q", got, tc.wantText)
			}
		})
	}
}
