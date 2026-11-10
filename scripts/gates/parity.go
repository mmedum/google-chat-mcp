package main

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"math/bits"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// `make check` says on its first line that CI runs the same gates, and
// the parity gate holds it to that. The two lists live in two
// files and a person edits one, so they are compared by what they run.
// Both sides run the same commands in the same words, CI's in a job a push
// to main runs. Each gate runs in the pipeline its dispatch entry records,
// with the same arguments. Every tagged file is vetted on both sides: one
// vetted only locally compiles only on a maintainer's machine.

const (
	parityCI = "ci.yml"
	// parityGoreleaser and the release workflows are where a release
	// command must be found.
	parityGoreleaser = ".goreleaser.yaml"
	// releaseWorkflow is the workflow a tag starts.
	releaseWorkflow = "release.yml"
	// parityGatesPackage is the gates as `go run` names them.
	parityGatesPackage = "./scripts/gates"
)

// parityPRJobs are ci.yml's jobs that run only on a pull request, where
// the merge-base a pull-request gate measures from exists.
var parityPRJobs = []string{"pr"}

// parityPrereqOnly are targets in `make check`'s closure that check
// nothing themselves, each with the reason CI does not run their recipe.
var parityPrereqOnly = map[string]string{}

// parityCIOnly are the go subcommands ci.yml runs that `make check` need
// not, each with the reason.
var parityCIOnly = map[string]string{
	"build": "CI compiles every package for the host before the tests, which checks nothing `make check` does not",
}

// parityGateName is a gates command's shape. The word after the package
// is a call only in that shape.
var parityGateName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func parity(out io.Writer, _ []string) error {
	in, err := readParityInputs()
	if err != nil {
		return err
	}
	r := parityCheck(in, commands)
	if len(r.problems) > 0 {
		sort.Strings(r.problems)
		for _, p := range r.problems {
			_, _ = fmt.Fprintln(out, "  "+p)
		}
		return fmt.Errorf("%d problem(s): `make check` and ci.yml disagree, and the Makefile says they do not", len(r.problems))
	}
	_, _ = fmt.Fprintf(out, "%d check prerequisites, %d %s command lines, %d gates in check, %d gate calls, "+
		"%d tagged //go:build lines vetted on both sides\n", r.prereqs, r.ciLines, parityCI, r.checkGates, r.gateCalls, r.vetTags)
	return nil
}

// parityInputs are the files parity reads, so a test can hand it an
// edited copy of any of them.
type parityInputs struct {
	mk         *makefile
	flows      []workflowFile
	goreleaser goreleaserFile
	needs      []parityTagNeed
	// prereqOnly is parityPrereqOnly, which a test hands a copy of.
	prereqOnly map[string]string
}

func readParityInputs() (parityInputs, error) {
	mk, err := readMakefile()
	if err != nil {
		return parityInputs{}, err
	}
	flows, err := readWorkflows()
	if err != nil {
		return parityInputs{}, err
	}
	goreleaser, err := readGoreleaser(".")
	if err != nil {
		return parityInputs{}, err
	}
	files, err := docsFiles(".")
	if err != nil {
		return parityInputs{}, couldNotRun{err}
	}
	needs, err := parityTreeTags(".", files)
	if err != nil {
		return parityInputs{}, fmt.Errorf("reading the build tags the code uses: %w", err)
	}
	return parityInputs{mk: mk, flows: flows, goreleaser: goreleaser, needs: needs,
		prereqOnly: parityPrereqOnly}, nil
}

// parityFloors are the fewest check prerequisites, ci.yml command lines,
// gates in check, distinct gate calls and tagged //go:build lines the gate
// may read.
type parityFloors struct{ prereqs, ciLines, checkGates, gateCalls, vetTags int }

// parityLimits are the repository's floors, each under what it holds.
var parityLimits = parityFloors{prereqs: 15, ciLines: 20, checkGates: 8, gateCalls: 12, vetTags: 2}

// parityReport is what the gate read and found.
type parityReport struct {
	problems                                         []string
	prereqs, ciLines, checkGates, gateCalls, vetTags int
}

func (r *parityReport) fail(format string, a ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, a...))
}

// parityLine is one command a target's recipe runs, expanded.
type parityLine struct{ target, text string }

