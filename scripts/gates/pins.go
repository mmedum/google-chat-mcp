package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The pins gate holds every third-party tool this repository runs to an
// exact version.
//
// It is the gate that pays for itself. google-docs-mcp lost a release to
// a pinned `cosign-installer` action that fetched cosign 3, where
// `--bundle` had become required: the action was pinned, the thing it
// installed was not. A release runs once, in public, on a tag, and a
// tool that moved under it is discovered by the release failing.
//
// Four rules, each for a way a version can float:
//
//   - an action `uses:` a mutable tag or branch instead of a commit
//   - an action input names a version range, or a bare major line
//   - `go run module@latest` resolves to whatever shipped this morning
//   - a container image with no tag, or `:latest`
//
// And one rule for a version that is pinned twice: a tool the Makefile
// runs at a version is the version any workflow also writes for it. Both pins
// are exact, so every rule above passes while the two name different
// versions — which the rehearsal's goreleaser and the release's did, in
// prose, by four minor releases. A rehearsal that builds with a different
// tool than the tag is a rehearsal of something else, and a local lint or
// scan passes on a version CI never runs.

// A `uses:` line: the action, its ref, and whatever comment follows.
var usesLine = regexp.MustCompile(`^\s*-?\s*uses:\s*([^\s@]+)@([^\s#]+)\s*(#.*)?$`)

// A commit sha is the only ref that cannot be moved under you.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// An exact version: v1.2.3, or 1.2.3, and nothing looser.
var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+[-.\w]*$`)

// A key that names a tool's version. `go-version-file` is deliberately
// not one: it names a file, and the file is the pin.
//
// The value runs to the end of the line rather than to the first space,
// because the values worth catching have spaces in them: `~> v2` is a
// range, and a pattern that stopped at the space matched nothing at all
// and reported the line as fine.
var versionKey = regexp.MustCompile(`^\s*-?\s*([a-z0-9-]*(?:version|release)):\s*(.+?)\s*(?:#.*)?$`)

// `go run module@version`, anywhere a command can appear, and the
// Makefile's `$(GO) run`, which is the same command.
var goRunPin = regexp.MustCompile(`(?:go|\$\(GO\)) run\s+([^\s@]+)@(\S+)`)

// A container image in a docker command.
var dockerImage = regexp.MustCompile(`docker run[^\n]*?\s([a-z0-9][\w.\-]*(?:/[\w.\-]+)+)(:(\S+))?`)

// pins reports every version this repository lets float.
func pins(_ []string, stdout, stderr io.Writer) int {
	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	// Slash paths, as the other gates and their messages spell them on
	// every platform, so a file is found by its path constant on Windows.
	for i, f := range files {
		files[i] = filepath.ToSlash(f)
	}
	// The Makefile runs pinned tools too, and `make check` is what CI
	// runs, so a float here is a float in CI.
	files = append(files, makefilePath)

	var problems []string
	var workflowTools []toolPin
	tools := map[string][]toolPin{}
	examined, installers := 0, 0
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // paths this gate chose
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
			return 1
		}
		found, lines := pinProblems(file, string(raw))
		problems = append(problems, found...)
		examined += lines

		i, toolProblems := unpinnedTools(file, string(raw))
		installers += i
		problems = append(problems, toolProblems...)

		tools[file] = toolPinsIn(file, string(raw))
		if file != makefilePath {
			workflowTools = append(workflowTools, tools[file]...)
		}
	}

	// Zero findings and zero inputs look the same from here.
	if examined == 0 {
		_, _ = fmt.Fprintln(stderr, "gates: pins read no lines: this gate is looking at nothing")
		return 1
	}

	// A classification table that matches nothing passes for the wrong
	// reason, the same way zero lines would.
	if installers < 3 {
		_, _ = fmt.Fprintf(stderr, "gates: only %d tool installers found: the classification "+
			"table has drifted from the actions the workflows use\n", installers)
		return 1
	}
	if n := toolCount(tools[makefilePath]); n < minMakefileTools {
		_, _ = fmt.Fprintf(stderr, "gates: the Makefile runs %d tools at a version, fewer than %d: "+
			"a tool runs from the PATH, or the `go run module@version` reader has stopped finding them\n", n, minMakefileTools)
		return 1
	}
	problems = append(problems, missingPins(tools)...)
	problems = append(problems, makefileToolProblems(tools[makefilePath], workflowTools)...)
	if len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: "+p)
		}
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "pins ok (%d lines across %d files, %d installers name their tool's version)\n",
		examined, len(files), installers)
	return 0
}

