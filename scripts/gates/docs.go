package main

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// The docs gate checks references, not claims. Decision and evidence IDs,
// every citation of one, every link to a heading, the codemap and the
// paths the docs name all have to resolve. Whether the cited text is true
// is for review and the live run.

// The files the docs gate reads besides the ones staleness names.
const (
	evidenceDocPath       = "docs/evidence.md"
	agentFilePath         = "CLAUDE.md"
	citationAllowlistPath = "testdata/citation-allowlist.txt"
	makefilePath          = "Makefile"
	golangciPath          = ".golangci.yml"
)

// idKind is one family of IDs. Its width is the fewest digits an ID is
// written with, zero-padded: two or more after D-, three or more after E-.
type idKind struct {
	prefix string
	width  int
	what   string
	// ref finds a citation; heading finds an entry, with its title.
	ref, heading *regexp.Regexp
}

func newIDKind(prefix string, width int, what string) idKind {
	return idKind{
		prefix:  prefix,
		width:   width,
		what:    what,
		ref:     regexp.MustCompile(fmt.Sprintf(`\b%s-\d+\b`, prefix)),
		heading: regexp.MustCompile(fmt.Sprintf(`^### (%s-\S*)(?:[ \t]+(.*))?$`, prefix)),
	}
}

var (
	decisionID = newIDKind("D", 2, "a decision ID")
	evidenceID = newIDKind("E", 3, "an evidence ID")
)

// number is the number in a canonical ID, or -1 when id is not one.
func (k idKind) number(id string) int {
	digits, ok := strings.CutPrefix(id, k.prefix+"-")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || k.text(n) != id {
		return -1
	}
	return n
}

func (k idKind) text(n int) string { return fmt.Sprintf("%s-%0*d", k.prefix, k.width, n) }

// pattern is the ID's shape as a message spells it: D-NN, E-NNN.
func (k idKind) pattern() string { return k.prefix + "-" + strings.Repeat("N", k.width) }

// describe is the kind with the ID a malformed one should be: the same
// number at the kind's width, or the first ID when it has no number.
func (k idKind) describe(id string) string {
	n, err := strconv.Atoi(strings.TrimPrefix(id, k.prefix+"-"))
	if err != nil || n < 1 {
		n = 1
	}
	return k.what + " like " + k.text(n)
}

// nextOn is the number a "Next ID:" line gives, or 0 when it is not one.
func (k idKind) nextOn(line string) int {
	id, ok := strings.CutPrefix(line, "Next ID: ")
	if !ok {
		return 0
	}
	return max(k.number(id), 0)
}

