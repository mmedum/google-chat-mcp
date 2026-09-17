package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The live driver and the eval harness may put a value into their
// transcript only through a redacting helper, and this reads their
// syntax trees to say so.
//
// The sibling servers gate the same promise over stdout, because their
// drivers print. These drivers log through testing.T, so a gate copied
// from a sibling would have had nothing to read and would have passed by
// construction — which is worse than no gate, because it reports a
// guarantee nobody is holding.
//
// It is not hypothetical. Writing this found `s.t.Fatalf("harness step
// %s failed: %s", name, b.String())`, where b held the tool response —
// document text, logged whole, at exactly the moment somebody pastes the
// log into an issue. Every neighbouring line went through clip. That is
// the shape the rule is for: not a rule anybody broke on purpose, but a
// line added where the habit did not reach.
const transcriptDirs = "internal/livecheck,internal/evals"

// redactors are the calls whose result may be logged. Kept to the two
// that actually redact: shown and clip both end in redact.Clip, and the
// redact package's own functions are the thing being wrapped.
var redactors = []string{"d.redact", "s.d.redact", "clip", "redactpkg.Accounts", "redact.Accounts", "redact.Account"}

// loggedPlain are the argument expressions allowed through unwrapped,
// with the reason each is safe.
//
// A list rather than a rule, because the rule people reach for — "it is
// only an identifier" — is what let b.String() through. Every entry here
// was read once; a new one fails the gate and has to be read too. The
// narrower the list, the less there is to launder.
var loggedPlain = map[string]string{
	// Names and expectations the driver itself wrote.
	"name":    "a step or tool name, from the driver's own table",
	"s.name":  "as name",
	"space":   "the scratch space this run created",
	"d.space": "as space; d.redact keeps the ids the run made and masks the rest",
	"what":    "a literal describing the step",
	"want":    "the expectation, written in the test",
	"v":       "a setting value the test passed in",
	"where":   "a file and line this process computed",
	"key":     "a settings key, from the driver's own table",
	"half":    "a literal saying which half of the answer was shown",
	"f":       "a file name this process chose",
	"p":       "as f",
	"path":    "a path this process chose under the output directory",
	"b":       "a byte count",

	// Counts, numbers and enums. None can carry a message.
	"counted":                       "a count",
	"printsSeen":                    "a count",
	"len(exercised) + len(excused)": "a count",
	"out.Unparsed":                  "a count",
	"out.NotificationSetting":       "an enum value, ALL or none",
	"got.LinkType":                  "an enum value, Google's kind of rich link",
	"res.Total":                     "a count",
	"res.Passed":                    "a count",
	"tr.Turns":                      "a count",
	"tr.Cost":                       "a number",
	"tr.Seconds":                    "a number",

	// Errors. Google's own text is masked in parseAPIError before it can
	// reach one; runErr is the exit status of a subprocess.
	"err":    "an error; the API's text is masked where it is parsed",
	"runErr": "the exit status of the claude CLI",

	// This server's own surface, not a person's content.
	"e.Name()": "a package name from the coverage list",

	// clip's own test, over a literal address. The values ARE clip's
	// output; the gate cannot see that through a variable, which is the
	// one thing it checks structurally rather than semantically.
	"got":                              "the output of clip in clip's own test",
	"cut":                              "as got",
	"strings.Join(missing, \"\\n  \")": "tool or option names the sweep did not reach",
	"strings.Join(tr.toolNames(), \" → \")": "the sequence of tool names a task called",
}

