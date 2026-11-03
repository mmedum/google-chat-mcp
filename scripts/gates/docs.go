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

// The docs gate checks references, not claims. Decision IDs, every
// citation of one, every link to a heading, the codemap, the paths the
// docs name and the live steps the code names all have to resolve.
// Whether the cited text is true is for review and the live run.
//
// Evidence has no IDs. What was verified upstream is stated at the line
// that depends on it, with its source link, so an E- citation fails.

// The files the docs gate reads besides the ones staleness names.
const (
	agentFilePath         = "CLAUDE.md"
	citationAllowlistPath = "testdata/citation-allowlist.txt"
	makefilePath          = "Makefile"
	golangciPath          = ".golangci.yml"
)

// The live driver's package, and the type its step tables hold.
const (
	liveStepPackage = "internal/livecheck"
	liveStepType    = "step"
)

// The fewest links and code-span paths the Markdown pass may check, each
// well under what the docs hold. A pass below its floor has stopped
// reading what it checks.
const (
	linkFloor     = 15
	spanPathFloor = 60
)

// A decision ID is D- and two or more digits, zero-padded.
const decisionPattern = "D-NN"

var (
	decisionRef = regexp.MustCompile(`\bD-\d+\b`)
	// decisionHeading finds an entry, with its title.
	decisionHeading = regexp.MustCompile(`^### (D-\S*)(?:[ \t]+(.*))?$`)
	evidenceRef     = regexp.MustCompile(`\bE-\d+\b`)
)

// decisionNumber is the number in a canonical decision ID, or -1 when id
// is not one.
func decisionNumber(id string) int {
	digits, ok := strings.CutPrefix(id, "D-")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || decisionText(n) != id {
		return -1
	}
	return n
}

func decisionText(n int) string { return fmt.Sprintf("D-%02d", n) }

