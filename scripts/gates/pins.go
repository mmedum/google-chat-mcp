package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The pins gate holds every third-party tool to one exact version, the
// same version everywhere it is named.
//
// An action is pinned by a full commit SHA, with the version it is in
// the comment after it. A tag "can be moved or deleted if a bad actor
// gains access to the repository storing the action", in GitHub's words.
// https://docs.github.com/en/actions/reference/security/secure-use#using-third-party-actions
// A container image, a step's, a job's or a service's, is pinned by its
// sha256 digest for the same reason.
//
// Every tool an action installs is pinned beside it. A SHA pins the
// action, never the tool the action fetches: cosign-installer pinned by
// SHA with no `cosign-release` installs whatever cosign is newest,
// whatever its signing format. goreleaser-action reads `~>` as "a max
// satisfying semver one", so `~> v2.18.1` floats across every 2.18.x and
// reads like a pin.
// https://github.com/goreleaser/goreleaser-action/blob/v7.3.0/README.md#inputs
// Every action is classified as an installer or not, and an unknown one
// fails: being unclassified is how an unpinned tool slips through.
//
// Every version, an input, an env value, a `go run module@version`, a
// Makefile variable or a string in the gates' own Go, is one exact
// version, and a tool named in two files is named at one version: a local
// lint or scan otherwise passes on a version CI never runs. An input given
// as an expression, such as `${{ env.NAME }}`, is no exact version. A
// Makefile variable no recipe runs with the go command pins nothing, since
// the tool then runs from the PATH, and a Makefile tool is one
// `NAME ?= module@version` variable. A known tool's name as a word of a
// recipe or a hook is refused for the same reason. A known tool's
// module@version on any line of a workflow or the Makefile but a comment
// is a pin too, whatever the readers made of the line: they do not follow
// bash -c, eval, a make conditional or an env value the Makefile's `?=`
// takes, and are not grown to. The goreleaser config's hooks, signs and
// SBOMs run in the release, so a known tool's module@version in one is a
// pin, and none floats. Its release-note prose
// runs nothing and is not read.
//
// And every workflow pins its shell at the workflow level. The Windows
// runner's default shell is PowerShell, which does not read
// `-coverprofile=cov.out` the way bash does, and a job-level block fixes
// the forgetting exactly one level up: a job added later is bare again.
// The shell is bash, and no job or step sets another.
// https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#defaultsrunshell

// pinsTool is one tool this repository runs, and every way it can be
// named.
type pinsTool struct {
	name string
	// modules match a `module@version` in the Makefile, a run line or the
	// goreleaser config.
	modules []string
	// envKeys are env names whose value is this tool's version.
	envKeys []string
	// actions install this tool, each with the with: input that pins it.
	actions map[string]string
}

// pinsTools is every tool, with the installer actions a workflow uses. A
// row with installers fails when no workflow step uses one.
var pinsTools = []pinsTool{
	// go-version-file names go.mod, which is a pin by reference: go.mod
	// cannot disagree with itself the way two literals can.
	{name: "go", actions: map[string]string{"actions/setup-go": "go-version-file"}},
	{name: "golangci-lint", modules: []string{"github.com/golangci/golangci-lint"},
		actions: map[string]string{"golangci/golangci-lint-action": "version"}},
	{name: "goreleaser", modules: []string{"github.com/goreleaser/goreleaser"},
		actions: map[string]string{"goreleaser/goreleaser-action": "version"}},
	{name: "cosign", actions: map[string]string{"sigstore/cosign-installer": "cosign-release"}},
	{name: "syft", actions: map[string]string{"anchore/sbom-action/download-syft": "syft-version"}},
	{name: "mcp-publisher", envKeys: []string{"PUBLISHER_VERSION"}},
	{name: "gitleaks", modules: []string{"github.com/zricethezav/gitleaks", "github.com/gitleaks/gitleaks"}},
	{name: "govulncheck", modules: []string{"golang.org/x/vuln/cmd/govulncheck"}},
	{name: "go-licenses", modules: []string{"github.com/google/go-licenses"}},
	{name: "actionlint", modules: []string{"github.com/rhysd/actionlint"}},
}

