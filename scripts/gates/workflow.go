package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// One reader for a GitHub workflow, shared by the pins and parity gates.
// It reads the block style these workflows are written in, a key and its
// value on one line, and refuses by name a shape it does not read, so a
// file written another way fails rather than yielding nothing to check.
// It is not a YAML parser, and is not grown into one.

const workflowDir = ".github/workflows"

// workflowFile is what the reader takes from one workflow.
type workflowFile struct {
	// path is slash-separated, for messages.
	path string
	text string
	// steps are the steps that use an action, and each job that calls a
	// reusable workflow, in file order.
	steps []workflowStep
	// env is every key under an env: block, at any level.
	env []workflowKey
	// images are each job's container image and each service's.
	images []workflowJobKey
	runs   []workflowRun
	// shell is the workflow-level `defaults: run: shell:`, or "".
	shell string
	// shells are every shell a run step can take, in file order: the
	// workflow's, each job's `defaults: run: shell:` and each step's.
	shells []workflowJobKey
	// permissions are the token's scopes each permissions: block sets, by
	// job, "" for the workflow's: a scope's level by its name, and a value
	// the whole block takes, such as write-all, under "".
	permissions map[string]map[string]string
	// refused are the shapes this reader does not read.
	refused []string
}

// workflowStep is one `uses:`, with the step's own with: and env: keys.
// A reusable one is a job that calls a reusable workflow, and its with:
// is the job's.
type workflowStep struct {
	job      string
	line     int
	uses     string
	comment  string
	with     map[string]workflowKey
	env      map[string]workflowKey
	reusable bool
}

// action is the step's action without its ref: "actions/checkout".
func (s workflowStep) action() string {
	a, _, _ := strings.Cut(s.uses, "@")
	return a
}

// input is the with: key that sets the input name, as workflowInputName
// reads both.
func (s workflowStep) input(name string) (workflowKey, bool) {
	for key, k := range s.with {
		if workflowInputName(key) == workflowInputName(name) {
			return k, true
		}
	}
	return workflowKey{}, false
}

// workflowInputName is an input's name as the runner passes it, after
// INPUT_: in upper case, a space read as an underscore
// (https://github.com/actions/runner/blob/v2.338.2/src/Runner.Worker/Handlers/Handler.cs#L185).
func workflowInputName(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, " ", "_"))
}

// workflowKey is one key and its value, unquoted, with its line. A block
// scalar's value is its lines, trimmed and joined by newlines.
type workflowKey struct {
	key, value string
	line       int
}

// workflowJobKey is a key that sets something a job runs, such as its
// container image or its shell, and what sets it.
type workflowJobKey struct {
	job, what string
	workflowKey
}

// workflowRun is one command line of a run: block, its comments and
// continuation breaks removed.
type workflowRun struct {
	job  string
	line int
	text string
}

var (
	// workflowKeyLine is a block mapping's plain key, with an optional
	// sequence dash before it and its value after it.
	workflowKeyLine = regexp.MustCompile(`^(-[ \t]+)?([^\s"'#{\[&*!|>%@` + "`" + `-][^:#]*?)[ \t]*:(?:[ \t]+(.*))?$`)
	// workflowQuotedKey is a key line whose key is quoted, which the reader
	// refuses rather than reads.
	workflowQuotedKey = regexp.MustCompile(`^(?:-[ \t]+)?("[^"]*"|'[^']*')[ \t]*:(?:[ \t]|$)`)
	// workflowBlockScalar is the indicator of a literal or folded scalar.
	workflowBlockScalar = regexp.MustCompile(`^[|>][-+0-9]*$`)
	// workflowKeyAt is a key at the start of a text, plain or in a pair of
	// quotes, after any explicit key's ?, anchor or tag, with its colon. A
	// quoted scalar with no colon after its closing quote is no key.
	workflowKeyAt = regexp.MustCompile(`^[ \t]*(?:(?:\?|[&!][^\s{}\[\],]*)[ \t]+)*(?:"([^"]*)"|'([^']*)'|([^\s"'{}\[\],:#]+))[ \t]*:`)
	// workflowVersionKey is a with: or env: key that names a tool's
	// version: an input such as cosign-release or syft-version, or an env
	// value such as GITLEAKS_VERSION. Its case is ignored: the runner reads
	// an input's name in upper case, and a run step reading $Tool_Version
	// takes a version all the same.
	workflowVersionKey = regexp.MustCompile(`(?i)(?:version|release)$`)
)

