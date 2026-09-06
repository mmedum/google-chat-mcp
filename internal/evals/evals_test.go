//go:build evals

package evals

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// result is one task's outcome, kept for the report.
type result struct {
	Task        string   `json:"task"`
	Space       string   `json:"space"`
	Passed      int      `json:"passed"`
	Total       int      `json:"total"`
	Checks      []string `json:"checks"`
	Trace       *trace   `json:"trace"`
	Failures    []string `json:"failures,omitempty"`
	Unreachable string   `json:"unreachable,omitempty"`
}

var (
	mu      sync.Mutex
	results []result
)

func outDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("LIVE_OUT")
	if base == "" {
		base = filepath.Join(os.TempDir(), "google-chat-mcp-live")
	}
	dir := filepath.Join(base, "evals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestEvals runs each task as a subtest, so -run filters tasks and a
// failure names the check that failed rather than a count.
func TestEvals(t *testing.T) {
	for _, tk := range tasks {
		t.Run(tk.name, func(t *testing.T) {
			s := connect(t)
			space := s.seed(tk.name)
			t.Logf("scratch space: %s", space)
			if tk.setup != nil {
				tk.setup(s, space)
			}
			prompt := tk.prompt
			if tk.promptFn != nil {
				prompt = tk.promptFn(s, space)
			} else {
				prompt = fmt.Sprintf(prompt, space)
			}

			// The evals are an unattended deployment, so they are exactly
			// the case GCM_INTERACTION_HINT exists for. Without it every
			// write is refused before it reaches Google and the trace
			// reads like a model that would not act.
			env := map[string]string{"GCM_LOCAL_DIR": s.dir, "GCM_INTERACTION_HINT": "false"}
			for k, v := range tk.env {
				env[k] = v
			}
			tr := runClaude(t, prompt, env)

			res := result{Task: tk.name, Space: space, Trace: tr}
			if tk.reachable != nil {
				if ok, why := tk.reachable(s, space); !ok {
					res.Unreachable = why
				}
			}
			if res.Unreachable == "" {
				checks := tk.check(s, space, tr)
				res.Total = len(checks)
				for _, c := range checks {
					mark := "fail"
					if c.ok {
						mark = "pass"
						res.Passed++
					} else {
						res.Failures = append(res.Failures, c.name+": "+c.detail)
					}
					res.Checks = append(res.Checks, mark+" "+c.name)
				}
			}

			if tk.cleanup != nil {
				tk.cleanup(s, space)
			}
			s.drop(space, len(res.Failures) > 0)

			// Redact before anything is written down, never after — and
			// over the failure details too, which quote the model's own
			// answer back.
			redact(tr, space)
			for i, f := range res.Failures {
				res.Failures[i] = scrubbed(space, f)
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
			if raw, err := json.MarshalIndent(res, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(outDir(t), tk.name+".json"), raw, 0o600)
			}

			if res.Unreachable != "" {
				t.Skipf("unreachable, not the model's doing: %s", res.Unreachable)
			}
			t.Logf("%d/%d checks, %d calls, %d turns, $%.2f, %.0fs: %s",
				res.Passed, res.Total, len(tr.Calls), tr.Turns, tr.Cost, tr.Seconds,
				strings.Join(tr.toolNames(), " → "))
			for _, f := range res.Failures {
				t.Errorf("%s", f)
			}
		})
	}
	writeReport(t)
}

// TestWhichHalfTheClientForwards answers the question the evals exist
// for, and it is not about any one task.
//
// Every tool here sends both halves: structuredContent under the output
// schema, and a compact rendering in content. google-docs-mcp found that
// Claude Code forwards only the structured half to the model, which
// would make the rendering dead weight in this client and change what
// the reads should look like. This asks the client directly rather than
// inheriting the answer.
func TestWhichHalfTheClientForwards(t *testing.T) {
	s := connect(t)
	space := s.seed("which-half")
	defer s.drop(space, false)
	s.post(space, "A message for the harness to read back.", "")

	tr := runClaude(t, fmt.Sprintf("Call get_messages for the Google Chat space %s. "+
		"Then repeat, word for word, the first 200 characters the tool gave back. Do not summarise it.",
		space), map[string]string{"GCM_LOCAL_DIR": s.dir, "GCM_INTERACTION_HINT": "false"})

	calls := tr.callsTo("get_messages")
	if len(calls) == 0 {
		t.Skipf("the model never called get_messages: %s", strings.Join(tr.toolNames(), " → "))
	}
	// After redaction, and only from a call that named the scratch space:
	// a model that read a real space first would otherwise put its
	// messages in a file and a test log.
	redact(tr, space)
	var seen string
	for i, c := range tr.Calls {
		if c.tool() == "get_messages" && namesSpace(c.Input, space) {
			seen = tr.resultFor(i).Text
			break
		}
	}
	if seen == "" {
		t.Skipf("no get_messages against the scratch space: %s", strings.Join(tr.toolNames(), " → "))
	}
	var structured map[string]any
	isJSON := json.Unmarshal([]byte(seen), &structured) == nil && structured["result"] != nil
	half := "the rendered text"
	if isJSON {
		half = "structuredContent only"
	}
	t.Logf("what the client put in front of the model: %s", half)
	t.Logf("first 300 characters: %s", clip(seen, 300))
	if err := os.WriteFile(filepath.Join(outDir(t), "which-half.txt"),
		[]byte(half+"\n\n"+clip(seen, 4000)+"\n"), 0o600); err != nil {
		t.Error(err)
	}
	if isJSON {
		t.Log("the rendering is not reaching the model in this client; see docs/architecture.md " +
			"on both halves before changing anything")
	}
}

// writeReport rebuilds report.md from whatever ran, so a filtered run
// still leaves a readable artefact.
func writeReport(t *testing.T) {
	t.Helper()
	if len(results) == 0 {
		return
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Task < results[j].Task })
	var b strings.Builder
	passed, skipped, cost := 0, 0, 0.0
	for _, r := range results {
		switch {
		case r.Unreachable != "":
			skipped++
		case r.Total > 0 && r.Passed == r.Total:
			passed++
		}
		cost += r.Trace.Cost
	}
	model := os.Getenv("EVAL_MODEL")
	if model == "" {
		model = "default"
	}
	fmt.Fprintf(&b, "# Agent eval report\n\nmodel: %s; %d/%d tasks passed", model, passed, len(results))
	if skipped > 0 {
		fmt.Fprintf(&b, "; %d unreachable", skipped)
	}
	fmt.Fprintf(&b, "; total $%.2f\n\n", cost)
	b.WriteString("| task | result | checks | calls | turns | cost | s |\n|---|---|---|---|---|---|---|\n")
	for _, r := range results {
		verdict := "pass"
		switch {
		case r.Unreachable != "":
			verdict = "unreachable"
		case r.Passed != r.Total:
			verdict = "FAIL"
		}
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %d | %d | %.2f | %.1f |\n",
			r.Task, verdict, r.Passed, r.Total, len(r.Trace.Calls), r.Trace.Turns, r.Trace.Cost, r.Trace.Seconds)
	}
	b.WriteString("\n## How the tools were used\n\n")
	for _, line := range measures() {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n## Tool call sequences\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "- %s: %s\n", r.Task, strings.Join(r.Trace.toolNames(), " → "))
	}
	for _, r := range results {
		if len(r.Failures) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", r.Task)
		for _, f := range r.Failures {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	path := filepath.Join(outDir(t), "report.md")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Errorf("write report: %v", err)
		return
	}
	t.Logf("report: %s", path)
}

