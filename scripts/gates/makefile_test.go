package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMakefileReadsAsMakeDoes(t *testing.T) {
	mk := parseMakefile(textLines(
		"A ?= one",
		"A ?= two",
		"B = x#comment",
		"B += y",
		"C += z",
		"export D := d",
		"override E = e",
		"F := first",
		"F = second",
		`G = docs/architecture.md\#d-67 # note`,
		`H = a\\#b`,
		`I = a\\\#b # c`,
		"J = one \\",
		"    two",
		"K = $(A) $(G)",
		"check: a",
		"\techo one",
		"check: b a ## a help text",
		"check:",
		"lint:",
		"\t-echo old",
		"lint:",
		"\techo new",
		"quiet:",
		"\t@-echo one",
		"\t+ echo two",
		"\t# a comment the shell would skip",
		"long:",
		"\techo $(A) \\",
		"\t  $$HOME $$(B)",
		".DEFAULT_GOAL := help # the first goal",
	))
	// \# is a literal #, and a run of backslashes before a # is halved, as
	// GNU make 4.4.1 reads them.
	wantVars := map[string]string{"A": "one", "B": "x y", "C": "z", "D": "d", "E": "e", "F": "second",
		"G": "docs/architecture.md#d-67", "H": `a\`, "I": `a\#b`, "J": "one two", "K": "$(A) $(G)"}
	if !maps.Equal(mk.vars, wantVars) {
		t.Errorf("vars = %q, want %q", mk.vars, wantVars)
	}
	if got := mk.targets["check"].prereqs; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("check's prerequisites = %q, want [a b]: a target's rules merge them, each once", got)
	}
	if got := mk.recipe("check"); !slices.Equal(got, []string{"echo one"}) {
		t.Errorf("check's recipe = %q, want [echo one]: a rule with no recipe leaves the one before", got)
	}
	if got := mk.recipe("lint"); !slices.Equal(got, []string{"echo new"}) {
		t.Errorf("lint's recipe = %q, want [echo new]: the last recipe is the one that runs", got)
	}
	if got := mk.recipe("quiet"); !slices.Equal(got, []string{"echo one", "echo two"}) {
		t.Errorf("quiet's recipe = %q, want [echo one echo two]: @, - and + are not part of a command", got)
	}
	// They are kept beside it: a - tells make to carry on past a failure.
	if got := mk.targets["quiet"].prefixes; !slices.Equal(got, []string{"@-", "+"}) {
		t.Errorf("quiet's prefixes = %q, want [@- +]", got)
	}
	if got := mk.targets["lint"].prefixes; !slices.Equal(got, []string{""}) {
		t.Errorf("lint's prefixes = %q, want one empty: the last recipe's, as the recipe is", got)
	}
	// Each recipe line by the line it starts on, the last recipe's as the
	// recipe is, a continued line by its first.
	for target, want := range map[string][]int{"lint": {23}, "quiet": {25, 26}, "long": {29}} {
		if got := mk.targets[target].lines; !slices.Equal(got, want) {
			t.Errorf("%s's lines = %v, want %v", target, got, want)
		}
	}
	// $$ is the $ make hands the shell, never the start of a variable.
	if got := mk.recipe("long"); !slices.Equal(got, []string{"echo one $HOME $(B)"}) {
		t.Errorf("long's recipe = %q, want [echo one $HOME $(B)]", got)
	}
	if got := mk.closure("check"); !slices.Equal(got, []string{"a", "b", "check"}) {
		t.Errorf("closure(check) = %q, want [a b check]", got)
	}
	// A variable's value names variables in turn, and expands through them.
	if got := mk.expand("$(K)"); got != "one docs/architecture.md#d-67" {
		t.Errorf("expand($(K)) = %q, want %q", got, "one docs/architecture.md#d-67")
	}
	if got := mk.refs("$(K) $(Z)"); !maps.Equal(got, map[string]bool{"K": true, "A": true, "G": true, "Z": true}) {
		t.Errorf("refs = %v, want K, A, G and Z", got)
	}
	if mk.refused != nil {
		t.Errorf("refused = %q, want nothing: every line is one the reader reads", mk.refused)
	}
	// A special variable that steers only make is recorded apart, so it is
	// neither a variable a gate expands nor a target.
	if want := map[string]string{".DEFAULT_GOAL": "help"}; !maps.Equal(mk.special, want) {
		t.Errorf("special = %q, want %q", mk.special, want)
	}
	if _, ok := mk.targets[".DEFAULT_GOAL"]; ok {
		t.Error(".DEFAULT_GOAL was read as a target")
	}
}

