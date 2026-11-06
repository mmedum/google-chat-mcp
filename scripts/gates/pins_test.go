package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// Each case is one way a version floats, and each is a way a release has
// actually broken somewhere in this family of servers.
func TestPinProblems(t *testing.T) {
	tests := []struct {
		name string
		line string
		// want is a fragment of the problem; empty means the line is
		// correctly pinned.
		want string
	}{
		{
			name: "an action pinned to a sha with its version beside it",
			line: "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1  # v7.0.1",
		},
		{
			// The one that cost google-docs-mcp a release: the action
			// was pinned, and the tool it installed was not.
			name: "an action pinned to a tag",
			line: "      - uses: actions/checkout@v7",
			want: `pinned to "v7", which is a tag or a branch`,
		},
		{
			name: "an action pinned to a branch",
			line: "      - uses: some/action@main",
			want: "which is a tag or a branch",
		},
		{
			name: "a sha with nothing saying what it is",
			line: "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
			want: "no comment saying which version it is",
		},
		{
			name: "an exact tool version",
			line: "          version: v2.13.2",
		},
		{
			name: "a floating major line",
			line: "          version: ~> v2",
			want: "not an exact version",
		},
		{
			name: "a bare major",
			line: "          cosign-release: v3",
			want: "not an exact version",
		},
		{
			// go-version-file names a file, and the file is the pin.
			name: "a version file is a pin by another name",
			line: "          go-version-file: go.mod",
		},
		{
			name: "a pinned go run",
			line: "\tgo run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...",
		},
		{
			name: "go run at latest",
			line: "\tgo run golang.org/x/vuln/cmd/govulncheck@latest ./...",
			want: "govulncheck@latest is not an exact version",
		},
		{
			// The Makefile spells the go command $(GO), and it floats the same.
			name: "a Makefile's $(GO) run at latest",
			line: "\t$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...",
			want: "govulncheck@latest is not an exact version",
		},
		{
			name: "a pinned image",
			line: "            zricethezav/gitleaks:v8.30.1 detect --source=/repo",
		},
		{
			name: "an image at latest",
			line: "          docker run --rm zricethezav/gitleaks:latest detect",
			want: "pinned to :latest, which is not a pin",
		},
		{
			// Every match on a line is judged, not only the first.
			name: "a second go run on the same line",
			line: "\tgo run golang.org/x/one/cmd/one@v1.2.3 ./... && go run golang.org/x/two/cmd/two@latest ./...",
			want: "go run golang.org/x/two/cmd/two@latest is not an exact version",
		},
		{
			name: "a second image on the same line",
			line: "          docker run --rm example.org/one:1.2.3 a && docker run --rm example.org/two:latest b",
			want: "the image example.org/two is pinned to :latest",
		},
		{
			name: "an image with no tag at all",
			line: "          docker run --rm zricethezav/gitleaks detect",
			want: "has no tag",
		},
		{
			// A commented-out step runs nothing, so it pins nothing.
			// The parity gate learned this the hard way.
			name: "a commented-out float",
			line: "      # - uses: actions/checkout@v7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems, _ := pinProblems("test.yml", tt.line)
			switch {
			case tt.want == "":
				if len(problems) > 0 {
					t.Fatalf("a correctly pinned line was reported: %s", strings.Join(problems, "; "))
				}
			case len(problems) != 1:
				t.Fatalf("got %d problem(s), want 1: %s", len(problems), strings.Join(problems, "; "))
			case !strings.Contains(problems[0], tt.want):
				t.Errorf("problem\n got %q\nwant something containing %q", problems[0], tt.want)
			}
		})
	}
}

// A gate that reads nothing passes, and reads exactly like one that
// found nothing wrong.
func TestPinProblemsCountsWhatItRead(t *testing.T) {
	_, lines := pinProblems("test.yml", "a\n# a comment\n\nb\n")
	if lines != 2 {
		t.Errorf("counted %d lines, want 2: neither the comment nor the blank is a line read", lines)
	}
}

