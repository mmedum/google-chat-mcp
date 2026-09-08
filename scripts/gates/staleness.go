package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
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
	// architectureDocPath states counts about the tool surface.
	architectureDocPath = "docs/architecture.md"
	changelogPath       = "CHANGELOG.md"
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

	// Any doc that states how many tools take dry_run has to state the
	// number the server actually registers.
	for _, p := range dryRunCountProblems(bin, load) {
		fail("%s", p)
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

// dryRunCountProblems holds every stated dry_run tool count to the
// number the server registers.
//
// A count in prose is the kind of claim nothing enforces and everything
// outlives: the README and the architecture doc both said thirteen while
// the server registered twenty-five, and both had been wrong since the
// port. A reader deciding whether a write is previewable is reading that
// sentence.
func dryRunCountProblems(bin string, load func(string) (string, bool)) []string {
	shipped, err := dryRunCount(bin)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, path := range []string{readmePath, architectureDocPath} {
		doc, ok := load(path)
		if !ok {
			continue
		}
		for _, claim := range statedCounts(doc, "dry_run") {
			if claim != shipped {
				problems = append(problems, fmt.Sprintf(
					"%s says %d tools take dry_run and the server registers %d",
					path, claim, shipped))
			}
		}
	}
	return problems
}

// countedTools matches a sentence that states how many tools do
// something: "25 tools carry the flag", "dry_run on 25 write tools",
// "thirteen tools carry the flag".
var countedTools = regexp.MustCompile(`(?i)\b([a-z]+(?:-[a-z]+)?|\d+)\s+(?:\w+\s+)?tools?\b`)

// numberWords is how prose spells a count.
//
// Words are read as well as digits, because the drift this gate exists
// to catch was spelled as one: both documents said "thirteen" while the
// server registered twenty-five. A gate that only read digits would not
// have caught the bug it was written for, which is a gate that reads
// like it works.
var numberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
	"eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
	"sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
	"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
}

// asCount reads a digit run or a spelled number, and reports whether the
// token was a number at all.
func asCount(token string) (int, bool) {
	token = strings.ToLower(token)
	if n, err := strconv.Atoi(token); err == nil {
		return n, true
	}
	if n, ok := numberWords[token]; ok {
		return n, true
	}
	// "twenty-five".
	if tens, units, ok := strings.Cut(token, "-"); ok {
		t, tensOK := numberWords[tens]
		u, unitsOK := numberWords[units]
		if tensOK && unitsOK && t >= 20 && t%10 == 0 && u < 10 {
			return t + u, true
		}
	}
	return 0, false
}

// statedCounts is every tool count asserted on a line that also mentions
// the subject.
func statedCounts(doc, subject string) []int {
	var out []int
	for _, line := range strings.Split(doc, "\n") {
		if !strings.Contains(line, subject) {
			continue
		}
		for _, m := range countedTools.FindAllStringSubmatch(line, -1) {
			if n, ok := asCount(m[1]); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// dryRunCount is how many registered tools take a dry_run argument.
func dryRunCount(bin string) (int, error) {
	raw, err := runDumpSchemas(bin)
	if err != nil {
		return 0, err
	}
	d, err := parseDump(raw, bin)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range d.Tools {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		encoded, err := json.Marshal(t.InputSchema)
		if err != nil {
			return 0, fmt.Errorf("%s: re-encode input schema: %w", t.Name, err)
		}
		if err := json.Unmarshal(encoded, &schema); err != nil {
			return 0, fmt.Errorf("%s: input schema: %w", t.Name, err)
		}
		if _, ok := schema.Properties["dry_run"]; ok {
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no registered tool takes dry_run: this check would be looking at nothing")
	}
	return n, nil
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