// parityCheck compares the inputs with a registry of commands.
func parityCheck(in parityInputs, registry map[string]command) *parityReport {
	r := &parityReport{}
	// What a reader refused is a command parity cannot see.
	r.problems = append(r.problems, in.mk.refused...)
	r.problems = append(r.problems, in.goreleaser.refused...)
	for i := range in.flows {
		r.problems = append(r.problems, in.flows[i].refused...)
	}
	check, ok := in.mk.targets["check"]
	if !ok {
		r.fail("the Makefile has no check: target")
		return r
	}
	ci, ok := workflowNamed(in.flows, parityCI)
	if !ok {
		r.fail("%s/%s does not exist", workflowDir, parityCI)
		return r
	}
	r.prereqs, r.ciLines = len(check.prereqs), len(ci.runs)
	closure := in.mk.closure("check")
	// Before CI's make lines are expanded, so a recipe line is reported once.
	r.checkFailuresStop(in.mk, closure, ci)
	ci.runs = parityMakeRuns(in.mk, ci.runs)
	checkLines := parityLines(in.mk, closure)
	for _, t := range check.prereqs {
		if _, defined := in.mk.targets[t]; !defined {
			r.fail("check: names %s, which the Makefile does not define", t)
		}
	}
	r.checkCommands(ci, checkLines, in.prereqOnly)
	r.checkCIGoCommands(ci, checkLines)
	r.checkPrereqOnly(in.mk, check.prereqs, closure, in.prereqOnly)
	r.checkGatesRun(in, ci, checkLines, registry)
	r.checkGateArgs(ci, checkLines, registry)
	r.checkVetTags(ci, checkLines, in.needs)
	r.problems = append(r.problems, floorProblems("check: prerequisites", r.prereqs, parityLimits.prereqs)...)
	r.problems = append(r.problems, floorProblems(parityCI+" command lines", r.ciLines, parityLimits.ciLines)...)
	r.problems = append(r.problems, floorProblems("gates in check", r.checkGates, parityLimits.checkGates)...)
	r.problems = append(r.problems, floorProblems("distinct gate calls", r.gateCalls, parityLimits.gateCalls)...)
	r.problems = append(r.problems, floorProblems("//go:build lines that need -tags", r.vetTags, parityLimits.vetTags)...)
	return r
}

// parityLines are the targets' recipe lines, expanded.
func parityLines(mk *makefile, targets []string) []parityLine {
	var out []parityLine
	for _, t := range targets {
		for _, line := range mk.recipe(t) {
			out = append(out, parityLine{t, line})
		}
	}
	return out
}

// parityLineTexts is the commands of recipe lines.
func parityLineTexts(lines []parityLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.text)
	}
	return out
}

// parityRunTexts is the commands of a workflow's run lines.
func parityRunTexts(runs []workflowRun) []string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.text)
	}
	return out
}

// parityPROnly is where ci.yml runs a check only its pull-request jobs
// run, as a message says it.
var parityPROnly = "only in its " + strings.Join(parityPRJobs, " and ") + " jobs, which a push to main does not run"

// parityByTrigger splits ci.yml's run lines and steps into the jobs a push
// to main runs and its pull-request jobs.
func parityByTrigger(ci workflowFile) (push, pr workflowFile) {
	push, pr = ci, ci
	push.runs, pr.runs, push.steps, pr.steps = nil, nil, nil, nil
	for _, run := range ci.runs {
		if slices.Contains(parityPRJobs, run.job) {
			pr.runs = append(pr.runs, run)
		} else {
			push.runs = append(push.runs, run)
		}
	}
	for _, s := range ci.steps {
		if slices.Contains(parityPRJobs, s.job) {
			pr.steps = append(pr.steps, s)
		} else {
			push.steps = append(push.steps, s)
		}
	}
	return push, pr
}

// parityRuns reports whether a workflow runs a command: a run line with
// the same words, read as the shell splits them, or a step that uses one
// of the actions that stand in for it.
func parityRuns(w workflowFile, words, actions []string) bool {
	for _, text := range parityRunTexts(w.runs) {
		for _, c := range parityCommandsOf(text) {
			if slices.Equal(shellTexts(c.words), words) {
				return true
			}
		}
	}
	return slices.ContainsFunc(w.steps, func(s workflowStep) bool { return slices.Contains(actions, s.action()) })
}