var (
	// A section number names nothing a reader can follow unless the line
	// also says which document it numbers.
	bareSection   = regexp.MustCompile(`§\s?\d+`)
	namedDocument = regexp.MustCompile(`RFC \d+|http`)

	mdLink = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	// A footnote, [^label]:, is not a link definition, though its text may
	// hold links.
	mdLinkDef  = regexp.MustCompile(`^\s{0,3}\[[^\]^][^\]]*\]:\s*<?([^\s>]+)`)
	atxHeading = regexp.MustCompile(`^#{1,6}[ \t]+(.*?)(?:[ \t]+#+)?$`)
	// Go has no code spans or links for the path check to read, so a doc
	// reference in a comment or a string is found by its shape: a file
	// under docs/ or at the root, with an optional anchor.
	goDocRef = regexp.MustCompile(`(?:^|[^\w/.-])(?:\./)?((?:docs/)?[\w.-]+\.md)(?:#([\p{L}\p{N}_-]+))?`)
	// The root documents are named in capitals. A lowercase name in a
	// comment is a file the code writes, which need not exist here.
	rootDocName = regexp.MustCompile(`^[A-Z][A-Z0-9_-]*\.md$`)
	// An entry runs to the next heading at its own level or above.
	entryEnd = regexp.MustCompile(`^#{1,3} `)

	separatorRow = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+$`)
	verdictWord  = regexp.MustCompile(`^[A-Za-z]+`)
	packageName  = regexp.MustCompile(`^((?:internal|cmd)/[^/\s]+)/?$`)
	gatesCommand = regexp.MustCompile(`(?:^|\s|\./)scripts/gates\s+(\S+)`)
	// A rule line: targets, a colon, and not the := of an assignment.
	makeRule = regexp.MustCompile(`(?m)^([^\s:#=]+(?:[ \t]+[^\s:#=]+)*)[ \t]*:(?:[^=]|$)`)
)

const supersededBy = "superseded by "

var (
	indexHeader = []string{"ID", "Decision", "Status", "Held by"}
	// The last status is a pattern; validStatus reads it.
	decisionStatuses = []string{"accepted", "rejected", "deprecated", supersededBy + decisionID.pattern()}
	decisionFields   = []string{"Rejected", "Cost"}
	evidenceFields   = []string{"Supports", "Source says", "Observed", "Verdict", "Held by"}
	evidenceVerdicts = []string{"confirmed", "contradicted", "refuted"}
)

// unfollowable marks a path token as a glob or a placeholder.
const unfollowable = "*?[]{}<>$…"

func docs(_ []string, stdout, stderr io.Writer) int {
	if problems := docsProblems(); len(problems) > 0 {
		return reportProblems(stderr, problems)
	}
	_, _ = fmt.Fprintln(stdout, "docs ok")
	return 0
}

// docsProblems runs every check over the checkout in the working
// directory. Later checks need what earlier ones read: the test names
// from the Go pass, and the decisions and evidence the citations resolve to.
func docsProblems() []string {
	files, err := checkoutFiles()
	if err != nil {
		return []string{err.Error()}
	}
	g := newDocsGate(files)
	g.readGo()
	g.readVocabulary()
	g.readDecisions()
	g.readEvidence()
	g.readAllowlist()
	g.checkCitations()
	g.checkOrphans()
	g.checkCodemap()
	return g.problems
}

// checkoutFiles is every file git tracks or would commit, as it is on
// disk, sorted. A new doc is untracked until the commit that adds it, and
// the gate has to read it before that commit, as CI will after it.
func checkoutFiles() ([]string, error) {
	out, err := git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	listed := strings.Split(out, "\x00")
	files := make([]string, 0, len(listed))
	for _, f := range listed {
		// A file deleted in the worktree stays listed until it is staged.
		if _, err := os.Lstat(f); f != "" && err == nil {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// inTestdata reports whether a file is a fixture. Go ignores testdata
// directories, and the gate reads neither their code nor their prose.
func inTestdata(file string) bool {
	return slices.Contains(strings.Split(path.Dir(file), "/"), "testdata")
}

// docsGate is what the checks share: the checkout, what they read from
// it, and the problems they found.
type docsGate struct {
	files   []string
	present map[string]bool
	dirs    map[string]bool

	problems []string
	reported map[string]bool

	proseOf  map[string][]string
	slugs    map[string]map[string]bool
	comments map[string][]sourceLine
	literals map[string][]sourceLine

	tests       map[string]bool
	makeTargets map[string]bool
	linters     map[string]bool

	// decisions and evidence stay nil when their document cannot be read,
	// so a citation of one is not also reported as unknown.
	decisions map[string]*decision
	evidence  map[string]*evidenceEntry

	cited     map[string]bool
	allowlist map[string]allowance
	// bare counts each allowlisted file's bare section citations.
	bare map[string]int
}

// sourceLine is one line of a Go comment or string literal.
type sourceLine struct {
	line int
	text string
}

func newDocsGate(files []string) *docsGate {
	g := &docsGate{
		files:    files,
		present:  map[string]bool{},
		dirs:     map[string]bool{},
		reported: map[string]bool{},
		proseOf:  map[string][]string{},
		slugs:    map[string]map[string]bool{},
		comments: map[string][]sourceLine{},
		literals: map[string][]sourceLine{},
		tests:    map[string]bool{},
		cited:    map[string]bool{},
		bare:     map[string]int{},
	}
	for _, f := range files {
		g.present[f] = true
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			g.dirs[d] = true
		}
	}
	return g
}

// exists reports whether rel is a file or a directory of the checkout.
func (g *docsGate) exists(rel string) bool { return g.present[rel] || g.dirs[rel] }

// fail records a problem once. A line that cites one ID twice is one
// problem, not two.
func (g *docsGate) fail(format string, a ...any) {
	if p := fmt.Sprintf(format, a...); !g.reported[p] {
		g.reported[p] = true
		g.problems = append(g.problems, p)
	}
}

// at reports a problem at a line of a file, or at the file when line is 0.
func (g *docsGate) at(file string, line int, format string, a ...any) {
	where := file
	if line > 0 {
		where += ":" + strconv.Itoa(line)
	}
	g.fail("%s: %s", where, fmt.Sprintf(format, a...))
}

// malformed reports an ID not written at its kind's width. A heading or
// index row is read twice, by its reader and the citation scan, and the
// one wording lets fail report it once.
func (g *docsGate) malformed(file string, line int, k idKind, id string) {
	g.at(file, line, "%q is not written as %s", id, k.describe(id))
}

func (g *docsGate) readFile(rel string) (string, bool) {
	if !g.present[rel] {
		g.fail("%s is missing", rel)
		return "", false
	}
	raw, err := os.ReadFile(rel)
	if err != nil {
		g.fail("gates cannot read %s: %v", rel, err)
		return "", false
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n"), true
}

// lines is a Markdown file's prose lines, read once. A file that cannot be
// read is tried again by the next caller, and fail drops the repeat.
func (g *docsGate) lines(rel string) ([]string, bool) {
	if l, ok := g.proseOf[rel]; ok {
		return l, true
	}
	raw, ok := g.readFile(rel)
	if !ok {
		return nil, false
	}
	l := prose(raw)
	g.proseOf[rel] = l
	return l, true
}

// prose is a Markdown document's lines with fenced code blanked, so an
// example in a fence is neither a heading nor a citation. Blanking rather
// than dropping keeps the line numbers.
func prose(doc string) []string {
	lines := strings.Split(doc, "\n")
	fence := ""
	for i, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		lines[i] = line
		unindented := strings.TrimLeft(line, " ")
		marker := fenceMarker(unindented)
		switch {
		case fence == "" && marker != "":
			fence = marker
			lines[i] = ""
		case fence != "":
			if strings.HasPrefix(marker, fence) && strings.TrimLeft(unindented, marker[:1]) == "" {
				fence = ""
			}
			lines[i] = ""
		}
	}
	return lines
}

// fenceMarker is the run of three or more backticks or tildes a line opens
// a fence with, or empty. A backtick fence's info string holds no backtick,
// so ```x``` is a code span (https://spec.commonmark.org/0.31.2/#fenced-code-blocks).
func fenceMarker(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 && (c == "~" || !strings.Contains(line[n:], "`")) {
			return line[:n]
		}
	}
	return ""
}

// headingSlugs is every anchor GitHub gives a Markdown file's headings.
func (g *docsGate) headingSlugs(rel string) map[string]bool {
	if s, ok := g.slugs[rel]; ok {
		return s
	}
	lines, _ := g.lines(rel)
	s := map[string]bool{}
	for _, line := range lines {
		m := atxHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// A repeated heading gets -1, -2 and so on, skipping any suffix a
		// real heading already took, the way GitHub numbers them.
		base := githubSlug(m[1])
		slug := base
		for n := 1; s[slug]; n++ {
			slug = base + "-" + strconv.Itoa(n)
		}
		s[slug] = true
	}
	g.slugs[rel] = s
	return s
}

// githubSlug is the anchor GitHub gives a heading: lowercase, with
// everything but letters, digits, spaces, hyphens and underscores dropped,
// and spaces turned into hyphens.
func githubSlug(heading string) string {
	heading = mdLink.ReplaceAllString(heading, "")
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readGo collects every comment line, every test function name, and the
// string literals outside tests. A string is read for doc paths only, as
// a message can send a user to a doc; a test's strings are fixtures.
func (g *docsGate) readGo() {
	fset := token.NewFileSet()
	read := 0
	for _, file := range g.files {
		if !strings.HasSuffix(file, ".go") || inTestdata(file) {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			g.fail("gates cannot parse %s: %v", file, err)
			continue
		}
		read++
		for _, group := range f.Comments {
			for _, c := range group.List {
				start := fset.Position(c.Slash).Line
				for i, text := range strings.Split(c.Text, "\n") {
					g.comments[file] = append(g.comments[file], sourceLine{start + i, text})
				}
			}
		}
		if !strings.HasSuffix(file, "_test.go") {
			g.literals[file] = stringLines(fset, f)
			continue
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				g.tests[fn.Name.Name] = true
			}
		}
	}
	if read == 0 {
		g.fail("no .go files read: this gate would be looking at nothing")
	}
}

// stringLines is the value of every string literal in a file, by source
// line. Only a raw string spans lines; an escaped newline stays on one.
func stringLines(fset *token.FileSet, f *ast.File) []sourceLine {
	var out []sourceLine
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		parts := []string{value}
		if lit.Value[0] == '`' {
			parts = strings.Split(value, "\n")
		}
		start := fset.Position(lit.Pos()).Line
		for i, text := range parts {
			out = append(out, sourceLine{start + i, text})
		}
		return true
	})
	return out
}

// readVocabulary reads the Makefile targets and enabled linters a
// "Held by" item may name.
func (g *docsGate) readVocabulary() {
	g.makeTargets = map[string]bool{}
	if mk, ok := g.readFile(makefilePath); ok {
		for _, m := range makeRule.FindAllStringSubmatch(mk, -1) {
			for _, target := range strings.Fields(m[1]) {
				if !strings.HasPrefix(target, ".") {
					g.makeTargets[target] = true
				}
			}
		}
		if len(g.makeTargets) == 0 {
			g.at(makefilePath, 0, "no targets read: a Held by naming one would never match")
		}
	}
	if cfg, ok := g.readFile(golangciPath); ok {
		g.linters = enabledLinters(cfg)
		if len(g.linters) == 0 {
			g.at(golangciPath, 0, "no enabled linters read: a Held by naming one would never match")
		}
	}
}

// enabledLinters is the linters.enable list of a golangci-lint config.
// It reads the indentation, so an enable: nested under one linter's own
// settings is not taken for the list.
func enabledLinters(cfg string) map[string]bool {
	out := map[string]bool{}
	inLinters, inEnable, childIndent := false, false, -1
	for _, raw := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentOf(raw)
		if indent == 0 {
			inLinters, inEnable, childIndent = trimmed == "linters:", false, -1
			continue
		}
		if !inLinters {
			continue
		}
		if item, ok := strings.CutPrefix(trimmed, "- "); ok && inEnable {
			value, _, _ := strings.Cut(item, " #")
			out[strings.Trim(strings.TrimSpace(value), `"'`)] = true
			continue
		}
		if childIndent < 0 {
			childIndent = indent
		}
		inEnable = indent == childIndent && trimmed == "enable:"
	}
	return out
}

