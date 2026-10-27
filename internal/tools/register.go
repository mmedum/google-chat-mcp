package tools

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v8/internal/config"
	"github.com/mmedum/google-chat-mcp/v8/internal/service"
)

// Kind is what a tool does to the world. It decides the annotations a
// client shows a person, whether the tool is registered under
// GCM_READ_ONLY, and whether it needs GCM_ENABLE_DESTRUCTIVE to be
// registered at all.
//
// It is one field per tool rather than three, because three rules
// enforced by author discipline is three ways to be quietly wrong: a
// destructive tool annotated as safe, a write tool still registered in
// read-only mode, a timestamped output that lost its format annotation.
// Fifty-eight tools is too many for that.
type Kind int

// Tool kinds.
const (
	// Read touches nothing.
	Read Kind = iota
	// ReadWritesLocally reads from Chat and writes to the operator's
	// own machine — download_attachment, and nothing else so far.
	//
	// It is its own kind rather than a Read with a comment beside it.
	// GCM_READ_ONLY is about Chat, so the tool stays registered under
	// it; but the annotation a client shows must not say the tool
	// touches nothing, because it creates a file. Holding those two
	// facts apart in prose at the call site is exactly what register
	// exists to stop.
	ReadWritesLocally
	// Write changes something and destroys nothing.
	Write
	// WriteIdempotent additionally lands the same way twice.
	WriteIdempotent
	// Destructive cannot be taken back: it removes something, opens a
	// space to an audience, makes someone a manager or adds someone from
	// outside the organization. It is registered only under
	// GCM_ENABLE_DESTRUCTIVE, and every call says confirm: true.
	// Idempotent by nature here: a second delete of the same thing is a
	// no-op, not a second deletion, and opening a space twice sets the
	// same audience.
	Destructive
)

// spec is one tool's registration, minus its handler.
type spec struct {
	// Name and Description are what the model reads.
	Name        string
	Description string
	// Kind decides the annotations and the gating.
	Kind Kind
	// Toolset is the group this tool belongs to. The zero value is
	// ToolsetCore, which is always registered.
	Toolset config.Toolset
	// Marked puts Claude Code's requiresUserInteraction mark on the
	// tool, so it asks the person on every call, in every interactive
	// mode. Only for a tool whose prompt is itself the point: deleting a
	// whole space or an emoji every message shares, and opening a space
	// to an audience (docs/architecture.md, "What a write may reach").
	// Never on a delete done many times in a row. Only a Destructive
	// tool may set it: the flag that registers the tool is the control,
	// and the mark is one client's prompt on top of it.
	Marked bool
	// WriteScope says the tool reads through a scope that can also
	// write, because Google offers no read-only one for it. Read-only
	// mode asks for read-only scopes, so it leaves the tool out.
	WriteScope bool
}

// annotations returns what a client shows for this kind.
func (k Kind) annotations() *mcp.ToolAnnotations {
	switch k {
	case ReadWritesLocally:
		// Not read-only: it writes a file. Not destructive either — the
		// directory is the operator's own and a download never
		// overwrites what is in it.
		return &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
	case Write:
		return &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
	case WriteIdempotent:
		return &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)}
	case Destructive:
		return &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(true)}
	default:
		return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
	}
}

// register adds one tool, or leaves it out when the configuration says
// so. Every tool in this package goes through here.
//
// Out is constrained to renderer so a tool cannot be added without the
// readable half of its reply. See render.go for why both halves are
// sent.
func register[In any, Out renderer](s *mcp.Server, d Deps, sp spec, h mcp.ToolHandlerFor[In, Out]) {
	// The spec is checked whatever the configuration, so a mistake fails
	// at start in every one, not only where the tool is registered.
	// confirm is the destructive kind's witness, and wrap refuses a call
	// without it wherever the input has one, so the two go together.
	confirm := boolField[In]("confirm")
	if (sp.Kind == Destructive) != (confirm >= 0) {
		panic("tools: " + sp.Name + ": a confirm input goes with the Destructive kind and nowhere else")
	}
	if sp.Marked && sp.Kind != Destructive {
		panic("tools: " + sp.Name + " carries the mark and is not Destructive")
	}
	if !registers(sp, d.Config) {
		return
	}
	tool := &mcp.Tool{
		Name:         sp.Name,
		Description:  sp.Description,
		Annotations:  sp.Kind.annotations(),
		OutputSchema: outputSchema[Out](),
	}
	if sp.Marked {
		// Claude Code (2.1.199 or later) prompts for a marked tool on
		// every call in every interactive mode, and denies it where no
		// prompt can be drawn. That is the point for the few tools that
		// carry it, and why nothing else does: every other write is the
		// client's to approve (docs/architecture.md, "What a write may
		// reach").
		tool.Meta = mcp.Meta{interactionKey: true}
	}
	mcp.AddTool(s, tool, wrap(h, boolField[In]("dry_run"), confirm, sp.Name))
}