// minMakefileTools is the fewest tools the Makefile may run at a version.
// Fewer means a tool runs from the PATH or the reader has stopped finding
// it. CI runs most of them through the same targets, so nothing else would
// hold that tool to a version.
const minMakefileTools = 6

// toolActions are the actions that run a tool the Makefile runs with `go
// run`, each with the tool its `version` input pins. inMakeCheck says a
// `make check` prerequisite runs the tool, which parity holds CI's action to.
var toolActions = map[string]struct {
	tool        string
	inMakeCheck bool
}{
	"golangci/golangci-lint-action": {"golangci-lint", true},
	// CI runs `goreleaser check`, and the Makefile runs goreleaser only in
	// release-rehearse, which `make check` does not depend on.
	"goreleaser/goreleaser-action": {"goreleaser", false},
}

// requiredPins are the tools a file must pin, so the version rule always
// has both sides to compare: with one gone it would pass for the wrong
// reason.
var requiredPins = []struct{ file, tool, gone string }{
	{makefilePath, "goreleaser", "the rehearsal target is gone, and nothing holds a local build to the version the tag uses"},
	{releaseWorkflowPath, "goreleaser", "the release's goreleaser pin is gone"},
}

// toolPin is one place a tool's version is written.
type toolPin struct{ tool, version, at string }