// pinsNotInstallers are the actions that install no tool, each with the
// reason, so that adding one is a decision rather than an omission.
var pinsNotInstallers = map[string]string{
	"actions/checkout":                "checks out the repository",
	"actions/upload-artifact":         "uploads, installs nothing",
	"actions/download-artifact":       "downloads, installs nothing",
	"actions/attest-build-provenance": "calls the attestation API",
	"github/codeql-action/init":       "CodeQL's bundle is GitHub's to manage",
	"github/codeql-action/analyze":    "CodeQL's bundle is GitHub's to manage",
}

const (
	// pinsScripts holds the gates and the evals, whose Go may name a tool.
	pinsScripts = "scripts"
)

var (
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	exactSemver = regexp.MustCompile(`^v?` + semverPattern + `$`)
	// pinsModuleAt is a module@version in text. The version ends where a
	// shell word or command does.
	pinsModuleAt = regexp.MustCompile(`([A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+)@([^\s"'` + "`" + `);&|\\]+)`)
	// pinsVersionVar is a Makefile variable that holds a version apart
	// from its module, such as GITLEAKS_VERSION. It is read as written: the
	// Makefile's own VERSION is the server's, which the build stamps in.
	pinsVersionVar = regexp.MustCompile(`(?:version|release|_VERSION|_RELEASE)$`)
	// pinsContextRef is an expression that is one context's property and
	// nothing else, such as ${{ github.ref_name }}: no literal, operator or
	// function (https://docs.github.com/en/actions/reference/workflows-and-actions/expressions).
	pinsContextRef = regexp.MustCompile(`^\$\{\{\s*[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)+\s*\}\}$`)
	// pinsImageDigest is a container image pinned by its content digest.
	pinsImageDigest = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
	// pinsGoModToolchain and pinsGoModGo are setup-go's own patterns for
	// the toolchain directive and the go line.
	pinsGoModToolchain = regexp.MustCompile(`(?m)^toolchain go(1\.\d+(?:\.\d+|rc\d+)?)`)
	pinsGoModGo        = regexp.MustCompile(`(?m)^go (\d+(?:\.\d+)*)`)
)

// floatingDownload matches a tool fetched at whatever version is newest:
// a GitHub "latest release" download URL, or a Go module at @latest. The
// MCP registry's own publishing example installs its publisher from
// `releases/latest/download`. `runs-on: ubuntu-latest` is not one: an
// image label is not a tool a release artifact comes out of.
var floatingDownload = regexp.MustCompile(`releases/latest/download|@latest\b`)

// pinsFloors are the fewest workflows, action references and pinned
// versions the gate may read.
type pinsFloors struct{ workflows, actions, places int }

// pinsLimits are the repository's floors, each under what it holds.
var pinsLimits = pinsFloors{workflows: 4, actions: 15, places: 15}

// pinsFound is one place a tool's version is written.
type pinsFound struct{ version, at string }

// pinsReport is what the gate read and found.
type pinsReport struct {
	problems                               []string
	workflows, actions, installers, places int
	tools                                  map[string][]pinsFound
	// installed names each tool a workflow step installs.
	installed map[string]bool
	// judged is each tool@version a rule has recorded or refused, which the
	// text rule does not report again.
	judged map[string]bool
}

func (r *pinsReport) fail(format string, a ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, a...))
}

// add records one pin, refusing anything but one exact version.
func (r *pinsReport) add(tool, version, at string) {
	r.tools[tool] = append(r.tools[tool], pinsFound{version, at})
	r.judged[tool+"@"+version] = true
	r.places++
	if !exactSemver.MatchString(version) {
		r.fail("%s: %s is pinned to %q, which is not one exact version", at, tool, version)
	}
}

// judgeLatest marks each known tool's module@latest in text as refused.
func (r *pinsReport) judgeLatest(text string) {
	for _, m := range pinsModuleAt.FindAllStringSubmatch(text, -1) {
		if tool := pinsToolByModule(m[1]); tool != "" && m[2] == "latest" {
			r.judged[tool+"@latest"] = true
		}
	}
}

