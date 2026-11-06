package main

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

// The parity gate holds `make check` and CI to the same gates.
//
// The two lists live in different files and an edit touches one of them,
// so they drift silently: a gate CI skips can break on main with every
// pull request green, and a gate only CI runs fails a pull request that
// passed `make check`.
//
// The comparison is by command. Each `check:` prerequisite's recipe, and
// the recipes of the targets it depends on, is read, and what it runs is
// looked for in what ci.yml runs. A target and its CI step need not
// share a name, and a new prerequisite cannot pass for being on no list.
// The registry's pipelines are held too, and each build tag the tree
// requires to a vet pass on both sides.

// The files the parity gate compares.
const (
	ciWorkflowPath       = ".github/workflows/ci.yml"
	releaseWorkflowPath  = ".github/workflows/release.yml"
	publishWorkflowPath  = ".github/workflows/publish-mcp.yml"
	goreleaserConfigPath = ".goreleaser.yaml"
)

// The fewest `check:` prerequisites, recipe lines CI runs too, and check
// gates in the registry that the gate accepts. Fewer means it has stopped
// reading the Makefile, ci.yml or the registry.
const (
	minCheckPrereqs = 5
	minMatchedLines = 8
	minCheckGates   = 5
)

var (
	// makeAssign is a `NAME ?= value`, `NAME := value` or `NAME = value`
	// line.
	makeAssign = regexp.MustCompile(`(?m)^([A-Z_][A-Z0-9_]*)[ \t]*[:?]?=[ \t]*(.*)$`)
	makeRef    = regexp.MustCompile(`\$\([A-Za-z_][A-Za-z0-9_]*\)`)
	// envAssign is a NAME=value word a shell line sets for its command.
	envAssign = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=\S*$`)
	// makeCall is a `make` in a workflow step, with the targets it names.
	makeCall = regexp.MustCompile(`\bmake((?:[ \t]+[\w.-]+)+)`)
	// vetTags is a tagged vet pass, with its comma-separated tags.
	vetTags = regexp.MustCompile(`\bvet -tags=([\w.,]+)`)
)

// ciActions maps each tool `make check` runs with `go run` and CI runs
// through an action to that action, which stands in for the command.
// Every other command is found in what ci.yml runs.
var ciActions = func() map[string]string {
	out := map[string]string{}
	for action, a := range toolActions {
		if a.inMakeCheck {
			out[a.tool] = action
		}
	}
	return out
}()

// parityFiles is the text of each file the parity gate compares. The
// release workflows and the goreleaser config between them run the
// release's own steps.
type parityFiles struct {
	makefile, ci, release, publish, goreleaser string
}

func parity(_ []string, stdout, stderr io.Writer) int {
	files, tags, err := readParityFiles()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 2
	}
	return reportParity(files, commands, tags, stdout, stderr)
}

// readParityFiles reads the files the gate compares, and the build tags
// of the checkout's Go files.
func readParityFiles() (parityFiles, []string, error) {
	var files parityFiles
	for _, f := range []struct {
		path string
		text *string
	}{
		{makefilePath, &files.makefile}, {ciWorkflowPath, &files.ci}, {releaseWorkflowPath, &files.release},
		{publishWorkflowPath, &files.publish}, {goreleaserConfigPath, &files.goreleaser},
	} {
		raw, err := os.ReadFile(f.path) //nolint:gosec // paths this gate names
		if err != nil {
			return parityFiles{}, nil, err
		}
		*f.text = string(raw)
	}
	checkout, err := checkoutFiles()
	if err != nil {
		return parityFiles{}, nil, err
	}
	tags, err := treeBuildTags(checkout)
	if err != nil {
		return parityFiles{}, nil, err
	}
	return files, tags, nil
}

// reportParity prints the verdict: exit 0 with a summary, 1 with each
// problem, or 2 when the inputs are too thin to reach one.
func reportParity(files parityFiles, registry map[string]command, tags []string, stdout, stderr io.Writer) int {
	problems, summary, err := parityCheck(files, registry, tags)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 2
	case len(problems) > 0:
		return reportProblems(stderr, problems)
	}
	_, _ = fmt.Fprintln(stdout, summary)
	return 0
}

// parityCheck is the rule itself, over the text of the files, a registry
// and the tree's build tags, so a test can hand it a Makefile and a
// workflow that disagree without breaking the repository first.
//
// What runs is read without comments: a step commented out to unblock a
// red build runs nothing. The workflows go through executes, which drops
// them itself, and the Makefile and the goreleaser config through
// uncommented. The goreleaser config is not workflow-shaped: its hooks are
// `post:` and `before:` rather than `run:`, so it is read whole.
func parityCheck(files parityFiles, registry map[string]command, tags []string) (problems []string, summary string, err error) {
	rules := parseMakefile(uncommented(files.makefile))
	release := executes(files.release) + "\n" + executes(files.publish) + "\n" + uncommented(files.goreleaser)
	check, ok := rules["check"]
	if !ok {
		return nil, "", fmt.Errorf("no `check:` target in the Makefile; the check is reading the wrong file")
	}
	prereqs := check.prereqs
	if len(prereqs) < minCheckPrereqs {
		return nil, "", fmt.Errorf("the check target has %d prerequisites; that is not the whole list", len(prereqs))
	}
	if len(tags) == 0 {
		return nil, "", fmt.Errorf("no //go:build line requires a tag; the tree is not being read")
	}

	remote := readCI(files.ci, rules)
	local, problems := readCheck(rules, prereqs, remote, registry)
	if local.matched < minMatchedLines {
		return nil, "", fmt.Errorf("only %d recipe lines matched; the Makefile's shape has changed and "+
			"this check is reading almost nothing", local.matched)
	}
	problems = append(problems, ciOnlyProblems(prereqs, local, remote)...)
	declared, counts := registryProblems(registry, local, remote, gatesIn(release))
	switch {
	case counts.check < minCheckGates:
		return nil, "", fmt.Errorf("only %d gates declared; the registry is not being read", counts.check)
	case counts.pr == 0:
		return nil, "", fmt.Errorf("no pull-request gate is declared; schema-ack belongs there, and the registry is not being read")
	case counts.release == 0:
		return nil, "", fmt.Errorf("no release step is declared; release-notes belongs there, and the registry is not being read")
	}
	problems = append(problems, declared...)
	problems = append(problems, vetProblems(tags, local, remote)...)
	summary = fmt.Sprintf("parity ok: %d gates in both, %d pull-request gates in ci.yml only, "+
		"%d release steps in the release files, %d commands of make check in ci.yml, a vet pass for -tags=%s in both",
		counts.check, counts.pr, counts.release, local.matched, strings.Join(tags, ", -tags="))
	return problems, summary, nil
}

// ciSide is what ci.yml runs: its run lines with the recipe of each target
// a step runs with `make`, the gates in them, and the actions it uses.
type ciSide struct {
	targets []string
	// steps is ci.yml's own run lines, by line number.
	steps []sourceLine
	runs  string
	gates map[string]bool
	uses  map[string]bool
}

func readCI(ci string, rules map[string]makeTarget) ciSide {
	steps := executedLines(ci)
	runs := joinLines(steps)
	c := ciSide{targets: ciMakeTargets(runs, rules), steps: steps, runs: runs, uses: actionsIn(ci)}
	for _, target := range c.targets {
		c.runs += "\n" + strings.Join(rules[target].recipe, "\n")
	}
	c.gates = gatesIn(c.runs)
	return c
}

// ran reports whether CI runs what a recipe line runs: its gate, when it
// runs one, and otherwise the command its signature names.
func (c ciSide) ran(sig, gate string) bool {
	if gate != "" {
		return c.gates[gate]
	}
	return c.uses[ciActions[sig]] || strings.Contains(c.runs, sig)
}

// checkSide is what `make check` runs: each recipe line's signature, the
// gates and vetted tags among them, and how many lines CI runs too.
type checkSide struct {
	sigs, gates, vetted map[string]bool
	matched             int
}

// readCheck reads the recipe of each prerequisite and of every target it
// depends on, once, and reports each command in them CI does not run. A
// gate the registry declares for CI is left to registryProblems.
func readCheck(rules map[string]makeTarget, prereqs []string, remote ciSide, registry map[string]command) (
	local checkSide, problems []string,
) {
	local = checkSide{sigs: map[string]bool{}, gates: map[string]bool{}, vetted: map[string]bool{}}
	seen := map[string]bool{}
	var visit func(target string)
	visit = func(target string) {
		if seen[target] {
			return
		}
		seen[target] = true
		rule := rules[target]
		if len(rule.recipe) == 0 && len(rule.prereqs) == 0 {
			problems = append(problems, fmt.Sprintf(
				"`make check` depends on %q and the Makefile gives it no recipe; it checks nothing", target))
		}
		for _, dep := range rule.prereqs {
			visit(dep)
		}
		for _, line := range rule.recipe {
			for _, tag := range tagsVetted(line) {
				local.vetted[tag] = true
			}
			sig, gate := signature(line)
			local.sigs[sig] = true
			if gate != "" {
				local.gates[gate] = true
			}
			switch c := registry[gate]; {
			case remote.ran(sig, gate):
				local.matched++
			case gate == "" || c.runsIn != inCheck && c.runsIn != inPR:
				problems = append(problems, fmt.Sprintf(
					"`make %s` runs %q and nothing in ci.yml does; `make check` claims to be what CI runs", target, sig))
			}
		}
	}
	for _, target := range prereqs {
		visit(target)
	}
	return local, problems
}

// ciOnlyProblems is the other direction, for what CI runs through make,
// an action or `go run module@version`: `make check` runs it too. A gate
// runs as ./scripts/gates, with no version, so the pull-request gates are
// not among the last.
func ciOnlyProblems(prereqs []string, local checkSide, remote ciSide) []string {
	var problems []string
	for _, target := range remote.targets {
		if !slices.Contains(prereqs, target) {
			problems = append(problems, fmt.Sprintf(
				"ci.yml runs `make %s` and `make check` does not depend on it; a contributor cannot reproduce it", target))
		}
	}
	for _, tool := range slices.Sorted(maps.Keys(ciActions)) {
		if action := ciActions[tool]; remote.uses[action] && !local.sigs[tool] {
			problems = append(problems, fmt.Sprintf(
				"ci.yml runs %s through %s and no `make check` prerequisite runs it; a contributor cannot reproduce it", tool, action))
		}
	}
	for _, step := range remote.steps {
		for _, m := range goRunPin.FindAllStringSubmatch(step.text, -1) {
			if tool := toolOf(m[1]); !local.sigs[tool] {
				problems = append(problems, fmt.Sprintf(
					"%s:%d runs %s and no `make check` prerequisite runs it; a contributor cannot reproduce it",
					ciWorkflowPath, step.line, tool))
			}
		}
	}
	return problems
}

// registryCounts is how many commands the registry puts in each pipeline.
type registryCounts struct{ check, pr, release int }

// registryProblems holds each command to the pipeline the registry says
// runs it: a check gate to both sides, a pull-request gate to CI alone,
// and a release step to the release files, which run gates.
func registryProblems(registry map[string]command, local checkSide, remote ciSide, release map[string]bool) (problems []string, counts registryCounts) {
	for _, name := range slices.Sorted(maps.Keys(registry)) {
		switch registry[name].runsIn {
		case inCheck:
			counts.check++
			if !remote.gates[name] {
				problems = append(problems, fmt.Sprintf("gate %q is declared in scripts/gates but no step in ci.yml runs it", name))
			}
			if !local.gates[name] {
				problems = append(problems, fmt.Sprintf("gate %q is declared in scripts/gates but no `make check` prerequisite runs it", name))
			}
		case inPR:
			counts.pr++
			if !remote.gates[name] {
				problems = append(problems, fmt.Sprintf("pull-request gate %q is declared but no step in ci.yml runs it", name))
			}
			if local.gates[name] {
				problems = append(problems, fmt.Sprintf("pull-request gate %q is in `make check`, where there is no merge-base", name))
			}
		case inRelease:
			counts.release++
			if !release[name] {
				problems = append(problems, fmt.Sprintf("release step %q is declared but no release file runs it; "+
					"a step dropped from them fails at a tag, in public", name))
			}
		}
	}
	return problems, counts
}

// vetProblems holds each tag the tree requires to a vet pass on both
// sides: a file behind a build tag compiles only in a pass with that tag.
func vetProblems(tags []string, local checkSide, remote ciSide) []string {
	var problems []string
	ciVetted := tagsVetted(remote.runs)
	for _, tag := range tags {
		if !local.vetted[tag] {
			problems = append(problems, fmt.Sprintf(
				"a file builds only with -tags=%s and `make check` vets no such pass; a contributor cannot see it break", tag))
		}
		if !slices.Contains(ciVetted, tag) {
			problems = append(problems, fmt.Sprintf(
				"a file builds only with -tags=%s and ci.yml vets no such pass; it compiles only on a maintainer's machine", tag))
		}
	}
	return problems
}

// makeTarget is one Makefile rule: what it depends on and what it runs.
type makeTarget struct{ prereqs, recipe []string }

// parseMakefile reads a Makefile, its comments removed, into its rules by
// target. A rule line is makeRule's, so `a b: deps` gives both targets the
// rule and `check:=` is an assignment. The prerequisites leave out the
// trailing `## help text`, which documents the target for a reader. The
// first rule for a target is the one kept.
func parseMakefile(makefile string) map[string]makeTarget {
	vars := makeVars(makefile)
	lines := strings.Split(makefile, "\n")
	rules := map[string]makeTarget{}
	for i, line := range lines {
		m := makeRule.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		_, after, _ := strings.Cut(line[m[3]:], ":")
		deps, _, _ := strings.Cut(after, "##")
		rule := makeTarget{prereqs: strings.Fields(deps), recipe: readRecipe(lines[i+1:], vars)}
		for _, target := range strings.Fields(line[m[2]:m[3]]) {
			if _, seen := rules[target]; !seen {
				rules[target] = rule
			}
		}
	}
	return rules
}

// makeVars reads the Makefile's simple variable assignments, so a recipe
// spelled `$(GO) run …` compares with a workflow that spells the same
// command literally.
func makeVars(makefile string) map[string]string {
	vars := map[string]string{}
	for _, m := range makeAssign.FindAllStringSubmatch(makefile, -1) {
		vars[m[1]] = strings.TrimSpace(m[2])
	}
	return vars
}

// expand substitutes $(NAME) until nothing changes, which is enough for
// assignments that refer to each other a few levels deep. A $$ is make's
// escape for one $ handed to the shell, so it is kept out of the
// substitution and unescaped last.
func expand(line string, vars map[string]string) string {
	line = strings.ReplaceAll(line, "$$", "\x00")
	for range 5 {
		next := makeRef.ReplaceAllStringFunc(line, func(ref string) string {
			if v, ok := vars[ref[2:len(ref)-1]]; ok {
				return v
			}
			return ref
		})
		if next == line {
			break
		}
		line = next
	}
	return strings.ReplaceAll(line, "\x00", "$")
}

// readRecipe is the recipe of the rule whose line precedes lines, one
// command per line, with a line a backslash continues joined to the next
// and variables expanded. A recipe line starts with a tab, and blank lines
// among them are ignored
// (https://www.gnu.org/software/make/manual/html_node/Recipe-Syntax.html).
func readRecipe(lines []string, vars map[string]string) []string {
	var recipe []string
	joining := false
	for _, l := range lines {
		body, isRecipe := strings.CutPrefix(l, "\t")
		if !isRecipe {
			if strings.TrimSpace(l) == "" {
				continue
			}
			break
		}
		body = strings.TrimSpace(body)
		if !joining {
			recipe = append(recipe, "")
			body = strings.TrimLeft(body, "@-+")
		}
		body, joining = strings.CutSuffix(body, `\`)
		recipe[len(recipe)-1] += " " + strings.TrimSpace(body)
	}
	for i, line := range recipe {
		recipe[i] = expand(strings.TrimSpace(line), vars)
	}
	return slices.DeleteFunc(recipe, func(line string) bool { return line == "" })
}

// signature reduces a recipe line to the shortest string that names what
// it runs, so the same job spelled differently in a workflow still
// matches: a gate by its subcommand, a tool `go run` runs at a version by
// the tool, and anything else by its first three words after the
// variables the line sets for its command. gate is the gates command the
// line runs, if it runs one.
func signature(line string) (sig, gate string) {
	if m := gatesCommand.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[0]), m[1]
	}
	if m := goRunPin.FindStringSubmatch(line); m != nil {
		return toolOf(m[1]), ""
	}
	fields := strings.Fields(line)
	for len(fields) > 1 && envAssign.MatchString(fields[0]) {
		fields = fields[1:]
	}
	return strings.Join(fields[:min(len(fields), 3)], " "), ""
}

// ciMakeTargets is each Makefile target a `make` in a workflow's run
// lines names, once.
func ciMakeTargets(runs string, rules map[string]makeTarget) []string {
	var targets []string
	for _, m := range makeCall.FindAllStringSubmatch(runs, -1) {
		for _, target := range strings.Fields(m[1]) {
			if _, isRule := rules[target]; isRule && !slices.Contains(targets, target) {
				targets = append(targets, target)
			}
		}
	}
	return targets
}

// gatesIn is every gate a text runs, by its exact name: schema-ack does
// not run schema-diff.
func gatesIn(text string) map[string]bool {
	in := map[string]bool{}
	for _, m := range gatesCommand.FindAllStringSubmatch(text, -1) {
		in[m[1]] = true
	}
	return in
}

// actionsIn is every action a workflow uses, less its ref.
func actionsIn(workflow string) map[string]bool {
	in := map[string]bool{}
	for _, m := range usesAnywhere.FindAllStringSubmatch(workflow, -1) {
		in[m[1]] = true
	}
	return in
}

// tagsVetted is every tag a tagged vet pass in text names.
func tagsVetted(text string) []string {
	matches := vetTags.FindAllStringSubmatch(text, -1)
	tags := make([]string, 0, len(matches))
	for _, m := range matches {
		tags = append(tags, strings.Split(m[1], ",")...)
	}
	return tags
}

// uncommented is a file with its comment lines removed. A comment is
// text that does not run, and a step commented out to unblock a red
// build would otherwise read as one that runs.
func uncommented(file string) string {
	var kept []string
	for _, line := range strings.Split(file, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// executes is the part of a workflow that actually runs, as one text.
func executes(workflow string) string {
	return joinLines(executedLines(workflow))
}

// joinLines is the lines' text, one to a line.
func joinLines(lines []sourceLine) string {
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n")
}

// executedLines is the part of a workflow that actually runs, by line
// number: the value of every `run:` step, the lines of a block form
// included. The actions it uses are actionsIn's.
//
// A step disabled by an `if:` that is never true still reads as running.
// Seeing that needs a YAML parser, and the gates have no dependency worth
// adding for it.
func executedLines(workflow string) []sourceLine {
	var out []sourceLine
	blockIndent := -1
	for i, raw := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		indent := indentOf(raw)
		// Inside a `run: |` block every deeper line is shell.
		if blockIndent >= 0 {
			if indent > blockIndent {
				// A `#` here is a shell comment, which runs no more
				// than a YAML one does.
				if !strings.HasPrefix(trimmed, "#") {
					out = append(out, sourceLine{i + 1, trimmed})
				}
				continue
			}
			blockIndent = -1
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A list item puts the key two columns right of the dash.
		key := strings.TrimPrefix(trimmed, "- ")
		keyIndent := indent + len(trimmed) - len(key)
		name, value, ok := strings.Cut(key, ":")
		if !ok || name != "run" {
			continue
		}
		value = strings.TrimSpace(value)
		// `|`, `>`, and their chomping variants open a block. A `run:` with
		// nothing after it does not: it is a mapping, which is what
		// `defaults.run.shell` is, and reading it as a block would put
		// `shell: bash` among the commands.
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			blockIndent = keyIndent
			continue
		}
		if value != "" {
			out = append(out, sourceLine{i + 1, value})
		}
	}
	return out
}