// holds reports whether a "Held by" item names something that exists.
func (g *docsGate) holds(item string) bool {
	_, gate := commands[item]
	return item == "live" || item == "review" || gate ||
		g.makeTargets[item] || g.linters[item] || g.tests[item]
}

func (g *docsGate) checkHolders(file string, line int, id string, holders []string) {
	if len(holders) == 0 {
		g.at(file, line, "%s is held by nothing", id)
		return
	}
	for _, h := range holders {
		if !g.holds(h) {
			g.at(file, line, "%s is held by %q, which is not live, review, a make target, "+
				"a gates command, an enabled linter or a test function", id, h)
		}
	}
}

// listItems splits a comma-separated cell or line. The closing period of
// a sentence and code quotes around an item are not part of it.
func listItems(s string) []string {
	parts := strings.Split(strings.TrimSuffix(strings.TrimSpace(s), "."), ",")
	out := make([]string, 0, len(parts))
	for _, item := range parts {
		if item = strings.Trim(strings.TrimSpace(item), "`"); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// orList is items the way a sentence lists them: "a, b or c".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// byNumber is a map's canonical IDs in numeric order.
func byNumber[V any](m map[string]V) []string {
	ids := slices.Collect(maps.Keys(m))
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	return ids
}

// lastLines are the indexes of a document's last n non-blank lines, in
// order, or nil when it has fewer.
func lastLines(lines []string, n int) []int {
	out := make([]int, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if lines[i] != "" {
			out = append(out, i)
		}
	}
	if len(out) < n {
		return nil
	}
	slices.Reverse(out)
	return out
}

// field is one "Name: value" line of an entry. A zero line means the
// entry has none.
type field struct {
	line  int
	value string
}

// entryFields reads the first line of each named field in the entry
// headed by lines[heading].
func entryFields(lines []string, heading int, names ...string) map[string]field {
	out := make(map[string]field, len(names))
	for j := heading + 1; j < len(lines) && !entryEnd.MatchString(lines[j]); j++ {
		line := strings.TrimSpace(lines[j])
		for _, name := range names {
			if v, ok := strings.CutPrefix(line, name+":"); ok && out[name].line == 0 {
				out[name] = field{j + 1, strings.TrimSpace(v)}
			}
		}
	}
	return out
}

// idTrailer is what a file's closing lines say about its IDs. Decisions
// are never retired, so theirs has no retired set.
type idTrailer struct {
	next, nextLine int
	retired        map[string]bool
	retiredLine    int
}

// checkIDRange holds a file's IDs to its trailer: each is below Next ID,
// and each number below it has an entry or is retired, so no ID returns.
func (g *docsGate) checkIDRange(file string, k idKind, ids map[string]int, t idTrailer) {
	nextText := k.text(t.next)
	for _, id := range byNumber(ids) {
		if k.number(id) >= t.next {
			g.at(file, ids[id], "%s is not below Next ID %s", id, nextText)
		}
	}
	for _, id := range byNumber(t.retired) {
		if k.number(id) >= t.next {
			g.at(file, t.retiredLine, "Retired lists %s, which is not below Next ID %s", id, nextText)
		}
	}
	missing := "an ID is never deleted"
	if t.retired != nil {
		missing = "list it under Retired"
	}
	for n := 1; n < t.next; n++ {
		id := k.text(n)
		_, has := ids[id]
		switch {
		case has && t.retired[id]:
			g.at(file, t.retiredLine, "%s is retired and still has an entry", id)
		case !has && !t.retired[id]:
			g.at(file, t.nextLine, "%s is below Next ID %s and is missing; %s", id, nextText, missing)
		}
	}
}

// decision is one D-ID as its index row and its entry give it. A zero
// line means that half is missing: line is the row's, entryLine the
// entry's heading.
type decision struct {
	id                    string
	line                  int
	title, status, heldBy string
	entryLine             int
	entryTitle            string
}

// lapsed reports whether a decision no longer holds. Only the architecture
// document may cite it, and the evidence that supported it, which stays true.
func (d *decision) lapsed() bool {
	return d.status == "deprecated" || strings.HasPrefix(d.status, supersededBy)
}

// validStatus reports whether s is one of decisionStatuses. Whether a
// successor exists is the citation check's, which reads the index too.
func validStatus(s string) bool {
	if successor, ok := strings.CutPrefix(s, supersededBy); ok {
		return decisionID.number(successor) > 0
	}
	return slices.Contains(decisionStatuses, s)
}

func (g *docsGate) decision(id string) *decision {
	if g.decisions[id] == nil {
		g.decisions[id] = &decision{id: id}
	}
	return g.decisions[id]
}

func (g *docsGate) readDecisions() {
	lines, ok := g.lines(architectureDocPath)
	if !ok {
		return
	}
	g.decisions = map[string]*decision{}
	g.readDecisionIndex(lines)
	g.readDecisionEntries(lines)
	rows, firstLines := 0, map[string]int{}
	for id, d := range g.decisions {
		firstLines[id] = cmp.Or(d.line, d.entryLine)
		if d.line != 0 {
			rows++
		}
	}
	if rows == 0 {
		g.at(architectureDocPath, 0, "no decisions in the index: this gate would be looking at nothing")
	}

	t := idTrailer{}
	if last := lastLines(lines, 1); last != nil {
		t.next, t.nextLine = decisionID.nextOn(lines[last[0]]), last[0]+1
	}
	if t.next == 0 {
		g.at(architectureDocPath, t.nextLine, `the last line is not "Next ID: %s"`, decisionID.pattern())
	}
	for _, id := range byNumber(g.decisions) {
		g.checkDecision(g.decisions[id])
	}
	if t.next > 0 {
		g.checkIDRange(architectureDocPath, decisionID, firstLines, t)
	}
}

func (g *docsGate) readDecisionIndex(lines []string) {
	start := slices.Index(lines, "## Decisions")
	if start < 0 {
		g.at(architectureDocPath, 0, `no "## Decisions" heading`)
		return
	}
	i := start + 1
	for i < len(lines) && lines[i] == "" {
		i++
	}
	if i+1 >= len(lines) || !strings.HasPrefix(lines[i], "|") || !separatorRow.MatchString(lines[i+1]) {
		g.at(architectureDocPath, start+1, `the decision index is not a table directly under "## Decisions"`)
		return
	}
	if header := tableCells(lines[i]); !slices.Equal(header, indexHeader) {
		g.at(architectureDocPath, i+1, "the decision index has the columns %q, want %q", header, indexHeader)
		return
	}
	for i += 2; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
		g.readIndexRow(i+1, tableCells(lines[i]))
	}
}

func tableCells(row string) []string {
	cells := strings.Split(strings.Trim(row, "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func (g *docsGate) readIndexRow(line int, row []string) {
	if len(row) != len(indexHeader) {
		g.at(architectureDocPath, line, "an index row has %d cells, want %d", len(row), len(indexHeader))
		return
	}
	id := row[0]
	if decisionID.number(id) < 0 {
		g.malformed(architectureDocPath, line, decisionID, id)
		return
	}
	d := g.decision(id)
	if d.line != 0 {
		g.at(architectureDocPath, line, "%s is in the index twice", id)
		return
	}
	d.line, d.title, d.status, d.heldBy = line, row[1], row[2], row[3]
}

func (g *docsGate) readDecisionEntries(lines []string) {
	for i, line := range lines {
		m := decisionID.heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id := m[1]
		if decisionID.number(id) < 0 {
			g.malformed(architectureDocPath, i+1, decisionID, id)
			continue
		}
		d := g.decision(id)
		if d.entryLine != 0 {
			g.at(architectureDocPath, i+1, "%s has two entries", id)
			continue
		}
		d.entryLine, d.entryTitle = i+1, m[2]
		fields := entryFields(lines, i, decisionFields...)
		for _, name := range decisionFields {
			if fields[name].line == 0 {
				g.at(architectureDocPath, i+1, "%s has no %q line", id, name+":")
			}
		}
	}
}

func (g *docsGate) checkDecision(d *decision) {
	switch {
	case d.line == 0:
		g.at(architectureDocPath, d.entryLine, "%s has no index row", d.id)
		return
	case d.entryLine == 0:
		g.at(architectureDocPath, d.line, "%s has no entry", d.id)
	case d.entryTitle != d.title:
		g.at(architectureDocPath, d.entryLine, "%s's entry is titled %q and its index row %q", d.id, d.entryTitle, d.title)
	}
	if !validStatus(d.status) {
		g.at(architectureDocPath, d.line, "%s has status %q, want %s", d.id, d.status, orList(decisionStatuses))
	}
	holders := listItems(d.heldBy)
	if !slices.Equal(holders, []string{"—"}) {
		g.checkHolders(architectureDocPath, d.line, d.id, holders)
	} else if d.status == "accepted" {
		g.at(architectureDocPath, d.line, "%s is accepted and held by —; an accepted decision names what holds it", d.id)
	}
}

// evidenceEntry is one E-ID. An entry with no home is an orphan: it backs
// no decision, nothing cites it, and it refutes nothing of ours.
type evidenceEntry struct {
	line     int
	refuted  bool
	supports bool
}

func (g *docsGate) readEvidence() {
	lines, ok := g.lines(evidenceDocPath)
	if !ok {
		return
	}
	g.evidence = map[string]*evidenceEntry{}
	for i, line := range lines {
		if m := evidenceID.heading.FindStringSubmatch(line); m != nil {
			g.readEvidenceEntry(lines, i, m[1])
		}
	}
	if len(g.evidence) == 0 {
		g.at(evidenceDocPath, 0, "no evidence entries: this gate would be looking at nothing")
	}
	g.checkEvidenceIDs(lines)
}

func (g *docsGate) readEvidenceEntry(lines []string, heading int, id string) {
	if evidenceID.number(id) < 0 {
		g.malformed(evidenceDocPath, heading+1, evidenceID, id)
		return
	}
	if g.evidence[id] != nil {
		g.at(evidenceDocPath, heading+1, "%s has two entries", id)
		return
	}
	e := &evidenceEntry{line: heading + 1}
	g.evidence[id] = e

	fields := entryFields(lines, heading, evidenceFields...)
	inOrder, last := true, 0
	for _, name := range evidenceFields {
		f := fields[name]
		if f.line == 0 {
			g.at(evidenceDocPath, e.line, "%s has no %q line", id, name+":")
			continue
		}
		inOrder = inOrder && f.line > last
		last = f.line
	}
	if !inOrder {
		g.at(evidenceDocPath, e.line, "%s's lines are out of order; want %s", id, strings.Join(evidenceFields, ", "))
	}
	if f := fields["Supports"]; f.line != 0 {
		e.supports = g.checkSupports(id, f)
	}
	if f := fields["Verdict"]; f.line != 0 {
		word := verdictWord.FindString(f.value)
		if !slices.Contains(evidenceVerdicts, word) {
			g.at(evidenceDocPath, f.line, "%s's verdict is %q, want %s", id, word, orList(evidenceVerdicts))
		}
		e.refuted = word == "refuted"
	}
	if f := fields["Held by"]; f.line != 0 {
		g.checkHolders(evidenceDocPath, f.line, id, listItems(f.value))
	}
}

// checkSupports holds the shape of a Supports line and reports whether it
// names a decision. Whether each D-ID exists is the citation check's,
// which reads this line too.
func (g *docsGate) checkSupports(id string, f field) bool {
	items := listItems(f.value)
	switch {
	case len(items) == 0:
		g.at(evidenceDocPath, f.line, "%s supports nothing; name D-IDs or none", id)
		return false
	case slices.Equal(items, []string{"none"}):
		return false
	}
	for _, item := range items {
		if decisionID.number(item) < 0 {
			g.at(evidenceDocPath, f.line, "%s supports %q, which is not a D-ID or none", id, item)
		}
	}
	return true
}

// checkEvidenceIDs reads the trailer, Retired and then Next ID, and holds
// the entries to it.
func (g *docsGate) checkEvidenceIDs(lines []string) {
	shape := fmt.Sprintf(`the last two lines are not "Retired: …" and "Next ID: %s"`, evidenceID.pattern())
	tail := lastLines(lines, 2)
	if tail == nil {
		g.at(evidenceDocPath, 0, "%s", shape)
		return
	}
	retiredValue, isRetiredLine := strings.CutPrefix(lines[tail[0]], "Retired:")
	t := idTrailer{
		next: evidenceID.nextOn(lines[tail[1]]), nextLine: tail[1] + 1,
		retired: map[string]bool{}, retiredLine: tail[0] + 1,
	}
	if !isRetiredLine || t.next == 0 {
		g.at(evidenceDocPath, t.nextLine, "%s", shape)
		return
	}
	if items := listItems(retiredValue); !slices.Equal(items, []string{"none"}) {
		for _, r := range items {
			if evidenceID.number(r) < 0 {
				g.at(evidenceDocPath, t.retiredLine, "Retired lists %q, which is not written as %s", r, evidenceID.describe(r))
				continue
			}
			t.retired[r] = true
		}
	}
	ids := map[string]int{}
	for id, e := range g.evidence {
		ids[id] = e.line
	}
	g.checkIDRange(evidenceDocPath, evidenceID, ids, t)
}

// allowance is an allowlist line: a file may keep count bare section
// citations, and the count may only fall.
type allowance struct{ line, count int }

func (g *docsGate) readAllowlist() {
	g.allowlist = map[string]allowance{}
	if !g.present[citationAllowlistPath] {
		return
	}
	raw, ok := g.readFile(citationAllowlistPath)
	if !ok {
		return
	}
	for i, line := range strings.Split(raw, "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		count := 0
		if len(fields) == 2 {
			count, _ = strconv.Atoi(fields[1])
		}
		if count < 1 {
			g.at(citationAllowlistPath, i+1, "%q is not a path and a count above zero", strings.TrimSpace(line))
			continue
		}
		g.allowlist[fields[0]] = allowance{i + 1, count}
	}
}

// isTrailer reports whether a line is one of the ID trailers, which name
// the IDs that do not exist.
func isTrailer(file, line string) bool {
	return (file == architectureDocPath || file == evidenceDocPath) &&
		(strings.HasPrefix(line, "Next ID:") || strings.HasPrefix(line, "Retired:"))
}

// scanned is the range of a Markdown file's lines the citation scan
// reads. A changelog's released sections were true when written; its
// [Unreleased] section becomes a release note, and ends where sectionFor ends it.
func scanned(file string, lines []string) (from, to int) {
	if file != changelogPath {
		return 0, len(lines)
	}
	start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "## [Unreleased]") })
	if start < 0 {
		return 0, 0
	}
	end := start + 1
	for end < len(lines) && !strings.HasPrefix(lines[end], "## ") && !isLinkDefinition(lines[end]) {
		end++
	}
	return start + 1, end
}