func pins(out io.Writer, _ []string) error {
	flows, err := readWorkflows()
	if err != nil {
		return err
	}
	mk, err := readMakefile()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	gomod := string(raw)
	goreleaser, err := readGoreleaser(".")
	if err != nil {
		return err
	}

	r := &pinsReport{tools: map[string][]pinsFound{}, installed: map[string]bool{}, judged: map[string]bool{},
		workflows: len(flows)}
	for i := range flows {
		pinsWorkflow(gomod, flows[i], r)
	}
	r.problems = append(r.problems, mk.refused...)
	pinsMakefileRecipes(mk, pinsMakefileVars(mk, r), r)
	r.add("go", pinsGoVersion(gomod), "go.mod")
	pinsGoreleaser(goreleaser, r)
	if err := pinsGoSource(r); err != nil {
		return err
	}
	// Last, so a version a reader recorded is not reported twice.
	for i := range flows {
		pinsTextVersions(flows[i].path, flows[i].text, r)
	}
	pinsTextVersions(makefilePath, mk.expand(mk.text), r)
	r.checkTools()
	// Zero findings and zero inputs look the same otherwise.
	r.problems = append(r.problems, floorProblems("workflows", r.workflows, pinsLimits.workflows)...)
	r.problems = append(r.problems, floorProblems("action references", r.actions, pinsLimits.actions)...)
	r.problems = append(r.problems, floorProblems("pinned versions", r.places, pinsLimits.places)...)
	if len(r.problems) > 0 {
		return fmt.Errorf("%d problem(s):\n  %s", len(r.problems), strings.Join(r.problems, "\n  "))
	}
	_, _ = fmt.Fprintf(out, "%d workflows with their shell pinned, %d actions by SHA, %d installers name their tool, "+
		"%d tools pinned in %d places, each at one version\n", r.workflows, r.actions, r.installers, len(r.tools), r.places)
	return nil
}

// checkTools holds each tool to one version wherever it is named, and to
// being named at all, goreleaser to the one its config's reader is checked
// against, and each installer to a step that uses it.
func (r *pinsReport) checkTools() {
	for _, t := range pinsTools {
		found := r.tools[t.name]
		if len(found) == 0 {
			r.fail("%s is pinned nowhere: a tool this gate names that no file pins is either gone or unread", t.name)
			continue
		}
		for _, f := range found[1:] {
			if pinsNormal(f.version) != pinsNormal(found[0].version) {
				r.fail("%s is %s at %s and %s at %s: one tool, one version",
					t.name, found[0].version, found[0].at, f.version, f.at)
			}
		}
	}
	// A version that is not one exact version is refused already.
	if found := r.tools["goreleaser"]; len(found) > 0 && exactSemver.MatchString(found[0].version) &&
		pinsNormal(found[0].version) != pinsNormal(goreleaserChecked) {
		r.fail("%s runs goreleaser %s, and the %s reader is checked against %s's source; check its key paths "+
			"and template rule against %s's, then set goreleaserChecked",
			found[0].at, found[0].version, parityGoreleaser, goreleaserChecked, found[0].version)
	}
	// A floor per installer, not a count of steps: one installer used in
	// every job would meet a count alone.
	for _, t := range pinsTools {
		if len(t.actions) > 0 && !r.installed[t.name] {
			r.fail("no workflow step installs %s through %s; the reader is not seeing the step, "+
				"or the table names an installer nothing uses", t.name, strings.Join(slices.Sorted(maps.Keys(t.actions)), " or "))
		}
	}
}

