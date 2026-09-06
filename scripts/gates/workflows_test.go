package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A workflow file without a shell gets PowerShell on the Windows runner
// and bash everywhere else, so a step written for one shell runs under
// the other on exactly one platform. The mangling then shows up on the
// line that runs — the sibling google-drive-mcp session lost a Windows
// job to `open cov.out: The system cannot find the file specified` —
// rather than on anything a reader would look at.
//
// The block belongs at workflow level, which covers every `run` step in
// the file. A job-level block fixes one job and leaves the next one to
// remember, which is the forgetting it was meant to stop. Confirmed
// against GitHub's workflow-syntax reference: `defaults.run` applies to
// every `run` step in the workflow, the most specific block wins, and an
// explicit `bash` runs `bash --noprofile --norc -eo pipefail {0}` rather
// than the implicit `bash -e {0}` — so it turns on pipefail too.
//
// This is also why the Linux-only workflows carry it: the file that will
// need it is the one nobody has written yet.

// shellValue is a YAML scalar as this file needs it: the quotes off and
// a trailing comment dropped.
func shellValue(raw string) string {
	value, _, _ := strings.Cut(raw, " #")
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

// pinsTheShell reports whether a workflow sets shell: bash at workflow
// level, and when it does not, what it found in place of it.
//
// Indentation is the whole question — a job's own defaults block is
// nested under `jobs:` and never starts at column zero — so the only
// depth this reads is "column zero" and "deeper than the `run:` it sits
// under", never a particular number of spaces. That is also what keeps
// `defaults.shell` out: a key at the `run:` depth ends the block, and
// `shell:` at that depth never opens one.
//
// The reason is returned because the interesting failure is not an
// absent block. `defaults.shell` is not a key GitHub has — `run` is the
// only documented child, with `shell` and `working-directory` under it —
// so a file carrying it pins nothing and still reads to a maintainer as
// though it did. "Missing" sends them looking in the wrong place.
func pinsTheShell(yaml string) (bool, string) {
	const beside = "defaults.shell, which is not a key GitHub has: shell goes under run"
	var sawDefaults, sawRun, sawBeside bool
	var otherShell string
	inDefaults, inRun := false, false
	childIndent, runIndent := 0, 0
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inDefaults = trimmed == "defaults:"
			sawDefaults = sawDefaults || inDefaults
			inRun, childIndent = false, 0
			continue
		}
		if !inDefaults {
			continue
		}
		if childIndent == 0 {
			childIndent = indent
		}
		// A key back at the run: depth has left that block behind.
		if inRun && indent <= runIndent {
			inRun = false
		}
		key, value, hasValue := strings.Cut(trimmed, ":")
		switch {
		case !inRun && trimmed == "run:" && indent == childIndent:
			inRun, runIndent, sawRun = true, indent, true
		case !inRun && key == "shell" && indent == childIndent:
			sawBeside = true
		case inRun && key == "shell" && hasValue:
			shell := shellValue(value)
			if shell == "bash" {
				return true, ""
			}
			otherShell = shell
		}
	}
	// Reported in the order a maintainer would want to hear it, not in
	// the order the lines happened to arrive: a file carrying
	// defaults.shell has a real mistake in it, and saying "no shell
	// under run" would send them to the wrong line.
	switch {
	case sawBeside:
		return false, beside
	case otherShell != "":
		return false, "defaults.run.shell: " + otherShell
	case sawRun:
		return false, "defaults.run with no shell in it"
	case sawDefaults:
		return false, "a defaults block with no run under it"
	}
	return false, "no workflow-level defaults block"
}