// workflowMergeKey refuses a merge key, as a key or inside a flow mapping.
const workflowMergeKey = "GitHub Actions does not document merge keys; write the mapping out"

// workflowReadKeys are the keys whose mapping or sequence the gates read.
var workflowReadKeys = map[string]bool{
	"jobs": true, "steps": true, "with": true, "env": true, "container": true, "services": true,
	"defaults": true, "run": true, "uses": true,
}

// workflowHeld reports whether the value of the key path ends at is one a
// gate holds, so it has to sit on the key's line: an action, an image, or
// a version under with: or env:.
func workflowHeld(path []string) bool {
	key, parent := workflowKeyParent(path)
	return key == "uses" || key == "image" || key == "container" ||
		(parent == "with" || parent == "env") && workflowVersionKey.MatchString(key)
}

// workflowKeyParent is a path's last key and the one before it, each ""
// where the path is too short.
func workflowKeyParent(path []string) (key, parent string) {
	if len(path) > 0 {
		key = path[len(path)-1]
	}
	if len(path) > 1 {
		parent = path[len(path)-2]
	}
	return key, parent
}

// readWorkflows reads every workflow of the working directory.
func readWorkflows() ([]workflowFile, error) { return workflowsUnder(".") }

// workflowsUnder reads every workflow under root, sorted, each by its
// slash-separated path from root.
func workflowsUnder(root string) ([]workflowFile, error) {
	var paths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		found, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(workflowDir), pattern))
		if err != nil {
			return nil, err
		}
		paths = append(paths, found...)
	}
	slices.Sort(paths)
	out := make([]workflowFile, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // a path under .github/workflows
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil, err
		}
		out = append(out, parseWorkflow(filepath.ToSlash(rel), string(data)))
	}
	return out, nil
}

// workflowNamed finds one workflow by its base name.
func workflowNamed(flows []workflowFile, base string) (workflowFile, bool) {
	for i := range flows {
		if flows[i].path == base || strings.HasSuffix(flows[i].path, "/"+base) {
			return flows[i], true
		}
	}
	return workflowFile{}, false
}

// workflowFrame is one open mapping key or sequence item, by the column
// it starts at. An item's key is "-", and an item of a job's steps: holds
// the index of its step. A key whose value was refused takes the lines
// under it with it. open is a key with no value on its line, whose value
// is the block under it.
type workflowFrame struct {
	indent  int
	key     string
	step    int
	refused bool
	open    bool
}

// workflowParser is parseWorkflow's state: the open frames, and in file
// order every step item read, with an action or without, and every job
// that calls a reusable workflow. calls is each job's with: keys, by the
// job's name, paths is each key line's key path, and refusedAt each line
// refused and bodyAt each line of a block scalar's body, by line number.
type workflowParser struct {
	w         workflowFile
	stack     []workflowFrame
	items     []workflowStep
	calls     map[string]map[string]workflowKey
	paths     map[int][]string
	refusedAt map[int]bool
	bodyAt    map[int]bool
}

// parseWorkflow reads one workflow's text.
func parseWorkflow(path, text string) workflowFile {
	p := workflowParser{
		w:     workflowFile{path: path, text: text},
		calls: map[string]map[string]workflowKey{},
		paths: map[int][]string{}, refusedAt: map[int]bool{}, bodyAt: map[int]bool{},
	}
	yamlStrayBreaks(text, p.w.refuse)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "---" {
			continue
		}
		m := workflowKeyLine.FindStringSubmatch(trimmed)
		if m == nil {
			p.unread(i+1, lines[i])
			continue
		}
		i = p.key(lines, i, m)
	}
	for _, s := range p.items {
		if s.line > 0 {
			if s.reusable {
				// A job's keys come in any order, so its with: may be read first.
				s.with = p.calls[s.job]
			}
			p.w.steps = append(p.w.steps, s)
		}
	}
	p.unrecorded(lines)
	return p.w
}