// measures are the cross-task numbers that say how the tools were used,
// not whether a task passed. The first one is a safety property, not a
// curiosity: a task that made the model list the account's spaces is a
// task to rewrite.
func measures() []string {
	order := []string{
		"calls to this server", "tool errors", "dry runs",
		"reached outside the scratch space", "listed the account's spaces",
		"searches scoped to one space", "plain reads of a space",
		"tool searches (deferred tool lookups)",
	}
	m := map[string]int{}
	for _, k := range order {
		m[k] = 0
	}
	for _, r := range results {
		for i, c := range r.Trace.Calls {
			if c.tool() == "ToolSearch" {
				m["tool searches (deferred tool lookups)"]++
				continue
			}
			m["calls to this server"]++
			if r.Trace.resultFor(i).Error {
				m["tool errors"]++
			}
			if b, _ := c.Input["dry_run"].(bool); b {
				m["dry runs"]++
			}
			if !namesSpace(c.Input, r.Space) {
				m["reached outside the scratch space"]++
			}
			switch c.tool() {
			case "list_spaces", "search_spaces", "find_direct_message", "find_group_chats":
				m["listed the account's spaces"]++
			case "search_messages":
				if sp, _ := c.Input["space_id"].(string); sp != "" {
					m["searches scoped to one space"]++
				}
			case "get_messages":
				m["plain reads of a space"]++
			}
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, fmt.Sprintf("%s: %d", k, m[k]))
	}
	return out
}
