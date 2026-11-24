package main

import (
	"cmp"
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
		message     string
		refused     bool
		// first, when set, is an earlier commit's message on the topic.
		first string
	}{
		{name: "a footer only in an earlier commit", files: code, message: "refactor, part two",
			first: "refactor\n\nChangelog: none, a rename", refused: true},
		{name: "a footer whose word runs on with an underscore", files: code,
			message: "change\n\nChangelog: none_yet, user-visible fix pending", refused: true},
		{name: "shipped code with an entry", files: withEntry(code, clWritten+clReleased)},
		{name: "shipped code with no entry", files: code, refused: true},
		{name: "shipped code with an entry under a released heading",
			files:   withEntry(code, clUnreleased+strings.Replace(clReleased, "- The first tool.\n", "- The first tool.\n- Late.\n", 1)),
			refused: true},
		{name: "shipped code with an entry the base already had", prep: map[string]string{changelogPath: clWritten + clReleased},
			files: code, refused: true},
		{name: "a release cut", prep: map[string]string{changelogPath: clWritten + clReleased},
			files: withEntry(code, clUnreleased+clNext+clReleased)},
		{name: "shipped code with an edited entry", prep: map[string]string{changelogPath: clWritten + clReleased},
			files: withEntry(code, strings.Replace(clWritten, "A second tool.", "A second tool, renamed.", 1)+clReleased), refused: true},
		{name: "shipped code with a version heading and no entries under it",
			files: withEntry(code, clUnreleased+clNextEmpty+clReleased), refused: true},
		{name: "an entry under a new version heading, [Unreleased] empty before",
			files: withEntry(code, clUnreleased+clNext+clReleased), refused: true},
		{name: "the bundle's launcher with no entry", files: map[string]string{"packaging/mcpb/launch-linux.sh": "#!/bin/sh\n"}, refused: true},
		{name: "the release build with no entry", files: map[string]string{".goreleaser.yaml": "version: 2\n"}, refused: true},
		{name: "a refactor with a Changelog: none footer", files: code,
			message: "refactor\n\nChangelog: none, the text is unchanged line for line"},
		{name: "a Changelog: None footer in another case", files: code, message: "refactor\n\nChangelog: None, a rename"},
		{name: "a Changelog: none footer with no reason", files: code, message: "refactor\n\nChangelog: none", refused: true},
		{name: "a footer whose word ends with a period", files: code, message: "refactor\n\nChangelog: none. internal rename only"},
		{name: "a footer in the reference form", files: code, message: "refactor\n\nChangelog #none, a rename", refused: true},
		{name: "a footer beside an entry under a released heading", message: "refactor\n\nChangelog: none, a rename",
			files: withEntry(code, clUnreleased+strings.Replace(clReleased, "- The first tool.\n", "- The first tool.\n- Late.\n", 1)), refused: true},
		{name: "a bump of the MCP SDK with a footer", prep: map[string]string{"go.mod": gomod + sdk}, message: "refactor\n\nChangelog: none, a rename",
			files: map[string]string{"go.mod": gomod + strings.Replace(sdk, "v1.8.0", "v1.9.0", 1)}, refused: true},
		{name: "a Changelog footer whose word only starts with none", files: code,
			message: "change\n\nChangelog: nonetheless user-visible", refused: true},
		{name: "a shipped file whose name git quotes", files: map[string]string{"internal/a/naïve notes.md": "x\n"}, refused: true},
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
			if tc.first != "" {
				stalenessGit(t, dir, "commit", "-q", "--allow-empty", "-m", tc.first)
			}
			stalenessWrite(t, dir, tc.files)
			stalenessGit(t, dir, "add", "-A")
			stalenessGit(t, dir, "commit", "-q", "-m", cmp.Or(tc.message, "change"))
			// The base branch moves on with shipped code and its own entry;
			// measured from the merge-base, neither is the topic's.
			stalenessGit(t, dir, "checkout", "-q", "main")
			stalenessChange(t, dir, map[string]string{"internal/a/c.go": "package a\n",
				changelogPath: strings.Replace(clUnreleased, "## [Unreleased]\n\n", "## [Unreleased]\n\n- Main's own.\n\n", 1) + clReleased})
			// The gate reads the binary's packages from the checkout, which
			// CI has at the head.
			stalenessGit(t, dir, "checkout", "-q", "topic")
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

// An empty `require ()` opens no block, so a replace after it is still
// read; `require(` and `require (` each open one.
func TestGoModBuildLinesReadsPastAnEmptyBlock(t *testing.T) {
	const base = "module m\n\nrequire ()\n\nreplace example.com/a => ./a\n"
	if goModBuildLines(base) == goModBuildLines(strings.Replace(base, "./a", "./b", 1)) {
		t.Error("a replace after `require ()` was swallowed")
	}
	for _, opener := range []string{"require (", "require(", "require ( // direct"} {
		gomod := "module m\n\n" + opener + "\n\texample.com/a v1.0.0\n)\n"
		if goModBuildLines(gomod) != goModBuildLines(strings.Replace(gomod, "v1.0.0", "v1.1.0", 1)) {
			t.Errorf("a bump inside `%s` counted as a build change", opener)
		}
	}
}

// staleness reads the waiver the changelog gate reads: after a release, a
// commit that says `Changelog: none, <why>` leaves [Unreleased] free to be
// empty, and a later commit without it does not. No footer waives a go.mod
// change that counts.
func TestStalenessHonorsTheChangelogWaiver(t *testing.T) {
	base := stalenessRepo(t)
	commit := func(dir, message string) {
		stalenessWrite(t, dir, map[string]string{"internal/a/b.go": "package a\n\n// " + message + "\n"})
		stalenessGit(t, dir, "add", "-A")
		stalenessGit(t, dir, "commit", "-q", "-m", message)
	}
	dir := stalenessCopy(t, base)
	commit(dir, "refactor\n\nChangelog: none, a rename")
	if got, err := checkChangelog(dir); err != nil || len(got) != 0 {
		t.Errorf("after a waived commit, checkChangelog() = %q, %v; want no problem", got, err)
	}
	commit(dir, "a user-visible change")
	if got, err := checkChangelog(dir); err != nil || len(got) != 1 || got[0] != clEmpty {
		t.Errorf("after a commit without the waiver, checkChangelog() = %q, %v; want %q", got, err, clEmpty)
	}

	// No footer waives a go.mod change staleness counts.
	dir = stalenessCopy(t, base)
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	stalenessWrite(t, dir, map[string]string{"go.mod": strings.Replace(string(raw), "go 1.21", "go 1.22", 1)})
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "build\n\nChangelog: none, only the go line")
	if got, err := checkChangelog(dir); err != nil || len(got) != 1 || got[0] != clEmpty {
		t.Errorf("after a waived go line change, checkChangelog() = %q, %v; want %q", got, err, clEmpty)
	}

	// Nor an MCP SDK bump.
	dir = stalenessCopy(t, base)
	sdk := string(raw) + "\nrequire github.com/modelcontextprotocol/go-sdk v1.8.0\n"
	stalenessWrite(t, dir, map[string]string{"go.mod": sdk})
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "the SDK at v1.8.0\n\nChangelog: none, setup")
	stalenessGit(t, dir, "tag", "v0.9.5")
	stalenessWrite(t, dir, map[string]string{"go.mod": strings.Replace(sdk, "v1.8.0", "v1.9.0", 1)})
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "bump\n\nChangelog: none, only the SDK")
	if got, err := checkChangelog(dir); err != nil || len(got) != 1 || got[0] != clEmpty {
		t.Errorf("after a waived MCP SDK bump, checkChangelog() = %q, %v; want %q", got, err, clEmpty)
	}

	// Nor a commit that also edits the CHANGELOG, where the entry it adds
	// would land under a released heading.
	dir = stalenessCopy(t, base)
	cl, err := os.ReadFile(filepath.Join(dir, changelogPath))
	if err != nil {
		t.Fatal(err)
	}
	stalenessWrite(t, dir, map[string]string{"internal/a/b.go": "package a\n\n// renamed\n",
		changelogPath: strings.Replace(string(cl), "- The first tool.\n", "- The first tool.\n- Late.\n", 1)})
	stalenessGit(t, dir, "add", "-A")
	stalenessGit(t, dir, "commit", "-q", "-m", "refactor\n\nChangelog: none, a rename")
	if got, err := checkChangelog(dir); err != nil || len(got) != 1 || got[0] != clEmpty {
		t.Errorf("after a waived commit that edits the CHANGELOG, checkChangelog() = %q, %v; want %q", got, err, clEmpty)
	}
}
