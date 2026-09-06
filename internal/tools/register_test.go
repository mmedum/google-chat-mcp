package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
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
// which is the point of having a wrapper: fifty-three tools is too many
// for four rules kept by hand.
func registered(t *testing.T, cfg config.Config, specs ...spec) map[string]*mcp.Tool {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	for _, sp := range specs {
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

// GCM_ALLOW_DESTRUCTIVE=false is for a deployer who wants this server
// posting but never deleting. It takes out the deletes and leaves every
// other write, which is what separates it from read-only mode.
func TestRefusingDeletesLeavesTheOtherWrites(t *testing.T) {
	cfg := allToolsets()
	cfg.RefuseDeletes = true
	got := registered(t, cfg,
		spec{Name: "a_read", Description: "d", Kind: Read},
		spec{Name: "a_write", Description: "d", Kind: Write},
		spec{Name: "an_idempotent_write", Description: "d", Kind: WriteIdempotent},
		spec{Name: "a_delete", Description: "d", Kind: Destructive},
	)
	if _, ok := got["a_delete"]; ok {
		t.Error("a delete tool is registered with deletes refused")
	}
	for _, name := range []string{"a_read", "a_write", "an_idempotent_write"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%q was taken out, and only the deletes should be", name)
		}
	}
}

func TestWritesAreRegisteredByDefault(t *testing.T) {
	got := registered(t, allToolsets(),
		spec{Name: "a_write", Description: "d", Kind: Write},
		spec{Name: "a_delete", Description: "d", Kind: Destructive},
	)
	if len(got) != 2 {
		t.Errorf("%d tools, want both", len(got))
	}
}

// A tool whose effect other people see, and cannot be undone, asks the
// client to put a person in the loop. Annotations are advice; this is
// the part with teeth, and attaching it by hand per tool is how one
// gets forgotten.
//
// The cut is "irreversible and visible", not "destroys something": a
// posted message cannot be recalled from anyone's notifications, while
// a reaction is undone by removing it.
func TestAnIrreversibleToolAsksForAPerson(t *testing.T) {
	got := registered(t, allToolsets(),
		spec{Name: "a_delete", Description: "d", Kind: Destructive},
		spec{Name: "a_write", Description: "d", Kind: Write},
		spec{Name: "an_idempotent_write", Description: "d", Kind: WriteIdempotent},
		spec{Name: "a_read", Description: "d", Kind: Read},
	)
	for _, name := range []string{"a_delete", "a_write"} {
		if v, ok := got[name].Meta["anthropic/requiresUserInteraction"]; !ok || v != true {
			t.Errorf("%s meta = %v, want a person asked for", name, got[name].Meta)
		}
	}
	for _, name := range []string{"an_idempotent_write", "a_read"} {
		if _, ok := got[name].Meta["anthropic/requiresUserInteraction"]; ok {
			t.Errorf("%s demands an interaction it does not need", name)
		}
	}
}

// A deployer running unattended can turn the hint off, and then it is
// off everywhere rather than on the tools somebody remembered.
//
// It is worth the setting because the mark is absolute in the client
// that reads it: an allow rule does not suppress it, and headless there
// is nobody to ask, so every write is refused. The spec puts the
// confirmation on the application — "the protocol itself does not
// mandate any specific user interaction model" — so a server that
// demands one leaves a deployer no way out. Default is on; this is the
// way out.
func TestADeployerCanTurnTheHintOff(t *testing.T) {
	cfg := allToolsets()
	cfg.SuppressInteractionHint = true
	got := registered(t, cfg,
		spec{Name: "a_delete", Description: "d", Kind: Destructive},
		spec{Name: "a_write", Description: "d", Kind: Write},
	)
	if len(got) != 2 {
		t.Fatalf("%d tools, want both still registered", len(got))
	}
	for name, tool := range got {
		if _, ok := tool.Meta["anthropic/requiresUserInteraction"]; ok {
			t.Errorf("%s still asks for a person after the hint was turned off", name)
		}
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
// dry_run and also have to remember to honour it.
func TestTheDryRunFlagIsFoundOnTheInputType(t *testing.T) {
	if got := dryRunField[dryRunProbe](); got != 1 {
		t.Errorf("dry_run field = %d, want the second field", got)
	}
	if got := dryRunField[probeIn](); got != -1 {
		t.Errorf("an input with no dry_run reported field %d", got)
	}
	if got := dryRunField[string](); got != -1 {
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
