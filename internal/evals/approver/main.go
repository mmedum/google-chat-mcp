//go:build evals

// Command approver answers the permission prompt for an eval run.
//
// It does not work, and the comment is kept because what it proves is
// worth more than the code. Checked against Claude Code 2.1.263:
//
//  1. Every write tool here carries `anthropic/requiresUserInteraction`.
//     Headless, the call is refused whatever the allowlist says — the
//     server-wide form, the exact tool name, `--permission-mode dontAsk`
//     and `bypassPermissions` were all tried, and all refused.
//  2. The documented escape is `--permission-prompt-tool`, which points
//     the client at an MCP tool that answers the prompt. This is that
//     tool.
//  3. The client calls it with `{tool_name, input, tool_use_id}`, where
//     `tool_use_id` is a **sibling** of `input`. The first version of
//     this file modelled the payload as a struct, whose inferred schema
//     refuses unknown properties, so the approval call itself failed
//     validation — and the client then merged the whole payload into the
//     target tool's arguments, where it failed as
//     `unexpected additional properties ["tool_use_id"]`. That symptom
//     appeared on `send_message` and looked like a client injecting a
//     field into every call. It was this file's bug, one tool away.
//  4. With the payload accepted, the approval succeeds and the client
//     still refuses, by name: "MCP tool requires user interaction; not
//     supported via --permission-prompt-tool".
//
// So the hint is absolute: a tool that declares it wants a person cannot
// be called by an unattended client through any route. That is the
// finding, and it is a fact about the product, not about the harness —
// unattended automation cannot write through this server while the hint
// is set.
//
// Two lessons kept here rather than in a commit message: a permission
// hook must accept a payload it does not model, or it fails in a place
// that implicates something else entirely; and a workaround worth
// building is worth testing against the thing it works around before
// anything is changed to accommodate it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// request is what the client asks about. It is deliberately permissive:
// a struct with named fields infers a schema that refuses anything else,
// and the client sends fields this does not model — `tool_use_id` among
// them. A permission hook that rejects the prompt is worse than none.
type request map[string]any

func (r request) toolName() string {
	name, _ := r["tool_name"].(string)
	return name
}

func (r request) input() map[string]any {
	in, _ := r["input"].(map[string]any)
	return in
}

// decision is the shape the client expects back, JSON-encoded inside a
// text block: allow with the input to use, or deny with a reason.
type decision struct {
	Behavior     string         `json:"behavior"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
	Message      string         `json:"message,omitempty"`
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "approver", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "approve",
		Description: "Answers a permission prompt during an eval run.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in request) (*mcp.CallToolResult, any, error) {
		// stderr, never stdout: stdout is the protocol.
		fmt.Fprintf(os.Stderr, "approver: allowing %s\n", in.toolName())
		// The arguments exactly as they came over the wire, not as this
		// struct decoded them: the question worth answering is what the
		// client sends, and a typed decode is what would hide it.
		if path := os.Getenv("EVAL_APPROVER_LOG"); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = fmt.Fprintf(f, "%s\n", req.Params.Arguments)
				_ = f.Close()
			}
		}
		raw, err := json.Marshal(decision{Behavior: "allow", UpdatedInput: in.input()})
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		}, nil, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("approver: %v", err)
	}
}