func TestPinsTheShellReadsTheIndentation(t *testing.T) {
	const jobLevel = `name: ci
jobs:
  test:
    defaults:
      run:
        shell: bash
    steps:
      - run: go test ./...
`
	tests := []struct {
		name string
		yaml string
		want bool
		// found is checked when it is set: the reason matters most
		// exactly where a maintainer would misread the file.
		found string
	}{
		{name: "workflow level", yaml: "name: ci\ndefaults:\n  run:\n    shell: bash\n\njobs:\n  test:\n", want: true},
		{name: "four-space indent", yaml: "name: ci\ndefaults:\n    run:\n        shell: bash\n", want: true},
		{name: "trailing comment", yaml: "name: ci\ndefaults:\n  run:\n    shell: bash  # every platform\n", want: true},
		{name: "quoted value", yaml: "name: ci\ndefaults:\n  run:\n    shell: \"bash\"\n", want: true},
		{name: "job level only", yaml: jobLevel, found: "no workflow-level defaults block"},
		{name: "nothing at all", yaml: "name: ci\njobs:\n  test:\n    steps:\n      - run: go test ./...\n",
			found: "no workflow-level defaults block"},
		{name: "another shell", yaml: "name: ci\ndefaults:\n  run:\n    shell: pwsh\n",
			found: "defaults.run.shell: pwsh"},
		{name: "working-directory but no shell", yaml: "name: ci\ndefaults:\n  run:\n    working-directory: ./x\n",
			found: "defaults.run with no shell in it"},
		{name: "commented out", yaml: "name: ci\n# defaults:\n#   run:\n#     shell: bash\n",
			found: "no workflow-level defaults block"},
		{name: "shell under a sibling of run",
			yaml:  "name: ci\ndefaults:\n  run:\n    working-directory: ./x\n  other:\n    shell: bash\n",
			found: "defaults.run with no shell in it"},
		{name: "run nested under another key",
			yaml:  "name: ci\ndefaults:\n  other:\n    run:\n      shell: bash\n",
			found: "a defaults block with no run under it"},
		// The mistake these two guard against is `defaults.shell`, which
		// is not a key GitHub has: it sits beside `run:` rather than under
		// it, so it pins nothing while reading to a maintainer as though
		// it did. A checker that only asks whether both words appeared
		// inside `defaults:` says yes to both. The google-docs-mcp session
		// had exactly that bug, in both orders.
		{name: "shell beside run, after it",
			yaml:  "name: ci\ndefaults:\n  run:\n    working-directory: ./x\n  shell: bash\n",
			found: "defaults.shell, which is not a key GitHub has: shell goes under run"},
		{name: "shell beside run, before it",
			yaml:  "name: ci\ndefaults:\n  shell: bash\n  run:\n    working-directory: ./x\n",
			found: "defaults.shell, which is not a key GitHub has: shell goes under run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := pinsTheShell(tt.yaml)
			if got != tt.want {
				t.Errorf("pinsTheShell = %v (%s), want %v", got, found, tt.want)
			}
			if tt.found != "" && found != tt.found {
				t.Errorf("reason\n got %q\nwant %q", found, tt.found)
			}
		})
	}
}

func TestWorkflowsPinTheShell(t *testing.T) {
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yml" && filepath.Ext(e.Name()) != ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		read++
		if ok, found := pinsTheShell(string(raw)); !ok {
			t.Errorf("%s does not set defaults.run.shell: bash at workflow level; found %s", e.Name(), found)
		}
	}
	// A floor on what was read: no workflows and no findings look the
	// same from here.
	if read < 3 {
		t.Fatalf("read %d workflow files, expected at least the three this repository has", read)
	}
}

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

// goreleaser's `before` hooks run before the dirty-tree check, so a hook
// that writes into the checkout fails the release — and `--snapshot`
// runs the hooks while skipping that check, so no rehearsal can show it.
// `go mod tidy` is the one that bites: it is a no-op right up until a
// dependency or a Go version resolves differently on the runner, and
// then it rewrites go.mod at tag time. Tidiness is CI's job anyway — it
// runs `go mod tidy` and fails on any diff, on the same commit the
// release is cut from, which `verify-ci` requires to be green.
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

// The coverage profile is built in two places — the Makefile for a local
// `make check`, and ci.yml because that job runs on three platforms while
// only one needs the floor. Two copies of one command is exactly the
// shape that drifts, and it did: the Makefile was widened to include
// cmd/ and ci.yml was not, so the floor read 58% locally and 0% in CI,
// and the gate that had just been fixed reported the package as
// completely uncovered.
//
// Nothing here checks the whole gate list against CI — that is the parity
// gate google-sheets-mcp built and this repository has not. This holds
// the one pair that has already broken.
func TestTheCoverageProfileIsBuiltTheSameWayInBothPlaces(t *testing.T) {
	root := repoRoot(t)
	find := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "-coverpkg=") {
				continue
			}
			// The flag, up to the next space.
			_, rest, _ := strings.Cut(line, "-coverpkg=")
			return strings.Fields(rest)[0]
		}
		t.Fatalf("no -coverpkg in %s: this test is looking at nothing", rel)
		return ""
	}

	mk, ci := find("Makefile"), find(filepath.Join(".github", "workflows", "ci.yml"))
	if mk != ci {
		t.Errorf("-coverpkg differs: Makefile has %q, ci.yml has %q. The floor then measures "+
			"a different set of packages in each place, and the narrower one reports 0%% for "+
			"whatever it cannot see", mk, ci)
	}
	// Both must cover the two trees that ship. cmd/ was outside for the
	// whole of v1.0.0 and nothing could report it.
	for _, want := range []string{"./cmd/...", "./internal/..."} {
		if !strings.Contains(mk, want) {
			t.Errorf("-coverpkg %q does not include %s", mk, want)
		}
	}
}