var (
	// A section number names nothing a reader can follow unless it says
	// which document it numbers: an RFC named right before it, or a URL
	// it sits in.
	bareSection   = regexp.MustCompile(`§\s?\d+`)
	sectionSource = regexp.MustCompile(`RFC \d+ ?§|https?://[^\s)>\]]+`)
	// A pointer like client.go:238 or client.go:238:5 names the file.
	lineSuffix = regexp.MustCompile(`:\d+(?::\d+)?$`)
	// A comment cites live steps by their quoted names, listed with commas,
	// "and" or "or".
	liveStepCite = regexp.MustCompile(`\blive steps? ((?:"[^"]*"(?:,? and |,? or |, )?)+)`)
	quotedName   = regexp.MustCompile(`"([^"]*)"`)
	// YAML and TOML end a line with a comment after white space.
	trailingComment = regexp.MustCompile(`\s#`)
	tomlMultiline   = regexp.MustCompile(`'''|"""`)

	mdLink = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	// A footnote, [^label]:, is not a link definition, though its text may
	// hold links.
	mdLinkDef  = regexp.MustCompile(`^\s{0,3}\[[^\]^][^\]]*\]:\s*<?([^\s>]+)`)
	atxHeading = regexp.MustCompile(`^#{1,6}[ \t]+(.*?)(?:[ \t]+#+)?$`)
	// Go has no code spans or links for the path check to read, so a doc
	// reference in a comment or a string is found by its shape: a relative
	// path ending in .md, with an optional anchor. A match never starts
	// right after a slash or a dot; one inside a URL is inURL's to drop.
	goDocRef = regexp.MustCompile(`(?:^|[^\w/.-])(?:\./)?((?:[\w.-]+/)*[\w.-]+\.md)(?:#([\p{L}\p{N}_-]+))?`)
	// The root documents are named in capitals. A lowercase name in a
	// comment is a file the code writes, which need not exist here.
	rootDocName = regexp.MustCompile(`^[A-Z][A-Z0-9_-]*\.md$`)
	// A root file or a source path is held even when its first segment is
	// gone. .txt and .json are left out: checksums.txt and manifest.json
	// name files in a release archive and a bundle.
	pathShape = regexp.MustCompile(`^(?:(?:cmd|internal|scripts|docs|testdata|packaging|\.github)/|[\w.-]+\.(?:md|ya?ml|toml|mod|sum)$|Makefile$|LICENSE$)`)
	// An entry runs to the next heading at its own level or above.
	entryEnd = regexp.MustCompile(`^#{1,3} `)

	separatorRow = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+$`)
	packageName  = regexp.MustCompile(`^((?:internal|cmd)/[^/\s]+)/?$`)
	gatesCommand = regexp.MustCompile(`(?:^|\s|\./)scripts/gates\s+(\S+)`)
	// A rule line: targets, a colon, and not the := of an assignment.
	makeRule = regexp.MustCompile(`(?m)^([^\s:#=]+(?:[ \t]+[^\s:#=]+)*)[ \t]*:(?:[^=]|$)`)
)

const supersededBy = "superseded by "

var (
	indexHeader = []string{"ID", "Decision", "Status", "Held by"}
	// The last status is a pattern; validStatus reads it.
	decisionStatuses = []string{"accepted", "rejected", "deprecated", supersededBy + decisionPattern}
	decisionFields   = []string{"Rejected", "Cost"}
)

// unfollowable marks a path token as a glob or a placeholder.
const unfollowable = "*?[]{}<>$…"

func docs(_ []string, stdout, stderr io.Writer) int {
	files, err := checkoutFiles()
	if err != nil {
		return reportProblems(stderr, []string{err.Error()})
	}
	return newDocsGate(files).report(stdout, stderr)
}

// report runs every check over the checkout and prints the verdict. Later
// checks need what earlier ones read: the test names from the Go pass, and
// the decisions the citations resolve to.
func (g *docsGate) report(stdout, stderr io.Writer) int {
	g.readGo()
	g.readVocabulary()
	g.readDecisions()
	g.readAllowlist()
	g.checkCitations()
	g.checkCodemap()
	if len(g.problems) > 0 {
		return reportProblems(stderr, g.problems)
	}
	_, _ = fmt.Fprintln(stdout, "docs ok")
	return 0
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
	liveSteps   map[string]bool

	// decisions stays nil when the architecture document cannot be read,
	// so a citation of one is not also reported as unknown.
	decisions map[string]*decision

	allowlist map[string]allowance
	// bare counts each allowlisted file's bare section citations.
	bare map[string]int

	// links and spanPaths count what the Markdown pass checked, each held
	// to its floor.
	links, spanPaths int
	floors           docsFloors
}

// docsFloors are the fewest links and code-span paths the Markdown pass
// may check.
type docsFloors struct{ links, spanPaths int }

// sourceLine is one line of a Go comment or string literal.
type sourceLine struct {
	line int
	text string
}

func newDocsGate(files []string) *docsGate {
	g := &docsGate{
		files:     files,
		present:   map[string]bool{},
		dirs:      map[string]bool{},
		reported:  map[string]bool{},
		proseOf:   map[string][]string{},
		slugs:     map[string]map[string]bool{},
		comments:  map[string][]sourceLine{},
		literals:  map[string][]sourceLine{},
		tests:     map[string]bool{},
		liveSteps: map[string]bool{},
		bare:      map[string]int{},
		floors:    docsFloors{links: linkFloor, spanPaths: spanPathFloor},
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

// malformed reports a decision ID not written with two or more digits,
// naming the ID it should be. A heading or index row is read twice, by its
// reader and the citation scan, and the one wording lets fail report it once.
func (g *docsGate) malformed(file string, line int, id string) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "D-"))
	if err != nil || n < 1 {
		n = 1
	}
	g.at(file, line, "%q is not written as a decision ID like %s", id, decisionText(n))
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
		if path.Dir(file) == liveStepPackage {
			g.readLiveSteps(f)
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
	if len(g.liveSteps) == 0 {
		g.at(liveStepPackage, 0, "no live steps read: a comment naming one would never match")
	}
}

// readLiveSteps collects the name of each step in a []step literal. A
// step built anywhere else, such as a unit test's fake, is not one the
// live run takes.
func (g *docsGate) readLiveSteps(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		table, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if array, ok := table.Type.(*ast.ArrayType); !ok || !isIdent(array.Elt, liveStepType) {
			return true
		}
		for _, elt := range table.Elts {
			if name, ok := stepName(elt); ok {
				g.liveSteps[name] = true
			}
		}
		return false
	})
}

// stepName is a step literal's name: its first field, or the field keyed
// name.
func stepName(elt ast.Expr) (string, bool) {
	step, ok := elt.(*ast.CompositeLit)
	if !ok || len(step.Elts) == 0 {
		return "", false
	}
	value := step.Elts[0]
	if _, keyed := value.(*ast.KeyValueExpr); keyed {
		value = nil
		for _, e := range step.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "name") {
				value = kv.Value
			}
		}
	}
	lit, ok := value.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
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