// checkCommands holds every command `make check` runs, other than a gate
// call and a prerequisite's that checks nothing, to a ci.yml job a push to
// main runs, that runs it with the same words or uses an action that
// stands in for it.
func (r *parityReport) checkCommands(ci workflowFile, lines []parityLine, prereqOnly map[string]string) {
	push, pr := parityByTrigger(ci)
	for _, l := range lines {
		if _, excused := prereqOnly[l.target]; excused {
			continue
		}
		for _, c := range parityCommandsOf(l.text) {
			words := shellTexts(c.words)
			tool, actions := parityStandIn(c.words)
			switch {
			case parityRuns(push, words, actions):
			case parityRuns(pr, words, actions):
				r.fail("`make %s` runs %q, and %s runs it %s", l.target, c.text, parityCI, parityPROnly)
			case tool != "":
				r.fail("`make %s` runs %q, and %s neither runs it nor uses %s, which stands in for it",
					l.target, c.text, parityCI, strings.Join(actions, " or "))
			default:
				r.fail("`make %s` runs %q and %s never does", l.target, c.text, parityCI)
			}
		}
	}
	uses := map[string]bool{}
	for _, s := range ci.steps {
		uses[s.action()] = true
	}
	for _, t := range pinsTools {
		for _, action := range slices.Sorted(maps.Keys(t.actions)) {
			if len(t.modules) == 0 || !uses[action] {
				continue
			}
			if !slices.ContainsFunc(lines, func(l parityLine) bool {
				tool, _ := parityStandIn(shellSplit(l.text))
				return tool == t.name
			}) {
				r.fail("%s uses %s, and `make check` runs no %s for it to stand in for", parityCI, action, t.name)
			}
		}
	}
}

// parityMakeRuns is ci.yml's run lines and, after them, the recipe lines
// each line of make and targets runs: theirs and those of what they depend
// on, at the make line's job and line. A line that sets a flag or a
// variable, or names a target the Makefile does not define, runs no recipe
// here, since it changes what the recipe runs. A redirection does not.
func parityMakeRuns(mk *makefile, runs []workflowRun) []workflowRun {
	out := slices.Clone(runs)
	for _, run := range runs {
		var words []string
		for _, w := range shellSplit(run.text) {
			if !w.redir {
				words = append(words, w.text)
			}
		}
		if len(words) < 2 || words[0] != "make" || slices.ContainsFunc(words[1:], func(t string) bool { return mk.targets[t] == nil }) {
			continue
		}
		for _, line := range parityLines(mk, mk.closure(words[1:]...)) {
			out = append(out, workflowRun{run.job, run.line, line.text})
		}
	}
	return out
}

// parityCommand is one command a line runs, its words and its text.
type parityCommand struct {
	text  string
	words []shellWord
}

// parityCommandsOf is the commands a line runs other than gate calls: the
// line whole when it calls no gate, and otherwise each command of its
// lists and pipelines but a gate call, so a gate call does not hide the
// command beside it, such as the binary whose output a gate reads.
func parityCommandsOf(line string) []parityCommand {
	words := shellSplit(line)
	if len(parityGateCalls(words)) == 0 {
		return []parityCommand{{line, words}}
	}
	var out []parityCommand
	for _, list := range shellLists(words) {
		for _, c := range shellSplitAt(list, shellPipeOps) {
			if len(parityGateCalls(c)) == 0 {
				out = append(out, parityCommand{strings.Join(shellTexts(c), " "), c})
			}
		}
	}
	return out
}

// parityStandIn is the tool a command runs with `go run` and the actions
// that install and run it in the command's place, the installers pinsTools
// names for it, or "" and none. The pins gate holds an action's version to
// the Makefile's.
func parityStandIn(words []shellWord) (tool string, actions []string) {
	for _, run := range goRunsOf(words) {
		module, _, _ := strings.Cut(run.pkg, "@")
		name := pinsToolByModule(module)
		for _, t := range pinsTools {
			if t.name == name && len(t.actions) > 0 {
				return name, slices.Sorted(maps.Keys(t.actions))
			}
		}
	}
	return "", nil
}

