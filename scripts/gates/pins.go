package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
// And one rule for a version that is pinned twice: the rehearsal and the
// release must run the same goreleaser. Both pins are exact, so every
// rule above passes while the two name different versions — which they
// did, in prose, by four minor releases. A rehearsal that builds with a
// different tool than the tag is a rehearsal of something else.

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

// `go run module@version`, anywhere a command can appear.
var goRunPin = regexp.MustCompile(`go run\s+([^\s@]+)@(\S+)`)

// A container image in a docker command.
var dockerImage = regexp.MustCompile(`docker run[^\n]*?\s([a-z0-9][\w.\-]*(?:/[\w.\-]+)+)(:(\S+))?`)

// pins reports every version this repository lets float.
func pins(_ []string, stdout, stderr io.Writer) int {
	files, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	// The Makefile runs pinned tools too, and `make check` is what CI
	// runs, so a float here is a float in CI.
	files = append(files, "Makefile")

	var problems []string
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
	}

	// Zero findings and zero inputs look the same from here.
	if examined == 0 {
		_, _ = fmt.Fprintln(stderr, "gates: pins read no lines: this gate is looking at nothing")
		return 1
	}
	if p := goreleaserAgrees(files); p != "" {
		problems = append(problems, p)
	}

	// A classification table that matches nothing passes for the wrong
	// reason, the same way zero lines would.
	if installers < 3 {
		_, _ = fmt.Fprintf(stderr, "gates: only %d tool installers found: the classification "+
			"table has drifted from the actions the workflows use\n", installers)
		return 1
	}
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

// goreleaserAction is the action that runs goreleaser on a tag; the
// `version:` under it is the release's pin.
const goreleaserAction = "goreleaser/goreleaser-action@"

// goreleaserPin is a version and where it is written down.
type goreleaserPin struct{ version, at string }

// goreleaserPins reads one file for the two ways this repository names a
// goreleaser: the rehearsal's `go run`, and the `version:` under the
// action that runs it on a tag.
func goreleaserPins(file, content string) (rehearse, release goreleaserPin) {
	inAction := false
	for i, line := range strings.Split(content, "\n") {
		where := fmt.Sprintf("%s:%d", file, i+1)
		if m := goRunPin.FindStringSubmatch(line); m != nil && strings.Contains(m[1], "goreleaser") {
			rehearse = goreleaserPin{strings.TrimPrefix(m[2], "v"), where}
		}
		if strings.Contains(line, goreleaserAction) {
			inAction = true
			continue
		}
		// The version belongs to the action it sits under, so the block
		// ends at the next step. Without this, the version of whatever
		// step comes next would be read as goreleaser's.
		if strings.Contains(line, "- uses:") || strings.Contains(line, "- name:") {
			inAction = false
		}
		if m := versionKey.FindStringSubmatch(line); m != nil && inAction {
			release = goreleaserPin{strings.TrimPrefix(m[2], "v"), where}
		}
	}
	return rehearse, release
}

// goreleaserVerdict compares the two pins, and says so when either is
// missing: a rule with nothing to compare passes for the wrong reason,
// which is how a version in prose went four minor releases stale.
func goreleaserVerdict(rehearse, release goreleaserPin) string {
	switch {
	case rehearse.version == "":
		return "no `go run ...goreleaser...@version` found: the rehearsal target is gone, " +
			"and nothing holds a local build to the version the tag uses"
	case release.version == "":
		return "no version found under " + goreleaserAction + ": the release's goreleaser pin is gone"
	case rehearse.version != release.version:
		return fmt.Sprintf("%s rehearses with goreleaser %s, and %s releases with %s: "+
			"a rehearsal has to build with the tool the tag will",
			rehearse.at, rehearse.version, release.at, release.version)
	}
	return ""
}

// goreleaserAgrees holds the rehearsal and the release to one version.
func goreleaserAgrees(files []string) string {
	var rehearse, release goreleaserPin
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // paths this gate chose
		if err != nil {
			return fmt.Sprintf("pins cannot read %s: %v", file, err)
		}
		r, rel := goreleaserPins(file, string(raw))
		if r.version != "" {
			rehearse = r
		}
		if rel.version != "" {
			release = rel
		}
	}
	return goreleaserVerdict(rehearse, release)
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
	// block and a line does not carry one.
	usesAnywhere = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*([^\s@]+)@[^\s#]+`)
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

// namesAnyKey reports whether the step sets one of the given keys.
func namesAnyKey(block string, keys []string) bool {
	for _, k := range keys {
		if regexp.MustCompile(`(?mi)^\s*` + regexp.QuoteMeta(k) + `:\s*\S`).MatchString(block) {
			return true
		}
	}
	return false
}