// listItems splits a comma-separated cell. A closing period and code
// quotes around an item are not part of it.
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

// hasField reports whether the entry headed by lines[heading] has a
// "name:" line.
func hasField(lines []string, heading int, name string) bool {
	for j := heading + 1; j < len(lines) && !entryEnd.MatchString(lines[j]); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), name+":") {
			return true
		}
	}
	return false
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
// document may cite it.
func (d *decision) lapsed() bool {
	return d.status == "deprecated" || strings.HasPrefix(d.status, supersededBy)
}

// validStatus reports whether s is one of decisionStatuses. Whether a
// successor exists is the citation check's, which reads the index too.
func validStatus(s string) bool {
	if successor, ok := strings.CutPrefix(s, supersededBy); ok {
		return decisionNumber(successor) > 0
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
	rows := 0
	for _, d := range g.decisions {
		if d.line != 0 {
			rows++
		}
	}
	if rows == 0 {
		g.at(architectureDocPath, 0, "no decisions in the index: this gate would be looking at nothing")
	}

	last := len(lines) - 1
	for last >= 0 && lines[last] == "" {
		last--
	}
	next := 0
	if last >= 0 {
		if id, ok := strings.CutPrefix(lines[last], "Next ID: "); ok {
			next = max(decisionNumber(id), 0)
		}
	}
	if next == 0 {
		g.at(architectureDocPath, last+1, `the last line is not "Next ID: %s"`, decisionPattern)
	}
	ids := slices.Collect(maps.Keys(g.decisions))
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(decisionNumber(a), decisionNumber(b)) })
	for _, id := range ids {
		g.checkDecision(g.decisions[id])
	}
	if next > 0 {
		g.checkNextID(ids, next, last+1)
	}
}

