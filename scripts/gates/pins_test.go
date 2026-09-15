package main

import (
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
			name: "a pinned image",
			line: "            zricethezav/gitleaks:v8.30.1 detect --source=/repo",
		},
		{
			name: "an image at latest",
			line: "          docker run --rm zricethezav/gitleaks:latest detect",
			want: "pinned to :latest, which is not a pin",
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
	const sha = "0000000000000000000000000000000000000000"
	cases := []struct {
		name    string
		yaml    string
		wantBad bool
	}{
		{
			"cosign with no release input — this failed a sibling's release",
			"jobs:\n  a:\n    steps:\n      - name: Install cosign\n        uses: sigstore/cosign-installer@" + sha + "\n",
			true,
		},
		{
			"cosign pinned",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + sha + "\n        with:\n          cosign-release: v3.1.3\n",
			false,
		},
		{
			"syft with no version input — the same hole one step below",
			"jobs:\n  a:\n    steps:\n      - uses: anchore/sbom-action/download-syft@" + sha + "\n",
			true,
		},
		{
			"syft pinned",
			"jobs:\n  a:\n    steps:\n      - uses: anchore/sbom-action/download-syft@" + sha + "\n        with:\n          syft-version: v1.51.1\n",
			false,
		},
		{
			"setup-go pins by reference through the file",
			"jobs:\n  a:\n    steps:\n      - uses: actions/setup-go@" + sha + "\n        with:\n          go-version-file: go.mod\n",
			false,
		},
		{
			"gitleaks takes its scanner version from the environment",
			"jobs:\n  a:\n    steps:\n      - uses: gitleaks/gitleaks-action@" + sha + "\n        env:\n          GITLEAKS_VERSION: 8.31.0\n",
			false,
		},
		{
			"an action that installs nothing needs no version",
			"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@" + sha + "\n",
			false,
		},
		{
			"an unknown action is not quietly trusted",
			"jobs:\n  a:\n    steps:\n      - uses: some-vendor/tool-installer@" + sha + "\n        with:\n          version: v1.2.3\n",
			true,
		},
		{
			"the next step's pin does not cover this one",
			"jobs:\n  a:\n    steps:\n      - uses: sigstore/cosign-installer@" + sha + "\n\n      - uses: anchore/sbom-action/download-syft@" + sha + "\n        with:\n          syft-version: v1.51.1\n",
			true,
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