// The real workflows have to pass, or the gate is aspirational.
func TestTheWorkflowsAreActuallyPinned(t *testing.T) {
	for _, rel := range []string{
		".github/workflows/ci.yml",
		".github/workflows/release.yml",
		".github/workflows/codeql.yml",
		"Makefile",
	} {
		problems, lines := pinProblems(rel, readRepoFile(t, rel))
		if lines == 0 {
			t.Errorf("%s read no lines", rel)
		}
		for _, p := range problems {
			t.Errorf("%s", p)
		}
	}
}

// The fifth rule: an action pinned by sha whose tool is left to float.
// The two named cases are the two that actually shipped unpinned in the
// Pipedrive server, one of them failing a release.
func TestUnpinnedTools(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantBad bool
	}{
		{
			"cosign with no release input — this failed a sibling's release",
			"jobs:\n  a:\n    steps:\n      - name: Install cosign\n        uses: sigstore/cosign-installer@" + zeroSHA + "\n",
			true,
		},
		{
			"cosign pinned",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA + "\n        with:\n          cosign-release: v3.1.3\n",
			false,
		},
		{
			"syft with no version input — the same hole one step below",
			"jobs:\n  a:\n    steps:\n      - uses: anchore/sbom-action/download-syft@" + zeroSHA + "\n",
			true,
		},
		{
			"syft pinned",
			"jobs:\n  a:\n    steps:\n      - uses: anchore/sbom-action/download-syft@" + zeroSHA + "\n        with:\n          syft-version: v1.51.1\n",
			false,
		},
		{
			"setup-go pins by reference through the file",
			"jobs:\n  a:\n    steps:\n      - uses: actions/setup-go@" + zeroSHA + "\n        with:\n          go-version-file: go.mod\n",
			false,
		},
		{
			"gitleaks takes its scanner version from the environment",
			"jobs:\n  a:\n    steps:\n      - uses: gitleaks/gitleaks-action@" + zeroSHA + "\n        env:\n          GITLEAKS_VERSION: 8.31.0\n",
			false,
		},
		{
			// The environment is case-sensitive, so the wrapper never sees it.
			"gitleaks with its scanner version under a lowercase name",
			"jobs:\n  a:\n    steps:\n      - uses: gitleaks/gitleaks-action@" + zeroSHA + "\n        env:\n          gitleaks_version: 8.31.0\n",
			true,
		},
		{
			"an action that installs nothing needs no version",
			"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@" + zeroSHA + "\n",
			false,
		},
		{
			"an unknown action is not quietly trusted",
			"jobs:\n  a:\n    steps:\n      - uses: some-vendor/tool-installer@" + zeroSHA + "\n        with:\n          version: v1.2.3\n",
			true,
		},
		{
			// Every action in the file is judged, not only the first.
			"an unpinned installer after an action that installs nothing",
			"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@" + zeroSHA + "\n      - uses: sigstore/cosign-installer@" + zeroSHA + "\n",
			true,
		},
		{
			// The step below names the very key this one is missing, so
			// reading past the step boundary would pass it.
			"a pinned twin in the next step does not cover this one",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA + "\n      - uses: sigstore/cosign-installer@" + zeroSHA + "\n        with:\n          cosign-release: v3.1.3\n",
			true,
		},
		{
			"the next step's pin does not cover this one",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA + "\n\n      - uses: anchore/sbom-action/download-syft@" + zeroSHA + "\n        with:\n          syft-version: v1.51.1\n",
			true,
		},
		{
			// A blank line above the step must not start its block, or
			// the block runs to the end of the file.
			"a blank line above an unpinned installer lends it no later step's version",
			"jobs:\n  a:\n    steps:\n      - run: echo\n\n      - uses: sigstore/cosign-installer@" + zeroSHA +
				"\n      - uses: anchore/sbom-action/download-syft@" + zeroSHA +
				"\n        with:\n          cosign-release: v3.1.3\n          syft-version: v1.51.1\n",
			true,
		},
		{
			// YAML reads an empty value as null, so the tool floats.
			"an empty version key, with another key on the next line",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA +
				"\n        with:\n          cosign-release:\n          install-dir: /tmp/cosign\n",
			true,
		},
		{
			"a version key whose value is only a comment",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA +
				"\n        with:\n          cosign-release: # v3.1.3\n",
			true,
		},
		{
			"a version key with a value and a comment",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + zeroSHA +
				"\n        with:\n          cosign-release: v3.1.3 # the bundle format\n",
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, problems := unpinnedTools("test.yml", c.yaml)
			if got := len(problems) > 0; got != c.wantBad {
				t.Errorf("found %d problem(s), want bad=%v: %v", len(problems), c.wantBad, problems)
			}
		})
	}
}

