package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// changelogBuild are the files outside staleness's shipped set that
// change what is released: the bundle's launcher and the release build.
var changelogBuild = []string{filepath.ToSlash(launcherSource), ".goreleaser.yaml"}

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
// without adding an entry under [Unreleased], measured from the merge-base
// so a base branch that moved on is not this pull request's change.
func changelogEntry(baseRef, head string, w io.Writer) error {
	base, err := git("merge-base", baseRef, head)
	if err != nil {
		return err
	}
	changed, err := git("diff", "--name-only", "--no-renames", "-z", base, head)
	if err != nil {
		return err
	}
	files := strings.FieldsFunc(changed, func(r rune) bool { return r == 0 })
	watched, bumpOnly, err := changelogWatched(base, head, files)
	if err != nil {
		return err
	}
	switch {
	case len(watched) == 0 && bumpOnly:
		_, _ = fmt.Fprintf(w, "changelog: %d files changed, none shipped, and go.mod only in its require lines; "+
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
	if cut := releaseCut(docs[0], docs[1]); cut != "" {
		_, _ = fmt.Fprintf(w, "changelog ok: a release cut, moving [Unreleased] under ## [%s]\n", cut)
		return nil
	}
	// More entries than at the merge-base, so an edited entry adds nothing.
	n := docs[1].entries("Unreleased") - docs[0].entries("Unreleased")
	if n <= 0 {
		log, err := git("log", "--format=%B%x00", base+".."+head)
		if err != nil {
			return err
		}
		if reason := changelogWaiver(strings.Split(log, "\x00")); reason != "" {
			_, _ = fmt.Fprintf(w, "changelog ok: %d shipped file(s) changed, and a commit says no entry is needed: %s\n", len(watched), reason)
			return nil
		}
		return refusal{fmt.Sprintf("%d shipped file(s) changed (%s) and no entry was added under ## [Unreleased], "+
			"which the next release's notes are cut from. If nothing a user sees changed, end a commit's message with "+
			"`Changelog: none, <why>`", len(watched), strings.Join(watched[:min(3, len(watched))], ", "))}
	}
	_, _ = fmt.Fprintf(w, "changelog ok: %d shipped file(s) changed, %d entries added under [Unreleased]\n", len(watched), n)
	return nil
}

// changelogWatched are the changed files that change what users get.
// bumpOnly says go.mod changed and was left out as a dependency bump. The
// binary's packages are read from the checkout for the host's platform, as
// staleness reads them, so the checkout has to be head or CI's merge commit
// of it, and a package only another platform imports is not counted.
func changelogWatched(base, head string, files []string) (watched []string, bumpOnly bool, err error) {
	var shipped *shippedSource
	for _, f := range files {
		if f != "go.mod" {
			if shipped == nil {
				if shipped, err = listShipped("."); err != nil {
					return nil, false, err
				}
			}
			if shipped.has(f) || slices.Contains(changelogBuild, f) {
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

// changelogWaiver is the reason a commit footer gives for needing no
// entry: `Changelog: none, <why>`, the token and the word in any case. A
// footer with no reason after "none" waives nothing.
func changelogWaiver(messages []string) string {
	for _, message := range messages {
		for _, f := range footers(message) {
			if !strings.EqualFold(f.token, "Changelog") {
				continue
			}
			text := strings.TrimSpace(f.text)
			end := strings.IndexAny(text, ",;:-— \n")
			if end < 0 {
				continue
			}
			if reason := strings.TrimSpace(strings.TrimLeft(text[end:], ",;:-— ")); strings.EqualFold(text[:end], "none") && reason != "" {
				return reason
			}
		}
	}
	return ""
}

// releaseCut is the version heading head adds when it moves base's
// [Unreleased] entries under it: base's [Unreleased] held some, head's
// holds none, and the new section holds at least as many as base's did.
func releaseCut(base, head changelogDoc) string {
	had := base.entries("Unreleased")
	if had == 0 || head.entries("Unreleased") > 0 {
		return ""
	}
	for _, h := range head.versions() {
		if slices.ContainsFunc(base.versions(), func(b changelogHeading) bool { return b.label == h.label }) {
			continue
		}
		if head.entries(h.label) >= had {
			return h.label
		}
	}
	return ""
}