// key reads the key line lines[i], whose match is m, and returns the index
// of the last line it took: a block scalar takes the lines under it.
func (p *workflowParser) key(lines []string, i int, m []string) int {
	n, col := i+1, indent(lines[i])
	p.pop(col, m[1] != "")
	keyCol := p.openItem(col, m[1])
	key, raw := m[2], strings.TrimSpace(m[3])
	path := append(workflowPath(p.stack), key)
	p.paths[n] = path
	var step *workflowStep
	if len(p.stack) >= 4 && path[0] == "jobs" && path[2] == "steps" && p.stack[3].step >= 0 {
		step = &p.items[p.stack[3].step]
	}
	value, comment := workflowValue(raw)
	p.stack = append(p.stack, workflowFrame{indent: keyCol, key: key, step: -1, open: value == ""})
	if p.refused(n, path, raw, value) {
		p.stack[len(p.stack)-1].refused = true
		return i
	}
	// A quoted | is a scalar of its own, not a block scalar's indicator.
	if workflowBlockScalar.MatchString(value) && strings.HasPrefix(raw, value) {
		return p.blockScalar(lines, i, keyCol, path, step, value)
	}
	p.read(path, step, workflowKey{key, value, n}, comment)
	return i
}

// openItem opens the sequence item a key line's dash starts at col, a
// step's when it is an item of a job's steps:, and returns the column the
// key starts at. A line with no dash opens nothing.
func (p *workflowParser) openItem(col int, dash string) int {
	if dash == "" {
		return col
	}
	item := workflowFrame{indent: col, key: "-", step: -1}
	if path := workflowPath(p.stack); len(path) == 3 && path[0] == "jobs" && path[2] == "steps" {
		item.step = len(p.items)
		p.items = append(p.items, workflowStep{job: path[1], with: map[string]workflowKey{}, env: map[string]workflowKey{}})
	}
	p.stack = append(p.stack, item)
	return col + len(dash)
}

// blockScalar takes the block scalar under the key at lines[i], whose path
// is path and indicator value, and returns the index of its last line.
func (p *workflowParser) blockScalar(lines []string, i, keyCol int, path []string, step *workflowStep, value string) int {
	n, key := i+1, path[len(path)-1]
	// The body is indented past the key, not the dash before it, so a
	// step's next key ends it.
	body, end := workflowScalarBody(lines, i, keyCol)
	for j := i + 1; j <= end; j++ {
		p.bodyAt[j+1] = true
	}
	switch {
	case key == "run":
		p.w.addRuns(workflowJob(path), n+1, body, strings.HasPrefix(value, ">"))
	case workflowHeld(path):
		p.refuse(n, "%s: a block scalar; write the value on the key's line", key)
	case strings.HasPrefix(value, ">"):
		// A folded scalar's lines are one line, joined by spaces.
		p.read(path, step, workflowKey{key, strings.ReplaceAll(workflowJoin(body), "\n", " "), n}, "")
	default:
		p.read(path, step, workflowKey{key, workflowJoin(body), n}, "")
	}
	return end
}

// read records one key the gates ask about.
func (p *workflowParser) read(path []string, step *workflowStep, k workflowKey, comment string) {
	depth, job := len(path), workflowJob(path)
	switch {
	case step != nil && p.readStep(path, step, k, comment):
	case job != "" && depth == 3 && k.key == "uses":
		p.items = append(p.items, workflowStep{job: job, line: k.line, uses: k.value, comment: comment, reusable: true})
	case depth >= 2 && path[depth-2] == "env":
		p.w.env = append(p.w.env, k)
		if step != nil && depth == 6 {
			step.env[k.key] = k
		}
	case job != "":
		p.readJob(path, job, k)
	case path[0] == "permissions" && depth <= 2:
		p.setPermission("", path[1:], k.value)
	case slices.Equal(path, []string{"defaults", "run", "shell"}):
		p.w.shell = k.value
		p.w.shells = append(p.w.shells, workflowJobKey{"", "the workflow", k})
	}
}

// readStep records a step's action, its run line or a with: key, and
// reports whether k was one. A step's keys sit at depth 5: jobs, the job,
// steps, the item, the key.
func (p *workflowParser) readStep(path []string, step *workflowStep, k workflowKey, comment string) bool {
	switch depth := len(path); {
	case depth == 5 && k.key == "uses":
		step.line, step.uses, step.comment = k.line, k.value, comment
	case depth == 5 && k.key == "run" && k.value != "":
		p.w.addRuns(workflowJob(path), k.line, []string{k.value}, false)
	case depth == 6 && path[4] == "with":
		p.setInput(step.with, k)
	default:
		return false
	}
	return true
}