func (g *docsGate) checkCitations() {
	// The agent file is one of the documents whose paths this holds.
	if !g.present[agentFilePath] {
		g.fail("%s is missing", agentFilePath)
	}
	for _, file := range g.files {
		switch {
		case inTestdata(file):
		case strings.HasSuffix(file, ".go"):
			for _, c := range g.comments[file] {
				g.checkLine(file, c.line, c.text)
				g.checkGoDocRefs(file, c.line, c.text, true)
			}
			for _, s := range g.literals[file] {
				g.checkGoDocRefs(file, s.line, s.text, false)
			}
		case strings.HasSuffix(file, ".md"):
			g.checkMarkdown(file)
		}
	}
	g.checkAllowlist()
}

func (g *docsGate) checkMarkdown(file string) {
	lines, ok := g.lines(file)
	if !ok {
		return
	}
	from, to := scanned(file, lines)
	for i := from; i < to; i++ {
		line := lines[i]
		if isTrailer(file, line) {
			continue
		}
		g.checkLine(file, i+1, line)
		g.checkLinks(file, i+1, line)
		if strings.Contains(line, "`") {
			for _, span := range codeSpans(line) {
				g.checkSpan(file, i+1, span)
			}
		}
	}
}

// checkLine resolves the IDs and section numbers a line cites. Each regex
// runs only on a line holding the literal it needs.
func (g *docsGate) checkLine(file string, n int, text string) {
	if strings.Contains(text, "D-") {
		for _, id := range decisionID.ref.FindAllString(text, -1) {
			g.citeDecision(file, n, id)
		}
	}
	if strings.Contains(text, "E-") {
		for _, id := range evidenceID.ref.FindAllString(text, -1) {
			g.citeEvidence(file, n, id)
		}
	}
	if !strings.Contains(text, "§") || namedDocument.MatchString(text) {
		return
	}
	bare := bareSection.FindAllString(text, -1)
	switch _, listed := g.allowlist[file]; {
	case len(bare) == 0:
	case listed:
		g.bare[file] += len(bare)
	default:
		g.at(file, n, "cites a section by number (%s); cite a D-ID, an E-ID or docs/<file>.md#<slug>", bare[0])
	}
}

