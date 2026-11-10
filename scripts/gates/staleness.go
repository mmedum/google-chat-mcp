package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/google-chat-mcp/v9/internal/config"
	"github.com/mmedum/google-chat-mcp/v9/internal/scopes"
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

// configDocProblems holds the configuration page to the code. It names
// every GCM_ variable the server reads, a removed one it refuses
// included, and says what a profile name is in the words the server
// refuses one with.
func configDocProblems(doc string) []string {
	var problems []string
	for _, v := range config.EnvVars() {
		if !mentions(doc, v) {
			problems = append(problems, fmt.Sprintf("%s does not mention %s", configDocPath, v))
		}
	}
	if !strings.Contains(doc, config.ProfileRule) {
		problems = append(problems, fmt.Sprintf("%s does not say a profile name is %q", configDocPath, config.ProfileRule))
	}
	return problems
}

// mentions reports whether doc names the variable as a whole word, so
// GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME does not count as GCM_CONFIG_DIR.
func mentions(doc, name string) bool {
	return regexp.MustCompile(`(^|[^A-Z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Z0-9_])`).MatchString(doc)
}

func staleness(args []string, stdout, stderr io.Writer) int {
	bin := binaryArg(args)

	// A finding exits 1. A document the gate cannot read, a binary it
	// cannot run and a git call that fails leave a rule with no verdict,
	// which is not a finding about the docs: it exits 2 when it is all
	// there is.
	var reported []string
	findings := 0
	fail := func(format string, a ...any) {
		reported = append(reported, fmt.Sprintf(format, a...))
		findings++
	}
	noVerdict := func(format string, a ...any) {
		reported = append(reported, fmt.Sprintf(format, a...))
	}
	// The caller skips the block of a document it cannot read rather than
	// walking an empty string and reporting every tool, variable and scope
	// as undocumented.
	load := func(path string) (string, bool) {
		raw, err := os.ReadFile(path)
		if err != nil {
			noVerdict("gates cannot read %s: %v", path, err)
			return "", false
		}
		return string(raw), true
	}

	// The README's tool table must list exactly the tools that ship.
	if readme, ok := load(readmePath); ok {
		shipped, err := shippedTools(bin)
		if err != nil {
			noVerdict("%v", err)
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
			// A Claude Code permission rule for a tool that does not exist
			// matches nothing, so the person it was meant to ask is never
			// asked.
			for _, name := range permissionRuleTools(readme) {
				if !slices.Contains(shipped, name) {
					fail("%s gives a permission rule for %s, which the server does not register", readmePath, name)
				}
			}
		}
	}

	// Any doc that states how many tools take dry_run has to state the
	// number the server actually registers.
	if found, err := dryRunCountProblems(bin, load); err != nil {
		noVerdict("%v", err)
	} else {
		for _, p := range found {
			fail("%s", p)
		}
	}

	if configDoc, ok := load(configDocPath); ok {
		for _, p := range configDocProblems(configDoc) {
			fail("%s", p)
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

	// What ships changed since the last release has to be written down.
	if problems, err := checkChangelog("."); err != nil {
		noVerdict("%v", err)
	} else {
		for _, p := range problems {
			fail("%s", p)
		}
	}

	return stalenessVerdict(reported, findings, stdout, stderr)
}

// stalenessVerdict prints what the gate reported and exits 1 for a
// finding, 2 when it reported only what it could not judge, and 0 when it
// reported nothing.
func stalenessVerdict(reported []string, findings int, stdout, stderr io.Writer) int {
	for _, p := range reported {
		_, _ = fmt.Fprintln(stderr, "gates: "+p)
	}
	switch {
	case findings > 0:
		return 1
	case len(reported) > 0:
		return 2
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

// permissionRule is a Claude Code permission rule for one of this
// server's tools, under the server name the README uses.
var permissionRule = regexp.MustCompile(`"mcp__google-chat__([a-z0-9_]+)"`)

// permissionRuleTools is every tool the README's permission rules name.
func permissionRuleTools(readme string) []string {
	matches := permissionRule.FindAllStringSubmatch(readme, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
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
func dryRunCountProblems(bin string, load func(string) (string, bool)) ([]string, error) {
	shipped, err := dryRunCount(bin)
	if err != nil {
		return nil, err
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
	return problems, nil
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
		if _, ok := t.InputSchema.Properties["dry_run"]; ok {
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

// newestVersion is the topmost release heading in the changelog, a
// prerelease's included, or empty when it documents no release yet.
func newestVersion(changelog string) string {
	if v := readChangelog(changelog).versions(); len(v) > 0 {
		return v[0].label
	}
	return ""
}

// checkChangelog holds the checkout at dir to changelogRule, measured from
// lastTag, the newest release HEAD reaches. No such tag, or a git or go
// call that cannot answer, is couldNotRun, never "source changed".
func checkChangelog(dir string) ([]string, error) {
	list, err := git("-C", dir, "tag")
	if err != nil {
		return nil, changelogUnrun(dir, err)
	}
	tags := strings.Fields(list)
	last, err := lastTag(dir, "HEAD")
	if err != nil {
		return nil, changelogUnrun(dir, err)
	}
	if last == "" {
		return nil, changelogUnrun(dir, errors.New("no release tag (vX.Y.Z) HEAD reaches is in the checkout to measure from"))
	}
	changed, err := shippedChangedSince(dir, last)
	if err != nil {
		return nil, changelogUnrun(dir, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, changelogPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", changelogPath, err)
	}
	tagged := func(version string) bool { return slices.Contains(tags, "v"+version) }
	return changelogRule(string(raw), changed, tagged), nil
}

// changelogUnrun is why the CHANGELOG rule could not run in the checkout at
// dir. A shallow clone fetches no tag and too little history, so the
// reason names one.
func changelogUnrun(dir string, err error) error {
	if shallow, _ := git("-C", dir, "rev-parse", "--is-shallow-repository"); shallow == "true" {
		err = fmt.Errorf("%w; the checkout is a shallow clone: check it out with fetch-depth: 0", err)
	}
	return couldNotRun{fmt.Errorf("the CHANGELOG rule: %w", err)}
}

// changelogRule requires a change to the binary's source to be written
// up. Normally that means entries under [Unreleased]. A release commit is
// the exception: it moves them under the new version heading, and CI runs
// on the release pull request before the tag exists, so that heading's
// entries count while the version has no tag.
func changelogRule(text string, sourceChanged bool, tagged func(version string) bool) []string {
	if !sourceChanged {
		return nil
	}
	doc := readChangelog(text)
	if doc.entries("Unreleased") > 0 {
		return nil
	}
	where := "[Unreleased]"
	if v := doc.versions(); len(v) > 0 && !tagged(v[0].label) {
		if doc.entries(v[0].label) > 0 {
			return nil
		}
		where = "[" + v[0].label + "] (untagged, so this is a release commit)"
	}
	return []string{"the binary's source changed but " + changelogPath + " " + where + " is empty"}
}

// lastTag is the newest release rev reaches in the checkout at dir: of the
// tags on its history, the highest vX.Y.Z by semver, a prerelease included,
// so a newer release on another line is not rev's. It is empty when rev
// reaches no release, which is a state, not a failure; a git error is one.
func lastTag(dir, rev string) (string, error) {
	tags, err := git("-C", dir, "tag", "--merged", rev)
	if err != nil {
		return "", err
	}
	if v := stalenessNewestRelease(strings.Fields(tags)); v != "" {
		return "v" + v, nil
	}
	return "", nil
}

// stalenessNewestRelease is the newest release tag's version by semver
// order, a prerelease included, or "" when there is none. git's version
// sort puts v2.0.0-rc.1 after v2.0.0, so the order is semver's.
func stalenessNewestRelease(tags []string) string {
	newest := ""
	for _, tag := range tags {
		v, ok := strings.CutPrefix(tag, "v")
		if ok && changelogVersion.MatchString(v) && (newest == "" || changelogCompare(v, newest) > 0) {
			newest = v
		}
	}
	return newest
}

// shippedChangedSince reports whether the commits since..HEAD of the
// checkout at dir change what users get: a file shippedSource has, or
// go.mod outside its require and tool lines. Commits are compared, as CI
// sees them.
func shippedChangedSince(dir, since string) (bool, error) {
	out, err := git("-C", dir, "diff", since+"..HEAD", "--name-only", "--no-renames", "-z")
	if err != nil {
		return false, err
	}
	var shipped *shippedSource
	for _, file := range strings.Split(out, "\x00") {
		switch file {
		case "":
		case "go.mod":
			if changed, err := goModChangedSince(dir, since); err != nil || changed {
				return changed, err
			}
		default:
			if shipped == nil {
				if shipped, err = listShipped(dir); err != nil {
					return false, err
				}
			}
			if shipped.has(file) {
				return true, nil
			}
		}
	}
	return false, nil
}

// shippedSource is what the binary is built from in this module, on this
// platform: its package directories and the files they embed, each
// slash-separated from the module root.
type shippedSource struct{ dirs, embeds map[string]bool }

// has reports whether a file is one users get: any file but a test in one
// of the binary's package directories, an embedded file, or the bundle's
// manifest. The README and LICENSE the bundle carries are docs, so they
// count for nothing. Every file of a package directory counts: the go
// command builds a package from assembly, a .syso and C as well as Go
// (`go help filetype`). A package counts by its directory, so a file of
// it another platform builds counts, and so does one removed from it. The
// packages are the host's, in the checkout: a package only another
// platform's files import, or a directory the head removes whole, is not
// one.
func (s *shippedSource) has(file string) bool {
	return file == filepath.ToSlash(manifestSource) || s.embeds[file] ||
		!strings.HasSuffix(file, "_test.go") && s.dirs[path.Dir(file)]
}

// listShipped asks the go command what ./cmd/... is built from in the
// module at dir. A package of another module is a dependency, which
// go.mod's require lines name.
func listShipped(dir string) (*shippedSource, error) {
	const what = "go list -deps ./cmd/..."
	cmd := exec.Command("go", "list", "-deps", "-json=Dir,EmbedFiles,Module", "./cmd/...")
	cmd.Dir = dir
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return nil, fmt.Errorf("%s: %s", what, strings.TrimSpace(string(exit.Stderr)))
	case err != nil:
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	s := &shippedSource{dirs: map[string]bool{}, embeds: map[string]bool{}}
	for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
		var p struct {
			Dir        string
			EmbedFiles []string
			Module     *struct {
				Main bool
				Dir  string
			}
		}
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if p.Module == nil || !p.Module.Main {
			continue
		}
		rel, err := filepath.Rel(p.Module.Dir, p.Dir)
		if err != nil {
			return nil, err
		}
		pkg := filepath.ToSlash(rel)
		s.dirs[pkg] = true
		for _, f := range p.EmbedFiles {
			s.embeds[path.Join(pkg, f)] = true
		}
	}
	// An empty listing is not reading the binary, and would excuse every
	// change.
	if len(s.dirs) == 0 {
		return nil, fmt.Errorf("%s named no package of this module, so it is not reading the binary", what)
	}
	return s, nil
}

// goModChangedSince reports whether go.mod at HEAD in the checkout at dir
// differs from since's outside its require and tool lines: a go, toolchain
// or replace line changes what the binary is built with.
func goModChangedSince(dir, since string) (bool, error) {
	before, err := git("-C", dir, "show", since+":go.mod")
	if err != nil {
		return false, err
	}
	after, err := git("-C", dir, "show", "HEAD:go.mod")
	if err != nil {
		return false, err
	}
	return goModBuildLines(before) != goModBuildLines(after), nil
}

// goModBuildLines is a go.mod's lines, each trimmed and the blank ones
// dropped, without its require and tool directives, a block's included
// (https://go.dev/ref/mod#go-mod-file-require). A tool directive adds a
// package `go tool` runs (https://go.dev/ref/mod#go-mod-file-tool). ( is a
// token of its own, so `require(` opens a block as `require (` does
// (https://go.dev/ref/mod#go-mod-file-lexical).
func goModBuildLines(text string) string {
	var kept []string
	inBlock := false
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		verb, paren := "", false
		if len(fields) > 0 {
			verb, _, paren = strings.Cut(fields[0], "(")
		}
		switch {
		case inBlock:
			inBlock = len(fields) == 0 || !strings.HasPrefix(fields[0], ")")
		case len(fields) == 0:
		case verb == "require" || verb == "tool":
			inBlock = paren || len(fields) > 1 && strings.HasPrefix(fields[1], "(")
		default:
			kept = append(kept, strings.Join(fields, " "))
		}
	}
	return strings.Join(kept, "\n")
}
