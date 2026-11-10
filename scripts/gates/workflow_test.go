package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The reader takes each key from the step that owns it, in any order, and
// each command line from its run: block, in either scalar style.
func TestWorkflowReaderReadsTheBlockStyle(t *testing.T) {
	w := parseWorkflow("w.yml", strings.ReplaceAll(textLines(
		"env:",
		"  TOP_VERSION: 1.0.0",
		"jobs:",
		"  build:",
		"    env:",
		"      JOB: x",
		"    container:",
		"      image: node@sha256:1",
		"    steps:",
		"      - name: first",
		"        with:",
		"          version: v1.2.3 # the tool",
		"        env:",
		"          STEP: y",
		`        uses: 'a/b@c' # v1`,
		"      - run: echo one",
		"      - with:",
		"          subject-path: |",
		"            dist/*.zip,",
		"            dist/*.mcpb",
		"        uses: d/e@f",
		"      - run: |",
		"          # a comment",
		"          go run x \\",
		"            ./...",
		"          echo two",
		"        env:",
		"          RUN_ENV: z",
		"      - run: >-",
		"          go vet",
		"          ./...",
		"  call:",
		"    uses: o/r/.github/workflows/x.yml@g",
		"on:",
		"  push:",
		"    branches:",
		"      - main",
		"      - 'release/**' # a comment",
		"    paths: [go.mod, go.sum]",
	), "\n", "\r\n"))
	got := make([]string, 0, len(w.steps))
	for _, s := range w.steps {
		got = append(got, fmt.Sprintf("%s:%d %s #%s with=%v env=%v", s.job, s.line, s.uses, s.comment, s.with, s.env))
	}
	want := []string{
		"build:15 a/b@c #v1 with=map[version:{version v1.2.3 12}] env=map[STEP:{STEP y 14}]",
		"build:21 d/e@f # with=map[subject-path:{subject-path dist/*.zip,\ndist/*.mcpb 18}] env=map[]",
		"call:33 o/r/.github/workflows/x.yml@g # with=map[] env=map[]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("steps:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	got = make([]string, 0, len(w.runs))
	for _, r := range w.runs {
		got = append(got, fmt.Sprintf("%s:%d %s", r.job, r.line, r.text))
	}
	want = []string{"build:16 echo one", "build:24 go run x ./...", "build:26 echo two", "build:30 go vet ./..."}
	if !slices.Equal(got, want) {
		t.Errorf("runs = %q, want %q", got, want)
	}
	got = make([]string, 0, len(w.env))
	for _, k := range w.env {
		got = append(got, fmt.Sprintf("%s=%s:%d", k.key, k.value, k.line))
	}
	if want := []string{"TOP_VERSION=1.0.0:2", "JOB=x:6", "STEP=y:14", "RUN_ENV=z:28"}; !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
	if len(w.images) != 1 || w.images[0].value != "node@sha256:1" || w.images[0].what != "the container" || w.images[0].job != "build" {
		t.Errorf("images = %+v", w.images)
	}
	if w.refused != nil {
		t.Errorf("refused = %q", w.refused)
	}
}

// A sequence may sit at its key's own column, the dash counted as
// indentation (https://yaml.org/spec/1.2.2/#823-block-nodes), and its
// items are read as if they were indented under it.
func TestWorkflowReaderReadsASequenceAtItsKeysColumn(t *testing.T) {
	w := parseWorkflow("w.yml", textLines(
		"jobs:",
		"  build:",
		"    steps:",
		"    - uses: a/b@c # v1",
		"      with:",
		"        version: v1.2.3",
		"    - run: echo one",
		"    - run: |",
		"        go vet ./...",
		"      env:",
		"        K: v",
		"    runs-on: x",
		"  next:",
		"    steps:",
		"    - run: echo two",
	))
	got := make([]string, 0, len(w.steps))
	for _, s := range w.steps {
		got = append(got, fmt.Sprintf("%s:%d %s #%s with=%v env=%v", s.job, s.line, s.uses, s.comment, s.with, s.env))
	}
	if want := []string{"build:4 a/b@c #v1 with=map[version:{version v1.2.3 6}] env=map[]"}; !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	got = make([]string, 0, len(w.runs))
	for _, r := range w.runs {
		got = append(got, fmt.Sprintf("%s:%d %s", r.job, r.line, r.text))
	}
	if want := []string{"build:7 echo one", "build:9 go vet ./...", "next:15 echo two"}; !slices.Equal(got, want) {
		t.Errorf("runs = %q, want %q", got, want)
	}
	got = make([]string, 0, len(w.env))
	for _, k := range w.env {
		got = append(got, fmt.Sprintf("%s=%s:%d", k.key, k.value, k.line))
	}
	if want := []string{"K=v:11"}; !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
	if w.refused != nil {
		t.Errorf("refused = %q", w.refused)
	}
}

// A shape the reader does not read is refused by name, never read as
// nothing.
func TestWorkflowReaderRefusesWhatItCannotRead(t *testing.T) {
	w := parseWorkflow("w.yml", textLines(
		"jobs:",
		"  build:",
		"    services:",
		"      db: {image: postgres}",
		"    steps:",
		"      - uses: a/b@c",
		"        with: {version: v1}",
		"      -",
		"        uses: d/e@f",
		"      - run: go run x",
		"          --flag",
		"      - uses: &anchor g/h@i",
		"      - *anchor",
		`      - "uses": j/k@l`,
		"      - name: x",
		"        'with':",
		"          version: v1",
		"  other:",
		"    steps:",
		"    - *anchor",
		`    - run: "echo \"a\" b"`,
		`      name: 'it''s'`,
		`    - run: "go vet C:\\x"`,
		`      name: "it's" # 'a'' b'`,
		`    - run: 'echo "a"'`,
		"    - name: a name",
		"        continued",
		"    - ? a complex key",
		"    - run: echo",
		"      shell: bash",
		"    runs-on:",
		"      - [self-hosted]",
		"      - - nested",
		"      -",
		"...",
	))
	want := []string{
		"w.yml:4: db: a flow collection; write it as a block, one key a line",
		"w.yml:7: with: a flow collection; write it as a block, one key a line",
		"w.yml:8: an item of steps: is not a block mapping; write it one key a line",
		"w.yml:11: run: a value continued on the next line; write it on one line or as a block scalar",
		"w.yml:12: uses: an anchor, an alias or a tag; write the value out",
		"w.yml:13: an item of steps: is not a block mapping; write it one key a line",
		`w.yml:14: "uses": a quoted key; write the key plain`,
		`w.yml:16: 'with': a quoted key; write the key plain`,
		"w.yml:20: an item of steps: is not a block mapping; write it one key a line",
		"w.yml:21: run: a quoted value with an escape in it; write it unquoted or as a block",
		"w.yml:22: name: a quoted value with an escape in it; write it unquoted or as a block",
		"w.yml:23: run: a quoted value with an escape in it; write it unquoted or as a block",
		`w.yml:27: "continued": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		"w.yml:28: an item of steps: is not a block mapping; write it one key a line",
		`w.yml:32: "- [self-hosted]": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		`w.yml:33: "- - nested": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		`w.yml:34: "-": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		`w.yml:35: "...": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		// A uses: and a version under a line refused above are read at a
		// path no rule reads, so they are named as well.
		"w.yml:9: uses: not read as a step's or a job's; write the step in block style, one key a line",
		"w.yml:17: version: not read as a step's with: or an env: key; write it in block style, one key a line",
	}
	if !slices.Equal(w.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(w.refused, "\n  "), strings.Join(want, "\n  "))
	}
}

// A flow collection whose brackets sit on lines of their own opens no step,
// so the action inside it, and the version it is given, land at a path no
// gate reads. Each bracket line is refused, and the uses: and the version
// are too.
func TestWorkflowReaderRefusesAFlowCollectionOnLinesOfItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{
			"an action in a flow sequence of steps",
			textLines(
				"jobs:",
				"  sneaky:",
				"    runs-on: ubuntu-latest",
				"    steps:",
				"      [",
				"        {",
				"          uses: evil-org/evil-action@main",
				"        }",
				"      ]",
			),
			[]string{
				`w.yml:5: "[": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:6: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:8: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:9: "]": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				"w.yml:7: uses: not read as a step's or a job's; write the step in block style, one key a line",
			},
		},
		{
			"a flow with: under it",
			textLines(
				"jobs:",
				"  announce:",
				"    steps:",
				"      [",
				"        {",
				"          uses: evil-org/release-notifier@main,",
				"          with:",
				"            {",
				`              token: "${{ github.token }}"`,
				"            }",
				"        }",
				"      ]",
			),
			[]string{
				`w.yml:4: "[": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:5: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:8: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:10: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:11: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:12: "]": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				"w.yml:6: uses: not read as a step's or a job's; write the step in block style, one key a line",
			},
		},
		{
			"an installer's version in a flow with:",
			textLines(
				"jobs:",
				"  resign:",
				"    steps:",
				"      [",
				"        {",
				"          uses: sigstore/cosign-installer@0123456789abcdef0123456789abcdef01234567,",
				"          with:",
				"            {",
				"              cosign-release: latest",
				"            }",
				"        }",
				"      ]",
			),
			[]string{
				`w.yml:4: "[": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:5: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:8: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:10: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:11: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				`w.yml:12: "]": not a block key, a one-line item or a comment; write it in block style, one key a line`,
				"w.yml:6: uses: not read as a step's or a job's; write the step in block style, one key a line",
				"w.yml:9: cosign-release: not read as a step's with: or an env: key; write it in block style, one key a line",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := parseWorkflow("w.yml", tc.text)
			if !slices.Equal(w.refused, tc.want) {
				t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(w.refused, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// A uses: is read only as a step's or a job's, and a version only as a
// step's or a reusable call's with: key or an env: key, so either one
// anywhere else on a line that is not a comment is refused. A reusable
// call's with: is its step's, in either order. A key line outside every
// job's steps and with:, a matrix included, holds no installer's version,
// and is read as any key is.
func TestWorkflowReaderHoldsEveryUsesAndVersionToWhatItRecorded(t *testing.T) {
	w := parseWorkflow("w.yml", textLines(
		"on:",
		"  release:",
		"    types: [published]",
		"  workflow_dispatch:",
		"    inputs:",
		"      version:",
		`        description: "the version: a tag"`,
		"jobs:",
		"  build:",
		"    outputs:",
		"      version: ${{ steps.v.outputs.version }}",
		"    strategy:",
		"      matrix:",
		"        go-version: [stable]",
		"    steps:",
		"      # - uses: a/b@main # cosign-release: latest",
		"      - id: v",
		"        run: make version",
		"  call:",
		"    uses: ./.github/workflows/x.yml",
		"    with:",
		"      version: ${{ github.ref_name }}",
		"  sign:",
		"    with:",
		"      cosign-release: latest",
		"    uses: o/r/.github/workflows/sign.yml@0123456789abcdef0123456789abcdef01234567 # v1.0.0",
		"  stray:",
		"    with:",
		"      cosign-release: latest",
	))
	want := []string{
		"w.yml:29: cosign-release: not read as a step's with: or an env: key; write it in block style, one key a line",
	}
	if !slices.Equal(w.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(w.refused, "\n  "), strings.Join(want, "\n  "))
	}
	got := make([]string, 0, len(w.steps))
	for _, s := range w.steps {
		got = append(got, fmt.Sprintf("%s:%d %s with=%v", s.job, s.line, s.uses, s.with))
	}
	if want := []string{
		"call:20 ./.github/workflows/x.yml with=map[version:{version ${{ github.ref_name }} 22}]",
		"sign:26 o/r/.github/workflows/sign.yml@0123456789abcdef0123456789abcdef01234567 with=map[cosign-release:{cosign-release latest 25}]",
	}; !slices.Equal(got, want) {
		t.Errorf("steps:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A key is read only where one can stand: at a line's start, after its
// indentation and any sequence dash, after a { or [, and after a , inside
// a flow collection, outside a quoted scalar. A value's text, a quoted
// scalar, a block scalar's body and a comment hold no key, and a key a
// flow collection writes is one however it is written.
func TestWorkflowReaderTakesAKeyOnlyWhereOneCanStand(t *testing.T) {
	steps := func(lines ...string) string {
		return textLines(append([]string{"jobs:", "  j:", "    steps:"}, lines...)...)
	}
	// Each flow row's line 6 sits in a flow mapping in a flow sequence of
	// steps, whose bracket lines are refused as well.
	flow := func(line string) string {
		return steps("      [", "        {", "          "+line, "        }", "      ]")
	}
	refusedIn := func(line6 ...string) []string {
		return append([]string{
			`w.yml:4: "[": not a block key, a one-line item or a comment; write it in block style, one key a line`,
			`w.yml:5: "{": not a block key, a one-line item or a comment; write it in block style, one key a line`,
			`w.yml:7: "}": not a block key, a one-line item or a comment; write it in block style, one key a line`,
			`w.yml:8: "]": not a block key, a one-line item or a comment; write it in block style, one key a line`,
		}, line6...)
	}
	const (
		uses   = "w.yml:6: uses: not read as a step's or a job's; write the step in block style, one key a line"
		cosign = "w.yml:6: cosign-release: not read as a step's with: or an env: key; write it in block style, one key a line"
	)
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"a version: in a run line's text", steps(`      - run: echo "go version: $(go version)"`), nil},
		{"a uses: in a quoted name", steps(`      - name: "Check uses: in workflows"`, "        run: make check"), nil},
		{"a uses: after a comma in a quoted value", steps(`      - name: 'lint, uses: none'`, "        run: make lint"), nil},
		{"a version: after a comma in a comment", steps("      - run: make check # lint, version: pinned"), nil},
		{"a version: inside a block scalar's line", steps("      - run: |", `          echo "version: 1"`), nil},
		{"a heredoc's keys in a run block", steps("      - run: |", "          cat > w.yml <<EOF", `          version: "2"`,
			"          uses: a/b@main", "          EOF"), nil},
		{"a description's key in a block scalar", textLines("on:", "  workflow_dispatch:", "    inputs:", "      release:",
			"        description: |", "          release: the tag to cut", "        required: true"), nil},
		{"a uses: after a comma in a plain value", steps("      - run: echo a,uses:b"), nil},
		{"a version: after a comma in an expression", steps("      - if: contains(github.ref, 'version:1')", "        run: make check"), nil},
		{"a version: after a comma once a flow on the line has closed", steps("      - run: echo [a] b, version:1"), nil},
		{"a quoted scalar holding a uses: and a release:", textLines("on:", "  workflow_dispatch:", "    inputs:", "      channel:",
			"        type: choice", "        options:", `          - "uses: a"`, "          - 'release: v1'"), nil},
		{"a quoted bar, which is no block scalar", steps(`      - run: "|"`, "          uses: evil-org/evil-action@main"),
			[]string{"w.yml:5: uses: not read as a step's or a job's; write the step in block style, one key a line"}},
		{"a version after a comma in a flow mapping on its line", steps("      - name: x", "        timeout: {a: b, cosign-release: latest}"),
			[]string{"w.yml:5: cosign-release: not read as a step's with: or an env: key; write it in block style, one key a line"}},
		{"a version in upper case", flow("name: x, with: [COSIGN-RELEASE: latest]"),
			refusedIn("w.yml:6: COSIGN-RELEASE: not read as a step's with: or an env: key; write it in block style, one key a line")},
		{"a uses: in an item outside steps", textLines("jobs:", "  j:", "    pre-steps:", "      - uses: evil-org/evil-action@main"),
			[]string{"w.yml:4: uses: not read as a step's or a job's; write the step in block style, one key a line"}},
		{"a uses: after a comma", flow("name: x, uses: evil-org/evil-action@main"), refusedIn(uses)},
		{"a version after a bracket", flow("name: x, with: [cosign-release: latest]"), refusedIn(cosign)},
		{"a quoted version after a brace", flow(`name: x, with: {"cosign-release": latest}`), refusedIn(cosign)},
		{"a uses: after a quoted value", flow(`name: "x, y", uses: evil-org/evil-action@main`), refusedIn(uses)},
		{"a uses: after an apostrophe in a plain value", flow("name: it's, uses: evil-org/evil-action@main"), refusedIn(uses)},
		{"a uses: after an explicit key's ?, and a version after an anchor",
			flow("name: x, ? uses : evil-org/evil-action@main, &v cosign-release: latest"), refusedIn(uses, cosign)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := parseWorkflow("w.yml", tc.text)
			if !slices.Equal(w.refused, tc.want) {
				t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(w.refused, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// The runner passes an input as INPUT_ and its name in upper case, a space
// read as an underscore, so two with: keys of one step that differ only so
// set one input twice, and the later one is refused. The step keeps the
// first, and a reusable call's with: is held the same way.
func TestWorkflowReaderRefusesAnInputSetTwice(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	w := parseWorkflow("w.yml", textLines(
		"jobs:",
		"  build:",
		"    steps:",
		"      - uses: sigstore/cosign-installer@"+sha+" # v4.1.2",
		"        with:",
		"          cosign-release: v3.1.3",
		"          COSIGN-RELEASE: latest",
		"      - uses: a/b@"+sha+" # v1.0.0",
		"        with:",
		"          tool version: v1.0.0",
		"          TOOL_VERSION: latest",
		"          tool-version: v2.0.0",
		"  call:",
		"    uses: o/r/.github/workflows/x.yml@"+sha+" # v1.0.0",
		"    with:",
		"      release-version: v1.0.0",
		"      Release-Version: latest",
	))
	want := []string{
		"w.yml:7: COSIGN-RELEASE: names the input cosign-release names, read ignoring case and a space as an underscore; set it once",
		"w.yml:11: TOOL_VERSION: names the input tool version names, read ignoring case and a space as an underscore; set it once",
		"w.yml:17: Release-Version: names the input release-version names, read ignoring case and a space as an underscore; set it once",
	}
	if !slices.Equal(w.refused, want) {
		t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(w.refused, "\n  "), strings.Join(want, "\n  "))
	}
	got := make([]string, 0, len(w.steps))
	for _, s := range w.steps {
		got = append(got, fmt.Sprintf("%s:%d with=%v", s.job, s.line, s.with))
	}
	if want := []string{
		"build:4 with=map[cosign-release:{cosign-release v3.1.3 6}]",
		"build:8 with=map[tool version:{tool version v1.0.0 10} tool-version:{tool-version v2.0.0 12}]",
		"call:14 with=map[release-version:{release-version v1.0.0 16}]",
	}; !slices.Equal(got, want) {
		t.Errorf("steps:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Every workflow here reads with nothing refused, and its steps are read.
func TestWorkflowReaderReadsEveryWorkflowHere(t *testing.T) {
	t.Chdir(parityRepoRoot)
	flows, err := readWorkflows()
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, w := range flows {
		if w.refused != nil {
			t.Errorf("%s refused:\n  %s", w.path, strings.Join(w.refused, "\n  "))
		}
		steps += len(w.steps)
	}
	if len(flows) < 5 || steps < 20 {
		t.Errorf("read %d workflows and %d steps, under the floors of 5 and 20", len(flows), steps)
	}
}

// A comment starts at a # after white space, a tab included, and a # inside
// a word or a quoted value is text (https://yaml.org/spec/1.2.2/#66-comments).
func TestWorkflowValueCutsTheComment(t *testing.T) {
	for _, tc := range []struct{ in, value, comment string }{
		{"a/b@c # v1", "a/b@c", "v1"},
		{"a/b@c\t# v1", "a/b@c", "v1"},
		{"a#b", "a#b", ""},
		{"# v1", "", "v1"},
		{`'a # b' # c`, "a # b", "c"},
		{"\"a\"\t# c", "a", "c"},
		{"", "", ""},
	} {
		if value, comment := workflowValue(tc.in); value != tc.value || comment != tc.comment {
			t.Errorf("workflowValue(%q) = %q, %q, want %q, %q", tc.in, value, comment, tc.value, tc.comment)
		}
	}
}

// Every workflow under a root is read, .yml and .yaml alike, sorted, each
// by its slash-separated path from that root, as a message names it.
func TestWorkflowsUnderReadsEachByItsPathFromTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"b.yml", "a.yaml", "notes.md"} {
		put(t, root, workflowDir+"/"+name, "on: push\n")
	}
	flows, err := workflowsUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(flows))
	for i := range flows {
		got = append(got, flows[i].path)
	}
	if want := []string{".github/workflows/a.yaml", ".github/workflows/b.yml"}; !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

// YAML breaks a line at a CR with no LF after it, and YAML 1.1 at NEL, LS
// and PS too, where the readers break at LF alone: a step written after
// one would be read as the rest of the line before, such as a comment.
// make breaks at LF alone, and drops only the CR before one.
func TestReadersRefuseALineBreakTheyDoNotRead(t *testing.T) {
	workflow := func(s string) []string { return parseWorkflow("w.yml", s).refused }
	goreleaser := func(s string) []string { return parseGoreleaser(s).refused }
	mk := func(s string) []string { return parseMakefile(s).refused }
	const yaml = " of the line, which YAML reads as a line break and this reader does not; break lines with LF or CRLF alone"
	const makeWhy = " of the line, which make keeps in the line and this reader may drop; break lines with LF or CRLF alone"
	for _, tc := range []struct {
		name string
		read func(string) []string
		text string
		want []string
	}{
		{"a workflow's CR", workflow, "name: x\n# hidden\r      - uses: evil/x@main\n", []string{"w.yml:2: a CR with no LF after it at byte 9" + yaml}},
		{"a workflow's NEL", workflow, "name: x\n# hidden\u0085jobs:\n", []string{"w.yml:2: U+0085 (NEXT LINE) at byte 9" + yaml}},
		{"a workflow's LS", workflow, "name: x\n# hidden jobs:\n", []string{"w.yml:2: U+2028 (LINE SEPARATOR) at byte 9" + yaml}},
		{"a workflow's PS", workflow, "name: x\n# hidden jobs:\n", []string{"w.yml:2: U+2029 (PARAGRAPH SEPARATOR) at byte 9" + yaml}},
		{"a workflow's CRLF, a break after it counted on its line", workflow, "name: x\r\n# hidden jobs:\r\n",
			[]string{"w.yml:2: U+2028 (LINE SEPARATOR) at byte 9" + yaml}},
		{"a workflow in CRLF", workflow, "name: x\r\n# c\r\njobs:\r\n", nil},
		{"the goreleaser config's CR", goreleaser, "version: 2\n# hidden\rbefore:\n", []string{".goreleaser.yaml:2: a CR with no LF after it at byte 9" + yaml}},
		{"the goreleaser config's NEL", goreleaser, "version: 2\n# hidden\u0085before:\n", []string{".goreleaser.yaml:2: U+0085 (NEXT LINE) at byte 9" + yaml}},
		{"the goreleaser config's LS", goreleaser, "version: 2\n# hidden before:\n", []string{".goreleaser.yaml:2: U+2028 (LINE SEPARATOR) at byte 9" + yaml}},
		{"the goreleaser config's PS", goreleaser, "version: 2\n# hidden before:\n", []string{".goreleaser.yaml:2: U+2029 (PARAGRAPH SEPARATOR) at byte 9" + yaml}},
		{"the goreleaser config in CRLF", goreleaser, "version: 2\r\n# c\r\nbefore:\r\n", nil},
		// make keeps a CR before a CR, so this line ends in no backslash
		// for make, and the reader would join the next line to it.
		{"the Makefile's CR", mk, "GO ?= go\n# hidden \\\r\r\nx:\n", []string{"Makefile:2: a CR with no LF after it at byte 11" + makeWhy}},
		{"the Makefile's NEL, LS and PS, which make reads as text", mk, "GO ?= go\n# a\u0085b c d\nx:\n", nil},
		{"the Makefile in CRLF", mk, "GO ?= go\r\n# c\r\nx:\r\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.read(tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("refused:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}