func (g *docsGate) citeDecision(file string, n int, id string) {
	d := g.decisions[id]
	switch {
	case decisionID.number(id) < 0:
		g.malformed(file, n, decisionID, id)
	case g.decisions == nil:
	case d == nil:
		g.at(file, n, "cites %s, which is not a decision", id)
	case file != architectureDocPath && file != evidenceDocPath && d.lapsed():
		g.at(file, n, "cites %s, which is %s", id, d.status)
	}
}

func (g *docsGate) citeEvidence(file string, n int, id string) {
	switch {
	case evidenceID.number(id) < 0:
		g.malformed(file, n, evidenceID, id)
	case g.evidence == nil:
	case g.evidence[id] == nil:
		g.at(file, n, "cites %s, which is not an evidence entry", id)
	case file != evidenceDocPath:
		g.cited[id] = true
	}
}

// checkAllowlist holds each allowlisted file's count to what it has, so
// the count can only fall and a file leaves the list when it reaches zero.
func (g *docsGate) checkAllowlist() {
	paths := slices.Collect(maps.Keys(g.allowlist))
	slices.SortFunc(paths, func(a, b string) int { return cmp.Compare(g.allowlist[a].line, g.allowlist[b].line) })
	for _, p := range paths {
		a, have := g.allowlist[p], g.bare[p]
		switch {
		case !g.present[p]:
			g.at(citationAllowlistPath, a.line, "lists %s, which does not exist", p)
		case have == 0:
			g.at(citationAllowlistPath, a.line, "lists %s, which has no bare section citation left; remove it", p)
		case have < a.count:
			g.at(citationAllowlistPath, a.line, "lists %s at %d bare section citations, and it has %d; lower the count", p, a.count, have)
		case have > a.count:
			g.at(citationAllowlistPath, a.line, "lists %s at %d bare section citations, and it has %d; "+
				"cite a D-ID, an E-ID or docs/<file>.md#<slug>", p, a.count, have)
		}
	}
}

