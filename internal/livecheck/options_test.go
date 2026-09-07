//go:build live

package livecheck

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The other half of the surface gate.
//
// TestEveryToolIsExercisedOrExcused holds the tool NAMES. This holds the
// tool OPTIONS, because a claim that the live driver covers the surface
// decays every time the surface grows — and it grows by argument far
// more often than by tool. Four read tools gained page_token and
// send_message gained client_message_id on the day this was written, and
// every one of them would have shipped exercised by nothing: the name
// gate was already satisfied, because the tools themselves have steps.
//
// An option nothing exercises is an option whose behaviour against the
// real API is a belief. That is the thing this whole package exists to
// test, and CLAUDE.md is explicit that every fake in the unit suite is
// written from the same belief.

// excusedOptions are arguments a live run must not or cannot send, each
// with the reason. A name here that the schema no longer has fails too,
// so the list cannot rot.
// excusedOptions are single arguments a live run does not send, each
// with the reason. These are an inventory taken the day the gate was
// written: pre-existing gaps, now visible and each decided rather than
// unnoticed. Several are "the same code path as one that is exercised",
// which is a reason; "nobody got round to it" is not, and none of these
// says that.
var excusedOptions = map[string]string{
	"create_space.member_emails":                     "would put a real colleague in a scratch space; a live run involves nobody else",
	"delete_message.force":                           "deletes somebody else's message, which needs both a manager role and a message this run did not write",
	"download_attachment.attachment_name":            "the by-name form of a download the step already makes by message",
	"get_messages.since":                             "a time bound on a space minutes old, where every bound is degenerate",
	"list_space_events.since":                        "same: the scratch space has no history to bound",
	"list_space_events.until":                        "same",
	"list_spaces.space_type":                         "filters the account's own spaces rather than the scratch one, so it reads what this run did not create",
	"remove_reaction.reaction_name":                  "the by-name form of a removal the step already makes by emoji",
	"search_people.sources":                          "narrows to one upstream; the step asserts a directory hit, which the default already reaches",
	"send_message.reply_fallback":                    "needs a thread that has since been deleted to fall back from",
	"send_message.thread_name":                       "the thread step reads a thread rather than posting into one; posting a second message to build one is a step nobody has written",
	"update_space.description":                       "the same call the rename step makes, with a different field",
	"update_space_notification_setting.mute_setting": "the same call the notification step makes, with a different field",
	"upload_attachment.file_name":                    "renames an upload the step already makes under its own name",
}

// excusedPatterns excuse a whole class of option with one reason, because
// fifty individually-worded excuses is a list nobody reads and everybody
// appends to. Each says what a live run would and would not learn.
//
// This list is an inventory taken the day the gate was written, not a
// considered position on all fifty. It is honest about that: the value
// is that adding an option now forces a decision, where before it forced
// nothing.
var excusedPatterns = []struct {
	match  func(tool, opt string) bool
	reason string
}{
	{
		// The dry-run guard is structural, not per-handler: the flag
		// puts the call on a context internal/gchat refuses to write
		// under, and one live step already proves that context blocks
		// the network. Exercising it on each of eleven tools would
		// re-test one mechanism eleven times.
		match:  func(_, opt string) bool { return opt == "dry_run" },
		reason: "one structural guard, already exercised once live",
	},
	{
		// A bound this server applies before the call. What a live run
		// would test is Google's paging, not this server's clamp, and
		// the clamp has unit tests either side of every limit.
		match:  func(_, opt string) bool { return opt == "limit" || opt == "max_pages" },
		reason: "a clamp applied before the call, with unit tests either side of it",
	},
	{
		// Needs a corpus the scratch space does not have: messages from
		// other people, with attachments, links, mentions and dates
		// spread far enough apart to filter on. Building one live would
		// mean writing as somebody else.
		match:  func(tool, _ string) bool { return tool == "search_messages" },
		reason: "needs a corpus the scratch space cannot contain: other people's messages, spread over time",
	},
	{
		// A page token on a listing the scratch space cannot fill past
		// one page. The four that a step does page are the four that
		// gained the option, and they are the ones whose paging was
		// broken.
		match:  func(_, opt string) bool { return opt == "page_token" },
		reason: "the scratch space holds too few of these to reach a second page",
	},
}