// pinsWorkflow applies the workflow rules to one file, with gomod the
// repository's go.mod.
func pinsWorkflow(gomod string, w workflowFile, r *pinsReport) {
	r.problems = append(r.problems, w.refused...)
	pinsShells(w, r)
	pinsFloating(w.path, w.text, r)
	pinsSteps(gomod, w, r)
	for _, img := range w.images {
		if !pinsImageDigest.MatchString(img.value) {
			r.fail("%s:%d: job %s runs %s from %s, which has no sha256 digest, so the image it runs can change",
				w.path, img.line, img.job, img.what, img.value)
		}
	}
	for _, k := range w.env {
		if !workflowVersionKey.MatchString(k.key) {
			continue
		}
		if tool := pinsToolByEnv(k.key); tool != "" {
			r.add(tool, k.value, fmt.Sprintf("%s:%d: %s", w.path, k.line, k.key))
		} else {
			r.fail("%s:%d: the env value %s names a version of a tool pinsTools does not know", w.path, k.line, k.key)
		}
	}
	pinsRuns(w.path, w.runs, r)
}

// pinsShells holds a workflow to a workflow-level shell, and every shell a
// run step can take to bash.
func pinsShells(w workflowFile, r *pinsReport) {
	if w.shell == "" {
		r.fail("%s: no workflow-level `defaults: run: shell:`. Every run step goes to a shell, "+
			"so a job-level block only fixes the jobs that exist today", w.path)
	}
	for _, sh := range w.shells {
		// An empty workflow shell is the missing one above.
		if sh.value != "bash" && (sh.value != "" || sh.job != "") {
			r.fail("%s:%d: %s sets the shell to %q, and every run step runs under bash: "+
				"another shell reads the command its own way", w.path, sh.line, sh.what, sh.value)
		}
	}
}

// pinsSteps holds each step and reusable call of a workflow: an action to
// a SHA and its classification, an image to a digest, and every version
// input to an exact version.
func pinsSteps(gomod string, w workflowFile, r *pinsReport) {
	for _, s := range w.steps {
		at := fmt.Sprintf("%s:%d", w.path, s.line)
		switch {
		case strings.HasPrefix(s.uses, "./"):
			// A local action or workflow carries no version of its own.
		case strings.HasPrefix(s.uses, "docker://"):
			r.actions++
			if !pinsImageDigest.MatchString(strings.TrimPrefix(s.uses, "docker://")) {
				r.fail("%s: job %s: %s is a container image with no sha256 digest, so the image it runs can change",
					at, s.job, s.uses)
			}
		default:
			pinsActionRef(at, s, r)
			if !s.reusable {
				pinsStep(gomod, w, s, r)
			}
		}
		pinsInputs(w.path, s, r)
	}
}

// pinsRuns records each module@version file's command lines run or install
// with the go command as a pin of a tool pinsTools knows.
func pinsRuns(file string, runs []workflowRun, r *pinsReport) {
	for _, run := range runs {
		for _, g := range pinsGoRuns(run.text) {
			module, version, _ := strings.Cut(g.pkg, "@")
			if tool := pinsToolByModule(module); tool != "" {
				r.add(tool, version, fmt.Sprintf("%s:%d: go %s %s", file, run.line, g.verb, module))
			} else {
				r.fail("%s:%d: go %s %s names a tool pinsTools does not know", file, run.line, g.verb, g.pkg)
			}
		}
	}
}

// pinsFloating refuses a tool fetched at whatever version is newest on
// any line of text but a comment line.
func pinsFloating(file, text string, r *pinsReport) {
	for i, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); !strings.HasPrefix(t, "#") {
			pinsFloatingAt(file, i+1, t, r)
		}
	}
}

// pinsFloatingAt refuses a tool line n of file fetches at whatever version
// is newest.
func pinsFloatingAt(file string, n int, line string, r *pinsReport) {
	if floating := floatingDownload.FindString(line); floating != "" {
		r.fail("%s:%d: %s installs whatever is newest. Name the version: what builds a release must not float",
			file, n, floating)
		r.judgeLatest(line)
	}
}