// checkGoDocRefs resolves the docs a line of Go names. A string names one
// only by docs/ path or anchor: a bare file name in a string is data.
func (g *docsGate) checkGoDocRefs(file string, n int, text string, inComment bool) {
	if !strings.Contains(text, ".md") {
		return
	}
	verb := "names"
	if inComment {
		verb = "cites"
	}
	for _, m := range goDocRef.FindAllStringSubmatch(text, -1) {
		target, slug := m[1], m[2]
		if strings.HasPrefix(target, "docs/") || slug != "" || inComment && rootDocName.MatchString(target) {
			g.checkTarget(file, n, verb, target, slug)
		}
	}
}

// checkLinks resolves a Markdown link relative to the file it is in.
func (g *docsGate) checkLinks(file string, n int, text string) {
	if !strings.Contains(text, "](") && !strings.Contains(text, "]:") {
		return
	}
	if m := mdLinkDef.FindStringSubmatch(text); m != nil {
		g.checkLinkTarget(file, n, m[1])
		return
	}
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		g.checkLinkTarget(file, n, m[1])
	}
}

func (g *docsGate) checkLinkTarget(file string, n int, target string) {
	p, slug, _ := strings.Cut(target, "#")
	resolved := path.Join(path.Dir(file), p)
	switch {
	case strings.Contains(p, ":"):
		// A URL, which is not this gate's to fetch.
	case p == "":
		g.checkSlug(file, n, "links to", file, slug)
	case strings.HasPrefix(p, "/"):
		g.checkTarget(file, n, "links to", strings.TrimPrefix(path.Clean(p), "/"), slug)
	case resolved == ".." || strings.HasPrefix(resolved, "../"):
		// Out of the checkout, as GitHub's ../../issues/51 is: nothing here to check.
	default:
		g.checkTarget(file, n, "links to", resolved, slug)
	}
}