// readJob records a job's container image or a service's, a job's shell or
// a step's, its permissions, and a reusable call's with: keys.
func (p *workflowParser) readJob(path []string, job string, k workflowKey) {
	switch rest := path[2:]; {
	case len(rest) > 0 && len(rest) <= 2 && rest[0] == "permissions":
		p.setPermission(job, rest[1:], k.value)
	case len(rest) == 2 && rest[0] == "with":
		p.setInput(p.callWith(job), k)
	case slices.Equal(rest, []string{"container"}) && k.value != "", slices.Equal(rest, []string{"container", "image"}):
		p.w.images = append(p.w.images, workflowJobKey{job, "the container", k})
	case len(rest) == 3 && rest[0] == "services" && k.key == "image":
		p.w.images = append(p.w.images, workflowJobKey{job, "the service " + rest[1], k})
	case slices.Equal(rest, []string{"defaults", "run", "shell"}):
		p.w.shells = append(p.w.shells, workflowJobKey{job, "job " + job, k})
	case len(rest) == 3 && rest[0] == "steps" && k.key == "shell":
		p.w.shells = append(p.w.shells, workflowJobKey{job, "a step of job " + job, k})
	}
}

// setPermission records one key of a permissions: block, job's or, where
// job is "", the workflow's: a scope's level, or with no scope the block's
// own value.
func (p *workflowParser) setPermission(job string, scope []string, value string) {
	if p.w.permissions == nil {
		p.w.permissions = map[string]map[string]string{}
	}
	if p.w.permissions[job] == nil {
		p.w.permissions[job] = map[string]string{}
	}
	switch {
	case len(scope) == 1:
		p.w.permissions[job][scope[0]] = value
	case value != "":
		p.w.permissions[job][""] = value
	}
}

// grantsWrite reports whether job's token may write scope. A job's
// permissions replace the workflow's, since a scope a block leaves out is
// none, and write-all grants every scope
// (https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions).
// With neither, the token takes a default no file in the repository sets.
func (w *workflowFile) grantsWrite(job, scope string) bool {
	perms, ok := w.permissions[job]
	if !ok {
		perms = w.permissions[""]
	}
	return perms[""] == "write-all" || perms[scope] == "write"
}

// callWith is a reusable call's with: keys, by its job's name.
func (p *workflowParser) callWith(job string) map[string]workflowKey {
	if p.calls[job] == nil {
		p.calls[job] = map[string]workflowKey{}
	}
	return p.calls[job]
}

// setInput records one with: key in with, and refuses one that names an
// input with sets already, as workflowInputName reads names: the runner
// passes one value for both. with keeps the first.
func (p *workflowParser) setInput(with map[string]workflowKey, k workflowKey) {
	for key := range with {
		if workflowInputName(key) == workflowInputName(k.key) {
			p.refuse(k.line, "%s: names the input %s names, read ignoring case and a space as an underscore; set it once",
				k.key, key)
			return
		}
	}
	with[k.key] = k
}

// refused refuses a value this reader would misread: a merge key, an
// anchor, an alias or a tag, a quoted value with an escape in it, a flow
// collection in or under a key it reads, and a held value on another line.
func (p *workflowParser) refused(n int, path []string, raw, value string) bool {
	key, parent := workflowKeyParent(path)
	switch {
	case key == "<<":
		p.refuse(n, workflowMergeKey)
	case raw != "" && strings.ContainsRune("&*!", rune(raw[0])):
		p.refuse(n, "%s: an anchor, an alias or a tag; write the value out", key)
	case workflowQuoteEscape(raw):
		p.refuse(n, "%s: a quoted value with an escape in it; write it unquoted or as a block", key)
	case raw != "" && strings.ContainsRune("{[", rune(raw[0])) && (workflowReadKeys[key] || workflowReadKeys[parent]):
		p.refuse(n, "%s: a flow collection; write it as a block, one key a line", key)
	case value == "" && workflowHeld(path) && key != "container":
		p.refuse(n, "%s: has its value on another line; write it on the key's line", key)
	default:
		return false
	}
	return true
}