// zeroSHA is the commit a fixture's `uses:` line pins: the shape the pins
// gate accepts, naming no real commit.
const zeroSHA = "0000000000000000000000000000000000000000"

// A checkout in this repository's shape, which the gate accepts. The
// Makefile runs six tools at a version, most through `$(GO) run`. CI
// writes a version only for golangci-lint and goreleaser, through their
// actions, and reaches the other tools through their make targets.
// release.yml pins goreleaser too, and between them the workflows hold
// the three installers the gate's own floor wants.
const (
	pinsFixtureWorkflow = "jobs:\n  a:\n    steps:\n" +
		"      - uses: golangci/golangci-lint-action@" + zeroSHA + "  # v9.3.0\n" +
		"        with:\n          version: v2.14.0\n" +
		"      - uses: goreleaser/goreleaser-action@" + zeroSHA + "  # v7.3.0\n" +
		"        with:\n          version: v2.18.1\n          args: check\n" +
		"      - run: make vuln\n" +
		"      - run: make licenses\n"
	pinsFixtureRelease = "jobs:\n  a:\n    steps:\n" +
		"      - uses: sigstore/cosign-installer@" + zeroSHA + "  # v4.1.2\n" +
		"        with:\n          cosign-release: v3.1.3\n" +
		"      - uses: anchore/sbom-action/download-syft@" + zeroSHA + "  # v0.24.3\n" +
		"        with:\n          syft-version: v1.51.1\n" +
		"      - uses: goreleaser/goreleaser-action@" + zeroSHA + "  # v7.3.0\n" +
		"        with:\n          version: v2.18.1\n          args: release --clean\n"
	pinsFixtureMakefile = "release-rehearse:\n\t$(GO) run github.com/goreleaser/goreleaser/v2@v2.18.1 release --snapshot\n" +
		"lint:\n\t$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run\n" +
		"# $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.0 run is a comment, and runs nothing\n" +
		"vuln:\n\t$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...\n" +
		"licenses:\n\tgo run github.com/google/go-licenses/v2@v2.0.1 check ./...\n" +
		"gitleaks:\n\t$(GO) run github.com/zricethezav/gitleaks/v8@v8.30.1 detect\n" +
		"actionlint:\n\t$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n"
)

