package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// releaseNotes prints one version's section of the changelog.
//
// The entry is the release note. Generating notes from commit subjects
// instead would publish "fix(gchat): a deleted message reads back as a
// tombstone" to people deciding whether to upgrade, which is not who
// that sentence was written for.
func releaseNotes(args []string, stdout, stderr io.Writer) int {
	version := strings.TrimPrefix(args[1], "v")
	file := "CHANGELOG.md"
	if len(args) > 2 && args[2] != "" {
		file = args[2]
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	notes := sectionFor(string(raw), version)
	if notes == "" {
		_, _ = fmt.Fprintf(stderr, "gates: no CHANGELOG section for %s in %s\n", version, file)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, notes)
	return 0
}

// sectionFor returns the body under `## [version]`, without the blank
// lines that top and tail it.
//
// It stops at the next heading and at the link footer. The footer is not
// part of any section, but it follows the oldest one with no heading in
// between — so without that second stop the oldest entry's notes end
// with a block of compare links.
func sectionFor(changelog, version string) string {
	want := "## [" + version + "]"
	var body []string
	inside := false
	for _, line := range strings.Split(changelog, "\n") {
		switch {
		case strings.HasPrefix(line, want):
			inside = true
			continue
		case !inside:
			continue
		case strings.HasPrefix(line, "## "), isLinkDefinition(line):
			return joinTrimmed(body)
		case len(body) == 0 && strings.TrimSpace(line) == "":
			// The blank lines under the heading.
			continue
		}
		body = append(body, line)
	}
	return joinTrimmed(body)
}

// joinTrimmed drops the blank lines at the end, so a section that runs to
// the end of the file reads the same as one followed by a heading.
func joinTrimmed(body []string) string {
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(body, "\n")
}

// isLinkDefinition reports whether the line is a markdown link
// definition, as the changelog's compare-link footer is made of.
func isLinkDefinition(line string) bool {
	if !strings.HasPrefix(line, "[") {
		return false
	}
	close := strings.Index(line, "]")
	return close > 0 && strings.HasPrefix(line[close:], "]: ")
}