// unread refuses a line that is not a plain block key, by what it is where
// it can be named: a quoted key anywhere, a step written as a flow mapping,
// an anchored or empty item, or a held value continued from the line above.
// Only an item holding one scalar under a key no gate reads is let through.
func (p *workflowParser) unread(n int, line string) {
	trimmed := strings.TrimSpace(line)
	item := trimmed == "-" || strings.HasPrefix(trimmed, "- ")
	p.pop(indent(line), item)
	path := workflowPath(p.stack)
	last, _ := workflowKeyParent(path)
	quoted := workflowQuotedKey.FindStringSubmatch(trimmed)
	onLine, _ := workflowLineKeys(trimmed, 0)
	switch {
	case len(p.stack) > 0 && p.stack[len(p.stack)-1].refused:
	case quoted != nil:
		p.refuse(n, "%s: a quoted key; write the key plain", quoted[1])
	case strings.Contains(trimmed, "<<:"):
		p.refuse(n, workflowMergeKey)
	case slices.Contains(onLine, "uses"):
		p.refuse(n, "uses: is not a block key on its own line; write the step in block style")
	case item && workflowReadKeys[last]:
		p.refuse(n, "an item of %s: is not a block mapping; write it one key a line", last)
	case !strings.HasPrefix(trimmed, "- ") && (workflowHeld(path) || last == "run"):
		p.refuse(n, "%s: a value continued on the next line; write it on one line or as a block scalar", last)
	case !workflowScalarItem(trimmed):
		// A flow collection's brackets on lines of their own, a continued
		// plain scalar, a complex key: what follows lands at a path no gate
		// reads.
		p.refuse(n, "%q: not a block key, a one-line item or a comment; write it in block style, one key a line", trimmed)
	}
}

// workflowScalarItem reports whether a line is a sequence item holding one
// scalar on its line, such as a branch name, which no gate reads. A value
// that starts with an indicator, a nested item's dash included, is not one.
func workflowScalarItem(trimmed string) bool {
	value, ok := strings.CutPrefix(trimmed, "- ")
	value = strings.TrimSpace(value)
	return ok && value != "" && !strings.ContainsAny(value[:1], "-[]{},#&*!|>?:%@`")
}

// unrecorded refuses, on each line not refused already, a uses: that is no
// step's or job's the reader recorded, and a version key that is no step's
// or reusable call's with: key or env: key it recorded. It reads the text
// rather than the frames, so a shape the reader misreads without refusing
// it still cannot hide an action or a version, and takes a key only where
// one can stand, so a value's text is none. A block scalar's body is text:
// in a file YAML reads, the lines indented past its key are its body
// (https://yaml.org/spec/1.2.2/#81-block-scalar-styles).
func (p *workflowParser) unrecorded(lines []string) {
	uses, keys := p.recorded()
	// depth is the flow collections open where each line starts.
	depth := 0
	for i, line := range lines {
		n, t := i+1, strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || p.bodyAt[n] {
			continue
		}
		found, open := workflowLineKeys(t, depth)
		depth = open
		if p.refusedAt[n] {
			continue
		}
		if slices.Contains(found, "uses") && !uses[n] {
			p.w.refuse(n, "uses: not read as a step's or a job's; write the step in block style, one key a line")
		}
		if keys[n] || !p.holdsInputs(n) {
			continue
		}
		for _, k := range found {
			if workflowVersionKey.MatchString(k) {
				p.w.refuse(n, "%s: not read as a step's with: or an env: key; write it in block style, one key a line", k)
			}
		}
	}
}

// recorded is the lines of every uses: the reader recorded, and of every
// with: and env: key.
func (p *workflowParser) recorded() (uses, keys map[int]bool) {
	uses, keys = map[int]bool{}, map[int]bool{}
	for i := range p.w.steps {
		uses[p.w.steps[i].line] = true
		for _, k := range p.w.steps[i].with {
			keys[k.line] = true
		}
	}
	for _, k := range p.w.env {
		keys[k.line] = true
	}
	return uses, keys
}

// holdsInputs reports whether line n can hold an installer's input: a
// line the reader read no key path for, or a key line in a job's steps or
// with:. A key line outside both, such as a trigger's input, a job's
// output or a matrix, is no installer's input. An input that takes its
// version from one through ${{ }} is not one exact version, which pins
// refuses at the input.
func (p *workflowParser) holdsInputs(n int) bool {
	path := p.paths[n]
	return len(path) == 0 || len(path) > 2 && path[0] == "jobs" && (path[2] == "steps" || path[2] == "with")
}