// pinsFromPath refuses each word of line n of file that names a tool
// pinsTools knows, by its name or a path ending in it, wherever it sits:
// a pipeline, a substitution and `env -i tool` run it from the PATH as a
// first word does, and a word that runs nothing is refused all the same.
// The go command is go.mod's, and the module a go run names is the run
// that pins the tool. remedy says how to run the tool instead.
func pinsFromPath(file string, n int, line, remedy string, r *pinsReport) {
	words := shellSplit(line)
	module := map[string]bool{}
	for _, run := range goRunsOf(words) {
		module[run.pkg] = true
	}
	named := map[string]bool{}
	for _, w := range words {
		tool := path.Base(w.text)
		if tool == "go" || module[w.text] || named[tool] ||
			!slices.ContainsFunc(pinsTools, func(t pinsTool) bool { return t.name == tool }) {
			continue
		}
		named[tool] = true
		r.fail("%s:%d: names %s as a word of its own, which runs it from the PATH at whatever version is installed there; %s",
			file, n, tool, remedy)
	}
}

// pinsGoreleaser holds each command the goreleaser config runs in the
// release as a workflow's run line is held: nothing fetched at whatever
// version is newest, and each module@version the go command runs a pin of
// a tool pinsTools knows. A sign's or an SBOM's command is words rather
// than a line, and a line may hide a run in bash -c, so in either a known
// tool's module@version is a pin too. An @latest the floating rule refused
// is not reported again. Release-note prose is not a command.
func pinsGoreleaser(g goreleaserFile, r *pinsReport) {
	r.problems = append(r.problems, g.refused...)
	for _, run := range slices.Concat(g.runs, g.words) {
		pinsFloatingAt(parityGoreleaser, run.line, run.text, r)
	}
	for _, run := range g.runs {
		for _, gr := range pinsGoRuns(run.text) {
			module, version, _ := strings.Cut(gr.pkg, "@")
			switch tool := pinsToolByModule(module); {
			case tool == "":
				r.fail("%s:%d: go %s %s names a tool pinsTools does not know", parityGoreleaser, run.line, gr.verb, gr.pkg)
			case !r.judged[tool+"@"+version]:
				r.add(tool, version, fmt.Sprintf("%s:%d: go %s %s", parityGoreleaser, run.line, gr.verb, module))
			}
		}
	}
	for _, word := range slices.Concat(g.runs, g.words) {
		for _, m := range pinsModuleAt.FindAllStringSubmatch(word.text, -1) {
			if tool := pinsToolByModule(m[1]); tool != "" && !r.judged[tool+"@"+m[2]] {
				r.add(tool, m[2], fmt.Sprintf("%s:%d: %s", parityGoreleaser, word.line, m[1]))
			}
		}
	}
}

// pinsActionRef holds one action reference to a commit SHA, with the
// version it is in the comment after it.
func pinsActionRef(at string, s workflowStep, r *pinsReport) {
	r.actions++
	action, sha, ok := strings.Cut(s.uses, "@")
	switch {
	case !ok:
		r.fail("%s: %s has no ref at all", at, action)
	case !fullSHA.MatchString(sha):
		r.fail("%s: %s is pinned to %q, a tag or branch that can be moved; pin the 40-character commit SHA",
			at, action, sha)
	case s.comment == "":
		r.fail("%s: %s is pinned by SHA with no comment saying which version it is", at, action)
	}
}

// pinsStep checks one step that uses an action: an installer sets the
// key that pins its tool, and any other action is classified.
func pinsStep(gomod string, w workflowFile, s workflowStep, r *pinsReport) {
	file, action := w.path, s.action()
	at := fmt.Sprintf("%s:%d: %s", file, s.line, action)
	installer := false
	for _, t := range pinsTools {
		key, ok := t.actions[action]
		if !ok {
			continue
		}
		installer = true
		r.installers++
		r.installed[t.name] = true
		v, set := s.input(key)
		switch {
		case !set:
			r.fail("%s is pinned by SHA and does not set %s, so the tool it installs is whatever "+
				"is current that morning", at, key)
		case t.name == "go" && path.Clean(v.value) != "go.mod":
			r.fail("%s sets %s to %q, which is not the repository's go.mod", at, key, v.value)
		case t.name == "go":
			r.add(t.name, pinsGoVersion(gomod), at)
		default:
			r.add(t.name, v.value, fmt.Sprintf("%s:%d: %s", file, v.line, action))
		}
		// setup-go reads go-version before go-version-file
		// (https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/README.md#L44),
		// so one beside go.mod is the Go the step installs.
		if v, set := s.input("go-version"); t.name == "go" && set {
			r.fail("%s:%d: %s sets go-version, which setup-go reads before %s; pin Go by go.mod alone",
				file, v.line, action, key)
		}
	}
	if _, known := pinsNotInstallers[action]; !installer && !known {
		r.fail("%s is not classified: add it to pinsTools with the input that pins its tool, "+
			"or to pinsNotInstallers with the reason", at)
	}
}

