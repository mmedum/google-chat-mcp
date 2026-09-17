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

// The sixth rule: two exact pins that name different versions. Every
// other rule passes on both lines, because each one is pinned — the
// defect is that they disagree, and it shipped as a runbook naming a
// goreleaser four minor releases behind the one the tag used.
func TestGoreleaserPinsAgree(t *testing.T) {
	const makefile = "release-rehearse:\n\tgo run github.com/goreleaser/goreleaser/v2@v2.18.1 release --snapshot\n"
	const workflow = "      - uses: goreleaser/goreleaser-action@" +
		"0000000000000000000000000000000000000000  # v7.2.3\n        with:\n          version: v2.18.1\n"

	rehearse, _ := goreleaserPins("Makefile", makefile)
	_, release := goreleaserPins("release.yml", workflow)
	if rehearse.version != "2.18.1" || release.version != "2.18.1" {
		t.Fatalf("read %q and %q, want both pins found", rehearse.version, release.version)
	}
	if got := goreleaserVerdict(rehearse, release); got != "" {
		t.Errorf("two pins at the same version: %s", got)
	}

	for _, tc := range []struct {
		name              string
		rehearse, release goreleaserPin
		want              string
	}{
		{
			name:     "the rehearsal drifts ahead of the release",
			rehearse: goreleaserPin{"2.18.2", "Makefile:33"},
			release:  goreleaserPin{"2.18.1", "release.yml:112"},
			want:     "a rehearsal has to build with the tool the tag will",
		},
		{
			// A rule that finds nothing to compare passes, which is the
			// failure this rule exists to stop.
			name:    "the rehearsal target is deleted",
			release: goreleaserPin{"2.18.1", "release.yml:112"},
			want:    "the rehearsal target is gone",
		},
		{
			name:     "the release's pin is deleted",
			rehearse: goreleaserPin{"2.18.1", "Makefile:33"},
			want:     "the release's goreleaser pin is gone",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := goreleaserVerdict(tc.rehearse, tc.release)
			if !strings.Contains(got, tc.want) {
				t.Errorf("verdict = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// A version belongs to the action it sits under. Reading the next step's
// version as goreleaser's would hold the release to the wrong number.
func TestGoreleaserPinIgnoresAnotherStepsVersion(t *testing.T) {
	const workflow = "      - uses: goreleaser/goreleaser-action@" +
		"0000000000000000000000000000000000000000  # v7.2.3\n" +
		"        with:\n          version: v2.18.1\n" +
		"      - uses: anchore/sbom-action@1111111111111111111111111111111111111111  # v0.20.9\n" +
		"        with:\n          syft-version: v1.51.1\n"

	_, release := goreleaserPins("release.yml", workflow)
	if release.version != "2.18.1" {
		t.Errorf("release pin = %q, want goreleaser's own version", release.version)
	}
}

// The repository's own files have to agree, or the rule is aspirational.
func TestTheRealGoreleaserPinsAgree(t *testing.T) {
	var rehearse, release goreleaserPin
	for _, rel := range []string{"Makefile", ".github/workflows/release.yml"} {
		r, x := goreleaserPins(rel, readRepoFile(t, rel))
		if r.version != "" {
			rehearse = r
		}
		if x.version != "" {
			release = x
		}
	}
	if got := goreleaserVerdict(rehearse, release); got != "" {
		t.Error(got)
	}
}
