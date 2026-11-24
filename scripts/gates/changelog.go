package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// changelogNotedModules need an entry whenever their version moves: the
// MCP SDK decides what the protocol does, and a client sees that.
var changelogNotedModules = []string{"github.com/modelcontextprotocol/go-sdk"}

// changelogEntryCommand runs changelogEntry as a command, with the exit
// codes schema-ack uses: 0 passes, 1 refuses, 2 could not run.
func changelogEntryCommand(baseRef, head string, stdout, stderr io.Writer) int {
	err := changelogEntry(baseRef, head, stdout)
	if err == nil {
		return 0
	}
	var r refusal
	if errors.As(err, &r) {
		_, _ = fmt.Fprintf(stderr, "gates: changelog: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "gates: changelog: could not run: %v\n", err)
	return 2
}

// changelogEntry refuses a pull request that changes what users get
// without adding an entry under [Unreleased]. What users get is what
// staleness's CHANGELOG rule counts: a file shippedSource has, or go.mod
// outside its require lines; a bump of a module in changelogNotedModules
// counts too. It measures from the merge-base, so a base branch that moved
// on is not this pull request's change, and a release cut, which adds a
// version heading, passes.
func changelogEntry(baseRef, head string, w io.Writer) error {
	base, err := git("merge-base", baseRef, head)
	if err != nil {
		return err
	}
	changed, err := git("diff", "--name-only", "--no-renames", base, head)
	if err != nil {
		return err
	}
	files := strings.Fields(changed)
	watched, bumpOnly, err := changelogWatched(base, head, files)
	if err != nil {
		return err
	}
	switch {
	case len(watched) == 0 && bumpOnly:
		_, _ = fmt.Fprintf(w, "changelog: %d files changed, go.mod only in the modules it requires; "+
			"a dependency bump needs no entry\n", len(files))
		return nil
	case len(watched) == 0:
		_, _ = fmt.Fprintf(w, "changelog: %d files changed, none of them shipped; no entry needed\n", len(files))
		return nil
	}

	var docs [2]changelogDoc
	for i, rev := range []string{base, head} {
		text, err := git("show", rev+":"+changelogPath)
		if err != nil {
			return err
		}
		docs[i] = readChangelog(text)
	}
	if cut := addedVersion(docs[0], docs[1]); cut != "" {
		_, _ = fmt.Fprintf(w, "changelog ok: a release cut, adding ## [%s]\n", cut)
		return nil
	}
	n := addedEntries(docs[0], docs[1])
	if n == 0 {
		return refusal{fmt.Sprintf("%d shipped file(s) changed (%s) and no entry was added under ## [Unreleased], "+
			"which the next release's notes are cut from", len(watched), strings.Join(watched[:min(3, len(watched))], ", "))}
	}
	_, _ = fmt.Fprintf(w, "changelog ok: %d shipped file(s) changed, %d entries added under [Unreleased]\n", len(watched), n)
	return nil
}

// changelogWatched are the changed files that change what users get.
// bumpOnly says go.mod changed and was left out as a dependency bump.
func changelogWatched(base, head string, files []string) (watched []string, bumpOnly bool, err error) {
	var shipped *shippedSource
	for _, f := range files {
		if f != "go.mod" {
			if shipped == nil {
				if shipped, err = listShipped("."); err != nil {
					return nil, false, err
				}
			}
			if shipped.has(f) && !strings.Contains("/"+f, "/testdata/") {
				watched = append(watched, f)
			}
			continue
		}
		before, err := git("show", base+":go.mod")
		if err != nil {
			return nil, false, err
		}
		after, err := git("show", head+":go.mod")
		if err != nil {
			return nil, false, err
		}
		if goModBuildLines(before) != goModBuildLines(after) ||
			!slices.Equal(goModNotedRequires(before), goModNotedRequires(after)) {
			watched = append(watched, f)
		} else {
			bumpOnly = true
		}
	}
	return watched, bumpOnly, nil
}

// goModNotedRequires are go.mod's require entries, on their own line or in
// a block, that name a module in changelogNotedModules, as module and
// version.
func goModNotedRequires(text string) []string {
	var noted []string
	for _, line := range strings.Split(text, "\n") {
		code, _, _ := strings.Cut(line, "//")
		f := strings.Fields(code)
		if len(f) > 0 && f[0] == "require" {
			f = f[1:]
		}
		if len(f) == 2 && slices.Contains(changelogNotedModules, strings.Trim(f[0], `"`)) {
			noted = append(noted, f[0]+" "+f[1])
		}
	}
	return noted
}

// addedVersion is the first version heading head has and base does not.
func addedVersion(base, head changelogDoc) string {
	for _, h := range head.versions() {
		if !slices.ContainsFunc(base.versions(), func(b changelogHeading) bool { return b.label == h.label }) {
			return h.label
		}
	}
	return ""
}

// addedEntries counts the entries under [Unreleased] at head that it does
// not hold at base, so an entry under a released heading counts nothing.
func addedEntries(base, head changelogDoc) int {
	had := map[string]int{}
	for _, e := range base.entryLines("Unreleased") {
		had[e]++
	}
	n := 0
	for _, e := range head.entryLines("Unreleased") {
		if had[e] > 0 {
			had[e]--
			continue
		}
		n++
	}
	return n
}

// entryLines are the lines entries counts in a section.
func (c changelogDoc) entryLines(label string) []string {
	from, to := c.section(label)
	var out []string
	for _, line := range c.prose[from:to] {
		if strings.HasPrefix(line, "- ") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}
