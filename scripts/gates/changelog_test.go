package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pull request that changes what ships adds an entry under
// [Unreleased]; one that cuts a release, bumps a required module or ships
// nothing needs none, and a bump of the MCP SDK needs one. What counts as
// shipped is staleness's, which TestTheChangelogRuleCountsWhatShips holds.
func TestTheChangelogGateWantsAnEntryPerPullRequest(t *testing.T) {
	base := stalenessRepo(t)
	raw, err := os.ReadFile(filepath.Join(base, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gomod := string(raw)
	const sdk = "require github.com/modelcontextprotocol/go-sdk v1.8.0\n"
	code := map[string]string{"internal/a/b.go": "package a\n\n// changed\n"}
	withEntry := func(files map[string]string, changelog string) map[string]string {
		out := map[string]string{changelogPath: changelog}
		for k, v := range files {
			out[k] = v
		}
		return out
	}
	for _, tc := range []struct {
		name string
		// prep is committed on main before the topic branches off.
		prep, files map[string]string
		refused     bool
	}{
		{name: "shipped code with an entry", files: withEntry(code, clWritten+clReleased)},
		{name: "shipped code with no entry", files: code, refused: true},
		{name: "shipped code with an entry under a released heading",
			files:   withEntry(code, clUnreleased+strings.Replace(clReleased, "- The first tool.\n", "- The first tool.\n- Late.\n", 1)),
			refused: true},
		{name: "shipped code with an entry the base already had", prep: map[string]string{changelogPath: clWritten + clReleased},
			files: code, refused: true},
		{name: "a release cut", prep: map[string]string{changelogPath: clWritten + clReleased},
			files: withEntry(code, clUnreleased+clNext+clReleased)},
		{name: "a dependency bump", files: map[string]string{"go.mod": strings.Replace(gomod, "dep v1.0.0", "dep v1.1.0", 1)}},
		{name: "a go line change", files: map[string]string{"go.mod": strings.Replace(gomod, "go 1.21", "go 1.22", 1)}, refused: true},
		{name: "a bump of the MCP SDK", prep: map[string]string{"go.mod": gomod + sdk},
			files: map[string]string{"go.mod": gomod + strings.Replace(sdk, "v1.8.0", "v1.9.0", 1)}, refused: true},
		{name: "nothing shipped", files: map[string]string{"docs/x.md": "changed\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := stalenessCopy(t, base)
			t.Chdir(dir)
			if tc.prep != nil {
				stalenessChange(t, dir, tc.prep)
			}
			stalenessGit(t, dir, "checkout", "-q", "-b", "topic")
			stalenessChange(t, dir, tc.files)
			// The base branch moves on with shipped code and its own entry;
			// measured from the merge-base, neither is the topic's.
			stalenessGit(t, dir, "checkout", "-q", "main")
			stalenessChange(t, dir, map[string]string{"internal/a/c.go": "package a\n",
				changelogPath: strings.Replace(clUnreleased, "## [Unreleased]\n\n", "## [Unreleased]\n\n- Main's own.\n\n", 1) + clReleased})
			var out strings.Builder
			err := changelogEntry("main", "topic", &out)
			var r refusal
			switch {
			case tc.refused && !errors.As(err, &r):
				t.Errorf("changelogEntry = %v, %q; want a refusal", err, out.String())
			case !tc.refused && err != nil:
				t.Errorf("changelogEntry = %v; want a pass", err)
			}
		})
	}
}

// A require of a noted module is read on its own line and in a block, with
// a comment after it, and nothing else is.
func TestGoModNotedRequires(t *testing.T) {
	const gomod = "module m\n\nrequire github.com/modelcontextprotocol/go-sdk v1.8.0 // direct\n\n" +
		"require (\n\texample.com/a v1.0.0\n\tgithub.com/modelcontextprotocol/go-sdk v1.9.0\n)\n"
	got := goModNotedRequires(gomod)
	want := []string{"github.com/modelcontextprotocol/go-sdk v1.8.0", "github.com/modelcontextprotocol/go-sdk v1.9.0"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("goModNotedRequires = %q, want %q", got, want)
	}
}
