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
	examined := 0
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // paths this gate chose
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
			return 1
		}
		found, lines := pinProblems(file, string(raw))
		problems = append(problems, found...)
		examined += lines
	}

	// Zero findings and zero inputs look the same from here.
	if examined == 0 {
		_, _ = fmt.Fprintln(stderr, "gates: pins read no lines: this gate is looking at nothing")
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: "+p)
		}
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "pins ok (%d lines across %d files)\n", examined, len(files))
	return 0
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
