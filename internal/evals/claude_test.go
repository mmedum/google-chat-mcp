//go:build evals

package evals

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// trace is what the model did: the tool calls it made, what came back,
// and what it finally said.
//
// Adapted from google-docs-mcp's eval harness, which worked out how to
// drive the client headless and how to read its transcript back.
type trace struct {
	Calls   []toolCall   `json:"calls"`
	Results []toolResult `json:"results"`
	Final   string       `json:"final"`
	Cost    float64      `json:"cost"`
	Turns   int          `json:"turns"`
	Seconds float64      `json:"seconds"`
	Exit    int          `json:"exit"`
	Stderr  string       `json:"stderr,omitempty"`
}

type toolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type toolResult struct {
	Error bool   `json:"error"`
	Text  string `json:"text"`
}

// tool is the bare tool name, without the client's mcp__server__ prefix.
func (c toolCall) tool() string { return strings.TrimPrefix(c.Name, "mcp__"+mcpName+"__") }

func (tr *trace) callsTo(name string) []toolCall {
	var out []toolCall
	for _, c := range tr.Calls {
		if c.tool() == name {
			out = append(out, c)
		}
	}
	return out
}

func (tr *trace) toolNames() []string {
	out := make([]string, 0, len(tr.Calls))
	for _, c := range tr.Calls {
		out = append(out, c.tool())
	}
	return out
}

// resultFor returns what came back from the i-th call, if anything did.
func (tr *trace) resultFor(i int) toolResult {
	if i < len(tr.Results) {
		return tr.Results[i]
	}
	return toolResult{}
}

// runClaude drives Claude Code headless with only this server's tools,
// in a workdir of its own so the client picks up no project config.
func runClaude(t *testing.T, prompt string, extraEnv map[string]string) *trace {
	t.Helper()
	dir := t.TempDir()
	serverEnv := map[string]string{"GCM_LOG_LEVEL": "warn"}
	for k, v := range extraEnv {
		serverEnv[k] = v
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		mcpName: map[string]any{"command": binPath(t), "env": serverEnv},
		// The thing that answers the permission prompt our own write
		// tools ask for. See internal/evals/approver.
		"approver": map[string]any{"command": approverPath(t)},
	}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose",
		"--mcp-config", cfgPath, "--strict-mcp-config",
		// Both spellings: the server-wide form is the bare
		// mcp__<server>, and a `*` suffix is a Bash-pattern thing that an
		// MCP tool name does not match.
		"--allowedTools", "mcp__" + mcpName, "mcp__" + mcpName + "__*",
		// An allowlist is not enough on its own, and the reason is ours:
		// every write tool here carries `anthropic/requiresUserInteraction`,
		// which this client treats as "a person, or nothing". Headless
		// there is no person, so the call is refused whatever the
		// allowlist says — under bypassPermissions too. Rather than drop
		// the hint from the server, the run supplies something that
		// answers the prompt and records what it approved.
		"--permission-prompt-tool", "mcp__approver__approve",
		// Nothing but this server. `--strict-mcp-config` keeps other MCP
		// servers out; these keep the built-in tools away from the
		// machine, and the approver out of the model's own reach.
		"--disallowedTools", "Bash", "Edit", "Write", "Read", "NotebookEdit", "WebFetch", "WebSearch", "Task",
		"mcp__approver__approve",
		"--max-budget-usd", budget(), "--no-session-persistence"}
	if m := os.Getenv("EVAL_MODEL"); m != "" {
		args = append(args, "--model", m)
	}
	cmd := exec.Command("claude", args...)
	cmd.Dir = dir
	// A nested run inherits the parent session's CLAUDE_* variables and
	// then refuses to start; strip them.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") {
			cmd.Env = append(cmd.Env, kv)
		}
	}

	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	runErr := cmd.Run()
	tr := &trace{Seconds: time.Since(started).Seconds()}
	if ee, ok := runErr.(*exec.ExitError); ok {
		tr.Exit = ee.ExitCode()
	} else if runErr != nil {
		t.Fatalf("claude: %v (is the CLI on PATH?)", runErr)
	}
	tr.Stderr = lastChars(stderr.String(), 2000)
	parseStream(stdout.String(), tr)
	return tr
}

// approverPath builds the permission-prompt server the run needs, into
// a directory that goes away with the test.
func approverPath(t *testing.T) string {
	t.Helper()
	approverOnce.Do(func() {
		// Not t.TempDir(): that goes away when the first task ends, and
		// every task after it would be handed a path to nothing.
		out := filepath.Join(outDir(t), "approver")
		cmd := exec.Command("go", "build", "-tags=evals", "-o", out, "./approver")
		cmd.Dir = "."
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build approver: %v\n%s", err, b)
		}
		approverBin = out
	})
	if approverBin == "" {
		t.Fatal("approver was not built")
	}
	return approverBin
}