// pinsInputs holds every version input of a step or a reusable call to
// one exact version, but the key an installer's tool is pinned by, which
// pinsStep holds. An action's code, a local action's included, and a
// reusable workflow from another repository are not read here, so the
// version each is given is held where it is given. This repository's own
// reusable workflow is read here, and holds an input where it uses it, so
// one context passed on to it is no literal; any other value is held, as
// a run step there may fetch whatever version it is given.
func pinsInputs(file string, s workflowStep, r *pinsReport) {
	action := s.action()
	local := s.reusable && strings.HasPrefix(s.uses, "./")
	for _, key := range slices.Sorted(maps.Keys(s.with)) {
		v := s.with[key]
		installs := slices.ContainsFunc(pinsTools, func(t pinsTool) bool {
			pin, ok := t.actions[action]
			return ok && workflowInputName(pin) == workflowInputName(key)
		})
		if installs || !workflowVersionKey.MatchString(key) || local && pinsContextRef.MatchString(v.value) {
			continue
		}
		if !exactSemver.MatchString(v.value) {
			r.fail("%s:%d: %s: %s is %q, which is not one exact version", file, v.line, action, key, v.value)
		}
	}
}

// pinsGoRun is one module@version the go command runs or installs.
type pinsGoRun struct{ verb, pkg string }

// pinsGoRuns is each module@version a command line runs or installs with
// the go command, read as the shell splits it, its flags skipped. A
// package of this module has no version and is not one.
func pinsGoRuns(line string) []pinsGoRun {
	var out []pinsGoRun
	for _, run := range goRunsIn(line) {
		if strings.Contains(run.pkg, "@") {
			out = append(out, pinsGoRun{"run", run.pkg})
		}
	}
	for _, c := range goCommands(line) {
		if c.sub != "install" {
			continue
		}
		for _, pkg := range goAfterFlags(c.args) {
			if strings.Contains(pkg, "@") {
				out = append(out, pinsGoRun{"install", pkg})
			}
		}
	}
	return out
}

// pinsGoVersion is the Go a go.mod runs: its toolchain directive, or else
// its go line. setup-go installs that, and the go command switches to it
// (https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/src/installer.ts#L645,
// setup-go v7.0.0).
func pinsGoVersion(gomod string) string {
	if m := pinsGoModToolchain.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	if m := pinsGoModGo.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	return ""
}

// pinsMakefileVars reads every `module@version` variable, and returns each
// module@version every such variable's value holds, by variable.
func pinsMakefileVars(mk *makefile, r *pinsReport) map[string][]string {
	pinnedBy := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(mk.vars)) {
		// Recipes are read expanded, so a version kept in another
		// variable is compared as the recipe runs it.
		value := mk.expand("$(" + name + ")")
		if pinsVersionVar.MatchString(name) {
			r.fail("%s: %s holds a version apart from its module; write the tool as one `NAME ?= module@version` variable",
				makefilePath, name)
			continue
		}
		found := pinsModuleAt.FindAllStringSubmatch(value, -1)
		for _, m := range found {
			pinnedBy[name] = append(pinnedBy[name], m[0])
		}
		if strings.Contains(value, "@latest") {
			r.fail("%s: %s is pinned to @latest", makefilePath, name)
			r.judgeLatest(value)
			continue
		}
		for _, m := range found {
			if tool := pinsToolByModule(m[1]); tool != "" {
				r.add(tool, m[2], makefilePath+": "+name)
			} else {
				r.fail("%s: %s names %s, which pinsTools does not know", makefilePath, name, m[1])
			}
		}
	}
	return pinnedBy
}