// checkCIGoCommands holds every go command ci.yml runs, other than a gate
// call and an excused subcommand, to one `make check` runs with the same
// words: a check only CI runs is one a contributor cannot see fail. An
// excuse holds a reason and a subcommand ci.yml runs.
func (r *parityReport) checkCIGoCommands(ci workflowFile, lines []parityLine) {
	local := map[string]bool{}
	for _, l := range lines {
		for _, c := range goCommands(l.text) {
			local[parityGoCommand(c)] = true
		}
	}
	ran := map[string]bool{}
	for _, run := range ci.runs {
		for _, c := range goCommands(run.text) {
			ran[c.sub] = true
			if _, excused := parityCIOnly[c.sub]; excused || parityIsGateRun(c) {
				continue
			}
			if !local[parityGoCommand(c)] {
				r.fail("%s:%d runs `%s` and `make check` does not", ci.path, run.line, parityGoCommand(c))
			}
		}
	}
	for _, sub := range slices.Sorted(maps.Keys(parityCIOnly)) {
		switch {
		case strings.TrimSpace(parityCIOnly[sub]) == "":
			r.fail("go %s is excused as CI-only with no reason", sub)
		case !ran[sub]:
			r.fail("go %s is excused as CI-only, and %s runs no go %s; drop the excuse", sub, parityCI, sub)
		}
	}
}

func parityGoCommand(c goCommand) string {
	return strings.Join(append([]string{"go", c.sub}, c.args...), " ")
}

// parityIsGateRun reports whether a go command runs a gate.
func parityIsGateRun(c goCommand) bool {
	rest := goAfterFlags(c.args)
	return c.sub == "run" && len(rest) > 0 && rest[0] == parityGatesPackage
}

// checkPrereqOnly holds each prerequisite-only target to being exactly
// that: defined, reached from check, and not a gate check: names itself.
func (r *parityReport) checkPrereqOnly(mk *makefile, prereqs, closure []string, prereqOnly map[string]string) {
	for _, t := range slices.Sorted(maps.Keys(prereqOnly)) {
		switch {
		case strings.TrimSpace(prereqOnly[t]) == "":
			r.fail("%s is excused as a prerequisite with no reason", t)
		case mk.targets[t] == nil:
			r.fail("%s is excused as a prerequisite and the Makefile does not define it; drop the excuse", t)
		case slices.Contains(prereqs, t):
			r.fail("%s is excused as a prerequisite and check: names it as a gate", t)
		case !slices.Contains(closure, t):
			r.fail("%s is excused as a prerequisite and nothing `make check` runs depends on it; drop the excuse", t)
		}
	}
}

// checkFailuresStop refuses a command in `make check`'s closure whose
// failure make ignores, by a - prefix or by .IGNORE: check would pass
// where CI, which runs it bare, fails
// (https://www.gnu.org/software/make/manual/html_node/Errors.html). It
// refuses a recipe line or a ci.yml command that lets its own failure pass
// as well, which passes on both sides.
func (r *parityReport) checkFailuresStop(mk *makefile, closure []string, ci workflowFile) {
	// .IGNORE with no prerequisite ignores every recipe's failure
	// (https://www.gnu.org/software/make/manual/html_node/Special-Targets.html).
	if ignore, ok := mk.targets[".IGNORE"]; ok {
		if len(ignore.prereqs) == 0 {
			r.fail(".IGNORE names no target: make ignores the failure of every command")
		}
		for _, t := range ignore.prereqs {
			if slices.Contains(closure, t) {
				r.fail(".IGNORE names %s: make ignores the failure of every command `make %s` runs", t, t)
			}
		}
	}
	for _, name := range closure {
		for i, line := range mk.recipe(name) {
			if strings.Contains(mk.targets[name].prefixes[i], "-") {
				r.fail("`make %s` runs %q behind a - prefix: make ignores this command's failure", name, line)
			}
			if how := shellSwallowed(line); how != "" {
				r.fail("`make %s` runs %q, where %s, so a failing check passes", name, line, how)
			}
		}
	}
	for _, run := range ci.runs {
		if how := shellSwallowed(run.text); how != "" {
			r.fail("%s:%d runs `%s`, where %s, so a failing check passes", ci.path, run.line, run.text, how)
		}
	}
}