// checkNextID holds the decisions to the Next ID line: each ID is below
// it, and each number below it has a decision, so no ID returns.
func (g *docsGate) checkNextID(ids []string, next, nextLine int) {
	for _, id := range ids {
		if decisionNumber(id) >= next {
			d := g.decisions[id]
			g.at(architectureDocPath, cmp.Or(d.line, d.entryLine), "%s is not below Next ID %s", id, decisionText(next))
		}
	}
	for n := 1; n < next; n++ {
		if id := decisionText(n); g.decisions[id] == nil {
			g.at(architectureDocPath, nextLine, "%s is below Next ID %s and is missing; an ID is never deleted", id, decisionText(next))
		}
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

// tableCells splits a table row at each pipe a backslash does not escape,
// even inside a code span, and unescapes the rest
// (https://github.github.com/gfm/#tables-extension-).
func tableCells(row string) []string {
	row = strings.TrimPrefix(strings.TrimSpace(row), "|")
	if !strings.HasSuffix(row, `\|`) {
		row = strings.TrimSuffix(row, "|")
	}
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case strings.HasPrefix(row[i:], `\|`):
			cell.WriteByte('|')
			i++
		case row[i] == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(row[i])
		}
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

func (g *docsGate) readIndexRow(line int, row []string) {
	if len(row) != len(indexHeader) {
		g.at(architectureDocPath, line, "an index row has %d cells, want %d", len(row), len(indexHeader))
		return
	}
	id := row[0]
	if decisionNumber(id) < 0 {
		g.malformed(architectureDocPath, line, id)
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
		m := decisionHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id := m[1]
		if decisionNumber(id) < 0 {
			g.malformed(architectureDocPath, i+1, id)
			continue
		}
		d := g.decision(id)
		if d.entryLine != 0 {
			g.at(architectureDocPath, i+1, "%s has two entries", id)
			continue
		}
		d.entryLine, d.entryTitle = i+1, m[2]
		for _, name := range decisionFields {
			if !hasField(lines, i, name) {
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
	switch holders := listItems(d.heldBy); {
	case slices.Equal(holders, []string{"—"}):
		if d.status == "accepted" {
			g.at(architectureDocPath, d.line, "%s is accepted and held by —; an accepted decision names what holds it", d.id)
		}
	case len(holders) == 0:
		g.at(architectureDocPath, d.line, "%s is held by nothing", d.id)
	default:
		for _, h := range holders {
			if !g.holds(h) {
				g.at(architectureDocPath, d.line, "%s is held by %q, which is not live, review, a make target, "+
					"a gates command, an enabled linter or a test function", d.id, h)
			}
		}
	}
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

// isTrailer reports whether a line is the decisions' Next ID line, which
// names an ID that does not exist.
func isTrailer(file, line string) bool {
	return file == architectureDocPath && strings.HasPrefix(line, "Next ID:")
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
		case isRecord(file):
			// A record's reasons cite decisions, as a comment does.
			if raw, ok := g.readFile(file); ok {
				for i, line := range strings.Split(raw, "\n") {
					g.checkComment(file, i+1, line)
				}
			}
		case inTestdata(file):
		case strings.HasSuffix(file, ".go"):
			for _, c := range g.comments[file] {
				g.checkComment(file, c.line, c.text)
			}
			for _, s := range g.literals[file] {
				g.checkGoDocRefs(file, s.line, s.text, false)
			}
			g.checkLiveSteps(file, g.comments[file])
		case strings.HasSuffix(file, ".md"):
			g.checkMarkdown(file)
		case isConfigFile(file):
			if raw, ok := g.readFile(file); ok {
				for _, c := range configComments(file, raw) {
					g.checkComment(file, c.line, c.text)
				}
			}
		}
	}
	if g.links < g.floors.links {
		g.fail("%d Markdown links checked, fewer than %d: the links are not being read", g.links, g.floors.links)
	}
	if g.spanPaths < g.floors.spanPaths {
		g.fail("%d code-span paths checked, fewer than %d: the code spans are not being read", g.spanPaths, g.floors.spanPaths)
	}
	g.checkAllowlist()
}

// checkComment resolves what a comment line cites: decision IDs, section
// numbers and docs.
func (g *docsGate) checkComment(file string, n int, text string) {
	g.checkLine(file, n, text)
	g.checkGoDocRefs(file, n, text, true)
}

// isRecord reports whether a file is a hand-written TSV record: the API
// coverage and field verdicts. Each is read whole although it sits in
// testdata, since nothing generates it; any other table there is a fixture.
func isRecord(file string) bool { return file == coverageFile || file == fieldsRecordFile }

// isConfigFile reports whether a file holds # comments the citation scan
// reads as it reads a Go comment.
func isConfigFile(file string) bool {
	base := path.Base(file)
	return base == "Makefile" || base == ".gitignore" || hasTrailingComments(file)
}

// hasTrailingComments reports whether a file's format ends a line with a
// comment, as YAML and TOML do.
func hasTrailingComments(file string) bool {
	return strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml") || strings.HasSuffix(file, ".toml")
}

// configComments is each # comment in a config file, by line: a line that
// starts with #, and in YAML or TOML the end of a line after white space
// and #. A " #" in a one-line quoted string is read too, which can only
// add a check; a TOML multi-line string is left out, as its # is text.
func configComments(file, raw string) []sourceLine {
	var out []sourceLine
	trailing := hasTrailingComments(file)
	toml, open := strings.HasSuffix(file, ".toml"), ""
	for i, line := range strings.Split(raw, "\n") {
		if toml {
			line, open = outsideTOMLStrings(line, open)
		}
		if text := strings.TrimLeft(line, " \t"); strings.HasPrefix(text, "#") {
			out = append(out, sourceLine{i + 1, text})
		} else if at := trailingComment.FindStringIndex(line); trailing && at != nil {
			out = append(out, sourceLine{i + 1, line[at[1]-1:]})
		}
	}
	return out
}

// outsideTOMLStrings is a TOML line less its multi-line strings, each
// between three single or three double quotes
// (https://toml.io/en/v1.0.0#string). open is the delimiter of the string
// the line starts in, and the result's that of the one it leaves open. A
// delimiter after a # is in a comment and opens nothing.
func outsideTOMLStrings(line, open string) (string, string) {
	var out strings.Builder
	for {
		if open != "" {
			end := strings.Index(line, open)
			if end < 0 {
				return out.String(), open
			}
			line = line[end+len(open):]
		}
		start := tomlMultiline.FindStringIndex(line)
		if start == nil || strings.Contains(line[:start[0]], "#") {
			out.WriteString(line)
			return out.String(), ""
		}
		out.WriteString(line[:start[0]])
		line, open = line[start[1]:], line[start[0]:start[1]]
	}
}

// checkLiveSteps holds each live step a file's comments name to one the
// live driver defines. A name wraps across comment lines, so adjacent
// lines are read as one text.
func (g *docsGate) checkLiveSteps(file string, comments []sourceLine) {
	if len(g.liveSteps) == 0 {
		return
	}
	for start := 0; start < len(comments); {
		end := start + 1
		for end < len(comments) && comments[end].line == comments[end-1].line+1 {
			end++
		}
		g.checkLiveStepRun(file, comments[start:end])
		start = end
	}
}

// checkLiveStepRun checks one run of adjacent comment lines, reporting an
// unknown name at the line its opening quote is on.
func (g *docsGate) checkLiveStepRun(file string, run []sourceLine) {
	var text strings.Builder
	starts := make([]int, len(run))
	for i, c := range run {
		starts[i] = text.Len()
		text.WriteString(strings.TrimSpace(strings.TrimPrefix(c.text, "//")))
		text.WriteByte(' ')
	}
	joined := text.String()
	if !strings.Contains(joined, "live step") {
		return
	}
	for _, cite := range liveStepCite.FindAllStringSubmatchIndex(joined, -1) {
		for _, q := range quotedName.FindAllStringSubmatchIndex(joined[cite[2]:cite[3]], -1) {
			at := cite[2] + q[0]
			name := strings.Join(strings.Fields(joined[cite[2]+q[2]:cite[2]+q[3]]), " ")
			if !g.liveSteps[name] {
				i, _ := slices.BinarySearch(starts, at+1)
				g.at(file, run[i-1].line, "names the live step %q, which %s does not define", name, liveStepPackage)
			}
		}
	}
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
		for _, id := range decisionRef.FindAllString(text, -1) {
			g.citeDecision(file, n, id)
		}
	}
	if strings.Contains(text, "E-") {
		for _, id := range evidenceRef.FindAllString(text, -1) {
			g.at(file, n, "cites %s; evidence has no IDs: state the behavior at the line that depends on it, with its source link", id)
		}
	}
	if !strings.Contains(text, "§") {
		return
	}
	var bare []string
	sources := sectionSource.FindAllStringIndex(text, -1)
	for _, at := range bareSection.FindAllStringIndex(text, -1) {
		if !slices.ContainsFunc(sources, func(s []int) bool { return s[0] <= at[0] && at[0] < s[1] }) {
			bare = append(bare, text[at[0]:at[1]])
		}
	}
	switch _, listed := g.allowlist[file]; {
	case len(bare) == 0:
	case listed:
		g.bare[file] += len(bare)
	default:
		g.at(file, n, "cites a section by number (%s); cite a D-ID, docs/<file>.md#<slug> or a source link", bare[0])
	}
}

func (g *docsGate) citeDecision(file string, n int, id string) {
	d := g.decisions[id]
	switch {
	case decisionNumber(id) < 0:
		g.malformed(file, n, id)
	case g.decisions == nil:
	case d == nil:
		g.at(file, n, "cites %s, which is not a decision", id)
	case file != architectureDocPath && d.lapsed():
		g.at(file, n, "cites %s, which is %s", id, d.status)
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
				"cite a D-ID, docs/<file>.md#<slug> or a source link", p, a.count, have)
		}
	}
}

// checkGoDocRefs resolves the docs a line of Go names. A string names one
// only by docs/ path or anchor, from the root: a bare file name in a
// string is data. A path inside a URL names nothing here.
func (g *docsGate) checkGoDocRefs(file string, n int, text string, inComment bool) {
	if !strings.Contains(text, ".md") {
		return
	}
	for _, m := range goDocRef.FindAllStringSubmatchIndex(text, -1) {
		if inURL(text, m[2]) {
			continue
		}
		target, slug := text[m[2]:m[3]], ""
		if m[4] >= 0 {
			slug = text[m[4]:m[5]]
		}
		switch {
		case inComment:
			if resolved, ok := g.commentDoc(file, target, slug); ok {
				g.checkTarget(file, n, "cites", resolved, slug)
			}
		case strings.HasPrefix(target, "docs/") || slug != "":
			g.checkTarget(file, n, "names", target, slug)
		}
	}
}

// inURL reports whether text[at:] sits inside a URL or a query string: the
// word it is in has :// or ? before it.
func inURL(text string, at int) bool {
	word := text[strings.LastIndexFunc(text[:at], unicode.IsSpace)+1 : at]
	return strings.Contains(word, "://") || strings.Contains(word, "?")
}

// commentDoc is the file a .md path in a comment names, read from the
// comment's directory first and then from the root, as a link is. A
// missing file is still a reference when the path has an anchor, starts
// at something in either place or has a path's shape, or is a root
// document's name, in capitals. A path out of the checkout never is.
func (g *docsGate) commentDoc(file, target, slug string) (string, bool) {
	dir := path.Dir(file)
	local := path.Join(dir, target)
	first, _, nested := strings.Cut(target, "/")
	switch {
	case local == ".." || strings.HasPrefix(local, "../"):
		return "", false
	case g.exists(local):
		return local, true
	case g.exists(target):
		return target, true
	case nested && g.exists(path.Join(dir, first)):
		return local, true
	case slug != "":
		return target, true
	case !nested:
		return target, rootDocName.MatchString(target)
	}
	return target, g.exists(first) || pathShape.MatchString(target)
}

// checkLinks resolves a Markdown link relative to the file it is in. A
// link written in a code span is an example, not a link.
func (g *docsGate) checkLinks(file string, n int, text string) {
	if !strings.Contains(text, "](") && !strings.Contains(text, "]:") {
		return
	}
	text = codeSpanRe.ReplaceAllStringFunc(text, func(span string) string { return strings.Repeat(" ", len(span)) })
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
	if strings.Contains(p, ":") {
		return // A URL, which is not this gate's to fetch.
	}
	resolved := path.Join(path.Dir(file), p)
	switch {
	case p == "":
		resolved = file
	case strings.HasPrefix(p, "/"):
		resolved = strings.TrimPrefix(path.Clean(p), "/")
	case resolved == ".." || strings.HasPrefix(resolved, "../"):
		return // Out of the checkout, as GitHub's ../../issues/51 is: nothing here to check.
	}
	g.links++
	g.checkTarget(file, n, "links to", resolved, slug)
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

// checkPath holds a word to the tree when it starts at a top-level entry
// of the checkout, or has a path's shape. Anything else is a name, not a
// path: a package, a function, a resource id. A glob or a placeholder is
// not followable. Sentence punctuation after a word and a :line pointer
// are not part of the path.
func (g *docsGate) checkPath(file string, n int, word string) {
	if strings.ContainsAny(word, unfollowable) || strings.Contains(word, "...") {
		return
	}
	word = strings.TrimRight(word, ".,;)")
	p, slug, _ := strings.Cut(strings.TrimPrefix(word, "./"), "#")
	if p = lineSuffix.ReplaceAllString(p, ""); p == "" {
		return
	}
	if first, _, _ := strings.Cut(p, "/"); !g.exists(first) && !pathShape.MatchString(p) {
		return
	}
	g.spanPaths++
	clean := strings.TrimSuffix(p, "/")
	target, ok := g.spanTarget(file, clean)
	switch {
	case !ok && !g.dirs[goPackage(clean)]:
		g.at(file, n, "names %s, which does not exist", word)
	case !ok:
		// A Go name in a package that exists.
	case strings.HasSuffix(p, "/") && g.present[target]:
		g.at(file, n, "names %s, which is not a directory", word)
	default:
		g.checkSlug(file, n, "names", target, slug)
	}
}

// spanTarget is the file or directory a code span's path names. A bare
// name is read as a link is, from the naming file's directory, then from
// the root, then as a workflow named the way a person says it: ci.yml.
func (g *docsGate) spanTarget(file, p string) (string, bool) {
	tries := []string{p}
	if !strings.Contains(p, "/") {
		tries = []string{path.Join(path.Dir(file), p), p, path.Join(".github/workflows", p)}
	}
	for _, t := range tries {
		if g.exists(t) {
			return t, true
		}
	}
	return "", false
}

// goPackage is the package directory of a Go name written pkg/path.Ident
// or pkg/path.Type.Method: everything before the first dot after the last
// slash, or empty. The segment after that dot has to be a Go identifier
// and not a file extension, so a missing internal/evals/approver.go is not
// excused by its directory.
func goPackage(p string) string {
	slash := strings.LastIndex(p, "/")
	dot := strings.Index(p[slash+1:], ".")
	if slash < 0 || dot < 0 {
		return ""
	}
	name, _, _ := strings.Cut(p[slash+1+dot+1:], ".")
	if !token.IsIdentifier(name) || fileExtensions[name] {
		return ""
	}
	return p[:slash+1+dot]
}

// fileExtensions are what goPackage reads as a file's extension rather
// than a Go name.
var fileExtensions = map[string]bool{
	"go": true, "md": true, "yml": true, "yaml": true, "json": true, "toml": true,
	"tsv": true, "txt": true, "mod": true, "sum": true, "sh": true,
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
