package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A package is scored on its own files and not on its subpackages'.
//
// Matching by prefix scores a parent on everything beneath it, so the
// number printed describes neither package — google-sheets-mcp had one
// reported at 68.7% whose own coverage was 90.1%, once a subpackage grew
// large enough to move it. No package in this repository has a child, so
// nothing here would have noticed; the fixture supplies the parent and
// child the tree does not have.
func TestAPackageIsScoredOnItsOwnFiles(t *testing.T) {
	blocks := []block{
		{dir: module + "/internal/parent", id: "a.go:1.1,2.2", statement: 1, covered: true},
		{dir: module + "/internal/parent/child", id: "b.go:1.1,2.2", statement: 1, covered: false},
	}
	if got := percent(blocks, module+"/internal/parent"); got != 100 {
		t.Errorf("parent scored %.1f%%, want 100%% — its own file is covered, and the uncovered "+
			"child must not drag it down", got)
	}
	if got := percent(blocks, module+"/internal/parent/child"); got != 0 {
		t.Errorf("child scored %.1f%%, want 0%%", got)
	}
}

// The same block appears once per test binary that touched it, because
// the profile is built with -coverpkg across both trees. Counting it
// twice inflates the total; counting it as uncovered because one binary
// missed it deflates the result.
func TestABlockSeenByTwoBinariesIsCountedOnce(t *testing.T) {
	blocks := []block{
		{dir: module + "/internal/x", id: "a.go:1.1,2.2", statement: 4, covered: false},
		{dir: module + "/internal/x", id: "a.go:1.1,2.2", statement: 4, covered: true},
	}
	if got := percent(blocks, module+"/internal/x"); got != 100 {
		t.Errorf("got %.1f%%, want 100%%: a block one binary covered is covered", got)
	}
}

// "Nothing below the floor" and "nothing read" print the same thing, so
// an empty profile is a failure rather than a pass.
func TestAnEmptyProfileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cov.out")
	if err := os.WriteFile(path, []byte("mode: atomic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := coverage([]string{"coverage", path}, &out, &errOut); code == 0 {
		t.Error("a profile with no blocks passed the floor")
	}
	if !bytes.Contains(errOut.Bytes(), []byte("looking at nothing")) {
		t.Errorf("stderr = %q, want it to say the gate read nothing", errOut.String())
	}
}
