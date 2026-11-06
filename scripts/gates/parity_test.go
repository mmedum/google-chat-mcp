package main

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"
)

// The gate itself, run against this repository from its root, as `make
// parity` runs it.
func TestParityGate(t *testing.T) {
	t.Chdir(repoRoot(t))
	var out, errOut bytes.Buffer
	if code := run([]string{"parity"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "parity ok: ") {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", code, out.String(), errOut.String())
	}
}

// A Makefile and a workflow that agree, in this repository's shape: some
// prerequisites run a gate, some a pinned tool through `go run`, some a
// plain go command, and two depend on a target that builds or tests. CI
// runs the linter through an action, and `goreleaser check` through one no
// prerequisite has to match. Every case below is this pair with one thing
// moved, because each divergence the gate catches is one edit away from
// correct.
const (
	parityMakefile = `# Common dev tasks.
GO       ?= go
BIN      ?= ./google-chat-mcp
LDFLAGS   = -s -w

.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/google-chat-mcp

.PHONY: fmt
fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

.PHONY: vet
vet:
	$(GO) vet ./...
	$(GO) vet -tags=live ./...

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run

.PHONY: test
test:
	LEAKCHECK_HISTORY=1 $(GO) test -race -coverprofile=cov.out ./...

.PHONY: cover
cover: test
	@$(GO) run ./scripts/gates coverage cov.out

.PHONY: vuln
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

.PHONY: licenses
licenses:
	go run github.com/google/go-licenses/v2@v2.0.1 check ./... \
	  --allowed_licenses=Apache-2.0,MIT

.PHONY: leaks
leaks:
	@$(GO) test ./internal/leakcheck -run 'TestTheRepositoryIsClean' -count=1

.PHONY: pins
pins:
	@$(GO) run ./scripts/gates pins

.PHONY: docs
docs:
	@$(GO) run ./scripts/gates docs

.PHONY: parity
parity:
	@$(GO) run ./scripts/gates parity

.PHONY: smoke
smoke: build
	@$(GO) run ./scripts/gates smoke $(BIN)

.PHONY: live-surface
live-surface: build
	@$(GO) test -tags=live ./internal/livecheck -count=1 -skip '^TestLive$$'

.PHONY: schema-ack
schema-ack:
	@$(GO) run ./scripts/gates schema-ack $(PR_BASE_REF) $(PR_HEAD)

.PHONY: check
check: fmt vet lint cover vuln licenses leaks pins docs parity smoke live-surface ## Everything CI runs
`
	parityCI = `name: ci
defaults:
  run:
    shell: bash
jobs:
  test:
    steps:
      - uses: actions/checkout@` + zeroSHA + `  # v7.0.1
      - run: go test -race -coverpkg=./cmd/... -coverprofile=cov.out ./...
      - name: coverage floor
        run: go run ./scripts/gates coverage cov.out
  lint:
    steps:
      - name: gofmt
        run: out=$(gofmt -l .); [ -z "$out" ] || { echo "$out"; exit 1; }
      - run: go vet ./...
      - run: go vet -tags=live ./...
      - uses: golangci/golangci-lint-action@` + zeroSHA + `  # v9.3.0
        with:
          version: v2.14.0
      - uses: goreleaser/goreleaser-action@` + zeroSHA + `  # v7.3.0
        with:
          version: v2.18.1
          args: check
  security:
    steps:
      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
      - name: licenses
        run: >
          go run github.com/google/go-licenses/v2@v2.0.1 check ./...
          --allowed_licenses=Apache-2.0,MIT
      - run: go test ./internal/leakcheck -run 'TestTheRepositoryIsClean' -count=1
  pr:
    steps:
      - run: go run ./scripts/gates schema-ack "$PR_BASE_REF" "$PR_HEAD"
  binary:
    steps:
      - run: CGO_ENABLED=0 go build -trimpath -o google-chat-mcp ./cmd/google-chat-mcp
      - run: go run ./scripts/gates pins
      - run: go run ./scripts/gates docs
      - run: go run ./scripts/gates parity
      - run: go run ./scripts/gates smoke ./google-chat-mcp
      - run: >
          go test -tags=live ./internal/livecheck -count=1
          -skip '^TestLive$'
`
	parityRelease = `jobs:
  goreleaser:
    steps:
      - run: go run ./scripts/gates release-notes "${REF_NAME}" > "${RUNNER_TEMP}/notes.md"
`
)

// parityRegistry declares the fixture's check gates, its pull-request
// gate, its release step and a command typed by hand.
var parityRegistry = map[string]command{
	"coverage": {runsIn: inCheck}, "docs": {runsIn: inCheck}, "parity": {runsIn: inCheck},
	"pins": {runsIn: inCheck}, "smoke": {runsIn: inCheck},
	"schema-ack":    {runsIn: inPR},
	"release-notes": {runsIn: inRelease},
	"scopes":        {runsIn: manual},
}

var parityTags = []string{"live"}

func parityFixture() parityFiles {
	return parityFiles{makefile: parityMakefile, ci: parityCI, release: parityRelease}
}

// parityProblems runs the rule, which must reach a verdict.
func parityProblems(t *testing.T, files parityFiles, registry map[string]command, tags []string) []string {
	t.Helper()
	problems, _, err := parityCheck(files, registry, tags)
	if err != nil {
		t.Fatalf("parityCheck: %v", err)
	}
	return problems
}

func TestParityAcceptsAMatchedPair(t *testing.T) {
	var out, errOut bytes.Buffer
	code := reportParity(parityFixture(), parityRegistry, parityTags, &out, &errOut)
	const want = "parity ok: 5 gates in both, 1 pull-request gates in ci.yml only, 1 release steps in the release files, " +
		"15 commands of make check in ci.yml, a vet pass for -tags=live in both\n"
	if code != 0 || out.String() != want || errOut.Len() != 0 {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", code, out.String(), errOut.String(), want)
	}
}

// Both directions, and the ways a step can be present in the text and
// not run.
func TestParityCatchesDivergence(t *testing.T) {
	const (
		pinsStep = "      - run: go run ./scripts/gates pins\n"
		notInCI  = " and nothing in ci.yml does; `make check` claims to be what CI runs"
	)
	cases := []struct {
		name     string
		makefile [2]string
		ci       [2]string
		want     []string
	}{
		{
			name: "CI stopped running a gate",
			ci:   [2]string{pinsStep, ""},
			want: []string{`gate "pins" is declared in scripts/gates but no step in ci.yml runs it`},
		},
		{
			name: "the step is commented out",
			ci:   [2]string{pinsStep, "      # - run: go run ./scripts/gates pins\n"},
			want: []string{`gate "pins" is declared in scripts/gates but no step in ci.yml runs it`},
		},
		{
			name: "the gate is named but nothing runs it",
			ci:   [2]string{pinsStep, "      - name: go run ./scripts/gates pins\n"},
			want: []string{`gate "pins" is declared in scripts/gates but no step in ci.yml runs it`},
		},
		{
			// A false alarm here would get the gate edited out.
			name: "a run: block with the gate inside it",
			ci:   [2]string{pinsStep, "      - run: |\n          go run ./scripts/gates pins\n"},
		},
		{
			name: "the block ended before the gate was named",
			ci:   [2]string{pinsStep, "      - run: |\n          echo building\n      - name: go run ./scripts/gates pins\n"},
			want: []string{`gate "pins" is declared in scripts/gates but no step in ci.yml runs it`},
		},
		{
			name:     "`make check` stopped depending on a gate",
			makefile: [2]string{"leaks pins docs", "leaks docs"},
			want:     []string{`gate "pins" is declared in scripts/gates but no ` + "`make check`" + ` prerequisite runs it`},
		},
		{
			name:     "a gate's recipe line is commented out",
			makefile: [2]string{"\t@$(GO) run ./scripts/gates parity", "\t# @$(GO) run ./scripts/gates parity"},
			want: []string{
				"`make check` depends on \"parity\" and the Makefile gives it no recipe; it checks nothing",
				`gate "parity" is declared in scripts/gates but no ` + "`make check`" + ` prerequisite runs it`,
			},
		},
		{
			// The hole a hand list leaves: a prerequisite on no list.
			name:     "a new prerequisite CI does not run",
			makefile: [2]string{"live-surface ##", "live-surface tidy ##\n\n.PHONY: tidy\ntidy:\n\t$(GO) mod tidy -diff\n#"},
			want:     []string{"`make tidy` runs \"go mod tidy\"" + notInCI},
		},
		{
			// CI runs schema-ack, which is not the gate named here.
			name:     "a gate whose name only starts one CI runs",
			makefile: [2]string{"live-surface ##", "live-surface ack ##\n\nack:\n\t@$(GO) run ./scripts/gates schema\n#"},
			want:     []string{"`make ack` runs \"./scripts/gates schema\"" + notInCI},
		},
		{
			name:     "a prerequisite with no recipe",
			makefile: [2]string{"live-surface ##", "live-surface placeholder ##\n\nplaceholder:\n#"},
			want:     []string{"`make check` depends on \"placeholder\" and the Makefile gives it no recipe; it checks nothing"},
		},
		{
			name: "CI stopped running a pinned tool",
			ci:   [2]string{"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...\n", ""},
			want: []string{"`make vuln` runs \"govulncheck\"" + notInCI},
		},
		{
			name: "CI runs a tool at a version that make check does not",
			ci: [2]string{"      - run: go test ./internal/leakcheck", "      - run: go run github.com/zricethezav/gitleaks/v8@v8.30.1 detect\n" +
				"      # - run: go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n      - run: go test ./internal/leakcheck"},
			want: []string{".github/workflows/ci.yml:32 runs gitleaks and no `make check` prerequisite runs it; " +
				"a contributor cannot reproduce it"},
		},
		{
			name: "the action is named only in a comment",
			ci:   [2]string{"      - uses: golangci/golangci-lint-action@", "      # - uses: golangci/golangci-lint-action@"},
			want: []string{"`make lint` runs \"golangci-lint\"" + notInCI},
		},
		{
			// CI's linter is pinned and the PATH's is not.
			name:     "the linter run from the PATH",
			makefile: [2]string{"\tgo run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run", "\tgolangci-lint run"},
			want: []string{
				"`make lint` runs \"golangci-lint run\"" + notInCI,
				"ci.yml runs golangci-lint through golangci/golangci-lint-action and no `make check` prerequisite " +
					"runs it; a contributor cannot reproduce it",
			},
		},
		{
			// cover runs the test target first, and its flags are compared.
			name: "a prerequisite's own prerequisite drifts",
			ci:   [2]string{"go test -race -coverpkg", "go test -coverpkg"},
			want: []string{"`make test` runs \"go test -race\"" + notInCI},
		},
		{
			name: "a tagged vet pass runs only locally",
			ci:   [2]string{"      - run: go vet -tags=live ./...\n", ""},
			want: []string{
				"`make vet` runs \"go vet -tags=live\"" + notInCI,
				"a file builds only with -tags=live and ci.yml vets no such pass; it compiles only on a maintainer's machine",
			},
		},
		{
			name:     "a tagged vet pass runs only in CI",
			makefile: [2]string{"\t$(GO) vet -tags=live ./...\n", ""},
			want:     []string{"a file builds only with -tags=live and `make check` vets no such pass; a contributor cannot see it break"},
		},
		{
			// CI's commands include the recipe of a target it runs.
			name: "CI vets through make",
			ci:   [2]string{"      - run: go vet ./...\n      - run: go vet -tags=live ./...\n", "      - run: make vet\n"},
		},
		{
			name:     "`make check` stopped running what CI runs through make",
			makefile: [2]string{"cover vuln licenses", "cover licenses"},
			ci:       [2]string{"      - run: go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...\n", "      - run: make vuln\n"},
			want:     []string{"ci.yml runs `make vuln` and `make check` does not depend on it; a contributor cannot reproduce it"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := parityFixture()
			files.makefile = strings.Replace(files.makefile, c.makefile[0], c.makefile[1], 1)
			files.ci = strings.Replace(files.ci, c.ci[0], c.ci[1], 1)
			if files == parityFixture() {
				t.Fatal("the case changed nothing; its replacement did not match")
			}
			if got := parityProblems(t, files, parityRegistry, parityTags); !slices.Equal(got, c.want) {
				t.Errorf("problems\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// Each tag the tree requires needs a vet pass on both sides, and a tag no
// file requires needs none.
func TestParityRequiresAVetPassPerBuildTag(t *testing.T) {
	got := parityProblems(t, parityFixture(), parityRegistry, []string{"evals", "live"})
	want := []string{
		"a file builds only with -tags=evals and `make check` vets no such pass; a contributor cannot see it break",
		"a file builds only with -tags=evals and ci.yml vets no such pass; it compiles only on a maintainer's machine",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems\n got %q\nwant %q", got, want)
	}
}

// A pull-request gate runs in ci.yml and stays out of `make check`,
// which has no merge-base to measure from, and a release step runs in a
// release file.
func TestParityHoldsTheOtherPipelines(t *testing.T) {
	const (
		ack    = `      - run: go run ./scripts/gates schema-ack "$PR_BASE_REF" "$PR_HEAD"` + "\n"
		notRun = `release step "release-notes" is declared but no release file runs it; a step dropped from them fails at a tag, in public`
		hook   = "  hooks:\n    post: go run ./scripts/gates release-notes v1\n"
	)
	// schema is a pull-request gate whose name starts schema-ack's.
	prefixed := maps.Clone(parityRegistry)
	prefixed["schema"] = command{runsIn: inPR}
	for _, tc := range []struct {
		name     string
		edit     func(f *parityFiles)
		registry map[string]command
		want     []string
	}{
		{
			name: "missing from ci.yml",
			edit: func(f *parityFiles) { f.ci = strings.Replace(f.ci, ack, "", 1) },
			want: []string{`pull-request gate "schema-ack" is declared but no step in ci.yml runs it`},
		},
		{
			name: "commented out",
			edit: func(f *parityFiles) { f.ci = strings.Replace(f.ci, ack, "      # "+strings.TrimLeft(ack, " "), 1) },
			want: []string{`pull-request gate "schema-ack" is declared but no step in ci.yml runs it`},
		},
		{
			name: "in make check",
			edit: func(f *parityFiles) {
				f.makefile = strings.Replace(f.makefile, "check: fmt", "check: schema-ack fmt", 1)
			},
			want: []string{`pull-request gate "schema-ack" is in ` + "`make check`, where there is no merge-base"},
		},
		{
			name:     "named only as the start of another gate",
			registry: prefixed,
			want:     []string{`pull-request gate "schema" is declared but no step in ci.yml runs it`},
		},
		{
			name: "a release step no release file runs",
			edit: func(f *parityFiles) {
				f.release, f.publish = "", "      - run: go run ./scripts/gates server-json v1 checksums.txt\n"
			},
			want: []string{notRun},
		},
		{
			name: "a release step named only by a step's name",
			edit: func(f *parityFiles) { f.release = strings.Replace(f.release, "- run: go run", "- name: go run", 1) },
			want: []string{notRun},
		},
		{
			name: "a release step in the publishing workflow",
			edit: func(f *parityFiles) { f.release, f.publish = "", f.release },
		},
		{
			name: "a release step in a goreleaser hook, which is not a run: step",
			edit: func(f *parityFiles) { f.release, f.goreleaser = "", hook },
		},
		{
			name: "a release step in a commented-out goreleaser hook",
			edit: func(f *parityFiles) { f.release, f.goreleaser = "", strings.Replace(hook, "post:", "# post:", 1) },
			want: []string{notRun},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, registry := parityFixture(), parityRegistry
			if tc.edit != nil {
				tc.edit(&files)
			}
			if tc.registry != nil {
				registry = tc.registry
			}
			if got := parityProblems(t, files, registry, parityTags); !slices.Equal(got, tc.want) {
				t.Errorf("problems\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// A gate that finds too little to be looking at anything says so, and
// exits 2 rather than pass.
func TestParityRefusesToRunOnTooLittle(t *testing.T) {
	cases := []struct {
		name     string
		makefile string
		registry map[string]command
		tags     []string
		want     string
	}{
		{
			name:     "the registry is not being read",
			registry: map[string]command{"pins": {runsIn: inCheck}, "schema-ack": {runsIn: inPR}, "release-notes": {runsIn: inRelease}},
			want:     "only 1 gates declared; the registry is not being read",
		},
		{
			name: "no pull-request gate is declared",
			registry: map[string]command{
				"coverage": {runsIn: inCheck}, "docs": {runsIn: inCheck}, "parity": {runsIn: inCheck},
				"pins": {runsIn: inCheck}, "smoke": {runsIn: inCheck}, "release-notes": {runsIn: inRelease},
			},
			want: "no pull-request gate is declared; schema-ack belongs there, and the registry is not being read",
		},
		{
			name: "no release step is declared",
			registry: map[string]command{
				"coverage": {runsIn: inCheck}, "docs": {runsIn: inCheck}, "parity": {runsIn: inCheck},
				"pins": {runsIn: inCheck}, "smoke": {runsIn: inCheck}, "schema-ack": {runsIn: inPR},
			},
			want: "no release step is declared; release-notes belongs there, and the registry is not being read",
		},
		{
			name:     "no check target",
			makefile: "build:\n\tgo build ./...\n",
			want:     "no `check:` target in the Makefile; the check is reading the wrong file",
		},
		{
			name:     "the check target is a stub",
			makefile: "check: fmt vet lint cover ## four\n",
			want:     "the check target has 4 prerequisites; that is not the whole list",
		},
		{
			// Every recipe unreadable: no comparison ran, so none passed.
			name:     "no prerequisite has a recipe",
			makefile: "check: a b c d e f g h\n",
			want:     "only 0 recipe lines matched; the Makefile's shape has changed and this check is reading almost nothing",
		},
		{
			name: "no build tag was read",
			tags: []string{},
			want: "no //go:build line requires a tag; the tree is not being read",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := parityFixture()
			if c.makefile != "" {
				files.makefile = c.makefile
			}
			registry, tags := c.registry, c.tags
			if registry == nil {
				registry = parityRegistry
			}
			if tags == nil {
				tags = parityTags
			}
			var out, errOut bytes.Buffer
			code := reportParity(files, registry, tags, &out, &errOut)
			if got := gatesProblems(t, errOut.String()); code != 2 || out.Len() != 0 || !slices.Equal(got, []string{c.want}) {
				t.Errorf("exit %d, stdout %q, problems %q; want 2, nothing and %q", code, out.String(), got, c.want)
			}
		})
	}
}

// The tags come from the //go:build lines of what git would commit: a tag
// a file builds only with, outside testdata, less an OS, an architecture
// and the tags the go command sets.
func TestTreeBuildTags(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initRepo(t)
	for rel, body := range map[string]string{
		"a/a.go":           "//go:build integration\n\npackage a\n",
		"b/b.go":           "//go:build e2e || integration\n\npackage b\n",
		"c/c.go":           "//go:build !live\n\npackage c\n",
		"d/d.go":           "// A comment first.\n\n//go:build smoke\n\npackage d\n",
		"e/e.go":           "package e\n\n//go:build late\n",
		"f/f_windows.go":   "//go:build windows\n\npackage f\n",
		"g/g.go":           "//go:build linux || arm64 || unix || cgo && !gccgo\n\npackage g\n",
		"h/h.go":           "//go:build amd64.v3 || go1.22 || gc || ignore\n\npackage h\n",
		"j/j.go":           "//go:build e2e || wasip1\n\npackage j\n",
		"k/k.go":           "//go:build live || !race\n\npackage k\n",
		"m/m.go":           "//go:build e2e && !experimental\n\npackage m\n",
		"a/testdata/x.go":  "//go:build fixture\n\npackage x\n",
		"ignored/i.go":     "//go:build ignored\n\npackage i\n",
		".gitignore":       "ignored/\n",
		"notes/README.txt": "//go:build text\n",
	} {
		put(t, dir, rel, body)
	}
	files, err := checkoutFiles()
	if err != nil {
		t.Fatal(err)
	}
	got, err := treeBuildTags(files)
	if err != nil {
		t.Fatalf("treeBuildTags: %v", err)
	}
	if want := []string{"e2e", "integration", "smoke"}; !slices.Equal(got, want) {
		t.Errorf("treeBuildTags = %q, want %q", got, want)
	}
}

// signature is what lets the two files spell the same job differently.
func TestSignatureNamesTheJob(t *testing.T) {
	for _, tc := range []struct{ line, sig, gate string }{
		{"go run ./scripts/gates docs", "./scripts/gates docs", "docs"},
		{"go run ./scripts/gates coverage cov.out", "./scripts/gates coverage", "coverage"},
		{"go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run", "golangci-lint", ""},
		{"go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...", "govulncheck", ""},
		{"go run github.com/google/go-licenses/v2@v2.0.1 check ./...", "go-licenses", ""},
		{"go run github.com/zricethezav/gitleaks/v8@v8.30.1 detect", "gitleaks", ""},
		{"go vet -tags=live ./...", "go vet -tags=live", ""},
		{"LEAKCHECK_HISTORY=1 go test -race -coverprofile=cov.out ./...", "go test -race", ""},
		{"CGO_ENABLED=0 GOOS=linux go build -trimpath -o x", "go build -trimpath", ""},
		{`out=$(gofmt -l .); if [ -n "$out" ]; then exit 1; fi`, "out=$(gofmt -l .);", ""},
		{"go build", "go build", ""},
	} {
		if sig, gate := signature(tc.line); sig != tc.sig || gate != tc.gate {
			t.Errorf("signature(%q) = %q, %q; want %q, %q", tc.line, sig, gate, tc.sig, tc.gate)
		}
	}
}

// A rule line is what make reads as one: every target before the colon
// gets the rule, an assignment is not a rule, the help text is no
// prerequisite, and a target's first rule is the one kept.
func TestParseMakefileReadsRuleLines(t *testing.T) {
	rules := parseMakefile("check:= fmt vet\n" +
		"a b: c d ## both build\n\techo both\n" +
		"a: e\n\techo again\n" +
		"COVER ?= 80\n")
	want := map[string]makeTarget{
		"a": {prereqs: []string{"c", "d"}, recipe: []string{"echo both"}},
		"b": {prereqs: []string{"c", "d"}, recipe: []string{"echo both"}},
	}
	if !maps.EqualFunc(rules, want, func(got, want makeTarget) bool {
		return slices.Equal(got.prereqs, want.prereqs) && slices.Equal(got.recipe, want.recipe)
	}) {
		t.Errorf("parseMakefile = %+v, want %+v", rules, want)
	}
}

// A recipe is read as make runs it: variables expanded, $$ handed on as
// one $, a continued line joined, the echo and error markers dropped, and
// blank lines skipped.
func TestParseMakefileReadsRecipes(t *testing.T) {
	rules := parseMakefile(uncommented(parityMakefile))
	for _, tc := range []struct {
		target string
		want   []string
	}{
		{"licenses", []string{"go run github.com/google/go-licenses/v2@v2.0.1 check ./... --allowed_licenses=Apache-2.0,MIT"}},
		{"vet", []string{"go vet ./...", "go vet -tags=live ./..."}},
		{"fmt", []string{`out=$(gofmt -l .); if [ -n "$out" ]; then echo "$out"; exit 1; fi`}},
		{"live-surface", []string{"go test -tags=live ./internal/livecheck -count=1 -skip '^TestLive$'"}},
		{"build", []string{`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ./google-chat-mcp ./cmd/google-chat-mcp`}},
		{"cover", []string{"go run ./scripts/gates coverage cov.out"}},
		{"nosuchtarget", nil},
	} {
		if got := rules[tc.target].recipe; !slices.Equal(got, tc.want) {
			t.Errorf("recipe of %q = %q, want %q", tc.target, got, tc.want)
		}
	}
	if got := expand("echo $$(GO) $(GO)", map[string]string{"GO": "go"}); got != "echo $(GO) go" {
		t.Errorf("expand = %q, want the escaped reference handed on and the other expanded", got)
	}
}

// The parity gate reads ci.yml by its path, and the tree's tags from the
// checkout: with ci.yml's tagged vet passes removed, this repository
// fails.
func TestParityReadsThisRepository(t *testing.T) {
	t.Chdir(repoRoot(t))
	files, tags, err := readParityFiles()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"evals", "live"}; !slices.Equal(tags, want) {
		t.Errorf("this repository's build tags = %q, want %q", tags, want)
	}
	without := strings.ReplaceAll(files.ci, "vet -tags=", "vet -tagz=")
	if without == files.ci {
		t.Fatal("ci.yml runs no tagged vet pass, so this test removes nothing")
	}
	files.ci = without
	got := parityProblems(t, files, commands, tags)
	const want = "a file builds only with -tags=live and ci.yml vets no such pass; it compiles only on a maintainer's machine"
	if !slices.Contains(got, want) {
		t.Errorf("problems = %q, want %q among them", got, want)
	}
}

func TestExecutesReadsOnlyWhatRuns(t *testing.T) {
	const workflow = `name: ci
jobs:
  test:
    steps:
      - uses: actions/checkout@v7
      - name: smoke
        run: go run ./scripts/gates smoke ./google-chat-mcp
      # - name: staleness
      #   run: go run ./scripts/gates staleness ./google-chat-mcp
      - name: licenses
        run: >
          go run github.com/google/go-licenses/v2 check ./...
          --allowed_licenses=MIT
      - name: several
        run: |
          go vet -tags=evals ./...
          # go vet -tags=live ./...
      - name: after the block
        run: echo done
defaults:
  run:
    shell: bash
`
	got := executes(workflow)
	for _, want := range []string{
		"go run ./scripts/gates smoke ./google-chat-mcp",
		"go run github.com/google/go-licenses/v2 check ./...",
		"--allowed_licenses=MIT",
		"go vet -tags=evals ./...",
		"echo done",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("executes dropped something that runs: %q\n%s", want, got)
		}
	}
	// The two comments are the point of the whole helper.
	for _, unwanted := range []string{"gates staleness", "go vet -tags=live"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("executes kept a commented-out step: %q\n%s", unwanted, got)
		}
	}
	// A block ends where the indentation says it does.
	if strings.Contains(got, "name: after the block") {
		t.Errorf("the block form swallowed the step after it:\n%s", got)
	}
	// `defaults.run` is a mapping, not a command. Reading it as a block
	// puts `shell: bash` into the list of things that run.
	if strings.Contains(got, "shell: bash") {
		t.Errorf("a bare `run:` mapping was read as a block scalar:\n%s", got)
	}
}

func TestUncommentedDropsCommentLines(t *testing.T) {
	const makefile = "check: fmt smoke\n" +
		"smoke: build\n" +
		"\t@$(GO) run ./scripts/gates smoke $(BIN)\n" +
		"# @$(GO) run ./scripts/gates staleness $(BIN)\n" +
		"\t# a recipe comment reaches the shell and still runs nothing\n"
	got := uncommented(makefile)
	if !strings.Contains(got, "gates smoke") {
		t.Errorf("uncommented dropped a live recipe line:\n%s", got)
	}
	for _, unwanted := range []string{"gates staleness", "a recipe comment"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("uncommented kept %q:\n%s", unwanted, got)
		}
	}
}
