package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

func newServer(t *testing.T, cfg config.Config) *mcp.Server {
	t.Helper()
	svc := service.New(gchat.New(gchat.Options{Tokens: noTokens{}}), nil, cfg, slog.New(slog.DiscardHandler))
	return New(Deps{Service: svc, Config: cfg, Version: "test"})
}

type noTokens struct{}

func (noTokens) Token(context.Context) (string, error) { return "", nil }

// dump reads the schemas the way the gate does.
func dump(t *testing.T, cfg config.Config) Dump {
	t.Helper()
	var buf bytes.Buffer
	if err := DumpSchemas(context.Background(), newServer(t, cfg), &buf, "test"); err != nil {
		t.Fatalf("DumpSchemas: %v", err)
	}
	var d Dump
	if err := json.Unmarshal(buf.Bytes(), &d); err != nil {
		t.Fatalf("decode dump: %v", err)
	}
	return d
}

func TestDumpSchemasReportsTheServerIdentity(t *testing.T) {
	d := dump(t, config.Config{})
	if d.Server != Name || d.Version != "test" || d.SDK != SDKVersion {
		t.Errorf("identity = %+v", d)
	}
	if len(d.Tools) == 0 {
		t.Fatal("no tools registered")
	}
}

func TestToolsAreSortedAndUnique(t *testing.T) {
	d := dump(t, config.Config{})
	names := make([]string, len(d.Tools))
	for i, tool := range d.Tools {
		names[i] = tool.Name
	}
	if !slices.IsSorted(names) {
		t.Errorf("tools are not sorted: %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate tool %q", n)
		}
		seen[n] = true
	}
}

