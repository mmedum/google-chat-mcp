package main

import (
	"bytes"
	"strings"
	"testing"
)

const changelogFixture = `# Changelog

Preamble that belongs to no section.

## [1.1.0] - 2026-02-02

### Added
- A thing.

## [1.0.0] - 2026-01-01

### Added
- The first thing.

[1.1.0]: https://example.com/compare/v1.0.0...v1.1.0
[1.0.0]: https://example.com/releases/v1.0.0
`

func TestSectionForStopsAtTheNextHeading(t *testing.T) {
	got := sectionFor(changelogFixture, "1.1.0")
	if !strings.Contains(got, "- A thing.") {
		t.Errorf("the section lost its own body:\n%s", got)
	}
	if strings.Contains(got, "The first thing") {
		t.Errorf("the section ran into the one below it:\n%s", got)
	}
	if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
		t.Errorf("the section is padded with blank lines: %q", got)
	}
}

// The oldest section is followed by the link footer with no heading in
// between, so without a second stop its notes end with a block of
// compare links — which is what a reader deciding whether to upgrade
// would see.
func TestSectionForStopsAtTheLinkFooter(t *testing.T) {
	got := sectionFor(changelogFixture, "1.0.0")
	if !strings.Contains(got, "- The first thing.") {
		t.Errorf("the oldest section lost its body:\n%s", got)
	}
	if strings.Contains(got, "https://example.com") {
		t.Errorf("the oldest section carried the link footer into the release notes:\n%s", got)
	}
}

// A tag with no section is a release with no notes, which must stop the
// release rather than publish an empty body.
func TestReleaseNotesFailsWhenTheVersionHasNoSection(t *testing.T) {
	path := write(t, "CHANGELOG.md", changelogFixture)
	var out, errOut bytes.Buffer
	if code := releaseNotes([]string{"release-notes", "9.9.9", path}, &out, &errOut); code == 0 {
		t.Error("a version with no section produced release notes")
	}
	if !strings.Contains(errOut.String(), "9.9.9") {
		t.Errorf("stderr = %q, want it to name the version it could not find", errOut.String())
	}
}

// The tag carries a v and the heading does not.
func TestReleaseNotesAcceptsATagName(t *testing.T) {
	path := write(t, "CHANGELOG.md", changelogFixture)
	var out, errOut bytes.Buffer
	if code := releaseNotes([]string{"release-notes", "v1.0.0", path}, &out, &errOut); code != 0 {
		t.Fatalf("v-prefixed tag rejected: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "The first thing") {
		t.Errorf("stdout = %q", out.String())
	}
}
