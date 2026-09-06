package tools

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/service"
)

// Kind is what a tool does to the world. It decides the annotations a
// client shows a person, whether the tool is registered at all under
// GCM_READ_ONLY and GCM_ALLOW_DESTRUCTIVE, and whether the client is
// asked to put a person in the loop first.
//
// It is one field per tool rather than four, because four rules
// enforced by author discipline is four ways to be quietly wrong: a
// destructive tool with no interaction hint, a write tool still
// registered in read-only mode, a timestamped output that lost its
// format annotation. Fifty-three tools is too many for that.
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
	// Destructive removes something. Idempotent by nature here: a
	// second delete of the same thing is a no-op, not a second
	// deletion.
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
	toolset := sp.Toolset
	if toolset == "" {
		toolset = config.ToolsetCore
	}
	if toolset != config.ToolsetCore && !d.Config.Enabled(toolset) {
		return
	}
	// GCM_READ_ONLY is about Chat. A tool that only writes into the
	// server's own local directory is still registered under it — and
	// that directory is opt-in, so a server with neither setting writes
	// nothing anywhere.
	if d.Config.ReadOnly && sp.Kind != Read && sp.Kind != ReadWritesLocally {
		return
	}
	// The shared standard leaves destructive tools unregistered unless a
	// flag enables them. This server defaults the other way, on purpose:
	// delete_message and the other three are part of the released
	// surface, and a tool that vanishes is a broken client rather than a
	// safer one. GCM_ALLOW_DESTRUCTIVE=false is the opt-in guard, and
	// delete_space is why it exists — it destroys other people's
	// messages and cannot be undone.
	if d.Config.RefuseDeletes && sp.Kind == Destructive {
		return
	}

	tool := &mcp.Tool{
		Name:         sp.Name,
		Description:  sp.Description,
		Annotations:  sp.Kind.annotations(),
		OutputSchema: outputSchema[Out](),
	}
	if (sp.Kind == Destructive || sp.Kind == Write) && !d.Config.SuppressInteractionHint {
		// This asks the client to put a person in the loop, and how much
		// that is worth depends entirely on the client. Two measurements,
		// both against Claude Code:
		//
		// Interactive, 2026-09-05: auto permission mode ran send_message
		// with no prompt at all. So it is not a guarantee.
		//
		// Headless, 2026-09-06: the opposite, and absolute. Every route
		// refuses — the allowlist by server or by exact tool name,
		// --permission-mode dontAsk and bypassPermissions, and the
		// documented --permission-prompt-tool, which answers by name:
		// "MCP tool requires user interaction; not supported via
		// --permission-prompt-tool". An unattended client cannot write
		// through this server at all while this is set, which is why
		// GCM_INTERACTION_HINT exists: default on, and a deployer who
		// runs unattended turns it off knowingly. What holds regardless
		// is server side: GCM_READ_ONLY leaves the tool unregistered,
		// and a dry run cannot write.
		//
		// It follows "irreversible, and other people see it" rather
		// than "destroys something". Those are not the same ranking: a
		// deleted sidebar section is private and reversible, while a
		// posted message cannot be recalled from anyone's notifications
		// and an invited member can read the whole space. Reversing the
		// two would ask for a person on the safe one and not on the
		// costly one.
		//
		// WriteIdempotent is left out on purpose: a reaction is
		// trivially undone, and a new direct message stays invisible
		// until something is posted in it, which asks here anyway.
		tool.Meta = mcp.Meta{"anthropic/requiresUserInteraction": true}
	}
	mcp.AddTool(s, tool, wrap(h, dryRunField[In]()))
}

// wrap is what every handler gets for free.
//
// Three rules that were each a line in every handler: a dry run may not
// write, an error reaches the model with its class on the front, and a
// reply carries its readable half as well as its structured one. All
// belong here for the same reason the annotations do — a rule kept by
// hand at fifty call sites is a rule that will be missed at one.
func wrap[In any, Out renderer](h mcp.ToolHandlerFor[In, Out], dryRun int) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		if dryRun >= 0 && reflect.ValueOf(in).Field(dryRun).Bool() {
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

// dryRunField is where an input type keeps its dry_run flag, or -1 when
// it has none. It is resolved once, at registration.
//
// Finding it by reflection rather than asking each tool to declare it
// is the point: a tool that offers dry_run cannot also have to remember
// to honour it. The flag puts the call on a context internal/gchat
// refuses to write under, so a handler that forgets its own preview
// branch fails loudly instead of posting.
func dryRunField[In any]() int {
	t := reflect.TypeFor[In]()
	if t.Kind() != reflect.Struct {
		return -1
	}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "dry_run" && t.Field(i).Type.Kind() == reflect.Bool {
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
