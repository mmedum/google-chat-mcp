package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// goreleaser refuses to release from a dirty tree, and an untracked file
// in the checkout is dirty. `release --clean` says nothing about that —
// it clears dist/ and leaves everything else alone. So a step that
// redirects into the working directory before goreleaser runs breaks the
// release and nothing earlier can see it: `--snapshot`, which is the only
// way to rehearse, skips the dirty check entirely. v1.0.0's first tag
// died on exactly this, with `?? release-notes.md`, after a green
// rehearsal.
//
// The rule this holds is narrow on purpose: anything goreleaser is handed
// as `--release-notes` has to live outside the checkout. Writing it to
// the runner's temp directory is how.
func TestReleaseNotesAreWrittenOutsideTheCheckout(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	var found int
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "--release-notes") {
			continue
		}
		found++
		_, path, ok := strings.Cut(line, "--release-notes=")
		if !ok {
			t.Errorf("--release-notes without a path: %s", strings.TrimSpace(line))
			continue
		}
		// Not Fields()[0]: the path is often "${{ runner.temp }}/...",
		// which has spaces inside the expression. The rest of the line
		// is the path, and it is the whole of it that has to be outside
		// the checkout.
		path = strings.TrimSpace(path)
		// runner.temp in the action's args, RUNNER_TEMP in a run step.
		if !strings.Contains(path, "runner.temp") && !strings.Contains(path, "RUNNER_TEMP") {
			t.Errorf("--release-notes=%s is inside the checkout, which leaves the tree dirty "+
				"and fails the release; write it under the runner's temp directory", path)
		}
	}
	if found == 0 {
		t.Fatal("no --release-notes in release.yml: this test is looking at nothing")
	}

	// The other half: the step that produces the file must not redirect
	// into the checkout either, or the tree is dirty however goreleaser
	// is then pointed at it.
	//
	// The producer is matched by what it is, not by the name of the file
	// it used to live in. When that name changed this loop matched
	// nothing and the check passed while examining no lines at all,
	// which is why it now asserts it found the step.
	producers := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "gates release-notes") || !strings.Contains(line, ">") {
			continue
		}
		producers++
		if !strings.Contains(line, "RUNNER_TEMP") && !strings.Contains(line, "runner.temp") {
			t.Errorf("release notes are written into the checkout: %s", strings.TrimSpace(line))
		}
	}
	if producers == 0 {
		t.Error("no step writes the release notes: this half of the check is looking at nothing")
	}
}

// The release stage has to be on. It shipped disabled on purpose, so
// that an early tag could not publish anything before the gates were
// green, and turning it back on was a step of the first release rather
// than a line to delete early.
//
// That step was missed, and the way it failed is the reason this test
// exists rather than a comment. goreleaser does not complain: it builds,
// signs and attests exactly as it would otherwise, and simply creates no
// GitHub release. The first thing to notice was the MCP registry, three
// steps later, refusing the entry because a HEAD on the bundle URL came
// back 404 — a message about the registry, pointing at a URL, for a
// setting in a different file. `goreleaser check` does say "release is
// disabled", in the middle of an otherwise clean run.
func TestTheReleaseStageIsEnabled(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var inRelease, seen bool
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "release:"):
			inRelease, seen = true, true
			continue
		// Any other key at column zero ends the block. `changelog:`
		// carries a disable of its own, and that one is correct: the
		// notes come from CHANGELOG.md rather than commit subjects.
		case len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inRelease = false
		}
		if !inRelease {
			continue
		}
		if field, value, ok := strings.Cut(strings.TrimSpace(line), ":"); ok &&
			field == "disable" && strings.TrimSpace(value) == "true" {
			t.Error(".goreleaser.yaml disables the release stage: goreleaser will build, sign and " +
				"attest, publish no GitHub release, and the failure will surface as a 404 from the " +
				"MCP registry")
		}
	}
	if !seen {
		t.Fatal("no release: block in .goreleaser.yaml: this test is looking at nothing")
	}
}

// goreleaser checks for a dirty tree once, before the `before` hooks run
// (v2.18.1: internal/pipeline/pipeline.go runs git.Pipe, then
// before.Pipe), so a hook that rewrites a tracked file is not refused.
// The release builds from files the tag does not hold, Go stamps the
// binary vcs.modified=true, and no rehearsal can show it.
// `go mod tidy` is the one that bites: it is a no-op right up until a
// dependency or a Go version resolves differently on the runner, and
// then it rewrites go.mod at tag time. Tidiness is CI's job anyway — it
// runs `go mod tidy` and fails on any diff, on the same commit the
// release is cut from, which `verify-ci` requires to be green. A hook
// may write a file git ignores, as `winres` does;
// TestWinresWritesWhereGoreleaserBuilds holds that it is ignored.
//
// google-sheets-mcp flagged this one; it was latent here rather than
// broken, which is the only reason it is a test and not a bug report.
func TestReleaseHooksDoNotWriteIntoTheCheckout(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Commands that rewrite files in the working tree. Named rather than
	// guessed at: a general rule would either miss things or fail on
	// every hook that happens to contain a verb.
	mutating := []string{"go mod tidy", "gofmt -w", "go generate", "go fmt"}

	var inBefore, seen bool
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "before:"):
			inBefore, seen = true, true
			continue
		case len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inBefore = false
		}
		if !inBefore || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, cmd := range mutating {
			if strings.Contains(line, cmd) {
				t.Errorf("before hook %q rewrites the checkout, which fails the release on a dirty "+
					"tree while every --snapshot rehearsal stays green: %s",
					cmd, strings.TrimSpace(line))
			}
		}
	}
	if !seen {
		t.Fatal("no before: block in .goreleaser.yaml: this test is looking at nothing")
	}
}

// The coverage floor measures the packages -coverpkg names, and cmd/ sat
// outside it for the whole of v1.0.0 with nothing to report it. Parity
// holds ci.yml's coverage command to the Makefile's, word for word.
func TestTheCoverageProfileCoversBothShippedTrees(t *testing.T) {
	test := parseMakefile(readRepoFile(t, makefilePath)).targets["test"]
	if test == nil {
		t.Fatal("no test target in the Makefile: this test is looking at nothing")
	}
	var coverpkg []string
	for _, line := range test.recipe {
		for _, word := range strings.Fields(line) {
			if list, ok := strings.CutPrefix(word, "-coverpkg="); ok {
				coverpkg = strings.Split(list, ",")
			}
		}
	}
	if coverpkg == nil {
		t.Fatal("no -coverpkg in `make test`: this test is looking at nothing")
	}
	for _, want := range []string{"./cmd/...", "./internal/..."} {
		if !slices.Contains(coverpkg, want) {
			t.Errorf("`make test`'s -coverpkg %q does not include %s", coverpkg, want)
		}
	}
}

// readRepoFile is one file from the repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
