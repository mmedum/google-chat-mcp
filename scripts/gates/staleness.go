package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// The staleness gate fails when the documentation drifts from the code.
//
// Every claim it checks is one a reader acts on: the tool table is what
// somebody reads before writing a client, the configuration page is
// where they look up a variable, and a scope missing from the consent
// screen is not granted — the tool that needs it then fails with a 403
// naming it.
//
// It asks the code rather than grepping it. The tools come from the
// binary's own dump, the variables from the settings the server reads,
// and the scopes from the set login asks for, so a rename is a compile
// error or a reported drift rather than a quietly shorter list.

// The documents this gate holds to the code.
const (
	readmePath    = "README.md"
	configDocPath = "docs/configuration.md"
	setupDocPath  = "docs/gcp-setup.md"
	changelogPath = "CHANGELOG.md"
)

func staleness(args []string, stdout, stderr io.Writer) int {
	bin := binaryArg(args)

	var problems []string
	fail := func(format string, a ...any) {
		problems = append(problems, fmt.Sprintf(format, a...))
	}
	// A document that cannot be read is a failure of the gate, not a
	// finding about the code, so it says so once in those words. The
	// caller skips its block rather than walking an empty string and
	// reporting every tool, variable and scope as undocumented.
	load := func(path string) (string, bool) {
		raw, err := os.ReadFile(path)
		if err != nil {
			fail("gates cannot read %s: %v", path, err)
			return "", false
		}
		return string(raw), true
	}

	// The README's tool table must list exactly the tools that ship.
	if readme, ok := load(readmePath); ok {
		shipped, err := shippedTools(bin)
		if err != nil {
			fail("%v", err)
		} else {
			documented := readmeToolNames(readme)
			for _, name := range shipped {
				if !slices.Contains(documented, name) {
					fail("%s does not list the tool %s", readmePath, name)
				}
			}
			for _, name := range documented {
				if !slices.Contains(shipped, name) {
					fail("%s lists %s, which the server does not register", readmePath, name)
				}
			}
		}
	}

	// The configuration page must name every GCM_ variable the server
	// reads.
	if configDoc, ok := load(configDocPath); ok {
		for _, v := range configVars() {
			if !strings.Contains(configDoc, v) {
				fail("%s does not mention %s", configDocPath, v)
			}
		}
	}

	// The setup page must list every scope login asks for.
	if setupDoc, ok := load(setupDocPath); ok {
		lines := strings.Split(setupDoc, "\n")
		for _, scope := range scopes.All {
			if !endsALine(lines, scope) {
				fail("%s does not list the scope %s", setupDocPath, scope)
			}
		}
	}

	// Source that changed since the last release has to be written down.
	switch tag, err := lastTag(); {
	case err != nil:
		fail("%v", err)
	case tag != "":
		changed, err := sourceChangedSince(tag)
		switch {
		case err != nil:
			fail("%v", err)
		case changed:
			if changelog, ok := load(changelogPath); ok {
				if p := changelogProblem(changelog, tag); p != "" {
					fail("%s", p)
				}
			}
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: "+p)
		}
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "staleness ok")
	return 0
}

// readmeRow matches a row of the tool table, which opens with the tool
// name in backticks. The heading and the separator do not match, and
// neither does prose that happens to mention a tool.
var readmeRow = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")

// readmeToolNames is every tool the README's table documents, sorted.
func readmeToolNames(readme string) []string {
	rows := readmeRow.FindAllStringSubmatch(readme, -1)
	names := make([]string, 0, len(rows))
	for _, m := range rows {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// toolNamesIn is every tool a dump registers, sorted.
func toolNamesIn(d *dump) []string {
	names := make([]string, 0, len(d.Tools))
	for _, t := range d.Tools {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	return names
}

// shippedTools is every tool the built binary registers, sorted.
func shippedTools(bin string) ([]string, error) {
	raw, err := runDumpSchemas(bin)
	if err != nil {
		return nil, err
	}
	d, err := parseDump(raw, bin)
	if err != nil {
		return nil, err
	}
	return toolNamesIn(d), nil
}

// endsALine reports whether any of the lines ends with s.
//
// Anchored, because one scope is a prefix of another: a page listing
// only chat.spaces.readonly would read as documenting chat.spaces too,
// and that is the scope whose absence from the consent screen turns
// every write into a 403.
//
// It takes the lines rather than the document because it is asked once
// per scope, and splitting a page twenty-six times to answer twenty-six
// questions about it is work for nobody.
func endsALine(lines []string, s string) bool {
	for _, line := range lines {
		if strings.HasSuffix(strings.TrimRight(line, " \t\r"), s) {
			return true
		}
	}
	return false
}

// changelogHeading matches a released version's heading.
var changelogHeading = regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`)

// newestVersion is the topmost released version in the changelog, or
// empty when it documents no release yet.
func newestVersion(changelog string) string {
	if m := changelogHeading.FindStringSubmatch(changelog); m != nil {
		return m[1]
	}
	return ""
}

// changelogProblem says what is wrong with the changelog when source has
// changed since the last release, and nothing when it is fine.
//
// Usually the answer is content under [Unreleased]. A release commit is
// the exception: it moves that content under a version heading, and CI
// runs on the release pull request before the tag exists. Requiring
// [Unreleased] content unconditionally fails every release, which is how
// the sibling google-docs-mcp repository discovered this — its gate
// blocked its own release pull request.
func changelogProblem(changelog, tag string) string {
	if sectionFor(changelog, "Unreleased") != "" {
		return ""
	}
	if newestVersion(changelog) != strings.TrimPrefix(tag, "v") {
		// A heading newer than the tag is the release being cut.
		return ""
	}
	return fmt.Sprintf("source changed since %s but %s documents nothing new: "+
		"put it under [Unreleased], or under the heading for the release being cut", tag, changelogPath)
}

// lastTag is the newest tag reachable from HEAD, or empty when there is
// none. A repository before its first release is an ordinary state, and
// the first release is exactly that state.
//
// Git is probed first so that an empty answer means "no tags yet" and
// nothing else. `git describe` exits the same way for a repository with
// no tags and for a directory git cannot read at all, and reading the
// second as the first skips the whole changelog half of this gate
// silently — which is the confusion sourceChangedSince below is written
// to avoid, one function apart.
func lastTag() (string, error) {
	if err := exec.Command("git", "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return "", fmt.Errorf("git cannot read this tree, so the CHANGELOG half of this gate "+
			"cannot run: %w", err)
	}
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		// Git itself works, so the ordinary reason to fail here is a
		// repository with no tags yet — which the first release is.
		return "", nil //nolint:nilerr // no tags is a state, not a failure
	}
	return strings.TrimSpace(string(out)), nil
}

// sourceChangedSince reports whether anything that ships differs from the
// tag.
//
// Tests are excluded because a change confined to _test.go files cannot
// change what the binary does, and CONTRIBUTING says an internal change
// with no user-visible effect gets no entry. Without the exclusion the
// gate demands a release note for a test, and the only way to satisfy it
// is to write one that lies about what shipped.
func sourceChangedSince(tag string) (bool, error) {
	err := exec.Command("git", "diff", "--quiet", tag, "--",
		"cmd", "internal", ":(exclude)*_test.go").Run()
	if err == nil {
		return false, nil
	}
	// git diff --quiet exits 1 for a difference and something else for a
	// failure. The shell this replaced could not tell those apart and
	// read a broken repository as a change.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff against %s: %w", tag, err)
}