// checkTarget holds a reference to a file or directory of the checkout,
// and its slug to a heading when it names a Markdown file.
func (g *docsGate) checkTarget(file string, n int, verb, target, slug string) {
	if !g.exists(target) {
		g.at(file, n, "%s %s, which does not exist", verb, target)
		return
	}
	g.checkSlug(file, n, verb, target, slug)
}

func (g *docsGate) checkSlug(file string, n int, verb, target, slug string) {
	if slug != "" && strings.HasSuffix(target, ".md") && !g.headingSlugs(target)[slug] {
		g.at(file, n, "%s %s#%s, and %s has no heading with that slug", verb, target, slug, target)
	}
}

// checkSpan holds the make targets, gates commands and repository paths a
// code span names to what exists.
func (g *docsGate) checkSpan(file string, n int, span string) {
	fields := strings.Fields(span)
	if len(fields) > 1 && fields[0] == "make" {
		for _, target := range fields[1:] {
			if !strings.HasPrefix(target, "-") && !strings.Contains(target, "=") && !g.makeTargets[target] {
				g.at(file, n, "names make %s, which is not a Makefile target", target)
			}
		}
	}
	if m := gatesCommand.FindStringSubmatch(span); m != nil {
		if _, ok := commands[m[1]]; !ok {
			g.at(file, n, "names scripts/gates %s, which is not a gates command", m[1])
		}
	}
	for _, f := range fields {
		g.checkPath(file, n, f)
	}
}