// parityGateSites are the gates each pipeline calls: push is ci.yml's jobs
// a push to main runs, and pr its pull-request jobs.
type parityGateSites struct {
	check, push, pr, release, manual map[string]bool
}

// checkGatesRun holds every gate to the pipeline the registry records for
// it, and every gate called anywhere to the registry.
func (r *parityReport) checkGatesRun(in parityInputs, ci workflowFile, checkLines []parityLine, registry map[string]command) {
	// What goreleaser runs and the release workflows run is the release.
	release := parityRunTexts(in.goreleaser.runs)
	flows := releaseWorkflows(in.flows)
	for i := range flows {
		release = append(release, parityRunTexts(flows[i].runs)...)
	}
	// Every other workflow is read too, so a gate one calls is held to the
	// dispatch.
	elsewhere := make([]string, 0, len(in.flows))
	for i := range in.flows {
		elsewhere = append(elsewhere, parityRunTexts(in.flows[i].runs)...)
	}
	all := parityLineTexts(parityLines(in.mk, slices.Sorted(maps.Keys(in.mk.targets))))
	push, pr := parityByTrigger(ci)
	sites := parityGateSites{
		check:   parityGatesIn(parityLineTexts(checkLines)),
		push:    parityGatesIn(parityRunTexts(push.runs)),
		pr:      parityGatesIn(parityRunTexts(pr.runs)),
		release: parityGatesIn(release),
		manual:  parityGatesIn(all),
	}
	called := map[string]bool{}
	for _, set := range []map[string]bool{sites.check, sites.push, sites.pr, sites.release, sites.manual, parityGatesIn(elsewhere)} {
		maps.Copy(called, set)
	}
	r.gateCalls = len(called)
	for _, name := range slices.Sorted(maps.Keys(called)) {
		if _, ok := registry[name]; !ok {
			r.fail("something runs `gates %s`, which the dispatch does not have", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(registry)) {
		r.checkGate(name, registry[name].runsIn, sites)
	}
}

// checkGate holds one gate to the pipeline its entry records.
func (r *parityReport) checkGate(name string, runs where, sites parityGateSites) {
	switch runs {
	case inCheck:
		r.checkGates++
		if !sites.check[name] {
			r.fail("the %s gate runs in make check by the dispatch, and no target `make check` runs calls it", name)
		}
		switch {
		case sites.push[name]:
		case sites.pr[name]:
			r.fail("the %s gate runs in make check by the dispatch, and %s calls it %s", name, parityCI, parityPROnly)
		default:
			r.fail("the %s gate runs in make check by the dispatch, and %s never calls it", name, parityCI)
		}
	case inPR:
		if !sites.pr[name] {
			r.fail("the %s gate runs on pull requests by the dispatch, and none of %s's %s jobs calls it", name, parityCI,
				strings.Join(parityPRJobs, " and "))
		}
		if sites.check[name] {
			r.fail("the %s gate runs on pull requests by the dispatch, and `make check` runs it, where there is no merge-base", name)
		}
	case inRelease:
		if !sites.release[name] {
			r.fail("the %s gate runs in release by the dispatch, and neither %s nor %s and the workflows it calls call it", name,
				parityGoreleaser, releaseWorkflow)
		}
		if sites.check[name] {
			r.fail("the %s gate runs in release by the dispatch, and `make check` runs it", name)
		}
	case manual:
		if sites.check[name] {
			r.fail("the %s gate is manual by the dispatch, and `make check` runs it; record it as inCheck", name)
		}
		if !sites.manual[name] {
			r.fail("the %s gate is manual by the dispatch, and no Makefile target or hook runs it: "+
				"a manual gate nobody can find is a gate that never fires", name)
		}
	default:
		r.fail("the %s gate records no pipeline", name)
	}
}

// releaseWorkflows is the release: release.yml and every workflow of this
// repository among flows it calls, and they call.
func releaseWorkflows(flows []workflowFile) []workflowFile {
	var out []workflowFile
	seen := map[string]bool{releaseWorkflow: true}
	for queue := []string{releaseWorkflow}; len(queue) > 0; queue = queue[1:] {
		w, ok := workflowNamed(flows, queue[0])
		if !ok {
			continue
		}
		out = append(out, w)
		for _, s := range w.steps {
			called, local := strings.CutPrefix(s.uses, "./"+workflowDir+"/")
			if s.reusable && local && !seen[called] {
				seen[called] = true
				queue = append(queue, called)
			}
		}
	}
	return out
}

// parityGatesIn are the gates command lines call.
func parityGatesIn(lines []string) map[string]bool {
	found := map[string]bool{}
	for _, line := range lines {
		for _, c := range parityGateCalls(shellSplit(line)) {
			found[c.name] = true
		}
	}
	return found
}

// parityGateCall is one call of a gate: its name, and the words after it
// to the end of its command.
type parityGateCall struct {
	name string
	args []string
}

// parityGateCalls are the gates a line's words call through `go run
// ./scripts/gates`, the line read as the shell splits it: a call quoted in
// an echo is a word of the echo, and flags before ./scripts/gates do not
// hide one.
func parityGateCalls(words []shellWord) []parityGateCall {
	var out []parityGateCall
	for _, run := range goRunsOf(words) {
		if run.pkg == parityGatesPackage && len(run.args) > 0 && parityGateName.MatchString(run.args[0]) {
			out = append(out, parityGateCall{run.args[0], run.args[1:]})
		}
	}
	return out
}

// parityGateSite is one gate call compared by checkGateArgs: where it is,
// its arguments as compared, and as written.
type parityGateSite struct {
	at, name, key, args string
}

// checkGateArgs holds every gate the dispatch runs in both `make check` and
// ci.yml to the same arguments on both sides, the Makefile's variables
// expanded: a floor of 85 in the Makefile and 80 in CI is two checks under
// one name.
func (r *parityReport) checkGateArgs(ci workflowFile, checkLines []parityLine, registry map[string]command) {
	sites := func(at, line string) []parityGateSite {
		var out []parityGateSite
		for _, c := range parityGateCalls(shellSplit(line)) {
			if registry[c.name].runsIn != inCheck {
				continue
			}
			args := "no arguments"
			if len(c.args) > 0 {
				args = "`" + strings.Join(c.args, " ") + "`"
			}
			out = append(out, parityGateSite{at, c.name, parityGateArgs(registry[c.name].args, c.args), args})
		}
		return out
	}
	local, remote := make([]parityGateSite, 0, len(checkLines)), make([]parityGateSite, 0, len(ci.runs))
	for _, l := range checkLines {
		local = append(local, sites("`make "+l.target+"`", l.text)...)
	}
	for _, run := range ci.runs {
		remote = append(remote, sites(fmt.Sprintf("%s:%d", ci.path, run.line), run.text)...)
	}
	// A gate one side never calls is checkGatesRun's to report.
	unmatched := func(s parityGateSite, other []parityGateSite) bool {
		return slices.ContainsFunc(other, func(o parityGateSite) bool { return o.name == s.name }) &&
			!slices.ContainsFunc(other, func(o parityGateSite) bool { return o.name == s.name && o.key == s.key })
	}
	for _, s := range local {
		if unmatched(s, remote) {
			r.fail("%s calls the %s gate with %s and %s never does", s.at, s.name, s.args, parityCI)
		}
	}
	for _, s := range remote {
		if unmatched(s, local) {
			r.fail("%s calls the %s gate with %s and `make check` never does", s.at, s.name, s.args)
		}
	}
}

// parityGateArgs is a gate call's arguments as compared, given how its
// dispatch entry spells them: a BINARY, optional or not, is a target. No
// omitted word has a value here.
func parityGateArgs(spelling string, args []string) string {
	out := slices.Clone(args)
	for i, word := range strings.Fields(spelling) {
		if strings.Trim(word, "[]") == "BINARY" && i < len(out) {
			out[i] = parityTarget(out[i])
		}
	}
	return strings.Join(out, "\x00")
}

// parityTarget is a binary smoke runs, as compared. smoke runs it through
// exec, which looks a name with no slash up on PATH
// (https://pkg.go.dev/os/exec#Command), so `google-chat-mcp` is not
// `./google-chat-mcp`. A path compares clean, keeping its ./.
func parityTarget(word string) string {
	if !strings.Contains(word, "/") {
		return word
	}
	clean := path.Clean(word)
	if strings.Contains(clean, "/") {
		return clean
	}
	return "./" + clean
}

// checkVetTags holds what each tagged file needs to a vet pass on both
// sides that compiles it, CI's in a job a push to main runs.
func (r *parityReport) checkVetTags(ci workflowFile, checkLines []parityLine, needs []parityTagNeed) {
	push, _ := parityByTrigger(ci)
	checkPasses, ciPasses := parityVetPasses(parityLineTexts(checkLines)), parityVetPasses(parityRunTexts(push.runs))
	r.vetTags = len(needs)
	for _, need := range needs {
		if !need.vetted(checkPasses) {
			r.fail("%s builds only with %s (//go:build %s) and `make check` vets no pass that builds it; "+
				"a contributor cannot see it break", need.file, need, need.expr)
		}
		if !need.vetted(ciPasses) {
			r.fail("%s builds only with %s (//go:build %s) and %s vets no pass that builds it; "+
				"it compiles only on a maintainer's machine", need.file, need, need.expr, parityCI)
		}
	}
}

// parityVetPasses is the tags of each `go vet` in lines that sets any,
// from its last -tags flag wherever it sits among the flags: -tags=a,b or
// -tags a,b, with one dash or two (https://pkg.go.dev/flag#hdr-Command_line_flag_syntax).
// Only `go vet` is a vet pass: `go test -vet=off` vets nothing.
func parityVetPasses(lines []string) [][]string {
	var passes [][]string
	for _, line := range lines {
		for _, c := range goCommands(line) {
			if c.sub != "vet" {
				continue
			}
			// Only the flags before the packages set the build.
			flags := c.args[:len(c.args)-len(goAfterFlags(c.args))]
			var tags []string
			for i, arg := range flags {
				name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
				if !strings.HasPrefix(arg, "-") || name != "tags" {
					continue
				}
				if !inline && i+1 < len(flags) {
					value = flags[i+1]
				}
				tags = strings.FieldsFunc(value, func(c rune) bool { return c == ',' })
			}
			if len(tags) > 0 {
				passes = append(passes, tags)
			}
		}
	}
	return passes
}

// parityTagNeed is what a tagged file needs from a vet pass: its
// //go:build expression, the free tags in it, and the smallest -tags lists
// that build it, for the message. file is the first file with the line.
type parityTagNeed struct {
	file  string
	expr  constraint.Expr
	free  []string
	lists []string
}

func (n parityTagNeed) String() string { return "-tags=" + strings.Join(n.lists, " or -tags=") }

// vetted reports whether one of the passes compiles the file: its line
// holds with exactly the pass's tags, so `live && !evals` is not compiled
// by -tags=live,evals.
func (n parityTagNeed) vetted(passes [][]string) bool {
	return slices.ContainsFunc(passes, func(pass []string) bool { return parityBuildsWith(n.expr, n.free, pass) })
}

// parityTreeTags is what each //go:build line among files needs, once per
// expression and in their order: a file that builds with tags and not
// without them, which an untagged vet never compiles. files are the
// checkout's at root as git lists them, so a file git ignores cannot
// change the verdict.
func parityTreeTags(root string, files []string) ([]parityTagNeed, error) {
	fset := token.NewFileSet()
	var needs []parityTagNeed
	for _, file := range files {
		if !strings.HasSuffix(file, ".go") || parityGoIgnores(file) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(file)), nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			return nil, err
		}
		need, ok := parityRequiredTags(f)
		if ok && !slices.ContainsFunc(needs, func(n parityTagNeed) bool { return n.expr.String() == need.expr.String() }) {
			need.file = file
			needs = append(needs, need)
		}
	}
	return needs, nil
}