func transcript(_ []string, stdout, stderr io.Writer) int {
	if err := transcriptCheck(stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	return 0
}

func transcriptCheck(w io.Writer) error {
	root := "."
	var problems []string
	checked := 0
	for _, dir := range strings.Split(transcriptDirs, ",") {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			return err
		}
		for _, file := range files {
			// Only the tagged driver files. A test that happens to live
			// beside them and compiles without the tag is an ordinary
			// unit test, not the transcript, and holding it to this rule
			// would fill the allowlist with counters until nobody read it.
			tagged, err := hasDriverTag(file)
			if err != nil {
				return err
			}
			if !tagged {
				continue
			}
			n, found, err := transcriptProblems(file)
			if err != nil {
				return err
			}
			checked += n
			problems = append(problems, found...)
		}
	}
	// Zero calls read is the same output as zero problems, and the two
	// are not the same thing.
	if checked < 15 {
		return fmt.Errorf("only %d transcript writes read across %s; the check is not reading the drivers",
			checked, transcriptDirs)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	_, err := fmt.Fprintf(w, "transcript ok: %d writes, each a literal or through %s\n",
		checked, strings.Join(redactors, "/"))
	return err
}

// transcriptProblems reads one file, returning how many transcript
// writes it saw and what is wrong with them.
func transcriptProblems(file string) (int, []string, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return 0, nil, err
	}
	var problems []string
	calls := 0
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isTestLog(sel.Sel.Name) {
			return true
		}
		calls++
		// The first argument is the format string; the rest are values.
		for _, arg := range call.Args[min(1, len(call.Args)):] {
			expr := exprText(arg)
			switch {
			case isLiteral(arg), isRedacted(arg), isCount(arg), loggedPlain[expr] != "":
			default:
				problems = append(problems, fmt.Sprintf(
					"%s: %s reaches the transcript unredacted; wrap it in %s, or add it to loggedPlain "+
						"with the reason it is safe", fset.Position(arg.Pos()), expr,
					strings.Join(redactors[:2], " or ")))
			}
		}
		return true
	})
	return calls, problems, nil
}

func isTestLog(name string) bool {
	return slices.Contains([]string{"Log", "Logf", "Error", "Errorf", "Fatal", "Fatalf"}, name)
}

// isCount reports a call to the len builtin, which returns an int and
// so cannot carry anything. Written as a rule rather than an allowlist
// entry because there are ten of them and each says nothing.
func isCount(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "len"
}

func isLiteral(e ast.Expr) bool {
	_, ok := e.(*ast.BasicLit)
	return ok
}

// isRedacted reports whether the expression is a call to one of the
// redactors, at any depth: clip(b.String(), 400) is redacted, and so is
// fmt.Sprintf("%s", clip(...)) — the wrapper does not matter, reaching
// the redactor does.
func isRedacted(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if slices.Contains(redactors, exprText(call.Fun)) {
			found = true
			return false
		}
		return true
	})
	return found
}

// exprText is the source of an expression, near enough to name it in a
// message and to match an allowlist entry.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		// Arguments rendered too, so the allowlist can bless one call and
		// not the shape of it: strings.Join over tool names is not the
		// same decision as strings.Join over document titles.
		parts := make([]string, 0, len(v.Args))
		for _, a := range v.Args {
			parts = append(parts, exprText(a))
		}
		return exprText(v.Fun) + "(" + strings.Join(parts, ", ") + ")"
	case *ast.BasicLit:
		return v.Value
	case *ast.BinaryExpr:
		return exprText(v.X) + " " + v.Op.String() + " " + exprText(v.Y)
	case *ast.IndexExpr:
		return exprText(v.X) + "[" + exprText(v.Index) + "]"
	case *ast.UnaryExpr:
		return v.Op.String() + exprText(v.X)
	}
	return fmt.Sprintf("%T", e)
}

// hasDriverTag reports whether a file compiles only under the live or
// evals tag, which is what makes it a driver rather than a unit test.
func hasDriverTag(file string) (bool, error) {
	body, err := os.ReadFile(file)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "package ") {
			return false, nil
		}
		if strings.HasPrefix(line, "//go:build ") {
			return strings.Contains(line, "live") || strings.Contains(line, "evals"), nil
		}
	}
	return false, nil
}