// A tool with no description is a tool the model cannot choose well.
func TestEveryToolHasADescription(t *testing.T) {
	for _, tool := range dump(t, config.Config{}).Tools {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
}

// Unknown input fields must be rejected. A misspelled dry_run that was
// silently ignored would post for real.
func TestToolInputsRejectUnknownFields(t *testing.T) {
	for _, tool := range dump(t, config.Config{}).Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema for %q: %v", tool.Name, err)
		}
		var schema struct {
			AdditionalProperties json.RawMessage `json:"additionalProperties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode schema for %q: %v", tool.Name, err)
		}
		if len(schema.AdditionalProperties) == 0 {
			t.Errorf("tool %q does not constrain additionalProperties", tool.Name)
		}
	}
}

// The annotations are what a client shows a person before it runs a
// tool, so they have to say the truth about each one.
func TestReadOnlyToolsAreAnnotated(t *testing.T) {
	want := map[string]bool{"whoami": true, "list_spaces": true}
	for _, tool := range dump(t, config.Config{}).Tools {
		if !want[tool.Name] {
			continue
		}
		if tool.Annotations == nil {
			t.Fatalf("tool %q has no annotations", tool.Name)
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q should be marked read-only", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Errorf("tool %q reaches Google and should be open-world", tool.Name)
		}
		delete(want, tool.Name)
	}
	for name := range want {
		t.Errorf("tool %q was not registered", name)
	}
}

// The instructions are the first thing a client shows the model, so an
// empty or placeholder string is a real defect — and so is naming a
// tool or an argument that is not there.
// Every configuration, not just the default one. This test read the
// default surface alone, so it passed while a read-only server told the
// model to "write with send_message" and registered no such tool: the
// instructions were a constant and read-only drops every write tool.
func TestInstructionsNameTheStartingPoints(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{name: "default", cfg: config.Config{Toolsets: config.AllToolsets}},
		{name: "read only", cfg: config.Config{Toolsets: config.AllToolsets, ReadOnly: true}},
		{name: "deletes refused", cfg: config.Config{Toolsets: config.AllToolsets, RefuseDeletes: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkInstructions(t, tc.cfg)
		})
	}
}

func checkInstructions(t *testing.T, cfg config.Config) {
	t.Helper()
	known := map[string]bool{}
	tools := dump(t, cfg).Tools
	if len(tools) == 0 {
		t.Fatal("no tools registered: this test is looking at nothing")
	}
	for _, tool := range tools {
		known[tool.Name] = true
		// Both halves: the instructions tell the model what to pass and
		// what to read back, and next_page_token is an output field.
		for _, field := range inputFields(t, tool) {
			known[field] = true
		}
		for _, field := range outputFields(t, tool) {
			known[field] = true
		}
	}
	instructions := instructionsFor(cfg)
	for _, word := range strings.Fields(instructions) {
		// A trailing underscore is a prefix the text uses to talk
		// about a family of tools, such as "the matching get_ tools".
		name := strings.Trim(word, ".,;:")
		if !strings.Contains(name, "_") || strings.HasSuffix(name, "_") {
			continue
		}
		if !known[name] {
			t.Errorf("the instructions name %q, which this configuration registers as neither "+
				"a tool nor an argument", name)
		}
	}

	for _, want := range []string{"list_spaces", "whoami", "search_messages"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
}

// outputFields is the field names one tool answers with.
func outputFields(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()
	if tool.OutputSchema == nil {
		return nil
	}
	raw, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatalf("marshal output schema for %q: %v", tool.Name, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse output schema for %q: %v", tool.Name, err)
	}
	return slices.Sorted(maps.Keys(schema.Properties))
}

// inputFields is the argument names one tool takes.
func inputFields(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal schema for %q: %v", tool.Name, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema for %q: %v", tool.Name, err)
	}
	return slices.Sorted(maps.Keys(schema.Properties))
}

// Every tool in the released surface must still exist, under the same
// name. testdata/schemas-baseline.json is the contract, and a caller
// written against it must keep working.
// This overlaps `gates schema-diff` on purpose. That gate is the
// authority — it also compares output fields — but it only runs under
// `make check`, on one platform. This runs under plain `go test`, on all
// three, so a rename is caught by whoever notices first.
func TestPortedToolsKeepTheirNames(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/schemas-baseline.json")
	if err != nil {
		t.Skipf("no baseline: %v", err)
	}
	var baseline struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}

	// Every toolset, because that is what ships: the zero config leaves
	// the seven section tools out, and comparing a narrowed surface
	// against the full baseline would report them as lost.
	have := map[string]bool{}
	for _, tool := range dump(t, config.Config{Toolsets: config.AllToolsets}).Tools {
		have[tool.Name] = true
	}

	var missing []string
	for _, tool := range baseline.Tools {
		if !have[tool.Name] {
			missing = append(missing, tool.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d released tools are gone: %s",
			len(missing), len(baseline.Tools), strings.Join(missing, ", "))
	}

	// A tool the baseline has must not have been renamed, which is what
	// the contract forbids. A tool it does not have is Phase 4 adding
	// coverage, and the schema diff reports those as additions.
}

// The dump is what the schema gate compares against the baseline, so
// the read surface and the resource templates both have to be in it.
func TestDumpCarriesTheReadSurface(t *testing.T) {
	d := dump(t, config.Config{Toolsets: config.AllToolsets})
	have := map[string]bool{}
	for _, tool := range d.Tools {
		have[tool.Name] = true
	}
	for _, want := range []string{
		"whoami", "list_spaces", "get_space", "list_members", "get_messages",
		"get_thread", "get_message", "list_reactions", "search_messages",
		"search_people", "list_sections", "list_section_items",
	} {
		if !have[want] {
			t.Errorf("tool %q is missing from the dump", want)
		}
	}
	if len(d.ResourceTemplates) != 3 {
		t.Errorf("%d resource templates, want 3", len(d.ResourceTemplates))
	}
	for _, tmpl := range d.ResourceTemplates {
		if !strings.HasPrefix(tmpl.URITemplate, "gchat://") {
			t.Errorf("template %q is not on this server's scheme", tmpl.URITemplate)
		}
	}
}

// Sections are per-user sidebar state, and a person who does not want
// those tools should be able to leave them out.
func TestToolsetsNarrowTheSurface(t *testing.T) {
	full := len(dump(t, config.Config{Toolsets: config.AllToolsets}).Tools)
	core := len(dump(t, config.Config{Toolsets: []config.Toolset{config.ToolsetCore}}).Tools)
	if core >= full {
		t.Errorf("core registers %d tools and all registers %d; the sections group should be optional", core, full)
	}
}