// registers reports whether a tool is registered under cfg.
//
// A Destructive tool is left out unless GCM_ENABLE_DESTRUCTIVE is on.
// Whatever a client does with a tool it lists, some mode of every
// widely used client runs it with nobody approving the call, so a tool
// that must not run unattended is not listed. It used to be registered
// and refused at call time, which left Claude Code prompting for a
// marked tool the server then refused.
func registers(sp spec, cfg config.Config) bool {
	toolset := sp.Toolset
	if toolset == "" {
		toolset = config.ToolsetCore
	}
	if toolset != config.ToolsetCore && !cfg.Enabled(toolset) {
		return false
	}
	// GCM_READ_ONLY is about Chat. A tool that only writes into the
	// server's own local directory is still registered under it — and
	// that directory is opt-in, so a server with neither setting writes
	// nothing anywhere.
	if cfg.ReadOnly && ((sp.Kind != Read && sp.Kind != ReadWritesLocally) || sp.WriteScope) {
		return false
	}
	return sp.Kind != Destructive || cfg.DestructiveRegisters()
}

// interactionKey is Claude Code's mark for a tool it prompts for on
// every call.
const interactionKey = "anthropic/requiresUserInteraction"

// wrap is what every handler gets for free.
//
// Four rules that were each a line in every handler: a dry run may not
// write, a Destructive call says confirm: true, an error reaches the
// model with its class on the front, and a reply carries its readable
// half as well as its structured one. All belong here for the same
// reason the annotations do — a rule kept by hand at fifty call sites is
// a rule that will be missed at one.
func wrap[In any, Out renderer](h mcp.ToolHandlerFor[In, Out], dryRun, confirm int, name string) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		v := reflect.ValueOf(in)
		dry := dryRun >= 0 && v.Field(dryRun).Bool()
		if confirm >= 0 && !dry && !v.Field(confirm).Bool() {
			// A witness that the caller meant this call, never an
			// approval: the model sets it. A dry run needs none, since it
			// changes nothing.
			var zero Out
			return nil, zero, fail(service.Invalidf("%s cannot be taken back, so every call says so: pass "+
				"confirm: true. Nothing was changed.", name))
		}
		if dry {
			ctx = service.Preview(ctx)
		}
		res, out, err := h(ctx, req, in)
		if err != nil {
			return res, out, fail(err)
		}
		if res == nil {
			res = &mcp.CallToolResult{}
		}
		// The SDK fills an empty Content with the serialized output,
		// which is the duplication render.go exists to replace. Set it
		// and the SDK leaves it alone.
		if res.Content == nil {
			res.Content = []mcp.Content{&mcp.TextContent{Text: out.Render()}}
		}
		return res, out, nil
	}
}

// boolField is where an input type keeps the boolean named name in
// JSON, or -1 when it has none. It is resolved once, at registration.
//
// Finding dry_run by reflection rather than asking each tool to declare
// it is the point: a tool that offers dry_run cannot also have to
// remember to honor it. The flag puts the call on a context
// internal/gchat refuses to write under, so a handler that forgets its
// own preview branch fails loudly instead of posting. confirm is found
// the same way, so a Destructive tool cannot forget to require it.
func boolField[In any](name string) int {
	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		return -1
	}
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == name && t.Field(i).Type.Kind() == reflect.Bool {
			return i
		}
	}
	return -1
}

// timeSchema is what a time.Time becomes in a tool schema.
//
// jsonschema-go infers a plain string for it, and the format annotation
// is what tells a client the string is a timestamp rather than free
// text. The released surface carries it, so every output schema is built
// here rather than left to the SDK to infer.
var timeSchema = &jsonschema.ForOptions{
	TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[time.Time](): {Type: "string", Format: "date-time"},
	},
}

// outputSchema builds a tool's output schema with timestamps annotated.
//
// It panics on failure because the only way to fail is a type this
// server declares, which no input can change: a broken schema is a
// build error that happens to be found at start.
func outputSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](timeSchema)
	if err != nil {
		panic("tools: output schema for " + reflect.TypeFor[T]().String() + ": " + err.Error())
	}
	return s
}

// nullable distinguishes "Google said nothing" from "Google said the
// empty string". Every optional string in this server's output means
// the first, and null says so where an empty string would read as a
// value the caller could use.
func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// nullableTime is nullable for a timestamp.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func ptr[T any](v T) *T { return &v }

// RenderedPayload is the request body a dry run would have sent.
//
// It is free-form on purpose: it is the JSON that would go to Google,
// not a shape this server defines. Everything else a tool returns is a
// declared field.
type RenderedPayload map[string]any

// rendered wraps a preview, and stays null when there is nothing to
// preview — which is every real write.
func rendered(m map[string]any) *RenderedPayload {
	if m == nil {
		return nil
	}
	p := RenderedPayload(m)
	return &p
}
