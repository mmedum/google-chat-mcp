//go:build live

package livecheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Everything this driver prints goes through the redactor.
//
// The driver runs against a real account, and hard rule 1 of this
// repository is that nothing internal ever reaches the repository — a
// transcript pasted into an issue included. Redaction is otherwise a
// list of call sites somebody remembered to route, and the next
// `t.Errorf` added while debugging looks exactly like the safe ones
// beside it. The shared standard asks for this gate because it caught a
// section header printing a document title in a sibling repository.
//
// The rule: a print may carry string literals, counts, and values that
// went through redact. Anything else is a value that came back from
// Google, and the gate cannot tell a safe one from a space name.

// drivesTheAPI are the files whose prints can carry what Google
// returned. Named rather than derived, so adding a file is a decision.
var drivesTheAPI = map[string]bool{
	"driver_test.go": true,
	"steps_test.go":  true,
}

// printers are the calls that reach a person's terminal.
var printers = map[string]bool{
	"Log": true, "Logf": true, "Error": true, "Errorf": true,
	"Fatal": true, "Fatalf": true, "Skipf": true,
}

// redactors are how a value is made safe to print.
var redactors = map[string]bool{"redact": true, "redactPrefixed": true}

// safeCalls produce a number or a shape, never account content.
var safeCalls = map[string]bool{"len": true, "count": true}

// safeFields are values a report may print as they are, each because it
// cannot carry account content — not because redacting it is
// inconvenient.
var safeFields = map[string]bool{
	// The scratch space this run created and named. The driver's own
	// comment calls it the only identifier a report may print
	// unredacted.
	"space": true,
	// The tool being called, from the step table in this repository.
	"name": true,
	// A count of rows the server could not model.
	"Unparsed": true,
	// A fixed enum from a closed set this repository declares.
	"NotificationSetting": true,
}

// beforeTheAccount are prints that happen before the driver has spoken
// to Google, so nothing they carry came from it.
//
// Both are start-up failures: the binary is not built, or the process
// would not connect. Redacting them would hide the path or the transport
// error that says what is actually wrong.
var beforeTheAccount = map[int]bool{79: true, 104: true}

func TestEveryPrintGoesThroughTheRedactor(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	var problems []string
	printsSeen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Only the files that talk to the account. The gates beside
		// them read the schema dump and this package's own source, so
		// everything they print is a tool name or a count — scanning
		// them would fill the report with findings that cannot be
		// account content, which is how a gate gets switched off.
		if !drivesTheAPI[e.Name()] {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !printers[sel.Sel.Name] {
				return true
			}
			printsSeen++
			for i, arg := range call.Args {
				// The format string itself is a literal by construction.
				if i == 0 {
					continue
				}
				if safeToPrint(arg) {
					continue
				}
				if e.Name() == "driver_test.go" && beforeTheAccount[fset.Position(arg.Pos()).Line] {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s: %s prints %s unredacted",
					fset.Position(arg.Pos()), sel.Sel.Name, render(fset, arg)))
			}
			return true
		})
	}

	if printsSeen == 0 {
		t.Fatal("no print calls found: this gate is looking at nothing")
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Errorf("%s\n  Wrap it in d.redact(), or add it to safeFields with the reason it cannot "+
			"carry account content.", p)
	}
	t.Logf("%d print calls, all redacted or literal", printsSeen)
}

// safeToPrint reports whether an argument cannot carry account content.
func safeToPrint(arg ast.Expr) bool {
	switch v := arg.(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return safeToPrint(v.X) && safeToPrint(v.Y)
	case *ast.UnaryExpr:
		return safeToPrint(v.X)
	case *ast.StarExpr:
		return safeToPrint(v.X)
	case *ast.ParenExpr:
		return safeToPrint(v.X)
	case *ast.Ident:
		// A bare name is a local, and a local holding account content
		// has to be redacted at the point it is printed.
		return safeFields[v.Name]
	case *ast.SelectorExpr:
		return safeFields[v.Sel.Name]
	case *ast.CallExpr:
		switch fn := v.Fun.(type) {
		case *ast.Ident:
			return redactors[fn.Name] || safeCalls[fn.Name]
		case *ast.SelectorExpr:
			return redactors[fn.Sel.Name] || safeCalls[fn.Sel.Name]
		}
	}
	return false
}

// render prints an expression back as source, for the failure message.
func render(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}