// workflowLineKeys is each key on a line where a key can stand, and the
// flow collections open at its end, given depth open at its start. A key
// stands at the line's start, after its dashes, after a { or [, and after
// a , inside a flow collection; a , outside one is a plain scalar's text.
// A quoted scalar, opened only where a scalar starts, and a comment hold no
// key (https://yaml.org/spec/1.2.2/#73-flow-scalar-styles, https://yaml.org/spec/1.2.2/#66-comments).
func workflowLineKeys(line string, depth int) (keys []string, open int) {
	t := workflowAfterDashes(strings.TrimSpace(line))
	keys = workflowKeyStart(nil, t)
	// start is where a scalar can start. A quoted one is skipped to the
	// next quote of its kind, an escaped one too, which can only find more
	// keys; one that goes on past the line takes the rest of it.
	start := true
	for i := 0; i < len(t); i++ {
		switch c := t[i]; {
		case strings.IndexByte(" \t", c) >= 0:
		case c == '#' && workflowBlankAt(t, i-1):
			return keys, depth
		case start && strings.IndexByte(`"'`, c) >= 0:
			end := strings.IndexByte(t[i+1:], c)
			if end < 0 {
				return keys, depth
			}
			i, start = i+1+end, false
		case strings.IndexByte("{[", c) >= 0:
			depth++
			keys, start = workflowKeyStart(keys, t[i+1:]), true
		case c == ',' && depth > 0:
			keys, start = workflowKeyStart(keys, t[i+1:]), true
		case strings.IndexByte("}]", c) >= 0:
			depth, start = max(depth-1, 0), false
		case c == ':' && workflowBlankAt(t, i+1):
			start = true
		default:
			start = false
		}
	}
	return keys, depth
}

// workflowAfterDashes is a trimmed line without the sequence dashes it
// starts with.
func workflowAfterDashes(t string) string {
	for t == "-" || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "-\t") {
		t = strings.TrimSpace(t[1:])
	}
	return t
}

// workflowBlankAt reports whether t holds a blank at i, or i is past
// either end of it, where a comment may start or a key's colon end.
func workflowBlankAt(t string, i int) bool {
	return i < 0 || i >= len(t) || t[i] == ' ' || t[i] == '\t'
}

// workflowKeyStart is keys with the key at the start of s added, if one
// stands there.
func workflowKeyStart(keys []string, s string) []string {
	if m := workflowKeyAt.FindStringSubmatch(s); m != nil {
		return append(keys, m[1]+m[2]+m[3])
	}
	return keys
}

// pop closes the frames a line at col ends: each at col or deeper, but an
// open key at col when the line is an item of it. A sequence may sit at its
// key's own column, the dash counted as indentation
// (https://yaml.org/spec/1.2.2/#823-block-nodes).
func (p *workflowParser) pop(col int, item bool) {
	for n := len(p.stack); n > 0; n = len(p.stack) {
		if top := p.stack[n-1]; top.indent < col || item && top.indent == col && top.open {
			return
		}
		p.stack = p.stack[:n-1]
	}
}

// workflowPath is the keys of the open frames, items included as "-".
func workflowPath(stack []workflowFrame) []string {
	out := make([]string, 0, len(stack)+1)
	for _, f := range stack {
		out = append(out, f.key)
	}
	return out
}

// workflowJob is the job a key's path sits in, or "".
func workflowJob(path []string) string {
	if len(path) > 1 && path[0] == "jobs" {
		return path[1]
	}
	return ""
}