// pinsMakefileRecipes holds each recipe line as make runs it, its variables
// expanded and its continued lines joined: nothing fetched at whatever
// version is newest, no known tool named outside a go run, and what the go
// command runs pinned. A module written into a recipe is a pin of its own,
// and a variable in pinnedBy whose module@version no recipe naming it runs
// pins nothing: the tool then runs from the PATH.
func pinsMakefileRecipes(mk *makefile, pinnedBy map[string][]string, r *pinsReport) {
	// ran is each variable and module@version a recipe naming it runs.
	ran := map[[2]string]bool{}
	for _, target := range slices.Sorted(maps.Keys(mk.targets)) {
		t := mk.targets[target]
		for i, line := range mk.recipe(target) {
			pinsFloatingAt(makefilePath, t.lines[i], line, r)
			pinsFromPath(makefilePath, t.lines[i], line, "run it as `$(GO) run $(NAME)`, NAME a `NAME ?= module@version` variable", r)
			refs := mk.refs(t.recipe[i])
			for _, g := range pinsGoRuns(line) {
				named := false
				for name := range refs {
					if slices.Contains(pinnedBy[name], g.pkg) {
						named = true
						ran[[2]string{name, g.pkg}] = true
					}
				}
				if named {
					continue
				}
				module, version, _ := strings.Cut(g.pkg, "@")
				if tool := pinsToolByModule(module); tool != "" {
					r.add(tool, version, makefilePath+": "+target)
				} else {
					r.fail("%s: %s runs go %s %s, a tool pinsTools does not know", makefilePath, target, g.verb, g.pkg)
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(pinnedBy)) {
		for _, pkg := range pinnedBy[name] {
			if !ran[[2]string{name, pkg}] {
				r.fail("%s: %s pins %s and no recipe runs it with the go command, so the tool runs from the PATH",
					makefilePath, name, pkg)
			}
		}
	}
}

// pinsTextVersions records a known tool's module@version on any line of
// text but a comment line as a pin, whatever the readers made of the line,
// so it is held to one exact version and to the tool's one version. A
// version a rule already recorded or refused is not recorded again.
func pinsTextVersions(file, text string, r *pinsReport) {
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range pinsModuleAt.FindAllStringSubmatch(line, -1) {
			if tool := pinsToolByModule(m[1]); tool != "" && !r.judged[tool+"@"+m[2]] {
				r.add(tool, m[2], fmt.Sprintf("%s:%d: %s", file, i+1, m[1]))
			}
		}
	}
}

// pinsGoSource reads every `module@version` of a known tool out of the
// string literals in the maintainer tooling's Go source, so a copy of a
// pin in a gate is held to the Makefile's like any other. A comment pins
// nothing, so an example in one is not read.
func pinsGoSource(r *pinsReport) error {
	if _, err := os.Stat(pinsScripts); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(pinsScripts, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// A slash path opens on Windows too, and makes a parse error's
		// position read the same on every OS.
		file, err := parser.ParseFile(token.NewFileSet(), filepath.ToSlash(p), nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, m := range pinsModuleAt.FindAllStringSubmatch(text, -1) {
				if tool := pinsToolByModule(m[1]); tool != "" {
					r.add(tool, m[2], filepath.ToSlash(p)+": "+m[1])
				}
			}
			return true
		})
		return nil
	})
}

// pinsToolByModule is the tool a module path is, or one of its packages
// is: a path that only starts with the same text is another module.
func pinsToolByModule(module string) string {
	for _, t := range pinsTools {
		for _, m := range t.modules {
			if module == m || strings.HasPrefix(module, m+"/") {
				return t.name
			}
		}
	}
	return ""
}

func pinsToolByEnv(key string) string {
	for _, t := range pinsTools {
		if slices.Contains(t.envKeys, key) {
			return t.name
		}
	}
	return ""
}

func pinsNormal(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

func indent(line string) int {
	for i, c := range line {
		if c != ' ' && c != '\t' {
			return i
		}
	}
	return -1 // a blank line has no indentation of its own
}