// toolOf is the tool a `go run` module path runs: its last element, less
// a major-version suffix, so github.com/google/go-licenses/v2 is
// go-licenses.
func toolOf(module string) string {
	parts := strings.Split(module, "/")
	for len(parts) > 1 && majorVersion.MatchString(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	return parts[len(parts)-1]
}

// toolPinsIn is every version a file pins a tool to: each `go run
// module@version` outside a comment, and the version input of each step
// that uses one of toolActions. The input is read from the step's own
// block, as unpinnedTools reads it, and one left unset is that rule's to
// report.
func toolPinsIn(file, content string) []toolPin {
	var out []toolPin
	next := 0
	for i, line := range strings.SplitAfter(content, "\n") {
		start := next
		next += len(line)
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range goRunPin.FindAllStringSubmatch(line, -1) {
			out = append(out, toolPin{toolOf(m[1]), strings.TrimPrefix(m[2], "v"), fmt.Sprintf("%s:%d", file, i+1)})
		}
		m := usesAnywhere.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if action, ok := toolActions[m[1]]; ok {
			if version, n := stepInput(stepBlock(content, start), "version"); version != "" {
				out = append(out, toolPin{action.tool, strings.TrimPrefix(version, "v"), fmt.Sprintf("%s:%d", file, i+1+n)})
			}
		}
	}
	return out
}

// missingPins is each of requiredPins its file does not hold.
func missingPins(pinned map[string][]toolPin) []string {
	var problems []string
	for _, r := range requiredPins {
		if !slices.ContainsFunc(pinned[r.file], func(p toolPin) bool { return p.tool == r.tool }) {
			problems = append(problems, fmt.Sprintf("%s pins no %s at a version: %s", r.file, r.tool, r.gone))
		}
	}
	return problems
}

// toolCount is how many tools pins name, each once.
func toolCount(pins []toolPin) int {
	tools := map[string]bool{}
	for _, p := range pins {
		tools[p.tool] = true
	}
	return len(tools)
}

// makefileToolProblems holds each tool the Makefile runs at a version to
// the version a workflow writes for it, where one does: an action's
// version input, or a `go run` of its own. A tool CI reaches through its
// make target has its one version in the Makefile, and parity holds CI to
// running it.
func makefileToolProblems(makefile, workflows []toolPin) []string {
	var problems []string
	for _, local := range makefile {
		for _, ci := range workflows {
			if ci.tool == local.tool && ci.version != local.version {
				problems = append(problems, fmt.Sprintf("%s runs %s %s and %s pins %s: "+
					"one tool, one version, or `make check` passes on a version CI never runs",
					local.at, local.tool, local.version, ci.at, ci.version))
			}
		}
	}
	return problems
}

// pinProblems is the rules, over one file's text. It returns what is
// wrong and how many lines it actually read.
func pinProblems(file, content string) ([]string, int) {
	var problems []string
	examined := 0
	fail := func(line int, format string, a ...any) {
		problems = append(problems, fmt.Sprintf("%s:%d: %s", file, line, fmt.Sprintf(format, a...)))
	}

	for i, line := range strings.Split(content, "\n") {
		number := i + 1
		// A comment runs nothing, so it pins nothing either way, and a
		// blank line is not a line read.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		examined++

		if m := usesLine.FindStringSubmatch(line); m != nil {
			action, ref, comment := m[1], m[2], strings.TrimSpace(m[3])
			switch {
			case !commitSHA.MatchString(ref):
				fail(number, "%s is pinned to %q, which is a tag or a branch and can be "+
					"moved under you. Pin the 40-character commit sha.", action, ref)
			case comment == "":
				// The sha says nothing to a reader. Without the version
				// beside it, nobody can tell what is installed or
				// whether it is current.
				fail(number, "%s is pinned to a sha with no comment saying which version it is",
					action)
			}
		}

		if m := versionKey.FindStringSubmatch(line); m != nil {
			key, value := m[1], strings.Trim(m[2], `"'`)
			// A file reference is a pin by another name.
			if !strings.HasSuffix(key, "version-file") && !exactVersion.MatchString(value) {
				fail(number, "%s: %q is not an exact version. A range or a major line resolves "+
					"to whatever shipped this morning, which is how a release discovers that a "+
					"tool changed.", key, value)
			}
		}

		for _, m := range goRunPin.FindAllStringSubmatch(line, -1) {
			module, version := m[1], strings.TrimSuffix(m[2], "@")
			if !exactVersion.MatchString(version) {
				fail(number, "go run %s@%s is not an exact version", module, version)
			}
		}

		for _, m := range dockerImage.FindAllStringSubmatch(line, -1) {
			image, tag := m[1], m[3]
			switch tag {
			case "":
				fail(number, "the image %s has no tag, so it means whatever :latest means today",
					image)
			case "latest":
				fail(number, "the image %s is pinned to :latest, which is not a pin", image)
			}
		}
	}
	return problems, examined
}

// The fifth rule, and the one the four above could not reach. Each of
// them judges a version that is *written*; an action that installs a
// tool and names no version at all is an absence, and nothing here could
// see it.
//
// That is not hypothetical. The Pipedrive server's release published
// nothing: `sigstore/cosign-installer` was pinned by sha with no
// `cosign-release`, so the job installed whatever cosign was newest, and
// that cosign had changed its default signing format.
// `anchore/sbom-action/download-syft` had the same hole one step below
// it. A sha pins the wrapper, not the tool.
//
// So every action is classified, and an unknown one fails — being
// unclassified is the state that let those two through.
var (
	// installerPins maps an action to the input keys that pin the tool
	// it installs. Any one of them satisfies the rule.
	installerPins = map[string][]string{
		"sigstore/cosign-installer":         {"cosign-release"},
		"anchore/sbom-action/download-syft": {"syft-version"},
		"goreleaser/goreleaser-action":      {"version"},
		"golangci/golangci-lint-action":     {"version"},
		// A file is a pin by reference, and a better one: go.mod cannot
		// disagree with itself the way two literals can.
		"actions/setup-go": {"go-version-file", "go-version"},
		// This wrapper has never had a version input; it reads the
		// scanner's version from the environment.
		"gitleaks/gitleaks-action": {"GITLEAKS_VERSION"},
	}

	// notInstallers are the actions that install no tool, each with the
	// reason, so that adding one is a decision rather than an omission.
	notInstallers = map[string]string{
		"actions/checkout":                "checks out the repository",
		"actions/upload-artifact":         "uploads, installs nothing",
		"actions/download-artifact":       "downloads, installs nothing",
		"actions/attest-build-provenance": "calls the attestation API",
		"github/codeql-action/init":       "CodeQL's bundle is GitHub's to manage",
		"github/codeql-action/analyze":    "CodeQL's bundle is GitHub's to manage",
	}

	// The same `uses:` line, matched over a whole file rather than one
	// line at a time, because this rule has to read the step's `with:`
	// block and a line does not carry one. Its spaces are [ \t], never
	// \s, which crosses a newline: a match would start on a blank line
	// above the step, and stepBlock would read the steps below as its own.
	usesAnywhere = regexp.MustCompile(`(?m)^[ \t]*-?[ \t]*uses:[ \t]*([^\s@]+)@[^\s#]+`)
)

// unpinnedTools reports every action whose tool is left to float, and
// every action nobody has classified.
func unpinnedTools(file, content string) (installers int, problems []string) {
	for _, loc := range usesAnywhere.FindAllStringSubmatchIndex(content, -1) {
		action := content[loc[2]:loc[3]]
		if strings.HasPrefix(action, "./") || strings.HasPrefix(action, "docker://") {
			continue
		}
		keys, isInstaller := installerPins[action]
		if !isInstaller {
			if _, known := notInstallers[action]; !known {
				problems = append(problems, fmt.Sprintf(
					"%s: %s is not classified in scripts/gates/pins.go — add it to installerPins "+
						"with the input that pins its tool, or to notInstallers with the reason. "+
						"A sha pins the wrapper, not the tool", file, action))
			}
			continue
		}
		installers++
		if !namesAnyKey(stepBlock(content, loc[0]), keys) {
			problems = append(problems, fmt.Sprintf(
				"%s: %s is pinned by sha but the tool it installs is not — set %s. "+
					"A sha pins the wrapper, not the tool", file, action, strings.Join(keys, " or ")))
		}
	}
	return installers, problems
}

// stepBlock returns the lines belonging to the step whose `uses:` line
// starts at start, so that a `with:` or `env:` key is read from the step
// that owns it rather than from the next one down the file.
func stepBlock(content string, start int) string {
	lines := strings.Split(content[start:], "\n")
	base := indentOf(lines[0])
	var out []string
	for i, ln := range lines {
		if i > 0 && strings.TrimSpace(ln) != "" {
			in := indentOf(ln)
			if in < base || (in == base && strings.HasPrefix(strings.TrimSpace(ln), "- ")) {
				break
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// indentOf counts the leading whitespace. A space and a tab each count
// as one.
func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// namesAnyKey reports whether the step sets one of the given keys to a
// value.
func namesAnyKey(block string, keys []string) bool {
	return slices.ContainsFunc(keys, func(k string) bool {
		value, _ := stepInput(block, k)
		return value != ""
	})
}

// stepInput is the value a step's block sets key to, and the index of the
// block's line that sets it. Quotes and a trailing comment are dropped, and a
// block scalar's lines are joined with newlines. The value is empty when
// the key is absent, or left empty or holding only a comment, which YAML
// reads as null. A key matches as written, since YAML keys and environment
// variables are case-sensitive.
func stepInput(block, key string) (value string, line int) {
	lines := strings.Split(block, "\n")
	for i, ln := range lines {
		raw, ok := strings.CutPrefix(strings.TrimSpace(ln), key+":")
		if !ok {
			continue
		}
		value = strings.Trim(withoutYAMLComment(raw), `"'`)
		if !strings.HasPrefix(value, "|") && !strings.HasPrefix(value, ">") {
			return value, i
		}
		var body []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) != "" && indentOf(next) <= indentOf(ln) {
				break
			}
			body = append(body, strings.TrimSpace(next))
		}
		return strings.TrimSpace(strings.Join(body, "\n")), i
	}
	return "", 0
}
