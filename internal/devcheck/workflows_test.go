package main

import (
	"os"
	"path/filepath"
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
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "extract-release-notes.sh") || !strings.Contains(line, ">") {
			continue
		}
		if !strings.Contains(line, "RUNNER_TEMP") && !strings.Contains(line, "runner.temp") {
			t.Errorf("release notes are written into the checkout: %s", strings.TrimSpace(line))
		}
	}
}
