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
	"strconv"
	"strings"
)

// The classes gate holds the tool error vocabulary closed from both
// sides.
//
// A class is the leading tag on every tool error — `[auth]`, `[scope]`,
// `[not_found]` — and the model reads it to decide what to do next. That
// makes it an interface, and an interface with no gate drifts in two
// directions at once: a class declared and never produced is a promise
// the server cannot keep, and a class produced without being declared is
// a tag nobody documented and no caller can switch on.
//
// google-drive-mcp's version of this gate compared two lists where there
// were three: it held every declared class to being emitted, and never
// read the list the code publishes, so a class could be declared,
// emitted, and missing from the published vocabulary with nothing
// objecting. This repository publishes no such list, which is why there
// are two lists here and not three — stated rather than left as an
// absence, so that whoever adds one knows to widen this.
const classesFile = "internal/service/errors.go"

// classType is the named type whose constants are the vocabulary.
const classType = "Class"

// classes reports what is wrong with the error vocabulary.
func classes(_ []string, stdout, stderr io.Writer) int {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, classesFile, nil, parser.SkipObjectResolution)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}

	declared := declaredClasses(file)
	if len(declared) == 0 {
		_, _ = fmt.Fprintf(stderr, "gates: no %s constants in %s: this gate is looking at nothing\n",
			classType, classesFile)
		return 1
	}

	used, conversions, err := classUsage(slices.Sorted(mapKeys(declared)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}

	if problems := classProblems(declared, used, conversions); len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: "+p)
		}
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "classes ok (%d in the vocabulary, all emitted)\n", len(declared))
	return 0
}

// classProblems is the rules, over the three things the source says: what
// is declared, where each one is used, and where the type is built from a
// string instead.
func classProblems(declared map[string]string, used map[string]int, conversions []string) []string {
	var problems []string
	fail := func(format string, a ...any) {
		problems = append(problems, fmt.Sprintf(format, a...))
	}

	// Two names for one tag is two ways to spell the same thing, and
	// whichever a caller switches on, half the code uses the other.
	seen := map[string]string{}
	for _, name := range slices.Sorted(mapKeys(declared)) {
		if first, ok := seen[declared[name]]; ok {
			fail("%s and %s are both %q; one tag, two names", first, name, declared[name])
			continue
		}
		seen[declared[name]] = name
	}

	// Every class has to be reachable. One that nothing produces is
	// vocabulary the documentation promises and the server never emits.
	for _, name := range slices.Sorted(mapKeys(declared)) {
		if used[name] == 0 {
			fail("%s is declared and never used, so no tool can answer with [%s]. "+
				"Emit it, or drop it from the vocabulary.", name, declared[name])
		}
	}

	// The other side of closed: a class built by converting a string
	// escapes the vocabulary entirely, and compiles.
	for _, where := range conversions {
		fail("%s builds a class from a string literal, which puts a tag outside the "+
			"vocabulary that nothing declares and no caller can switch on", where)
	}
	return problems
}

// mapKeys is the keys of a map, for slices.Sorted.
func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// declaredClasses is every constant of the class type, by name and tag.
func declaredClasses(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := value.Type.(*ast.Ident); !ok || id.Name != classType {
				continue
			}
			for i, name := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if tag, err := strconv.Unquote(lit.Value); err == nil {
					out[name.Name] = tag
				}
			}
		}
	}
	return out
}

// classUsage counts where each class name is used across the code that
// ships, and finds every place a class is built by converting a string.
//
// Tests are excluded: a class only a test names is still one no tool can
// answer with.
func classUsage(names []string) (map[string]int, []string, error) {
	counts := map[string]int{}
	var conversions []string
	fset := token.NewFileSet()

	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			found, built := usageIn(file, fset, path, names)
			for name, n := range found {
				counts[name] += n
			}
			conversions = append(conversions, built...)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return counts, conversions, nil
}

// usageIn counts class names used in one file, and the conversions in it.
//
// Both come from the parsed syntax rather than from a text scan: a class
// named in a comment is not a use, and `Class("x")` inside a string is
// not a conversion.
func usageIn(file *ast.File, fset *token.FileSet, path string, names []string) (map[string]int, []string) {
	counts := map[string]int{}
	var conversions []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			// A name in the const block is the declaration, not a use.
			// Everywhere else counts, the declaring file included —
			// Classify maps Google's refusals to classes there, and
			// that is emitting them.
			if slices.Contains(names, node.Name) && !inConstBlock(file, node.Pos()) {
				counts[node.Name]++
			}
		case *ast.CallExpr:
			id, ok := node.Fun.(*ast.Ident)
			if !ok || id.Name != classType || len(node.Args) != 1 {
				return true
			}
			if lit, ok := node.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				conversions = append(conversions,
					fmt.Sprintf("%s:%d", path, fset.Position(node.Pos()).Line))
			}
		}
		return true
	})
	return counts, conversions
}

// inConstBlock reports whether a position falls inside a const
// declaration, which is where a class is declared rather than used.
func inConstBlock(file *ast.File, pos token.Pos) bool {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if ok && gen.Tok == token.CONST && gen.Pos() <= pos && pos <= gen.End() {
			return true
		}
	}
	return false
}
