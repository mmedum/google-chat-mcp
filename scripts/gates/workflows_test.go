package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// The attestation's subject-path is read only on a tag, so a file it
// leaves out is published with no attestation while every rehearsal is
// green.
func TestTheReleaseAttestsEveryFileItPublishes(t *testing.T) {
	subjects := actionInput(readRepoFile(t, releaseWorkflowPath), "actions/attest-build-provenance", "subject-path")
	if subjects == "" {
		t.Fatal("no subject-path under actions/attest-build-provenance in release.yml: this test is looking at nothing")
	}
	checksum := checksumName(readRepoFile(t, goreleaserConfigPath))
	if checksum == "" {
		t.Fatal("no checksum name_template in .goreleaser.yaml: this test is looking at nothing")
	}
	for _, p := range releaseSubjectProblems(subjects, checksum) {
		t.Error(p)
	}
}

// releaseSubjectProblems holds the attestation's subject-path to what a
// release publishes: the archives, the bundle and the checksum file. The
// action splits the value as CSV, at commas and newlines, so a list
// separated by spaces is one glob
// (https://github.com/actions/attest/blob/508db95dd578ae2727ebd6217d5ba78e4fbda05d/src/subject.ts#L303,
// actions/attest v4.2.1, which attest-build-provenance v4.2.2 runs).
func releaseSubjectProblems(subjects, checksum string) []string {
	var problems []string
	parts := strings.FieldsFunc(subjects, func(r rune) bool { return r == ',' || r == '\n' })
	if slices.ContainsFunc(parts, func(p string) bool { return strings.ContainsAny(strings.TrimSpace(p), " \t") }) {
		problems = append(problems, fmt.Sprintf("subject-path %q separates with a space, which is read as one "+
			"glob; separate with commas or newlines", subjects))
	}
	for _, want := range []string{".tar.gz", ".zip", ".mcpb", checksum} {
		if !slices.ContainsFunc(parts, func(p string) bool { return strings.Contains(p, want) }) {
			problems = append(problems, fmt.Sprintf("subject-path %q does not name %s, so it carries no attestation",
				subjects, want))
		}
	}
	return problems
}

// actionInput is the value of one input of the first step that uses an
// action, or empty when that step leaves it unset.
func actionInput(workflow, action, key string) string {
	for _, loc := range usesAnywhere.FindAllStringSubmatchIndex(workflow, -1) {
		if workflow[loc[2]:loc[3]] == action {
			value, _ := stepInput(stepBlock(workflow, loc[0]), key)
			return value
		}
	}
	return ""
}

// checksumName is the checksum file a goreleaser config names: the
// name_template under its top-level checksum block.
func checksumName(config string) string {
	inChecksum := false
	for _, line := range strings.Split(config, "\n") {
		text := withoutYAMLComment(line)
		if text == "" {
			continue
		}
		if indentOf(line) == 0 {
			inChecksum = text == "checksum:"
			continue
		}
		if value, ok := strings.CutPrefix(text, "name_template:"); ok && inChecksum {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func TestReleaseSubjectProblems(t *testing.T) {
	attest := func(with string) string {
		return "jobs:\n  release:\n    steps:\n      - uses: actions/attest-build-provenance@" + zeroSHA +
			"  # v4.2.2\n        with:\n" + with +
			"      - uses: actions/upload-artifact@" + zeroSHA + "  # v7.0.2\n        with:\n          subject-path: dist/*.deb\n"
	}
	const all = "dist/*.tar.gz,dist/*.zip,dist/*.mcpb,dist/checksums.txt"
	for _, tc := range []struct {
		name, with string
		want       []string
	}{
		{"a comma list naming every file", "          subject-path: \"" + all + "\"\n", nil},
		{"a comma list with a trailing comment", "          subject-path: \"" + all + "\"  # what the release publishes\n", nil},
		{"a block scalar, one glob a line",
			"          subject-path: |\n            dist/*.tar.gz\n            dist/*.zip\n            dist/*.mcpb\n" +
				"            dist/checksums.txt\n          show-summary: true\n", nil},
		{"a list separated by spaces", "          subject-path: \"dist/*.tar.gz dist/*.zip dist/*.mcpb dist/checksums.txt\"\n",
			[]string{`subject-path "dist/*.tar.gz dist/*.zip dist/*.mcpb dist/checksums.txt" separates with a space, ` +
				"which is read as one glob; separate with commas or newlines"}},
		{"the bundle left out", "          subject-path: dist/*.tar.gz,dist/*.zip,dist/checksums.txt\n",
			[]string{`subject-path "dist/*.tar.gz,dist/*.zip,dist/checksums.txt" does not name .mcpb, so it carries no attestation`}},
		{"the checksum file and the Windows archives left out", "          subject-path: dist/*.tar.gz,dist/*.mcpb\n",
			[]string{
				`subject-path "dist/*.tar.gz,dist/*.mcpb" does not name .zip, so it carries no attestation`,
				`subject-path "dist/*.tar.gz,dist/*.mcpb" does not name checksums.txt, so it carries no attestation`,
			}},
		{"no subject-path in the attest step", "          show-summary: true\n",
			[]string{
				`subject-path "" does not name .tar.gz, so it carries no attestation`,
				`subject-path "" does not name .zip, so it carries no attestation`,
				`subject-path "" does not name .mcpb, so it carries no attestation`,
				`subject-path "" does not name checksums.txt, so it carries no attestation`,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subjects := actionInput(attest(tc.with), "actions/attest-build-provenance", "subject-path")
			if got := releaseSubjectProblems(subjects, "checksums.txt"); !slices.Equal(got, tc.want) {
				t.Errorf("problems\n got %q\nwant %q", got, tc.want)
			}
		})
	}
	const config = "version: 2\nrelease:\n  name_template: \"{{ .Tag }}\"\nchecksum:\n# Signed by cosign.\n" +
		"  name_template: \"SHA256SUMS\"  # the file cosign signs\n"
	if got := checksumName(config); got != "SHA256SUMS" {
		t.Errorf("checksumName = %q, want the checksum block's SHA256SUMS, its comments dropped", got)
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

// The coverage profile is built in two places — the Makefile for a local
// `make check`, and ci.yml because that job runs on three platforms while
// only one needs the floor. Two copies of one command is exactly the
// shape that drifts, and it did: the Makefile was widened to include
// cmd/ and ci.yml was not, so the floor read 58% locally and 0% in CI,
// and the gate that had just been fixed reported the package as
// completely uncovered.
//
// The parity gate compares the two commands by their first words, which
// a -coverpkg difference does not change. This holds the one flag that
// has already broken, in what each side runs, as the parity gate reads it.
func TestTheCoverageProfileIsBuiltTheSameWayInBothPlaces(t *testing.T) {
	find := func(where, runs string) string {
		_, rest, ok := strings.Cut(runs, "-coverpkg=")
		if !ok {
			t.Fatalf("no -coverpkg in what %s runs: this test is looking at nothing", where)
		}
		// The flag, up to the next space.
		return strings.Fields(rest)[0]
	}

	rules := parseMakefile(uncommented(readRepoFile(t, makefilePath)))
	mk := find("`make test`", strings.Join(rules["test"].recipe, "\n"))
	ci := find(ciWorkflowPath, executes(readRepoFile(t, ciWorkflowPath)))
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

// readRepoFile is one file from the repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