// := expands its value once, where it is set, with the variables set so
// far: one no line sets, such as PATH, stays as written, whatever this
// host's environment holds. The value is final, so
// PATH := $(GOBIN):$(PATH) names PATH once
// (https://www.gnu.org/software/make/manual/html_node/Simple-Assignment.html).
func TestMakefileExpandsASimpleVariableWhereItIsSet(t *testing.T) {
	// The host's environment changes nothing: MAKEFILE_TEST_UNSET is set by
	// neither it nor a line of the file.
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("MAKEFILE_TEST_UNSET", "")
	if err := os.Unsetenv("MAKEFILE_TEST_UNSET"); err != nil {
		t.Fatal(err)
	}
	mk := parseMakefile(textLines(
		"A := x@v1",
		"R = $(A)",
		"B := $(A)",
		"C := $(R) $(MAKEFILE_TEST_UNSET)end",
		"A := y@v2",
		"GO ?= go",
		"GOBIN := $(shell $(GO) env GOPATH)/bin",
		"export PATH := $(GOBIN):$(PATH)",
		"D := $$HOME",
		"L = late",
		"S := one",
		"S += $(A)",
		"P := first",
		"P = $(L)",
		"A := z@v3",
		"run:",
		"\techo $(B) $(R) $(PATH) $(D)",
	))
	for ref, want := range map[string]string{
		"$(B)":     "x@v1",
		"$(C)":     "x@v1 $(MAKEFILE_TEST_UNSET)end",
		"$(R)":     "z@v3",
		"$(GOBIN)": "$(shell go env GOPATH)/bin",
		"$(PATH)":  "$(shell go env GOPATH)/bin:$(PATH)",
		"$(D)":     "$HOME",
		"$(S)":     "one y@v2",
		"$(P)":     "late",
	} {
		if got := mk.expand(ref); got != want {
			t.Errorf("expand(%s) = %q, want %q", ref, got, want)
		}
	}
	if got := mk.recipe("run"); !slices.Equal(got, []string{"echo x@v1 z@v3 $(shell go env GOPATH)/bin:$(PATH) $HOME"}) {
		t.Errorf("run's recipe = %q", got)
	}
	if mk.refused != nil {
		t.Errorf("refused = %q, want nothing", mk.refused)
	}
	// A simple variable names what its value named when it was set.
	if got := mk.refs("$(B)"); !maps.Equal(got, map[string]bool{"B": true, "A": true}) {
		t.Errorf("refs($(B)) = %v, want B and A", got)
	}
}

// A := naming a variable no line has set yet reads the host's environment
// there, or nothing, so a line that sets it later is refused by name: the
// reader reads no environment, and make would not read the later value.
// A variable only the environment sets, and a := naming the variable it
// sets, read the same on every host.
func TestMakefileRefusesASimpleVariableNamingOneSetLater(t *testing.T) {
	t.Setenv("L", "from this host")
	mk := parseMakefile(textLines(
		"D := $$HOME $(L)",
		"E := $(L) $(R)",
		"L = late",
		"L += later",
		"R ?= r",
		"export PATH := $(GOBIN):$(PATH)",
		"GOBIN := $(HOME)/bin",
		"F := $(MAKEFILE_TEST_UNSET)",
	))
	want := []string{
		"Makefile:1: a := names L, which line 3 sets after it, so make expands it from the environment or empty there; set L first",
		"Makefile:2: a := names R, which line 5 sets after it, so make expands it from the environment or empty there; set R first",
		"Makefile:6: a := names GOBIN, which line 7 sets after it, so make expands it from the environment or empty there; set GOBIN first",
	}
	if !slices.Equal(mk.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(mk.refused, "\n  "), strings.Join(want, "\n  "))
	}
	// What it refuses it reads as written, never as this host's value.
	if got := mk.expand("$(D)"); got != "$HOME $(L)" {
		t.Errorf("expand($(D)) = %q, want %q", got, "$HOME $(L)")
	}
}