// treeBuildTags is every tag a //go:build line in the checkout requires,
// each once and sorted: a file that builds with the tag and not without
// it, which an untagged vet never compiles.
func treeBuildTags(files []string) ([]string, error) {
	fset := token.NewFileSet()
	var tags []string
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") || inTestdata(rel) {
			continue
		}
		f, err := parser.ParseFile(fset, rel, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return nil, err
		}
		tags = append(tags, requiredTags(f)...)
	}
	slices.Sort(tags)
	return slices.Compact(tags), nil
}

// goBuildExpr is a file's //go:build expression, or nil when it has none
// or the line does not parse.
// https://pkg.go.dev/cmd/go#hdr-Build_constraints
func goBuildExpr(f *ast.File) constraint.Expr {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return nil
			}
			return expr
		}
	}
	return nil
}

// requiredTags is each tag a file's //go:build line names that the file
// builds only with, less the tags the go command sets itself: the file
// builds with the tag set and not with none.
func requiredTags(f *ast.File) []string {
	expr := goBuildExpr(f)
	if expr == nil || expr.Eval(func(string) bool { return false }) {
		return nil
	}
	var named []string
	// Eval calls its function for every tag, even past a decided result.
	expr.Eval(func(tag string) bool {
		named = append(named, tag)
		return false
	})
	return slices.DeleteFunc(named, func(tag string) bool {
		return goToolchainTag(tag) || !expr.Eval(func(t string) bool { return t == tag })
	})
}

// goKnownOS and goKnownArch are the GOOS and GOARCH values go/build
// knows, as https://go.dev/src/internal/syslist/syslist.go lists them.
var (
	goKnownOS = map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
		"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
		"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	goKnownArch = map[string]bool{
		"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
		"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
		"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
		"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
		"sparc": true, "sparc64": true, "wasm": true,
	}
	// goOtherTags are set by the go command for a target and a toolchain,
	// except ignore, which keeps a file out of every build.
	goOtherTags = map[string]bool{"unix": true, "cgo": true, "gc": true, "gccgo": true, "ignore": true}
	// goReleaseTag is a release tag, such as go1.22, set for each release
	// up to the toolchain's own.
	goReleaseTag = regexp.MustCompile(`^go1\.\d+$`)
)

// goToolchainTag reports whether a build tag is one no -tags flag needs
// to name: an OS, an architecture or one of its features, such as
// amd64.v2, or a tag the go command sets.
// https://pkg.go.dev/cmd/go#hdr-Build_constraints
func goToolchainTag(tag string) bool {
	arch, _, _ := strings.Cut(tag, ".")
	return goKnownOS[tag] || goKnownArch[arch] || goOtherTags[tag] || goReleaseTag.MatchString(tag)
}
