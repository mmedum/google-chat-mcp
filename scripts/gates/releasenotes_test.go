package main

import (
	"bytes"
	"cmp"
	"os"
	"path/filepath"
	"slices"
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

// The page and the file are different documents: GitHub renders the tag
// name as the h1, so a section published unaltered starts at h3 under an
// h1 and skips a rank.
func TestReleaseNotesLiftHeadingsOneLevel(t *testing.T) {
	var out, errOut bytes.Buffer
	path := write(t, "CHANGELOG.md", changelogFixture)
	if code := releaseNotes([]string{"release-notes", "1.0.0", path}, &out, &errOut); code != 0 {
		t.Fatalf("releaseNotes exited %d: %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "## Added") || strings.Contains(got, "### ") {
		t.Errorf("headings were not lifted, so the page still skips a rank:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "# ") {
			t.Errorf("emitted an h1, which duplicates the tag heading: %q", line)
		}
	}
}

func TestPromoteHeadingsLeavesFencedCodeAlone(t *testing.T) {
	const body = "### Added\n- a thing\n\n```bash\n# not a heading\n### also not a heading\n```\n\n#### Deeper\n"
	got := promoteHeadings(body)
	for _, want := range []string{"## Added", "# not a heading", "### also not a heading", "### Deeper"} {
		if !strings.Contains(got, want) {
			t.Errorf("promoteHeadings dropped or mangled %q:\n%s", want, got)
		}
	}
}

// release.yml passes the tag alone and leaves the file to its default,
// CHANGELOG.md in the directory the gate runs from.
func TestReleaseNotesReadsTheDefaultChangelog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelogFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	if code := releaseNotes([]string{"release-notes", "v1.0.0"}, &out, &errOut); code != 0 {
		t.Fatalf("releaseNotes exited %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "The first thing") {
		t.Errorf("stdout = %q, want the 1.0.0 section", out.String())
	}
}

func TestReadChangelog(t *testing.T) {
	doc := readChangelog(strings.ReplaceAll("# Changelog\n\n## [Unreleased]\n\n- a\n### [8.8.8]\n\n~~~markdown\n- in a fence\n## [9.9.9]\n"+
		"[9.9.9]: https://example.com/x\n~~~\n\n- b\n\n## [2.0.0-rc.1] - 2026-01-01\n\n- c\n##\tLater\n- d\n\n"+
		"[Unreleased]: https://example.com/u\n[2.0.0-rc.1]:\n", "\n", "\r\n"))
	if want := []changelogHeading{{"Unreleased", 2}, {"2.0.0-rc.1", 15}}; !slices.Equal(doc.headings, want) {
		t.Errorf("headings = %v, want %v", doc.headings, want)
	}
	if want := []changelogHeading{{"2.0.0-rc.1", 15}}; !slices.Equal(doc.versions(), want) {
		t.Errorf("versions = %v, want %v", doc.versions(), want)
	}
	if from, to := readChangelog("## [1.0.0]\n- a\n# Notes\n- b\n").section("1.0.0"); from != 1 || to != 2 {
		t.Errorf("a section ends at an h1 too: lines [%d, %d), want [1, 2)", from, to)
	}
	// The footer's own labels end a section, its URL written or not, with
	// or without a v and in any case, as a label matches: a link definition
	// with any other label is prose.
	for line, ends := range map[string]bool{
		"[Unreleased]:":                       true,
		"[Unreleased]: https://example.com/u": true,
		"[unreleased]: https://example.com/u": true,
		"[2.0.0-rc.1]:\thttps://example.com":  true,
		"[2.0.0-RC.1]: https://example.com":   true,
		"[v1.0.0]: https://example.com":       true,
		"[note]: see below":                   false,
		"[1.0.0]:x":                           false,
		"[1.0]: https://example.com":          false,
		"[1.0.0--]: https://example.com":      false,
	} {
		if _, to := readChangelog("## [1.0.0]\n- a\n" + line + "\n- b\n").section("1.0.0"); (to == 2) != ends {
			t.Errorf("%q ends the section: %v, want %v", line, to == 2, ends)
		}
	}
	// A fence closes only at a line of its own character, as many or more,
	// and a # with no blank after it starts no heading.
	inner := readChangelog("##\t[1.0.0]\n````\n```bash\n## [9.9.9]\n```\n````\n#hashtag\n####### seven\n- a\n")
	if want := []changelogHeading{{"1.0.0", 0}}; !slices.Equal(inner.versions(), want) {
		t.Errorf("versions = %v, want %v: a tab after ## starts a heading", inner.versions(), want)
	}
	if from, to := inner.section("1.0.0"); from != 1 || to != 10 {
		t.Errorf("section 1.0.0 = lines [%d, %d), want [1, 10)", from, to)
	}
	for label, want := range map[string]string{
		"Unreleased": "\n- a\n### [8.8.8]\n\n~~~markdown\n- in a fence\n## [9.9.9]\n[9.9.9]: https://example.com/x\n~~~\n\n- b\n",
		"2.0.0-rc.1": "\n- c",
		"9.9.9":      "",
	} {
		if from, to := doc.section(label); strings.Join(doc.lines[from:to], "\n") != want {
			t.Errorf("section %s = %q, want %q", label, strings.Join(doc.lines[from:to], "\n"), want)
		}
	}
	if got := doc.entries("Unreleased"); got != 2 {
		t.Errorf("entries(Unreleased) = %d, want 2: a bullet in a fence is an example", got)
	}
}

// The release note is the section the docs gate scans, fenced code kept
// and never cut at, and a fenced heading is not lifted.
func TestReleaseNotesReadTheFence(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, changelogPath, "## [Unreleased]\n\n## [1.0.0]\n\n### Added\n\n- a\n\n~~~bash\n## not a heading\n### nor this\n~~~\n\n- b\n\n[1.0.0]: https://example.com\n")
	var out, errOut bytes.Buffer
	if code := releaseNotes([]string{"release-notes", "1.0.0", filepath.Join(dir, changelogPath)}, &out, &errOut); code != 0 {
		t.Fatalf("releaseNotes exited %d: %s", code, errOut.String())
	}
	if want := "## Added\n\n- a\n\n~~~bash\n## not a heading\n### nor this\n~~~\n\n- b\n"; out.String() != want {
		t.Errorf("notes = %q, want %q", out.String(), want)
	}
}

// Versions order as semver orders them, so a release candidate is a
// release and v1.10.0 comes after v1.9.0.
func TestChangelogCompareOrdersAsSemver(t *testing.T) {
	chain := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.9.0", "1.10.0", "10.0.0"}
	for i := range chain {
		for j := range chain {
			if got, want := changelogCompare(chain[i], chain[j]), cmp.Compare(i, j); got != want {
				t.Errorf("changelogCompare(%s, %s) = %d, want %d", chain[i], chain[j], got, want)
			}
		}
	}
}