var (
	approverOnce sync.Once
	approverBin  string
)

func budget() string {
	if b := os.Getenv("EVAL_BUDGET_USD"); b != "" {
		return b
	}
	return "1.50"
}

// parseStream turns the stream-json transcript into the trace. Tool
// results arrive later than their calls and are matched by id.
func parseStream(out string, tr *trace) {
	byID := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev struct {
			Type    string  `json:"type"`
			Result  string  `json:"result"`
			Cost    float64 `json:"total_cost_usd"`
			Turns   int     `json:"num_turns"`
			Message struct {
				Content []struct {
					Type      string          `json:"type"`
					ID        string          `json:"id"`
					Name      string          `json:"name"`
					Input     map[string]any  `json:"input"`
					ToolUseID string          `json:"tool_use_id"`
					IsError   bool            `json:"is_error"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		for _, c := range ev.Message.Content {
			switch {
			case ev.Type == "assistant" && c.Type == "tool_use":
				byID[c.ID] = len(tr.Calls)
				tr.Calls = append(tr.Calls, toolCall{Name: c.Name, Input: c.Input})
				tr.Results = append(tr.Results, toolResult{})
			case ev.Type == "user" && c.Type == "tool_result":
				if i, ok := byID[c.ToolUseID]; ok {
					tr.Results[i] = toolResult{Error: c.IsError, Text: clip(resultText(c.Content), 4000)}
				}
			}
		}
		if ev.Type == "result" {
			tr.Final, tr.Cost, tr.Turns = ev.Result, ev.Cost, ev.Turns
		}
	}
}

// resultText accepts both shapes a tool result takes: a bare string, or
// a list of content blocks.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, x := range blocks {
			b.WriteString(x.Text)
		}
		return b.String()
	}
	return ""
}

// spaceRef matches a Chat space resource name anywhere in a string.
var spaceRef = regexp.MustCompile(`spaces/[A-Za-z0-9_-]+`)

// redact keeps the rule the standard asks of a live run: what is written
// down is only what the run itself created.
//
// A model that calls list_spaces or search_people gets rows about real
// conversations and real colleagues, and those rows would otherwise land
// in a saved trace and a report. So a result is kept only when the call
// that produced it named the scratch space, and any other space id is
// rewritten wherever it appears, the model's own answer included.
//
// What this cannot catch is a display name or a sentence the model
// repeats in prose. That is the same gap the leak gate has, and the same
// answer applies: the tasks address the scratch space directly, and a
// task that made the model list anything is a task to rewrite.
func redact(tr *trace, space string) {
	for i, c := range tr.Calls {
		if !namesSpace(c.Input, space) {
			tr.Results[i].Text = "[redacted: this call was not scoped to the scratch space]"
		}
		// The arguments carry as much as the answers do: a get_messages
		// on a space the model found by listing names that space in its
		// input, and a search_people carries the person. Scrubbing the
		// result and keeping the argument that named it is the wrong
		// half.
		tr.Calls[i].Input = scrubValues(space, c.Input)
	}
	for i := range tr.Results {
		tr.Results[i].Text = scrubbed(space, tr.Results[i].Text)
	}
	tr.Final = scrubbed(space, tr.Final)
	tr.Stderr = scrubbed(space, tr.Stderr)
}

// scrubbed rewrites every space id that is not the scratch one. A check
// detail quotes the model's own answer, so this has to run over the
// report's prose as well as over the trace.
func scrubbed(space, s string) string {
	return spaceRef.ReplaceAllStringFunc(s, func(m string) string {
		if m == space {
			return m
		}
		return "spaces/[redacted]"
	})
}

// scrubValues rewrites every string in a tool's arguments, at any depth.
func scrubValues(space string, in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case string:
			out[k] = scrubbed(space, t)
		case map[string]any:
			out[k] = scrubValues(space, t)
		case []any:
			list := make([]any, 0, len(t))
			for _, e := range t {
				switch et := e.(type) {
				case string:
					list = append(list, scrubbed(space, et))
				case map[string]any:
					list = append(list, scrubValues(space, et))
				default:
					list = append(list, e)
				}
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}

// namesSpace reports whether a tool's arguments address the scratch
// space, at any depth: the id appears in a message or thread name too.
func namesSpace(input map[string]any, space string) bool {
	for _, v := range input {
		switch t := v.(type) {
		case string:
			if strings.Contains(t, space) {
				return true
			}
		case map[string]any:
			if namesSpace(t, space) {
				return true
			}
		case []any:
			for _, e := range t {
				if m, ok := e.(map[string]any); ok && namesSpace(m, space) {
					return true
				}
				if s, ok := e.(string); ok && strings.Contains(s, space) {
					return true
				}
			}
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