// parityGoIgnores reports whether the go command ignores a file for the
// directory it sits in: one named testdata, or one whose name starts with
// a dot (https://pkg.go.dev/cmd/go#hdr-Package_lists_and_patterns).
func parityGoIgnores(file string) bool {
	return slices.ContainsFunc(strings.Split(path.Dir(file), "/"), func(dir string) bool {
		return dir == "testdata" || dir != "." && strings.HasPrefix(dir, ".")
	})
}

// parityGoBuildExpr is a file's //go:build expression, or nil when it has
// none or the line does not parse
// (https://pkg.go.dev/cmd/go#hdr-Build_constraints).
func parityGoBuildExpr(f *ast.File) constraint.Expr {
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

// parityRequiredTags is what a file's //go:build line needs from a vet
// pass, false when it builds with no custom tag. Its lists are each minimal
// set of custom tags it builds with, joined by commas. A tag the go
// command sets for a target or a toolchain is free to be either value, so
// `e2e && linux` needs e2e, `a && b` needs one pass that sets both,
// `a || b` a pass that sets either, and `!windows` none.
func parityRequiredTags(f *ast.File) (parityTagNeed, bool) {
	expr := parityGoBuildExpr(f)
	if expr == nil {
		return parityTagNeed{}, false
	}
	var free, custom []string
	// Eval calls its function for every tag, even past a decided result.
	expr.Eval(func(tag string) bool {
		switch {
		// ignore keeps a file out of every build, so no -tags can need it.
		case tag == "ignore" || slices.Contains(free, tag) || slices.Contains(custom, tag):
		case parityToolchainTag(tag):
			free = append(free, tag)
		default:
			custom = append(custom, tag)
		}
		return false
	})
	slices.Sort(custom)
	if parityBuildsWith(expr, free, nil) {
		return parityTagNeed{}, false
	}
	var found []int
	var lists []string
	for size := 1; size <= len(custom); size++ {
		for set := range 1 << len(custom) {
			// A set holding one that builds is not minimal.
			if bits.OnesCount(uint(set)) != size || slices.ContainsFunc(found, func(f int) bool { return set&f == f }) {
				continue
			}
			var with []string
			for i, tag := range custom {
				if set&(1<<i) != 0 {
					with = append(with, tag)
				}
			}
			if parityBuildsWith(expr, free, with) {
				found = append(found, set)
				lists = append(lists, strings.Join(with, ","))
			}
		}
	}
	return parityTagNeed{expr: expr, free: free, lists: lists}, lists != nil
}

// parityBuildsWith reports whether a file builds with the custom tags in
// with, and no other, for some value of each free tag.
func parityBuildsWith(expr constraint.Expr, free, with []string) bool {
	for set := range 1 << len(free) {
		if expr.Eval(func(tag string) bool {
			if i := slices.Index(free, tag); i >= 0 {
				return set&(1<<i) != 0
			}
			return slices.Contains(with, tag)
		}) {
			return true
		}
	}
	return false
}

// parityKnownOS and parityKnownArch are the GOOS and GOARCH values
// go/build knows, as https://go.dev/src/internal/syslist/syslist.go lists
// them.
var (
	parityKnownOS = map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
		"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
		"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	parityKnownArch = map[string]bool{
		"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
		"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
		"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
		"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
		"sparc": true, "sparc64": true, "wasm": true,
	}
	// parityOtherTags are set by the go command for a target and a
	// toolchain, and race, msan and asan by -race, -msan and -asan
	// (https://github.com/golang/go/blob/go1.27.3/src/cmd/go/internal/work/init.go#L212).
	parityOtherTags = map[string]bool{
		"unix": true, "cgo": true, "gc": true, "gccgo": true, "race": true, "msan": true, "asan": true,
	}
	// parityReleaseTag is a release tag, such as go1.22, set for each
	// release up to the toolchain's own.
	parityReleaseTag = regexp.MustCompile(`^go1\.\d+$`)
)

// parityToolchainTag reports whether a build tag is one no -tags flag
// needs to name: an OS, an architecture or one of its features, such as
// amd64.v2, or a tag the go command sets
// (https://pkg.go.dev/cmd/go#hdr-Build_constraints). A goexperiment tag
// is GOEXPERIMENT's to set (https://pkg.go.dev/internal/goexperiment).
func parityToolchainTag(tag string) bool {
	arch, _, _ := strings.Cut(tag, ".")
	return parityKnownOS[tag] || parityKnownArch[arch] || parityOtherTags[tag] || parityReleaseTag.MatchString(tag) ||
		strings.HasPrefix(tag, "goexperiment.")
}