// The pins gate run against this repository from its root, as `make pins`
// runs it.
func TestPinsGate(t *testing.T) {
	t.Chdir(repoRoot(t))
	var out, errOut bytes.Buffer
	if code := run([]string{"pins"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "pins ok (") {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", code, out.String(), errOut.String())
	}
}

// The rule for a version pinned twice: a tool `make check` runs at a
// version is the version CI writes for it, where CI writes one, through
// `go run` or the action that runs it. The rehearsal's goreleaser is held
// to the release's this way, and neither pin may go.
func TestTheMakefileRunsTheToolVersionsCIRuns(t *testing.T) {
	const oneVersion = "one tool, one version, or `make check` passes on a version CI never runs"
	for _, tc := range []struct {
		name                        string
		workflow, release, makefile [2]string
		want                        []string
	}{
		{name: "every tool at one version"},
		{
			name:     "the linter's action at another version",
			workflow: [2]string{"version: v2.14.0", "version: v2.15.0"},
			want:     []string{"Makefile:4 runs golangci-lint 2.14.0 and .github/workflows/ci.yml:6 pins 2.15.0: " + oneVersion},
		},
		{
			name:     "the action's version with a trailing comment",
			workflow: [2]string{"version: v2.14.0", "version: v2.14.0  # the one make lint runs"},
		},
		{
			name:     "a workflow's own go run at another version",
			workflow: [2]string{"      - run: make vuln\n", "      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.7.1 ./...\n"},
			want:     []string{"Makefile:7 runs govulncheck 1.7.0 and .github/workflows/ci.yml:11 pins 1.7.1: " + oneVersion},
		},
		{
			// The version below belongs to the next step, so the
			// linter's action pins nothing, and 2.15.0 is no drift.
			name: "a version under the next step's action",
			workflow: [2]string{"        with:\n          version: v2.14.0\n",
				"      - uses: actions/checkout@" + zeroSHA + "  # v7.0.1\n        with:\n          version: v2.15.0\n"},
			want: []string{".github/workflows/ci.yml: golangci/golangci-lint-action is pinned by sha but the tool it " +
				"installs is not — set version. A sha pins the wrapper, not the tool"},
		},
		{
			name:     "the rehearsal drifts ahead of the release",
			makefile: [2]string{"goreleaser/v2@v2.18.1", "goreleaser/v2@v2.18.2"},
			want: []string{
				"Makefile:2 runs goreleaser 2.18.2 and .github/workflows/ci.yml:9 pins 2.18.1: " + oneVersion,
				"Makefile:2 runs goreleaser 2.18.2 and .github/workflows/release.yml:12 pins 2.18.1: " + oneVersion,
			},
		},
		{
			// ci.yml still pins goreleaser, so the tool rule alone would
			// pass with the tag running whatever is on the PATH.
			name: "the release runs goreleaser from the PATH",
			release: [2]string{"      - uses: goreleaser/goreleaser-action@" + zeroSHA + "  # v7.3.0\n" +
				"        with:\n          version: v2.18.1\n          args: release --clean\n", "      - run: goreleaser release --clean\n"},
			want: []string{".github/workflows/release.yml pins no goreleaser at a version: the release's goreleaser pin is gone"},
		},
		{
			name:     "a Makefile reader that finds too few tools",
			makefile: [2]string{"licenses:\n\tgo run github.com/google/go-licenses/v2@v2.0.1 check ./...\n", ""},
			want: []string{"the Makefile runs 5 tools at a version, fewer than 6: " +
				"a tool runs from the PATH, or the `go run module@version` reader has stopped finding them"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workflow := strings.Replace(pinsFixtureWorkflow, tc.workflow[0], tc.workflow[1], 1)
			release := strings.Replace(pinsFixtureRelease, tc.release[0], tc.release[1], 1)
			makefile := strings.Replace(pinsFixtureMakefile, tc.makefile[0], tc.makefile[1], 1)
			if tc.want != nil && workflow == pinsFixtureWorkflow && release == pinsFixtureRelease && makefile == pinsFixtureMakefile {
				t.Fatal("the case changed nothing; its replacement did not match")
			}
			dir := t.TempDir()
			t.Chdir(dir)
			put(t, dir, ".github/workflows/ci.yml", workflow)
			put(t, dir, ".github/workflows/release.yml", release)
			put(t, dir, "Makefile", makefile)
			var out, errOut bytes.Buffer
			code := run([]string{"pins"}, &out, &errOut)
			got := gatesProblems(t, errOut.String())
			wantCode := 0
			if tc.want != nil {
				wantCode = 1
			}
			if code != wantCode || !slices.Equal(got, tc.want) {
				t.Errorf("exit %d with problems\n %q\nwant exit %d with\n %q", code, got, wantCode, tc.want)
			}
		})
	}
}

// A rehearsal pin gone from a Makefile that still runs six tools at a
// version passes the floor, so the rule has to name it. In this
// repository's shape the floor catches it first.
func TestMissingPins(t *testing.T) {
	goreleaser := []toolPin{{tool: "goreleaser", version: "2.18.1"}}
	for _, tc := range []struct {
		name   string
		pinned map[string][]toolPin
		want   []string
	}{
		{"both pins", map[string][]toolPin{makefilePath: goreleaser, releaseWorkflowPath: goreleaser}, nil},
		{"the rehearsal target is gone", map[string][]toolPin{releaseWorkflowPath: goreleaser},
			[]string{"Makefile pins no goreleaser at a version: the rehearsal target is gone, and nothing holds " +
				"a local build to the version the tag uses"}},
		{"the release's pin is in another workflow only", map[string][]toolPin{makefilePath: goreleaser, ciWorkflowPath: goreleaser},
			[]string{".github/workflows/release.yml pins no goreleaser at a version: the release's goreleaser pin is gone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingPins(tc.pinned); !slices.Equal(got, tc.want) {
				t.Errorf("missingPins\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}