// callSites are the driver helpers that reach a tool.
var callSites = map[string]bool{"must": true, "call": true}

// exercisedOptions reads this package's own source and reports which
// arguments each tool is called with.
//
// From the syntax rather than from a list somebody keeps: a list is one
// more thing to forget, and forgetting it is indistinguishable from
// coverage.
func exercisedOptions(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		read++
		// A step may build its arguments once and pass them twice —
		// which is the shape of an idempotency check, where passing the
		// SAME map is the whole point. Reading only inline literals
		// would report those options as unexercised, so a variable
		// assigned a map literal is resolved back to it.
		literals := map[string]*ast.CompositeLit{}
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(assign.Rhs) {
					continue
				}
				if lit, ok := assign.Rhs[i].(*ast.CompositeLit); ok {
					literals[id.Name] = lit
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !callSites[sel.Sel.Name] {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			tool, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if out[tool] == nil {
				out[tool] = map[string]bool{}
			}
			args, ok := call.Args[1].(*ast.CompositeLit)
			if !ok {
				id, isIdent := call.Args[1].(*ast.Ident)
				if !isIdent {
					return true
				}
				if args, ok = literals[id.Name]; !ok {
					return true
				}
			}
			for _, elt := range args.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.BasicLit)
				if !ok || key.Kind != token.STRING {
					continue
				}
				if name, err := strconv.Unquote(key.Value); err == nil {
					out[tool][name] = true
				}
			}
			return true
		})
	}
	if read == 0 {
		t.Fatal("no source read: this gate is looking at nothing")
	}
	return out
}

// toolOptions is every argument each registered tool accepts.
func toolOptions(t *testing.T) map[string][]string {
	t.Helper()
	out, err := exec.Command(binPath(t), "--dump-schemas").Output()
	if err != nil {
		t.Fatalf("--dump-schemas: %v", err)
	}
	var dump struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("decode the schema dump: %v", err)
	}
	options := map[string][]string{}
	for _, tool := range dump.Tools {
		for name := range tool.InputSchema.Properties {
			options[tool.Name] = append(options[tool.Name], name)
		}
		sort.Strings(options[tool.Name])
	}
	if len(options) == 0 {
		t.Fatal("the schema dump named no tools: this gate is looking at nothing")
	}
	return options
}

func TestEveryToolOptionIsExercisedOrExcused(t *testing.T) {
	exercised := exercisedOptions(t)
	options := toolOptions(t)

	// Only tools a step actually reaches. A tool that is excused
	// wholesale by the name gate has no options to answer for.
	var missing []string
	for tool, names := range options {
		if _, reached := exercised[tool]; !reached {
			continue
		}
		for _, opt := range names {
			if exercised[tool][opt] {
				continue
			}
			if _, ok := excusedOptions[tool+"."+opt]; ok {
				continue
			}
			if excusedByPattern(tool, opt) {
				continue
			}
			missing = append(missing, tool+"."+opt)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d option(s) on tools the driver does call are exercised by nothing:\n  %s\n"+
			"Pass it in a step, or add it to excusedOptions with the reason a live run cannot.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// The other direction: an excuse for an option that no longer exists
	// is a rule about nothing, and reads exactly like one that works.
	for key := range excusedOptions {
		tool, opt, _ := strings.Cut(key, ".")
		if !slicesContains(options[tool], opt) {
			t.Errorf("excusedOptions names %q, which is not an option on that tool any more", key)
		}
	}

	counted := 0
	for tool := range exercised {
		counted += len(options[tool])
	}
	t.Logf("%d tools reached by a step, %d options between them", len(exercised), counted)
}

// excusedByPattern reports whether a class excuse covers this option.
func excusedByPattern(tool, opt string) bool {
	for _, p := range excusedPatterns {
		if p.match(tool, opt) {
			return true
		}
	}
	return false
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
