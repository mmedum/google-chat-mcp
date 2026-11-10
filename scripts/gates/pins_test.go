package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestPinsShell is the case the check exists for. A job-level shell reads as correct and is not: a job
// added later does not inherit it. So the shape that must fail is a file
// that sets the shell under a job.
func TestPinsShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"workflow level", "name: ci\n\ndefaults:\n  run:\n    shell: bash\n\njobs:\n  test:\n    runs-on: x\n", "bash"},
		{"workflow level with a comment above it and after it",
			"name: ci\n\n# why\ndefaults:\n  # every run step\n  run:\n    shell: bash # pipefail on\n\njobs:\n  test:\n    runs-on: x\n", "bash"},
		{"job level only", "name: ci\n\njobs:\n  test:\n    runs-on: x\n    defaults:\n      run:\n        shell: bash\n", ""},
		{"defaults with no run", "name: ci\n\ndefaults:\n  something: else\n\njobs:\n  test:\n    runs-on: x\n", ""},
		{"run with no shell", "name: ci\n\ndefaults:\n  run:\n    working-directory: ./x\n\njobs:\n  a:\n    runs-on: x\n", ""},
		{"shell with no value", "defaults:\n  run:\n    shell:\n", ""},
		{"shell with only a comment", "defaults:\n  run:\n    shell: # bash\n", ""},
		{"nothing at all", "name: ci\n\njobs:\n  test:\n    runs-on: x\n", ""},
		{"a step-level shell is not it", "jobs:\n  test:\n    steps:\n      - run: x\n        shell: bash\n", ""},
		// defaults.shell is a sibling of defaults.run, not a child of it.
		{"shell beside run, not under it", "defaults:\n  run:\n  shell: bash\njobs:\n  test:\n", ""},
		// The depth rule is a depth rule rather than a two-space one.
		{"four-space indentation", "defaults:\n    run:\n        shell: bash\njobs:\n    test:\n", "bash"},
		{"tab indentation", "defaults:\n\trun:\n\t\tshell: bash\n", "bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseWorkflow("w.yml", tc.in).shell; got != tc.want {
				t.Errorf("shell = %q, want %q", got, tc.want)
			}
		})
	}
	// And the shell is bash at every level a run step can take one from:
	// another shell reads the same command its own way.
	const other = " sets the shell to \"pwsh\", and every run step runs under bash: another shell reads the command its own way"
	jobShell := func(shell string) docsEdit {
		return replace(fxCI, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    defaults:\n      run:\n        shell: "+shell+"\n")
	}
	stepShell := func(shell string) docsEdit {
		return replace(fxCI, "govulncheck@v1.8.0 ./...\n", "govulncheck@v1.8.0 ./...\n        shell: "+shell+"\n")
	}
	for _, tc := range []struct {
		name  string
		edits []docsEdit
		want  []string
	}{
		{"pwsh for the workflow", []docsEdit{replace(fxCodeQL, "    shell: bash", "    shell: pwsh")},
			[]string{".github/workflows/codeql.yml:4: the workflow" + other}},
		{"no shell for the workflow, reported once", []docsEdit{replace(fxCodeQL, "    shell: bash", "    shell:")},
			[]string{".github/workflows/codeql.yml: no workflow-level `defaults: run: shell:`. " +
				"Every run step goes to a shell, so a job-level block only fixes the jobs that exist today"}},
		{"pwsh for a job", []docsEdit{jobShell("pwsh")}, []string{".github/workflows/ci.yml:11: job test" + other}},
		{"pwsh for a step", []docsEdit{stepShell("pwsh")}, []string{".github/workflows/ci.yml:15: a step of job test" + other}},
		{"no shell for a step", []docsEdit{stepShell("")}, []string{".github/workflows/ci.yml:15: a step of job test" +
			strings.Replace(other, `"pwsh"`, `""`, 1)}},
		{"bash for a job and a step", []docsEdit{jobShell("bash"), stepShell("bash")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinsProblems(t, applyEdits(t, pinsFixture(), tc.edits...)); !slices.Equal(got, tc.want) {
				t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// TestAToolDownloadedAtLatestIsNotPinned. The gate read the YAML inputs
// an action takes and nothing else, so a tool fetched by curl in a run
// step floated past it, and the MCP registry's own publishing example
// installs its publisher from `releases/latest/download`.
//
// `runs-on: ubuntu-latest` must not trip it: an image label is not a tool
// a release artifact comes out of.
func TestAToolDownloadedAtLatestIsNotPinned(t *testing.T) {
	for _, c := range []struct {
		name, line string
		want       bool
	}{
		{"the registry's own documented example", `curl -L "https://github.com/x/y/releases/latest/download/z.tar.gz"`, true},
		{"a Go module at latest", `go run example.com/tool@latest`, true},
		{"a pinned download", `curl -L "https://github.com/x/y/releases/download/v1.8.1/z.tar.gz"`, false},
		{"a pinned module", `go run example.com/tool@v1.8.1`, false},
		{"a runner image", `    runs-on: ubuntu-latest`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := floatingDownload.MatchString(c.line); got != c.want {
				t.Errorf("floating = %v, want %v for %q", got, c.want, c.line)
			}
		})
	}
	const publish = "      - run: ./mcp-publisher publish\n"
	for _, tc := range []struct {
		name  string
		edits []docsEdit
		want  []string
	}{
		{"a download from the latest release", []docsEdit{replace(fxPublish, publish,
			"      - run: curl -fsSLO https://github.com/x/y/releases/latest/download/z.tar.gz\n"+publish)},
			[]string{".github/workflows/publish-mcp.yml:16: releases/latest/download installs whatever is newest. " +
				"Name the version: what builds a release must not float"}},
		{"a known tool at @latest", []docsEdit{replace(fxPublish, publish,
			"      - run: go run github.com/google/go-licenses@latest check ./...\n"+publish)},
			[]string{
				".github/workflows/publish-mcp.yml:16: @latest installs whatever is newest. Name the version: what builds a release must not float",
				`.github/workflows/publish-mcp.yml:16: go run github.com/google/go-licenses: go-licenses is pinned to "latest", which is not one exact version`,
				"go-licenses is v1.6.0 at .github/workflows/ci.yml:15: go run github.com/google/go-licenses and latest at " +
					".github/workflows/publish-mcp.yml:16: go run github.com/google/go-licenses: one tool, one version",
			}},
		{"latest in a comment", []docsEdit{replace(fxPublish, publish,
			"      # not releases/latest/download, which floats\n"+publish)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinsProblems(t, applyEdits(t, pinsFixture(), tc.edits...)); !slices.Equal(got, tc.want) {
				t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

func TestPinsRejectsEveryRangeForm(t *testing.T) {
	// Every range form, and every form that reads like one version and
	// is not. "~> v2.18.0" is the one that matters: it reads like a pin,
	// and goreleaser-action takes it as the newest 2.18.x.
	for _, bad := range []string{"~> v2.18.0", "~> v2", "latest", "v2", "v2.18", "1.27", "nightly", "^1.2.3", "",
		// A prerelease is dot-separated identifiers, none empty and each
		// holding a letter or a digit (https://semver.org/#spec-item-9).
		"v1.2.3-", "v1.2.3-.", "v1.2.3--", "v1.2.3-rc..1", "v1.2.3-rc.", "v1.2.3-rc.-"} {
		if exactSemver.MatchString(bad) {
			t.Errorf("%q was accepted as an exact version", bad)
		}
	}
	for _, good := range []string{"v2.18.0", "1.51.1", "v3.1.3", "v2.13.2", "v1.0.0-rc.1", "v2.0.0-rc-1", "v1.2.3-0.x-y"} {
		if !exactSemver.MatchString(good) {
			t.Errorf("%q was rejected, and it is one exact version", good)
		}
	}
	// go-version-file names a file rather than a version, and go.mod is
	// the pin. A workflow's key is read in any case.
	for key, want := range map[string]bool{
		"go-version-file": false, "version": true, "cosign-release": true, "syft-version": true,
		"GITLEAKS_VERSION": true, "COSIGN_RELEASE": true, "persist-credentials": false,
		"Tool_Version": true, "TOOL_Release": true, "Syft-Version": true, "VERSION": true,
	} {
		if got := workflowVersionKey.MatchString(key); got != want {
			t.Errorf("%q read as a workflow's version key = %v, want %v", key, got, want)
		}
	}
	// The Makefile's VERSION is the server's, not a tool's.
	for name, want := range map[string]bool{
		"GITLEAKS_VERSION": true, "COSIGN_RELEASE": true, "VERSION": false, "GOVULNCHECK": false,
	} {
		if got := pinsVersionVar.MatchString(name); got != want {
			t.Errorf("%q read as a Makefile version variable = %v, want %v", name, got, want)
		}
	}
	const sha = "f06c13b6b1a9625abc9e6e439d9c05a8f2190e94"
	if !fullSHA.MatchString(sha) {
		t.Error("a full SHA was rejected")
	}
	for _, bad := range []string{"v7.2.3", "f06c13b", strings.ToUpper(sha)} {
		if fullSHA.MatchString(bad) {
			t.Errorf("%q was accepted as a full SHA", bad)
		}
	}
}

// TestPinsGate is the gate run against this repository: the command
// `make pins` runs, so a failure here is the failure a maintainer sees.
func TestPinsGate(t *testing.T) {
	// The gate runs from the repository root; the tests run from the
	// package directory.
	t.Chdir(parityRepoRoot)
	var out strings.Builder
	if err := pins(&out, nil); err != nil {
		t.Fatalf("pins: %v\n%s", err, out.String())
	}
	// A check that silently examined nothing would report success too.
	// Literal floors under the real counts.
	var workflows, actions, installers, tools, places int
	if _, err := fmt.Sscanf(out.String(), "%d workflows with their shell pinned, %d actions by SHA, %d installers name their tool, "+
		"%d tools pinned in %d places", &workflows, &actions, &installers, &tools, &places); err != nil {
		t.Fatalf("cannot read the summary %q: %v", out.String(), err)
	}
	if workflows < 5 || actions < 25 || installers < 12 || tools < 9 || places < 20 {
		t.Errorf("summary %q is under its floors of 5 workflows, 25 actions, 12 installers, 9 tools, 20 places", out.String())
	}
}

// A made-up commit SHA and a made-up image digest.
const (
	pinSHA    = "0123456789abcdef0123456789abcdef01234567"
	pinDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// pinsFixture is a checkout the pins gate accepts: every tool it knows
// pinned once or more at one version, each installer used, every action
// classified and pinned by SHA.
func pinsFixture() map[string]string {
	setupGo := []string{"      - uses: actions/setup-go@" + pinSHA + " # v7.0.0", "        with:", "          go-version-file: go.mod"}
	steps := func(head []string, rest ...string) string {
		return textLines(append(append(head, setupGo...), rest...)...)
	}
	return map[string]string{
		"go.mod": textLines("module example.com/fixture", "", "go 1.27.3"),
		fxMakefile: textLines(
			"GO ?= go",
			"GOLANGCI_LINT ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0",
			"GOVULNCHECK   ?= golang.org/x/vuln/cmd/govulncheck@v1.8.0",
			"GO_LICENSES   ?= github.com/google/go-licenses@v1.6.0",
			"GITLEAKS      ?= github.com/zricethezav/gitleaks/v8@v8.30.1",
			"",
			"lint:",
			"\t$(GO) run $(GOLANGCI_LINT) run",
			"vuln:",
			"\t$(GO) run $(GOVULNCHECK) ./...",
			"licenses:",
			"\t$(GO) run $(GO_LICENSES) check ./...",
			"gitleaks:",
			"\t$(GO) run $(GITLEAKS) git --log-opts=HEAD .",
			"gitleaks-staged:",
			"\t$(GO) run $(GITLEAKS) git --staged .",
			"actionlint:",
			"\t$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12",
		),
		fxCI: textLines(
			"name: ci",
			"on: [push]",
			"defaults:",
			"  run:",
			"    shell: bash",
			"jobs:",
			"  test:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: actions/checkout@"+pinSHA+" # v7.0.1",
			"      - uses: actions/setup-go@"+pinSHA+" # v7.0.0",
			"        with:",
			"          go-version-file: go.mod",
			"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...",
			"      - run: go run github.com/google/go-licenses@v1.6.0 check ./...",
			"      - uses: golangci/golangci-lint-action@"+pinSHA+" # v9.3.0",
			"        with:",
			"          version: v2.14.0",
		),
		fxRelease: textLines(
			"name: release",
			"on:",
			"  push:",
			`    tags: ["v*"]`,
			"defaults:",
			"  run:",
			"    shell: bash",
			"jobs:",
			"  release:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: actions/checkout@"+pinSHA+" # v7.0.1",
			"      - uses: actions/setup-go@"+pinSHA+" # v7.0.0",
			"        with:",
			"          go-version-file: go.mod",
			"      - uses: sigstore/cosign-installer@"+pinSHA+" # v4.1.2",
			"        with:",
			"          cosign-release: v3.1.3",
			"      - uses: anchore/sbom-action/download-syft@"+pinSHA+" # v0.24.3",
			"        with:",
			"          syft-version: v1.51.1",
			"      - name: Release",
			"        with:",
			"          version: v2.18.1",
			"        uses: goreleaser/goreleaser-action@"+pinSHA+" # v7",
			"      - uses: actions/attest-build-provenance@"+pinSHA+" # v4",
			"        with:",
			"          subject-path: |",
			"            dist/x_*.tar.gz",
			"            dist/checksums.txt",
			"  registry:",
			"    needs: release",
			"    uses: ./"+fxPublish,
		),
		fxCodeQL: steps([]string{
			"name: codeql",
			"defaults:",
			"  run:",
			"    shell: bash",
			"jobs:",
			"  analyze:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: actions/checkout@" + pinSHA + " # v7.0.1",
		},
			"      - uses: github/codeql-action/init@"+pinSHA+" # codeql-bundle-v2.26.4",
			"      - uses: github/codeql-action/analyze@"+pinSHA+" # codeql-bundle-v2.26.4",
		),
		fxPublish: steps([]string{
			"name: publish",
			"defaults:",
			"  run:",
			"    shell: bash",
			"jobs:",
			"  publish:",
			"    runs-on: ubuntu-latest",
			"    steps:",
			"      - uses: actions/checkout@" + pinSHA + " # v7.0.1",
		},
			"      - uses: sigstore/cosign-installer@"+pinSHA+" # v4.1.2",
			"        with:",
			"          cosign-release: v3.1.3",
			"      - run: ./mcp-publisher publish",
			"        env:",
			"          PUBLISHER_VERSION: v1.8.1",
		),
		parityGoreleaser: textLines("version: 2", "before:", "  hooks:", "    - go mod tidy"),
	}
}

// pinsProblems runs the gate over files and returns each problem it
// reports, or the whole error when it reports no list.
func pinsProblems(t *testing.T, files map[string]string) []string {
	t.Helper()
	t.Chdir(docsTree(t, files))
	err := pins(&strings.Builder{}, nil)
	if err == nil {
		return nil
	}
	head, list, ok := strings.Cut(err.Error(), "\n")
	if !ok {
		return []string{head}
	}
	var out []string
	for _, line := range strings.Split(list, "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// TestUnpinnedTools covers what a SHA on the action cannot: an action on a
// mutable tag, an action pinned with no word of its version, an action
// nobody classified, and an installer whose tool nobody pinned. Without
// its version cosign-installer installs whatever cosign is newest.
func TestUnpinnedTools(t *testing.T) {
	const tag = "actions/checkout@v4"
	ci := func(from, to string) docsEdit { return replace(fxCI, from, to) }
	checkout := "      - uses: actions/checkout@" + pinSHA + " # v7.0.1\n      - uses: actions/setup-go"
	cosign := "      - uses: sigstore/cosign-installer@" + pinSHA + " # v4.1.2\n"
	for _, tc := range []struct {
		name  string
		edits []docsEdit
		want  []string
	}{
		{"the fixture", nil, nil},
		{"an action by tag, one with no comment, one nobody classified", []docsEdit{
			ci(checkout, "      - uses: "+tag+"\n      - uses: actions/upload-artifact@"+pinSHA+
				"\n      - uses: some-vendor/tool-installer@"+pinSHA+" # v1.0.0\n      - uses: actions/setup-go"),
		}, []string{
			`.github/workflows/ci.yml:10: actions/checkout is pinned to "v4", a tag or branch that can be moved; pin the 40-character commit SHA`,
			".github/workflows/ci.yml:11: actions/upload-artifact is pinned by SHA with no comment saying which version it is",
			".github/workflows/ci.yml:12: some-vendor/tool-installer is not classified: add it to pinsTools with the input that pins its tool, " +
				"or to pinsNotInstallers with the reason",
		}},
		{"a quoted action, read without its quotes", []docsEdit{
			ci("- uses: actions/checkout@"+pinSHA+" # v7.0.1", `- uses: "actions/checkout@`+pinSHA+`" # v7.0.1`),
		}, nil},
		{"an empty cosign-release, a key on the line below", []docsEdit{
			replace(fxRelease, "cosign-release: v3.1.3", "cosign-release:\n          other: x"),
		}, []string{
			".github/workflows/release.yml:18: cosign-release: has its value on another line; write it on the key's line",
			".github/workflows/release.yml:16: sigstore/cosign-installer is pinned by SHA and does not set cosign-release, " +
				"so the tool it installs is whatever is current that morning",
		}},
		{"an installer's input holding only a comment", []docsEdit{
			replace(fxRelease, "syft-version: v1.51.1", "syft-version: # v1.51.1"),
		}, []string{
			".github/workflows/release.yml:21: syft-version: has its value on another line; write it on the key's line",
			".github/workflows/release.yml:19: anchore/sbom-action/download-syft is pinned by SHA and does not set syft-version, " +
				"so the tool it installs is whatever is current that morning",
			"syft is pinned nowhere: a tool this gate names that no file pins is either gone or unread",
		}},
		// The next step's pin of the same tool does not cover this one.
		{"two cosign steps, one pinned", []docsEdit{replace(fxPublish, cosign, cosign+cosign)}, []string{
			".github/workflows/publish-mcp.yml:13: sigstore/cosign-installer is pinned by SHA and does not set cosign-release, " +
				"so the tool it installs is whatever is current that morning",
		}},
		{"an installer whose version key sits under env:", []docsEdit{
			replace(fxRelease, "      - name: Release\n        with:\n", "      - name: Release\n        env:\n"),
		}, []string{
			".github/workflows/release.yml:25: goreleaser/goreleaser-action is pinned by SHA and does not set version, " +
				"so the tool it installs is whatever is current that morning",
			".github/workflows/release.yml:24: the env value version names a version of a tool pinsTools does not know",
			"goreleaser is pinned nowhere: a tool this gate names that no file pins is either gone or unread",
		}},
		// setup-go reads go-version before go-version-file, so a go-version
		// beside go.mod is the Go the step installs.
		{"setup-go with no go-version-file, with another go.mod, and with a go-version beside go.mod", []docsEdit{
			ci("        with:\n          go-version-file: go.mod\n", ""),
			replace(fxCodeQL, "go-version-file: go.mod", "go-version-file: tools/go.mod"),
			replace(fxPublish, "go-version-file: go.mod", "go-version: 1.26.0\n          go-version-file: go.mod"),
		}, []string{
			".github/workflows/ci.yml:11: actions/setup-go is pinned by SHA and does not set go-version-file, " +
				"so the tool it installs is whatever is current that morning",
			`.github/workflows/codeql.yml:10: actions/setup-go sets go-version-file to "tools/go.mod", which is not the repository's go.mod`,
			".github/workflows/publish-mcp.yml:12: actions/setup-go sets go-version, which setup-go reads before go-version-file; " +
				"pin Go by go.mod alone",
		}},
		// An installer's version given as an expression is no exact version,
		// whatever env: sets it, and that env value names no tool.
		{"an installer's version given as an expression", []docsEdit{
			replace(fxRelease, "defaults:\n", "env:\n  GORELEASER_VERSION: v2.18.1\ndefaults:\n"),
			replace(fxRelease, "          version: v2.18.1", "          version: ${{ env.GORELEASER_VERSION }}"),
		}, []string{
			`.github/workflows/release.yml:26: goreleaser/goreleaser-action: goreleaser is pinned to "${{ env.GORELEASER_VERSION }}", which is not one exact version`,
			".github/workflows/release.yml:6: the env value GORELEASER_VERSION names a version of a tool pinsTools does not know",
		}},
		{"an installer nothing uses", []docsEdit{
			replace(fxRelease, "      - uses: anchore/sbom-action/download-syft@"+pinSHA+" # v0.24.3\n        with:\n          syft-version: v1.51.1\n", ""),
		}, []string{
			"syft is pinned nowhere: a tool this gate names that no file pins is either gone or unread",
			"no workflow step installs syft through anchore/sbom-action/download-syft; the reader is not seeing the step, " +
				"or the table names an installer nothing uses",
		}},
		{"jobs that call a reusable workflow, held to the SHA rule and an exact version", []docsEdit{
			replace(fxCodeQL, "analyze@"+pinSHA+" # codeql-bundle-v2.26.4\n", "analyze@"+pinSHA+" # codeql-bundle-v2.26.4\n"+
				"  call:\n    uses: o/r/.github/workflows/x.yml@"+pinSHA+" # v1.0.0\n    with:\n      cosign-release: latest\n"+
				"      ref-version: ${{ github.ref_name }}\n"+
				"  tagged:\n    uses: o/r/.github/workflows/x.yml@v1\n"),
		}, []string{
			`.github/workflows/codeql.yml:18: o/r/.github/workflows/x.yml: cosign-release is "latest", which is not one exact version`,
			`.github/workflows/codeql.yml:19: o/r/.github/workflows/x.yml: ref-version is "${{ github.ref_name }}", which is not one exact version`,
			`.github/workflows/codeql.yml:21: o/r/.github/workflows/x.yml is pinned to "v1", a tag or branch that can be moved; pin the 40-character commit SHA`,
		}},
		// The runner reads an input's name in upper case, so an installer's
		// input in another case is the one that pins its tool.
		{"an installer's input in another case", []docsEdit{
			replace(fxRelease, "cosign-release: v3.1.3", "COSIGN-RELEASE: latest"),
		}, []string{
			`.github/workflows/release.yml:18: sigstore/cosign-installer: cosign is pinned to "latest", which is not one exact version`,
			"cosign is v3.1.3 at .github/workflows/publish-mcp.yml:15: sigstore/cosign-installer and " +
				"latest at .github/workflows/release.yml:18: sigstore/cosign-installer: one tool, one version",
		}},
		{"a version input in another case on an action that installs nothing", []docsEdit{
			ci("      - uses: actions/checkout@"+pinSHA+" # v7.0.1\n", "      - uses: actions/checkout@"+pinSHA+" # v7.0.1\n"+
				"        with:\n          Tool-Version: latest\n"),
		}, []string{
			`.github/workflows/ci.yml:12: actions/checkout: Tool-Version is "latest", which is not one exact version`,
		}},
		// This repository's own reusable workflow is read as a workflow, so a
		// version it is given is held where it uses it. A local action's
		// action.yml is read by no gate, so its inputs are held here.
		{"a version given to a local reusable workflow, and to a local action", []docsEdit{
			replace(fxRelease, "    uses: ./"+fxPublish+"\n", "    uses: ./"+fxPublish+"\n    with:\n      release-version: ${{ github.ref_name }}\n"),
			ci("      - uses: actions/checkout@"+pinSHA+" # v7.0.1\n", "      - uses: actions/checkout@"+pinSHA+" # v7.0.1\n"+
				"      - uses: ./.github/actions/setup\n        with:\n          tool-version: latest\n          ref-version: ${{ github.ref_name }}\n"),
		}, []string{
			`.github/workflows/ci.yml:14: ./.github/actions/setup: ref-version is "${{ github.ref_name }}", which is not one exact version`,
			`.github/workflows/ci.yml:13: ./.github/actions/setup: tool-version is "latest", which is not one exact version`,
		}},
		// A literal given to this repository's own reusable workflow is held
		// there too: a run step in it may fetch whatever version it is given.
		// A literal inside an expression is a literal all the same.
		{"a literal version given to a local reusable workflow", []docsEdit{
			replace(fxRelease, "    uses: ./"+fxPublish+"\n", "    uses: ./"+fxPublish+"\n    with:\n      tool-version: latest\n"+
				"      Other-Release: v1\n      quoted-version: ${{ 'latest' }}\n      fallback-version: ${{ inputs.v || 'latest' }}\n"+
				"      ref-version: ${{ github.ref_name }}\n      exact-version: v1.2.3\n"),
		}, []string{
			`.github/workflows/release.yml:36: ./.github/workflows/publish-mcp.yml: Other-Release is "v1", which is not one exact version`,
			`.github/workflows/release.yml:38: ./.github/workflows/publish-mcp.yml: fallback-version is "${{ inputs.v || 'latest' }}", which is not one exact version`,
			`.github/workflows/release.yml:37: ./.github/workflows/publish-mcp.yml: quoted-version is "${{ 'latest' }}", which is not one exact version`,
			`.github/workflows/release.yml:35: ./.github/workflows/publish-mcp.yml: tool-version is "latest", which is not one exact version`,
		}},
		{"a container image by tag, by digest and on the line after its key", []docsEdit{
			ci(checkout, "      - uses: docker://alpine:3.20\n      - uses: docker://alpine@"+pinDigest+"\n"+checkout),
			ci("    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    container: node:18\n"+
				"    services:\n      db:\n        image: postgres@"+pinDigest+"\n      cache:\n        image:\n          redis:7\n      api:\n        image: redis:7\n"),
		}, []string{
			".github/workflows/ci.yml:14: image: has its value on another line; write it on the key's line",
			".github/workflows/ci.yml:19: job test: docker://alpine:3.20 is a container image with no sha256 digest, so the image it runs can change",
			".github/workflows/ci.yml:9: job test runs the container from node:18, which has no sha256 digest, so the image it runs can change",
			".github/workflows/ci.yml:17: job test runs the service api from redis:7, which has no sha256 digest, so the image it runs can change",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pinsProblems(t, applyEdits(t, pinsFixture(), tc.edits...))
			if !slices.Equal(got, tc.want) {
				t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// recipeRemedy is how a recipe runs a tool instead of from the PATH.
const recipeRemedy = "run it as `$(GO) run $(NAME)`, NAME a `NAME ?= module@version` variable"

// Each row is a way a tool's version floats, two files disagree, or a
// shape the reader cannot see, and every problem the gate names for it.
func TestPinsNamesEachProblem(t *testing.T) {
	const tag = "actions/checkout@v4"
	ci := func(from, to string) docsEdit { return replace(fxCI, from, to) }
	mk := func(from, to string) docsEdit { return replace(fxMakefile, from, to) }
	checkout := "      - uses: actions/checkout@" + pinSHA + " # v7.0.1\n      - uses: actions/setup-go"
	for _, tc := range []struct {
		name  string
		edits []docsEdit
		want  []string
	}{
		{"a uses: with its action on the next line", []docsEdit{
			ci(checkout, "      - uses:\n          "+tag+"\n      - uses: actions/setup-go"),
		}, []string{".github/workflows/ci.yml:10: uses: has its value on another line; write it on the key's line"}},
		{"a step as a flow mapping", []docsEdit{
			ci(checkout, "      - {uses: "+tag+"}\n      - uses: actions/setup-go"),
		}, []string{".github/workflows/ci.yml:10: uses: is not a block key on its own line; write the step in block style"}},
		{"a merge key", []docsEdit{
			ci("      - uses: golangci", "      - <<: {with: {x: y}}\n        uses: golangci"),
		}, []string{".github/workflows/ci.yml:16: GitHub Actions does not document merge keys; write the mapping out"}},
		{"an alias and a block scalar where an action goes", []docsEdit{
			ci(checkout, "      - uses: *checkout\n      - uses: >-\n          "+tag+"\n      - uses: actions/setup-go"),
		}, []string{
			".github/workflows/ci.yml:10: uses: an anchor, an alias or a tag; write the value out",
			".github/workflows/ci.yml:11: uses: a block scalar; write the value on the key's line",
		}},
		{"a range, a major line and a version input of no known tool", []docsEdit{
			replace(fxRelease, "          version: v2.18.1", "          version: \"~> v2\"\n          setup-version: v2"),
		}, []string{
			`.github/workflows/release.yml:24: goreleaser/goreleaser-action: goreleaser is pinned to "~> v2", which is not one exact version`,
			`.github/workflows/release.yml:25: goreleaser/goreleaser-action: setup-version is "v2", which is not one exact version`,
		}},
		// The .goreleaser.yaml reader trusts one goreleaser's key paths.
		{"goreleaser at a version the config reader was not checked against", []docsEdit{
			replace(fxRelease, "          version: v2.18.1", "          version: v2.13.1"),
		}, []string{".github/workflows/release.yml:24: goreleaser/goreleaser-action runs goreleaser v2.13.1, and the " +
			".goreleaser.yaml reader is checked against v2.18.1's source; check its key paths and template rule " +
			"against v2.13.1's, then set goreleaserChecked"}},
		{"an env version of a tool nobody named", []docsEdit{
			replace(fxPublish, "PUBLISHER_VERSION: v1.8.1", "PUBLISHER_VERSION: v1.8.1\n          NODE_VERSION: 22.1.0"),
		}, []string{".github/workflows/publish-mcp.yml:19: the env value NODE_VERSION names a version of a tool pinsTools does not know"}},
		// The with: inputs and the env keys are read by one predicate, so a
		// release key under env: is a version as cosign-release is.
		{"env versions named another way", []docsEdit{
			replace(fxPublish, "PUBLISHER_VERSION: v1.8.1", "PUBLISHER_VERSION: v1.8.1\n          COSIGN_RELEASE: latest\n          node-version: latest"),
		}, []string{
			".github/workflows/publish-mcp.yml:19: the env value COSIGN_RELEASE names a version of a tool pinsTools does not know",
			".github/workflows/publish-mcp.yml:20: the env value node-version names a version of a tool pinsTools does not know",
		}},
		// An env value's name is read ignoring case, as an input's is, so a
		// run step downloading by ${Tool_Version} takes a version all the same.
		{"env versions in another case", []docsEdit{
			replace(fxPublish, "PUBLISHER_VERSION: v1.8.1", "PUBLISHER_VERSION: v1.8.1\n          Tool_Version: latest\n"+
				"          TOOL_Release: latest\n          Version: latest\n          Syft_Version: >-\n            latest"),
		}, []string{
			".github/workflows/publish-mcp.yml:22: Syft_Version: a block scalar; write the value on the key's line",
			".github/workflows/publish-mcp.yml:19: the env value Tool_Version names a version of a tool pinsTools does not know",
			".github/workflows/publish-mcp.yml:20: the env value TOOL_Release names a version of a tool pinsTools does not know",
			".github/workflows/publish-mcp.yml:21: the env value Version names a version of a tool pinsTools does not know",
		}},
		{"an env version that is not one version", []docsEdit{
			replace(fxPublish, "PUBLISHER_VERSION: v1.8.1", "PUBLISHER_VERSION: v1"),
		}, []string{`.github/workflows/publish-mcp.yml:18: PUBLISHER_VERSION: mcp-publisher is pinned to "v1", which is not one exact version`}},
		{"a run's own arguments, which are not packages", []docsEdit{
			ci("govulncheck@v1.8.0 ./...", "govulncheck@v1.8.0 ./...\n      - run: go run ./cmd/notify -to user@example.com"),
		}, nil},
		{"a tool CI runs that the gate does not know", []docsEdit{
			ci("govulncheck@v1.8.0 ./...", "govulncheck@v1.8.0 ./...\n      - run: go run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./..."),
		}, []string{".github/workflows/ci.yml:15: go run honnef.co/go/tools/cmd/staticcheck@v0.6.1 names a tool pinsTools does not know"}},
		{"tools CI installs, each read past the flags", []docsEdit{
			ci("govulncheck@v1.8.0 ./...", "govulncheck@v1.8.0 ./...\n      - run: go install -v golang.org/x/vuln/cmd/govulncheck@v1.8.1 honnef.co/go/tools/cmd/staticcheck@v0.6.1"),
		}, []string{
			".github/workflows/ci.yml:15: go install honnef.co/go/tools/cmd/staticcheck@v0.6.1 names a tool pinsTools does not know",
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.8.1 at " +
				".github/workflows/ci.yml:15: go install golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		{"a version a reader cannot see, through bash -c", []docsEdit{
			ci("govulncheck@v1.8.0 ./...", "govulncheck@v1.8.0 ./...\n      - run: bash -c \"go run golang.org/x/vuln/cmd/govulncheck@master ./...\""),
		}, []string{
			`.github/workflows/ci.yml:15: golang.org/x/vuln/cmd/govulncheck: govulncheck is pinned to "master", which is not one exact version`,
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and master at " +
				".github/workflows/ci.yml:15: golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		// make's ?= takes a value the environment gives, so an env: naming a
		// tool's module@version is the version make runs.
		{"a tool's version in an env value a make target takes", []docsEdit{
			ci("govulncheck@v1.8.0 ./...", "govulncheck@v1.8.0 ./...\n      - run: make vuln\n        env:\n"+
				"          GOVULNCHECK: golang.org/x/vuln/cmd/govulncheck@v1.1.0"),
		}, []string{
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.1.0 at " +
				".github/workflows/ci.yml:17: golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		{"CI and the Makefile at two versions of each tool", []docsEdit{
			ci("govulncheck@v1.8.0", "govulncheck@v1.8.1"),
			ci("          version: v2.14.0", "          version: v2.13.0"),
			ci("go-licenses@v1.6.0", "go-licenses@v1.6.1"),
		}, []string{
			"golangci-lint is v2.13.0 at .github/workflows/ci.yml:18: golangci/golangci-lint-action and v2.14.0 at Makefile: GOLANGCI_LINT: one tool, one version",
			"govulncheck is v1.8.1 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.8.0 at Makefile: GOVULNCHECK: one tool, one version",
			"go-licenses is v1.6.1 at .github/workflows/ci.yml:15: go run github.com/google/go-licenses and v1.6.0 at Makefile: GO_LICENSES: one tool, one version",
		}},
		{"the Makefile at latest, a range and a variable no recipe runs", []docsEdit{
			mk("go-licenses@v1.6.0", "go-licenses@latest"),
			mk("golangci-lint@v2.14.0", "golangci-lint@v2.14"),
			mk("\t$(GO) run $(GOVULNCHECK) ./...", "\t@echo $(GOVULNCHECK)\n\tgovulncheck ./..."),
		}, []string{
			`Makefile: GOLANGCI_LINT: golangci-lint is pinned to "v2.14", which is not one exact version`,
			"Makefile: GO_LICENSES is pinned to @latest",
			"Makefile:13: @latest installs whatever is newest. Name the version: what builds a release must not float",
			"Makefile:11: names govulncheck as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile: GOVULNCHECK pins golang.org/x/vuln/cmd/govulncheck@v1.8.0 and no recipe runs it with the go command, so the tool runs from the PATH",
			"golangci-lint is v2.14.0 at .github/workflows/ci.yml:18: golangci/golangci-lint-action and v2.14 at Makefile: GOLANGCI_LINT: one tool, one version",
		}},
		// One variable holds the module and its version, so the version
		// cannot drift from the module it is for.
		{"a version kept apart from its module", []docsEdit{
			mk("GO ?= go\n", "GO ?= go\nGOVULNCHECK_VERSION ?= v1.8.0\n"),
			mk("\t$(GO) run $(GOVULNCHECK) ./...", "\t$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./..."),
		}, []string{
			"Makefile: GOVULNCHECK_VERSION holds a version apart from its module; write the tool as one `NAME ?= module@version` variable",
			"Makefile: GOVULNCHECK pins golang.org/x/vuln/cmd/govulncheck@v1.8.0 and no recipe runs it with the go command, so the tool runs from the PATH",
		}},
		// A Makefile variable's name is read as written: VERSION is the
		// server's own, which the build stamps into the binary.
		{"the server's own VERSION", []docsEdit{
			mk("GO ?= go\n", "GO ?= go\nVERSION ?= dev\n"),
		}, nil},
		{"a recipe that runs a tool at its own version", []docsEdit{
			mk("\t$(GO) run $(GO_LICENSES) check ./...",
				"\t$(GO) run $(GO_LICENSES) check ./...\n\tgo run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."),
		}, []string{
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.6.0 at Makefile: licenses: one tool, one version",
		}},
		{"a toolchain line that names a Go line, not a release", []docsEdit{
			replace("go.mod", "go 1.27.3", "go 1.27.3\n\ntoolchain go1.27"),
		}, []string{
			`.github/workflows/ci.yml:11: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`.github/workflows/codeql.yml:10: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`.github/workflows/publish-mcp.yml:10: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`.github/workflows/release.yml:13: actions/setup-go: go is pinned to "1.27", which is not one exact version`,
			`go.mod: go is pinned to "1.27", which is not one exact version`,
		}},
		{"a version in a Makefile comment, which pins nothing", []docsEdit{
			mk("GO ?= go\n", "GO ?= go\n# go run golang.org/x/vuln/cmd/govulncheck@master was the old way.\n"),
		}, nil},
		{"a Makefile line the reader does not read", []docsEdit{
			mk("GO ?= go\n", "ifdef CI\nGO ?= go\nendif\n"),
		}, []string{
			`Makefile:1: "ifdef CI" is a directive or an assignment, which this reader does not read`,
			`Makefile:3: "endif" is a directive or an assignment, which this reader does not read`,
		}},
		// goreleaser runs its hooks in the release, so a tool one fetches is
		// held as a workflow's is.
		{"goreleaser hooks at a floating version, another version and the latest release", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				"    - go run golang.org/x/vuln/cmd/govulncheck@master ./...\n"+
				"    - go run github.com/google/go-licenses@v1.6.1 check ./...\n"+
				"    - curl -fsSLO https://github.com/x/y/releases/latest/download/z.tar.gz\n"+
				"    # - go run golang.org/x/vuln/cmd/govulncheck@latest was the old way\n"),
		}, []string{
			".goreleaser.yaml:7: releases/latest/download installs whatever is newest. Name the version: what builds a release must not float",
			`.goreleaser.yaml:5: go run golang.org/x/vuln/cmd/govulncheck: govulncheck is pinned to "master", which is not one exact version`,
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and master at " +
				".goreleaser.yaml:5: go run golang.org/x/vuln/cmd/govulncheck: one tool, one version",
			"go-licenses is v1.6.0 at .github/workflows/ci.yml:15: go run github.com/google/go-licenses and v1.6.1 at " +
				".goreleaser.yaml:6: go run github.com/google/go-licenses: one tool, one version",
		}},
		// A sign's or an SBOM's command is words, where a known tool's
		// module@version is the pin.
		{"an SBOM's words naming a tool at another version", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\nsboms:\n  - cmd: go\n    args:\n"+
				"      - run\n      - golang.org/x/vuln/cmd/govulncheck@v1.7.0\n"),
		}, []string{
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.7.0 at " +
				".goreleaser.yaml:9: golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		{"a goreleaser hook running a tool through bash -c", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n    - bash -c \"go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...\"\n"),
		}, []string{
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.7.0 at " +
				".goreleaser.yaml:5: golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		// goreleaser runs the one branch it renders, so a hook whose else
		// branch fetches an unknown tool at a branch is refused, not read whole.
		{"a goreleaser hook running a tool at a branch in its else", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				`    - "{{ if .IsSnapshot }}true{{ else }}go run github.com/someone/tool@main dist{{ end }}"`+"\n"),
		}, []string{".goreleaser.yaml:5: hooks: a template control action; goreleaser runs one branch, and the gates read only " +
			"a command wrapped whole in one {{ if }}...{{ end }}"}},
		{"a goreleaser hook at latest, reported once", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n    - go run golang.org/x/vuln/cmd/govulncheck@latest ./...\n"),
		}, []string{".goreleaser.yaml:5: @latest installs whatever is newest. Name the version: what builds a release must not float"}},
		// The release runs its hooks, signs, SBOMs and publishers, and only
		// they are held: a release note may tell a person to install @latest.
		{"a release footer and a changelog filter naming latest", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				"release:\n"+
				"  footer: |\n"+
				"    go install github.com/mmedum/google-docs-mcp/v3/cmd/google-docs-mcp@latest\n"+
				"    go run golang.org/x/vuln/cmd/govulncheck@master ./...\n"+
				"    https://github.com/x/y/releases/latest/download/z.tar.gz\n"+
				"changelog:\n"+
				"  filters:\n"+
				"    exclude:\n"+
				"      - go run golang.org/x/vuln/cmd/govulncheck@v0.0.1\n"),
		}, nil},
		{"latest in each command the release runs", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				"builds:\n"+
				"  - id: server\n"+
				"    hooks:\n"+
				"      post: go run golang.org/x/vuln/cmd/govulncheck@latest ./...\n"+
				"      pre:\n"+
				"        - cmd: go run github.com/google/go-licenses@latest check ./...\n"+
				"          dir: .\n"+
				"universal_binaries:\n"+
				"  - id: universal\n"+
				"    hooks:\n"+
				"      pre: |\n"+
				"        go run golang.org/x/vuln/cmd/govulncheck@latest\n"+
				"signs:\n"+
				"  - cmd: curl\n"+
				"    args:\n"+
				"      - -fsSLO\n"+
				"      - https://github.com/x/y/releases/latest/download/z\n"+
				"sboms:\n"+
				"  - cmd: go\n"+
				"    args:\n"+
				"    - run\n"+
				"    - golang.org/x/vuln/cmd/govulncheck@latest\n"+
				"publishers:\n"+
				"  - name: x\n"+
				"    cmd: go run github.com/google/go-licenses@latest\n"),
		}, []string{
			".goreleaser.yaml:8: @latest installs whatever is newest. Name the version: what builds a release must not float",
			".goreleaser.yaml:10: @latest installs whatever is newest. Name the version: what builds a release must not float",
			".goreleaser.yaml:16: @latest installs whatever is newest. Name the version: what builds a release must not float",
			".goreleaser.yaml:29: @latest installs whatever is newest. Name the version: what builds a release must not float",
			".goreleaser.yaml:21: releases/latest/download installs whatever is newest. Name the version: what builds a release must not float",
			".goreleaser.yaml:26: @latest installs whatever is newest. Name the version: what builds a release must not float",
		}},
		{"a goreleaser shape the reader does not read", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				"builds:\n"+
				"  - id: server\n"+
				"    hooks:\n"+
				"      post: *common\n"+
				"      pre: [go mod tidy]\n"+
				"signs:\n"+
				"  - args: {a: b}\n"+
				`  - cmd: "go run \"x\""`+"\n"+
				"sboms:\n"+
				"  - cmd: go run golang.org/x/vuln/cmd/govulncheck@v1.8.0\n"+
				"      --flag\n"+
				"dockers_v2: [{hooks: {pre: go mod tidy}}]\n"+
				"<<: *base\n"),
		}, []string{
			".goreleaser.yaml:8: post: an anchor, an alias or a tag; write the value out",
			".goreleaser.yaml:9: pre: a flow collection; write it as a block, one key or item a line",
			".goreleaser.yaml:11: args: a flow collection; write it as a block, one key or item a line",
			".goreleaser.yaml:12: cmd: a quoted value with an escape in it; write it unquoted or as a block",
			".goreleaser.yaml:15: cmd: a value continued on the next line; write it on one line or as a block scalar",
			".goreleaser.yaml:16: dockers_v2: a flow collection; write it as a block, one key or item a line",
			".goreleaser.yaml:17: <<: a merge key; write the mapping out",
		}},
		// A key the reader cannot read hides the commands under it.
		{"a quoted goreleaser key over a command", []docsEdit{
			replace(parityGoreleaser, "before:\n", "\"before\":\n"),
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go run example.com/x/tool@latest\n"+
				"signs:\n"+
				"  - 'cmd': go run example.com/x/tool@latest\n"),
		}, []string{
			`.goreleaser.yaml:2: "before": a quoted key; write the key plain`,
			".goreleaser.yaml:6: 'cmd': a quoted key; write the key plain",
		}},
		// A command on the line after its key is the key's value.
		{"a goreleaser command on the line after its key", []docsEdit{
			replace(parityGoreleaser, "    - go mod tidy\n", "    - go mod tidy\n"+
				"publishers:\n"+
				"  - name: x\n"+
				"    cmd:\n"+
				"      go run github.com/google/go-licenses@latest\n"),
		}, []string{".goreleaser.yaml:8: cmd: a value continued on the next line; write it on one line or as a block scalar"}},
		// A tool run by its name is whatever version the PATH holds, so the
		// Makefile and the hooks run each through a version the Makefile
		// pins. A known tool's name as a word anywhere on a line is refused,
		// in a pipeline, a substitution or after env's flags as much as first.
		{"a recipe that names known tools", []docsEdit{
			mk("\t$(GO) run $(GITLEAKS) git --staged .\n", "\t$(GO) run $(GITLEAKS) git --staged .\npackage:\n"+
				"\tVERSION=1 goreleaser release --snapshot && CGO_ENABLED=0 go build ./... && echo golangci-lint\n"+
				"\t$(GO) run $(GOLANGCI_LINT) run; golangci-lint run | tee lint.log\n"+
				"\t@git describe | goreleaser release --clean\n"+
				"\tOUT=$$(gitleaks dir .); echo $$OUT\n"+
				"\tenv -i golangci-lint run > lint.log; golangci-lint cache clean\n"+
				"\t/usr/local/bin/goreleaser check 2>/dev/null\n"),
		}, []string{
			"Makefile:18: names goreleaser as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:18: names golangci-lint as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:19: names golangci-lint as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:20: names goreleaser as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:21: names gitleaks as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:22: names golangci-lint as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
			"Makefile:23: names goreleaser as a word of its own, which runs it from the PATH at whatever version is installed there; " + recipeRemedy,
		}},
		// The module a go run names is the pinned run, even where its path
		// ends in the tool's name.
		{"a go run of a module whose path ends in a known tool's name", []docsEdit{
			mk("\t$(GO) run $(GOVULNCHECK) ./...", "\t$(GO) run $(GOVULNCHECK) ./... && $(GO) run golang.org/x/vuln/cmd/govulncheck ./..."),
		}, nil},
		// A recipe is read as make runs it, its variables expanded.
		{"a recipe that fetches the latest release through a variable", []docsEdit{
			mk("GO ?= go\n", "GO ?= go\nSYFT_URL = https://github.com/anchore/syft/releases/latest/download/syft.tar.gz\n"),
			mk("\t$(GO) run $(GITLEAKS) git --staged .\n", "\t$(GO) run $(GITLEAKS) git --staged .\nsbom:\n\tcurl -fsSLO $(SYFT_URL)\n"),
		}, []string{
			"Makefile:19: releases/latest/download installs whatever is newest. Name the version: what builds a release must not float",
		}},
		// The gates' own Go may run a tool, so a module@version in one of
		// its strings is a pin. A comment or a test's fixture pins nothing.
		{"a tool pinned in the gates' Go", []docsEdit{
			add("scripts/gates/scan.go", textLines(
				"package main",
				"",
				"// go run golang.org/x/vuln/cmd/govulncheck@master was the old way.",
				`const scanner = "golang.org/x/vuln/cmd/govulncheck@v1.7.0"`,
				"const licenses = `github.com/google/go-licenses@v1.6.0`",
			)),
			add("scripts/gates/scan_test.go", textLines("package main", "", `const old = "golang.org/x/vuln/cmd/govulncheck@v0.0.1"`)),
			add("scripts/gates/testdata/x.go", textLines("package x", "", `const old = "golang.org/x/vuln/cmd/govulncheck@v0.0.2"`)),
		}, []string{
			"govulncheck is v1.8.0 at .github/workflows/ci.yml:14: go run golang.org/x/vuln/cmd/govulncheck and v1.7.0 at " +
				"scripts/gates/scan.go: golang.org/x/vuln/cmd/govulncheck: one tool, one version",
		}},
		{"gates' Go that does not parse", []docsEdit{add("scripts/gates/broken.go", "package main\n\nfunc {\n")},
			[]string{"scripts/gates/broken.go:3:6: expected 'IDENT', found '{'"}},
		{"a workflow with no shell", []docsEdit{
			replace(fxCodeQL, "defaults:\n  run:\n    shell: bash\n", ""),
		}, []string{".github/workflows/codeql.yml: no workflow-level `defaults: run: shell:`. " +
			"Every run step goes to a shell, so a job-level block only fixes the jobs that exist today"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pinsProblems(t, applyEdits(t, pinsFixture(), tc.edits...))
			if !slices.Equal(got, tc.want) {
				t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// A gate that reads nothing passes everything, so each count has a floor.
func TestPinsRefusesToReadTooLittle(t *testing.T) {
	got := pinsProblems(t, applyEdits(t, pinsFixture(), drop(fxCodeQL)))
	want := []string{
		"read 3 workflows, want at least 4: the reader is not seeing its input",
		"read 12 action references, want at least 15: the reader is not seeing its input",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