// What CI must run for each target `make check` depends on.
//
// A map rather than a name comparison, because the two sides say the
// same thing differently: `make cover` runs a script called
// coverage-check.sh, `make live-surface` runs a Go test by name. That
// mismatch is what google-sheets-mcp warned about — a gate comparing the
// names alone reports phantom gaps and misses real ones.
//
// The map is hand-kept, which is the thing this document keeps warning
// about, so its completeness is asserted rather than assumed: a target
// with no entry fails, which forces a decision instead of a silent pass.
var ciRunsForTarget = map[string]string{
	"fmt":          "gofmt -l",
	"vet":          "go vet ./...",
	"lint":         "golangci-lint",
	"cover":        "gates coverage",
	"vuln":         "govulncheck",
	"licenses":     "go-licenses",
	"smoke":        "stdio-smoke.sh",
	"schema-diff":  "gates schema-diff",
	"live-surface": "TestEveryToolIsExercisedOrExcused",
	"staleness":    "staleness-check.sh",
}

// `make check` and CI are two lists in two files, and whoever adds a gate
// is only ever editing one of them. Three of the four sibling servers had
// them diverged, the local one usually ahead — so the build-tagged files
// compiled on a maintainer's laptop and nowhere else. This repository had
// it twice in one day: CI skipped `go vet -tags=evals`, and later built
// the coverage profile over a narrower set than the Makefile did.
//
// §7b of the shared standard requires asserting the two run the same set.
func TestMakeCheckAndCIRunTheSameGates(t *testing.T) {
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(ci)

	var targets []string
	for _, line := range strings.Split(string(mk), "\n") {
		rest, ok := strings.CutPrefix(line, "check:")
		if !ok {
			continue
		}
		if i := strings.Index(rest, "##"); i >= 0 {
			rest = rest[:i]
		}
		targets = strings.Fields(rest)
		break
	}
	// Two empty lists compare equal, and a gate that passes because it
	// read the wrong file is worse than no gate. google-drive-mcp's
	// parity gate refuses the same way.
	if len(targets) == 0 {
		t.Fatal("no check: prerequisites found in the Makefile: this gate is looking at nothing")
	}
	if len(workflow) == 0 {
		t.Fatal("ci.yml is empty: this gate is looking at nothing")
	}

	for _, target := range targets {
		want, ok := ciRunsForTarget[target]
		if !ok {
			t.Errorf("`make check` runs %q and this gate does not know what CI runs for it. "+
				"Add it to ciRunsForTarget, then make sure ci.yml actually runs it.", target)
			continue
		}
		if !strings.Contains(workflow, want) {
			t.Errorf("`make check` runs %q but ci.yml contains no %q, so the two disagree "+
				"and a local green does not mean a green build", target, want)
		}
	}

	// The other direction: an entry for a target that check no longer
	// runs is a rule about nothing, and reads exactly like a rule that works.
	inCheck := map[string]bool{}
	for _, target := range targets {
		inCheck[target] = true
	}
	for target := range ciRunsForTarget {
		if !inCheck[target] {
			t.Errorf("ciRunsForTarget names %q, which `make check` no longer runs; drop it", target)
		}
	}

	// Every command the registry marks as a gate has to reach both
	// places too. That flag is the single list google-drive-mcp arrived
	// at after adding a gate to the dispatch, the Makefile and the
	// workflow but not the usage text — so it is only worth having if
	// something reads it.
	for _, name := range gateNames() {
		if !strings.Contains(string(mk), "gates "+name) {
			t.Errorf("the registry marks %q a gate and the Makefile never runs it", name)
		}
		if !strings.Contains(workflow, "gates "+name) {
			t.Errorf("the registry marks %q a gate and ci.yml never runs it", name)
		}
	}

	// Build tags are the half that actually bit: a suite behind a tag
	// compiles only where somebody vets it, and the Makefile is usually
	// the side that remembers.
	tags := map[string]bool{}
	for _, m := range regexp.MustCompile(`vet -tags=([a-z]+)`).FindAllStringSubmatch(string(mk), -1) {
		tags[m[1]] = true
	}
	if len(tags) == 0 {
		t.Fatal("no tagged vet passes found in the Makefile: this half is looking at nothing")
	}
	for tag := range tags {
		if !strings.Contains(workflow, "go vet -tags="+tag) {
			t.Errorf("the Makefile vets -tags=%s and ci.yml does not, so those files compile "+
				"only where someone runs make", tag)
		}
	}
}