// checkPath holds a token to the tree when it starts at a top-level entry
// of the checkout. Anything else is a name, not a path: a package, a
// function, a resource id. A glob or a placeholder is not followable.
func (g *docsGate) checkPath(file string, n int, token string) {
	p, slug, _ := strings.Cut(strings.TrimPrefix(token, "./"), "#")
	if p == "" || strings.ContainsAny(p, unfollowable) || strings.Contains(p, "...") {
		return
	}
	if first, _, _ := strings.Cut(p, "/"); !g.exists(first) {
		return
	}
	clean := strings.TrimSuffix(p, "/")
	switch {
	case strings.HasSuffix(p, "/") && g.present[clean]:
		g.at(file, n, "names %s, which is not a directory", token)
	case g.exists(clean):
		g.checkSlug(file, n, "names", clean, slug)
	case !g.dirs[goPackage(clean)]:
		g.at(file, n, "names %s, which does not exist", token)
	}
}

// goPackage is the package directory of a Go name written pkg/path.Ident:
// everything before the first dot after the last slash, or empty.
func goPackage(p string) string {
	slash := strings.LastIndex(p, "/")
	dot := strings.Index(p[slash+1:], ".")
	if slash < 0 || dot < 0 {
		return ""
	}
	return p[:slash+1+dot]
}

func (g *docsGate) checkOrphans() {
	for _, id := range byNumber(g.evidence) {
		if e := g.evidence[id]; !e.refuted && !e.supports && !g.cited[id] {
			g.at(evidenceDocPath, e.line, "%s supports no decision and is cited nowhere outside %s; "+
				"cite it where it is relied on", id, evidenceDocPath)
		}
	}
}

// checkCodemap holds the codemap to the tree: every package directly
// under internal/ and cmd/ is named, and every directory named is one. A
// name that does not exist is the path check's to report.
func (g *docsGate) checkCodemap() {
	lines, ok := g.lines(architectureDocPath)
	if !ok {
		return
	}
	start := slices.Index(lines, "## Codemap")
	if start < 0 {
		g.at(architectureDocPath, 0, `no "## Codemap" heading`)
		return
	}
	named := map[string]int{}
	for i := start + 1; i < len(lines) && !strings.HasPrefix(lines[i], "## "); i++ {
		for _, span := range codeSpans(lines[i]) {
			if m := packageName.FindStringSubmatch(span); m != nil && named[m[1]] == 0 {
				named[m[1]] = i + 1
			}
		}
	}
	tree := g.packageDirs()
	if len(tree) == 0 {
		g.fail("no package directories under internal/ or cmd/: the codemap check would be looking at nothing")
	}
	for _, pkg := range tree {
		if named[pkg] == 0 {
			g.at(architectureDocPath, start+1, `"## Codemap" does not name %s`, pkg)
		}
	}
	for _, pkg := range slices.Sorted(maps.Keys(named)) {
		if g.dirs[pkg] && !slices.Contains(tree, pkg) {
			g.at(architectureDocPath, named[pkg], `"## Codemap" names %s, which holds no .go file`, pkg)
		}
	}
}

// packageDirs is every directory directly under internal/ or cmd/ that
// holds a .go file of its own, sorted.
func (g *docsGate) packageDirs() []string {
	var out []string
	for _, f := range g.files {
		parts := strings.Split(f, "/")
		if len(parts) == 3 && (parts[0] == "internal" || parts[0] == "cmd") && strings.HasSuffix(f, ".go") && !inTestdata(f) {
			out = append(out, parts[0]+"/"+parts[1])
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