// workflowQuoteEscape reports whether a quoted value holds an escape that
// workflowValue does not read: a doubled single quote in single quotes, or
// a backslash in double quotes (https://yaml.org/spec/1.2.2/#73-flow-scalar-styles).
func workflowQuoteEscape(raw string) bool {
	if len(raw) < 2 || raw[0] != '"' && raw[0] != '\'' {
		return false
	}
	end := strings.IndexByte(raw[1:], raw[0]) + 1
	if raw[0] == '"' {
		return end > 0 && strings.Contains(raw[1:end], `\`)
	}
	return end > 0 && strings.HasPrefix(raw[end+1:], "'")
}

// workflowValue is a value without its quotes and the comment after it.
func workflowValue(s string) (value, comment string) {
	if len(s) > 1 && (s[0] == '"' || s[0] == '\'') {
		if end := strings.IndexByte(s[1:], s[0]); end >= 0 {
			_, comment := yamlCutComment(strings.TrimSpace(s[end+2:]))
			return s[1 : end+1], comment
		}
	}
	return yamlCutComment(s)
}

// loneCR names a CR that is not half of a CRLF pair.
const loneCR = "a CR with no LF after it"

// yamlBreaks are the line breaks a YAML parser may read that the YAML
// readers here do not, each by its name. They break lines at LF alone,
// and drop the CR of a CRLF pair. YAML 1.2 breaks a line at a lone CR too
// (https://yaml.org/spec/1.2.2/#54-line-break-characters), and YAML 1.1
// at NEL, LS and PS as well (https://yaml.org/spec/1.1/#id871136), so a
// line written after one would be read as the rest of the line before.
var yamlBreaks = map[rune]string{
	'\r':     loneCR,
	'\u0085': "U+0085 (NEXT LINE)",
	'\u2028': "U+2028 (LINE SEPARATOR)",
	'\u2029': "U+2029 (PARAGRAPH SEPARATOR)",
}

// strayBreak is a line break a reader does not read: its name, the line it
// sits on, counted at LF as the readers count, and the byte of the line it
// starts at, from 1.
type strayBreak struct {
	name     string
	line, at int
}

// strayBreaks is each rune of text that breaks names, but a CR followed by
// an LF.
func strayBreaks(text string, breaks map[rune]string) []strayBreak {
	var out []strayBreak
	line, start := 1, 0
	for i, r := range text {
		name, isBreak := breaks[r]
		switch {
		case r == '\n':
			line, start = line+1, i+1
		case !isBreak, r == '\r' && strings.HasPrefix(text[i+1:], "\n"):
		default:
			out = append(out, strayBreak{name, line, i - start + 1})
		}
	}
	return out
}

// yamlStrayBreaks refuses, through refuse, each line break of text a YAML
// parser may read and the YAML readers do not, by its line and its byte.
func yamlStrayBreaks(text string, refuse func(n int, format string, a ...any)) {
	for _, b := range strayBreaks(text, yamlBreaks) {
		refuse(b.line, "%s at byte %d of the line, which YAML reads as a line break and this reader does not; "+
			"break lines with LF or CRLF alone", b.name, b.at)
	}
}

// yamlCutComment is a YAML line or value without the comment after it, and
// the comment's text. A comment starts at a # that begins the text or
// follows white space (https://yaml.org/spec/1.2.2/#66-comments).
func yamlCutComment(s string) (value, comment string) {
	if rest, ok := strings.CutPrefix(s, "#"); ok {
		return "", strings.TrimSpace(rest)
	}
	if at := docsTrailingComment.FindStringIndex(s); at != nil {
		return strings.TrimSpace(s[:at[0]]), strings.TrimSpace(s[at[1]:])
	}
	return strings.TrimSpace(s), ""
}

// workflowScalarBody is a block scalar's lines after the key at line i,
// indented past col, and the index of its last line.
func workflowScalarBody(lines []string, i, col int) (body []string, end int) {
	end = i
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) != "" && indent(lines[j]) <= col {
			break
		}
		end = j
	}
	return lines[i+1 : end+1], end
}

// workflowJoin is a block scalar's lines, each trimmed, the blank ones
// dropped, joined by newlines.
func workflowJoin(body []string) string {
	var kept []string
	for _, l := range body {
		if t := strings.TrimSpace(l); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, "\n")
}

// addRuns adds a run: block's command lines from first on. A folded
// scalar is one line; a literal one is a script, a trailing backslash
// joining a line to the next.
func (w *workflowFile) addRuns(job string, first int, body []string, folded bool) {
	if folded {
		body = []string{strings.ReplaceAll(workflowJoin(body), "\n", " ")}
	}
	pending, start := "", first
	for i, l := range body {
		t := strings.TrimSpace(l)
		if pending == "" && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		if pending == "" {
			start = first + i
		}
		if cont, ok := strings.CutSuffix(t, "\\"); ok {
			pending += strings.TrimRight(cont, " \t") + " "
			continue
		}
		w.runs = append(w.runs, workflowRun{job, start, strings.TrimSpace(pending + t)})
		pending = ""
	}
	if t := strings.TrimSpace(pending); t != "" {
		w.runs = append(w.runs, workflowRun{job, start, t})
	}
}

func (w *workflowFile) refuse(n int, format string, a ...any) {
	w.refused = append(w.refused, fmt.Sprintf("%s:%d: %s", w.path, n, fmt.Sprintf(format, a...)))
}

// refuse refuses line n, which unrecorded then reads no further.
func (p *workflowParser) refuse(n int, format string, a ...any) {
	p.refusedAt[n] = true
	p.w.refuse(n, format, a...)
}
