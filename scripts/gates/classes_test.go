package main

import (
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// parseClasses reads a synthetic source file the way the gate reads the
// real one.
func parseClasses(t *testing.T, src string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "errors.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return declaredClasses(file)
}

func TestDeclaredClassesReadsTheConstBlock(t *testing.T) {
	const src = `package service

type Class string

const (
	// ClassAuth means no usable credentials.
	ClassAuth Class = "auth"
	ClassScope Class = "scope"
)

// A constant of another type is not vocabulary.
const Timeout time.Duration = 3

// Neither is a variable.
var somewhere = ClassAuth
`
	got := parseClasses(t, src)
	want := map[string]string{"ClassAuth": "auth", "ClassScope": "scope"}
	if len(got) != len(want) {
		t.Fatalf("declaredClasses = %v, want %v", got, want)
	}
	for name, tag := range want {
		if got[name] != tag {
			t.Errorf("%s = %q, want %q", name, got[name], tag)
		}
	}
}

// The whole point of the gate: the two ways a closed vocabulary opens.
func TestClassProblems(t *testing.T) {
	declared := map[string]string{
		"ClassAuth":     "auth",
		"ClassNotFound": "not_found",
	}

	tests := []struct {
		name        string
		declared    map[string]string
		used        map[string]int
		conversions []string
		want        string
	}{
		{
			name:     "every class emitted, nothing converted",
			declared: declared,
			used:     map[string]int{"ClassAuth": 3, "ClassNotFound": 1},
		},
		{
			// Vocabulary the documentation promises and no tool can
			// ever answer with.
			name:     "a class nothing emits",
			declared: declared,
			used:     map[string]int{"ClassAuth": 3},
			want:     "ClassNotFound is declared and never used",
		},
		{
			// Compiles, ships, and puts a tag outside the set that
			// nothing declares and no caller can switch on.
			name:        "a class built from a string",
			declared:    declared,
			used:        map[string]int{"ClassAuth": 1, "ClassNotFound": 1},
			conversions: []string{"internal/service/spaces.go:88"},
			want:        "builds a class from a string literal",
		},
		{
			// Two names for one tag: whichever a caller switches on,
			// half the code uses the other.
			name: "one tag under two names",
			declared: map[string]string{
				"ClassAuth":  "auth",
				"ClassLogin": "auth",
			},
			used: map[string]int{"ClassAuth": 1, "ClassLogin": 1},
			want: `are both "auth"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := classProblems(tt.declared, tt.used, tt.conversions)
			switch {
			case tt.want == "":
				if len(problems) > 0 {
					t.Fatalf("a healthy vocabulary was reported: %s", strings.Join(problems, "; "))
				}
			case len(problems) != 1:
				t.Fatalf("got %d problem(s), want 1: %s", len(problems), strings.Join(problems, "; "))
			case !strings.Contains(problems[0], tt.want):
				t.Errorf("problem\n got %q\nwant something containing %q", problems[0], tt.want)
			}
		})
	}
}

// A name in the const block is the declaration. A name anywhere else is
// a use — including elsewhere in the declaring file, where Classify maps
// Google's refusals onto classes.
func TestClassUsageDoesNotCountTheDeclaration(t *testing.T) {
	const src = `package service

type Class string

const (
	ClassAuth Class = "auth"
	ClassQuota Class = "quota"
)

func Classify(err error) error {
	if err != nil {
		return Failf(ClassAuth, "log in")
	}
	return nil
}

func escape() Class { return Class("typo") }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "errors.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"ClassAuth", "ClassQuota"}
	counts, conversions := usageIn(file, fset, "errors.go", names)

	if counts["ClassAuth"] != 1 {
		t.Errorf("ClassAuth used %d times, want 1 — the const block is not a use", counts["ClassAuth"])
	}
	if counts["ClassQuota"] != 0 {
		t.Errorf("ClassQuota used %d times, want 0 — it is only declared", counts["ClassQuota"])
	}
	if len(conversions) != 1 {
		t.Fatalf("found %d conversions, want 1: %v", len(conversions), conversions)
	}
}

// The real vocabulary has to pass, or the gate is aspirational.
func TestTheRealVocabularyIsClosed(t *testing.T) {
	declared := parseClasses(t, readRepoFile(t, classesFile))
	if len(declared) < 5 {
		t.Fatalf("found %d classes in %s: this test is looking at nothing", len(declared), classesFile)
	}
	// Every one is a lower-case tag a model can read, not a Go name
	// that leaked into the wire.
	for _, name := range slices.Sorted(mapKeys(declared)) {
		tag := declared[name]
		if tag != strings.ToLower(tag) || strings.Contains(tag, " ") {
			t.Errorf("%s is the tag %q, which is not the shape the others are", name, tag)
		}
	}
}