// A line outside what the reader knows is refused by name, never dropped:
// a recipe or a variable it hides is one no gate would see.
func TestMakefileRefusesWhatItCannotRead(t *testing.T) {
	mk := parseMakefile(textLines(
		"ifeq ($(OS),Windows_NT)",
		"EXE = .exe",
		"endif",
		"-include local.mk",
		"VERSION != git describe",
		"$(BIN): main.go",
		"\tgo build -o $(BIN) \\",
		"\t  ./cmd/x",
		"lint: GOFLAGS = -mod=mod",
		"vet test: gates",
		"%.out: %.in",
		"clean:: cache",
		"fmt: ; gofmt -l .",
		"check: lint # fmt: ; x",
		"\tgo vet ./...",
		".RECIPEPREFIX = >",
		".DEFAULT_GOAL += lint",
		".EXTRA_PREREQS := gen",
	))
	want := []string{
		`Makefile:1: "ifeq ($(OS),Windows_NT)" is a directive or an assignment, which this reader does not read`,
		`Makefile:3: "endif" is a directive or an assignment, which this reader does not read`,
		`Makefile:4: "-include local.mk" is a directive or an assignment, which this reader does not read`,
		`Makefile:5: "VERSION != git describe" is a directive or an assignment, which this reader does not read`,
		`Makefile:6: "$(BIN): main.go" is a rule whose target is a variable, which this reader does not read`,
		`Makefile:7: "go build -o $(BIN) ./cmd/x" is a recipe line outside any rule this reader reads`,
		`Makefile:9: "lint: GOFLAGS = -mod=mod" is a target-specific variable, which this reader does not read`,
		`Makefile:10: "vet test: gates" is a rule with more than one target, which this reader does not read`,
		`Makefile:11: "%.out: %.in" is a pattern rule, which this reader does not read`,
		`Makefile:12: "clean:: cache" is a double-colon rule, which this reader does not read`,
		`Makefile:13: "fmt: ; gofmt -l ." is a recipe after a semicolon, which this reader does not read`,
		`Makefile:16: ".RECIPEPREFIX = >" sets .RECIPEPREFIX, a special variable that changes how make reads or runs the file, which this reader does not read`,
		`Makefile:17: ".DEFAULT_GOAL += lint" sets .DEFAULT_GOAL with +=; this reader reads it set with = or :=`,
		`Makefile:18: ".EXTRA_PREREQS := gen" sets .EXTRA_PREREQS, a special variable that changes how make reads or runs the file, which this reader does not read`,
	}
	if !slices.Equal(mk.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(mk.refused, "\n  "), strings.Join(want, "\n  "))
	}
	// A refused rule is no target, and a rule beside it is read whole.
	if got := slices.Sorted(maps.Keys(mk.targets)); !slices.Equal(got, []string{"check"}) {
		t.Errorf("targets = %q, want [check]", got)
	}
	if got := mk.recipe("check"); !slices.Equal(got, []string{"go vet ./..."}) {
		t.Errorf("check's recipe = %q, want [go vet ./...]", got)
	}
}

// GNU make reads GNUmakefile, then makefile, before Makefile, so a file by
// either name is the one `make` runs and every gate would read another. The
// reader refuses each by name.
func TestMakefileRefusesAFileMakeReadsFirst(t *testing.T) {
	const (
		gnu   = "GNUmakefile: GNU make reads it before Makefile, so `make` runs a file no gate reads; fold it into Makefile"
		lower = "makefile: GNU make reads it before Makefile, so `make` runs a file no gate reads; fold it into Makefile"
	)
	for _, tc := range []struct {
		name  string
		files []string
		want  []string
	}{
		{"Makefile alone", nil, nil},
		{"a GNUmakefile", []string{"GNUmakefile"}, []string{gnu}},
		{"a makefile", []string{"makefile"}, []string{lower}},
		{"both", []string{"makefile", "GNUmakefile"}, []string{gnu, lower}},
		{"a GNUmakefile in a subdirectory", []string{"sub/GNUmakefile"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, makefilePath, "all:\n\techo all\n")
			for _, f := range tc.files {
				if _, err := os.Stat(filepath.Join(root, f)); err == nil {
					t.Skipf("this file system reads %s as %s, so the two are one file", f, makefilePath)
				}
				put(t, root, f, "secrets:\n\t@echo skipped\n")
			}
			t.Chdir(root)
			mk, err := readMakefile()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(mk.refused, tc.want) {
				t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(mk.refused, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}
