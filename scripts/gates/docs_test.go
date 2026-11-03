package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The fixture checkout's paths, named once because most rows edit them.
const (
	fxArch      = "docs/architecture.md"
	fxEvidence  = "docs/evidence.md"
	fxRunbook   = "docs/runbook.md"
	fxReadme    = "README.md"
	fxAgent     = "CLAUDE.md"
	fxChangelog = "CHANGELOG.md"
	fxClient    = "internal/gchat/client.go"
	fxAllowlist = "testdata/citation-allowlist.txt"
	fxGolangci  = ".golangci.yml"
	fxMakefile  = "Makefile"
)

// textLines is a file of these lines, so a fixture's line numbers can be
// read off its source: the nth argument is line n.
func textLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// The index rows, named because several rows edit them whole.
const (
	fxRow01 = "| D-01 | Retries back off | accepted | TestRetry, smoke |"
	fxRow02 = "| D-02 | Retry forever | superseded by D-03 | — |"
	fxRow03 = "| D-03 | Retry three times | accepted | forbidigo, lint, review |"
)

// docsFixture is a checkout the docs gate accepts as it stands. Between
// them its files hold one of everything the gate must stay quiet about:
// each kind of Held by item, a superseded decision cited only where that
// is allowed, a refutation nobody cites, a retired evidence ID, a
// repeated heading, an allowlisted bare section number, links to a file
// and a directory, and citations in a fence, a string literal, a testdata
// page and a released changelog section.
func docsFixture() map[string]string {
	return map[string]string{
		fxMakefile: textLines(
			"check: lint docs",
			"",
			".PHONY: lint",
			"lint:",
			"\tgolangci-lint run",
			"",
			".PHONY: docs",
			"docs:",
			"\t@go run ./scripts/gates docs",
		),
		fxGolangci: textLines(
			`version: "2"`,
			"linters:",
			"  default: none",
			"  enable:",
			"    - errcheck",
			"    - forbidigo # stdout carries only frames",
			"  settings:",
			"    govet:",
			"      enable:",
			"        - nilness",
			"    misspell:",
			"      locale: US",
			"formatters:",
			"  enable:",
			"    - gofmt",
		),
		".gitignore": textLines("internal/generated/"),
		"go.mod":     textLines("module example.com/fixture", "", "go 1.21"),
		fxClient: textLines(
			"package gchat",
			"",
			"// Retries back off (D-01), as measured in E-001.",
			"// A redirect is followed once, as RFC 9110 §15.4 allows.",
			`func Retry() string { return "D-99, E-999 and §4 in a string are data" }`,
		),
		"internal/gchat/client_test.go": textLines(
			"package gchat",
			"",
			`import "testing"`,
			"",
			"func TestRetry(t *testing.T) { _ = Retry() }",
		),
		// Ignored, so neither a package nor a file to parse.
		"internal/generated/broken.go":  "this is not Go\n",
		"internal/gchat/testdata/pg.md": textLines("A fixture page citing E-777 and §5, and `internal/gone`."),
		"cmd/server/main.go":            textLines("package main", "", "func main() {}"),
		fxChangelog: textLines(
			"# Changelog",
			"",
			"## [Unreleased]",
			"",
			"- Retries back off (D-01), as `docs/architecture.md#codemap` says.",
			"",
			"## [1.0.0]",
			"",
			"- Cites D-99 and `internal/gone`, true when written.",
			"",
			"[Unreleased]: https://example.com/compare/v1.0.0...HEAD",
			"[1.0.0]: https://example.com/releases/v1.0.0",
		),
		fxArch: textLines(
			"# Architecture",
			"",
			"## Codemap",
			"",
			"- `internal/gchat` owns the REST client, in `internal/gchat/client.go`.",
			"- `cmd/server` is the binary.",
			"",
			"## Decisions",
			"",
			"| ID | Decision | Status | Held by |",
			"| --- | --- | --- | --- |",
			fxRow01,
			fxRow02,
			fxRow03,
			"",
			"### D-01 Retries back off",
			"",
			"Google answers a burst with 429, so a retry waits.",
			"",
			"```markdown",
			"### D-09 An example entry, not a decision",
			"It has no Rejected or Cost line.",
			"```",
			"",
			"Rejected: retrying at once, which meets the same 429.",
			"Cost: a burst takes longer to drain.",
			"",
			"### D-02 Retry forever",
			"",
			"Rejected: giving up, which drops the write.",
			"Cost: a write can wait forever.",
			"",
			"### D-03 Retry three times",
			"",
			"Three tries replace D-02, since a write that waits forever never reports.",
			"",
			"Rejected: retrying forever.",
			"Cost: a fourth 429 fails the call.",
			"",
			"Next ID: D-04",
		),
		fxEvidence: textLines(
			"# Evidence",
			"",
			"What was checked, against which source. Each entry says what holds it.",
			"",
			"## Google Chat API",
			"",
			"### E-001 A burst is answered with 429",
			"",
			"Supports: D-01",
			`Source says: "429 means too many requests" ([limits](https://example.com/limits)).`,
			"Observed: a burst of 100 writes returns 429; Chat API v1, 2026-10-03.",
			"Verdict: confirmed — the reference describes it.",
			"Held by: live",
			"",
			"### E-002 A retried write lands twice",
			"",
			"Supports: none",
			"Source says: nothing",
			"Observed: a retried write lands once; Chat API v1, 2026-10-03.",
			"Verdict: refuted — each write carries a request id.",
			"Held by: review",
			"",
			"Retired: E-003",
			"Next ID: E-004",
		),
		fxRunbook: textLines(
			"# Runbook",
			"",
			"## Steps",
			"",
			"See [the codemap](architecture.md#codemap) and [the readme](../README.md).",
			"The retry is in `internal/gchat/client.go`, linked as [the client](../internal/gchat/client.go).",
			"",
			"## Steps",
			"",
			"Follow §2 of the old runbook, in [the server](/cmd/server).",
			"",
			"Back to [the first steps](#steps).",
			"",
			"[map]: architecture.md#codemap",
		),
		fxReadme: textLines(
			"# Fixture",
			"",
			"Read the [decisions](docs/architecture.md#decisions) and [the second steps](docs/runbook.md#steps-1).",
			"Code cites `docs/architecture.md#d-01-retries-back-off`.",
			"A redirect is followed once, as RFC 9110 §15.4 allows.",
			"The [spec](https://example.com/spec) §2 says so too.",
			"",
			"```text",
			"§3, D-99, E-999 and [a link](nowhere.md) in a fence are an example.",
			"```",
		),
		fxAgent: textLines(
			"# CLAUDE.md",
			"",
			"Read `docs/architecture.md` and `docs/architecture.md#codemap` first.",
			"The client is in `internal/gchat/`, and `go.mod` names the module.",
			"Run `make check`, or `go run ./scripts/gates docs` alone; `scripts/gates smoke` needs a binary.",
			"A made-up id says so: `spaces/AAAAspace1`. Streams are named in `main`, never in `cmd/*/x.go`.",
		),
		fxAllowlist: textLines(
			"# Files that still cite a section by number alone, and how many times.",
			"docs/runbook.md 1",
		),
	}
}

// docsEdit changes one fixture file: old replaced by new, the whole file
// written when old is empty, or the file removed when both are.
type docsEdit struct{ file, old, new string }

func replace(file, old, new string) docsEdit { return docsEdit{file, old, new} }
func add(file, body string) docsEdit         { return docsEdit{file, "", body} }
func drop(file string) docsEdit              { return docsEdit{file: file} }

// edited is the fixture with the edits applied. An edit whose old text is
// not there fails the test, so a fixture change cannot quietly turn a
// reject row into a copy of the accepted checkout.
func edited(t *testing.T, edits ...docsEdit) map[string]string {
	t.Helper()
	files := docsFixture()
	for _, e := range edits {
		switch body := files[e.file]; {
		case e.old == "" && e.new == "":
			delete(files, e.file)
		case e.old == "":
			files[e.file] = e.new
		case !strings.Contains(body, e.old):
			t.Fatalf("the fixture's %s does not contain %q", e.file, e.old)
		default:
			files[e.file] = strings.Replace(body, e.old, e.new, 1)
		}
	}
	return files
}

// docsCheckout makes the working directory a fresh git checkout holding
// files, untracked, with no global git configuration or ignore file.
func docsCheckout(t *testing.T, files map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	initRepo(t)
	for rel, body := range files {
		put(t, ".", rel, body)
	}
}

// runDocsGate runs `gates docs` in the working directory and returns its
// exit code, its stdout and the problems it printed, prefix removed.
func runDocsGate(t *testing.T) (code int, stdout string, problems []string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run([]string{"docs"}, &out, &errOut)
	for _, line := range strings.Split(strings.TrimSpace(errOut.String()), "\n") {
		if line == "" {
			continue
		}
		p, ok := strings.CutPrefix(line, "gates: ")
		if !ok {
			t.Errorf("a line without the gates: prefix: %q", line)
		}
		problems = append(problems, p)
	}
	return code, out.String(), problems
}

// docsCase is one checkout and every problem the gate reports for it. A
// nil want means the gate accepts it.
type docsCase struct {
	name  string
	edits []docsEdit
	want  []string
}

// checkDocsCases runs each row against its own checkout and compares the
// whole problem list, so a row also fails on a problem it did not expect.
func checkDocsCases(t *testing.T, cases []docsCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docsCheckout(t, edited(t, tc.edits...))
			code, stdout, got := runDocsGate(t)
			if !slices.Equal(got, tc.want) {
				t.Errorf("problems\n got %q\nwant %q", got, tc.want)
			}
			wantCode, wantStdout := 1, ""
			if tc.want == nil {
				wantCode, wantStdout = 0, "docs ok\n"
			}
			if code != wantCode || stdout != wantStdout {
				t.Errorf("exit %d with stdout %q, want %d and %q", code, stdout, wantCode, wantStdout)
			}
		})
	}
}

func TestDocsGateAcceptsAConsistentCheckout(t *testing.T) {
	checkDocsCases(t, []docsCase{
		{name: "the fixture as it stands"},
		{"no allowlist and no bare section number", []docsEdit{
			drop(fxAllowlist),
			replace(fxRunbook, "Follow §2 of the old runbook", "Follow the old runbook"),
		}, nil},
		{"a nested package and a directory without Go files need no codemap entry", []docsEdit{
			add("internal/evals/approver/main.go", "package main\n"),
			add("internal/assets/logo.txt", "a logo\n"),
		}, nil},
		{"a Go file under testdata is neither parsed nor read for citations", []docsEdit{
			add("internal/gchat/testdata/fixture.go", "package fixture\n\n// Cites D-99.\nfunc {\n"),
		}, nil},
		{"footnotes, and a link out of the repository", []docsEdit{
			replace(fxReadme, "says so too.", "says so too.[^spec] See [issue 51](../../issues/51)."),
			replace(fxReadme, "```text", "[^spec]: The spec's own words.\n\n```text"),
		}, nil},
		{"a root doc's anchor, and a lowercase file name, in a Go comment", []docsEdit{
			replace(fxClient, "§15.4 allows.", "§15.4 allows, as README.md#fixture says.\n// Each retry is logged to retries.md."),
		}, nil},
		{"a bare file name in a Go string, and a doc path in a test's string", []docsEdit{
			replace(fxClient, "E-999 and §4", "E-999, §4 and NOTES.md"),
			replace("internal/gchat/client_test.go", "_ = Retry()", `_ = Retry() + "docs/gone.md"`),
		}, nil},
	})
}

// A file deleted in the worktree stays in the index until the deletion is
// staged, and the gate reads what is on disk.
func TestDocsGateSkipsAFileDeletedButStillStaged(t *testing.T) {
	docsCheckout(t, edited(t, add("internal/old/old.go", "package old\n")))
	gitRun(t, "add", "-A")
	if err := os.Remove(filepath.Join("internal", "old", "old.go")); err != nil {
		t.Fatal(err)
	}
	if code, _, problems := runDocsGate(t); code != 0 {
		t.Errorf("exit %d with %q, want 0", code, problems)
	}
}

func TestDocsGateHoldsDecisionIntegrity(t *testing.T) {
	checkDocsCases(t, []docsCase{
		{"an ID in the index twice", []docsEdit{
			replace(fxArch, fxRow03, fxRow03+"\n"+fxRow03),
		}, []string{"docs/architecture.md:15: D-03 is in the index twice"}},
		{"an ID with two entries", []docsEdit{
			replace(fxArch, "Next ID: D-04", "### D-03 Retry three times\n\nRejected: none.\nCost: none.\n\nNext ID: D-04"),
		}, []string{"docs/architecture.md:40: D-03 has two entries"}},
		{"an ID at Next ID", []docsEdit{
			replace(fxArch, "Next ID: D-04", "Next ID: D-03"),
		}, []string{"docs/architecture.md:14: D-03 is not below Next ID D-03"}},
		{"an ID below Next ID that is gone", []docsEdit{
			replace(fxArch, "Next ID: D-04", "Next ID: D-05"),
		}, []string{"docs/architecture.md:40: D-04 is below Next ID D-05 and is missing; an ID is never deleted"}},
		{"no Next ID line", []docsEdit{
			replace(fxArch, "\n\nNext ID: D-04", ""),
		}, []string{`docs/architecture.md:38: the last line is not "Next ID: D-NN"`}},
		{"a status outside the set", []docsEdit{
			replace(fxArch, "| D-01 | Retries back off | accepted |", "| D-01 | Retries back off | proposed |"),
		}, []string{`docs/architecture.md:12: D-01 has status "proposed", want accepted, rejected, deprecated or superseded by D-NN`}},
		{"a superseded status that names no decision ID", []docsEdit{
			replace(fxArch, "| superseded by D-03 |", "| superseded by D-3 |"),
		}, []string{
			`docs/architecture.md:13: D-02 has status "superseded by D-3", want accepted, rejected, deprecated or superseded by D-NN`,
			`docs/architecture.md:13: "D-3" is not written as a decision ID like D-03`,
		}},
		{"a superseded status naming a decision that does not exist", []docsEdit{
			replace(fxArch, "| superseded by D-03 |", "| superseded by D-07 |"),
		}, []string{"docs/architecture.md:13: cites D-07, which is not a decision"}},
		{"an entry titled unlike its index row", []docsEdit{
			replace(fxArch, "| D-01 | Retries back off |", "| D-01 | Retries back off fast |"),
		}, []string{`docs/architecture.md:16: D-01's entry is titled "Retries back off" and its index row "Retries back off fast"`}},
		{"an entry with no Rejected line", []docsEdit{
			replace(fxArch, "Rejected: giving up, which drops the write.\n", ""),
		}, []string{`docs/architecture.md:28: D-02 has no "Rejected:" line`}},
		{"an entry with no Cost line", []docsEdit{
			replace(fxArch, "Cost: a fourth 429 fails the call.\n", ""),
		}, []string{`docs/architecture.md:33: D-03 has no "Cost:" line`}},
		{"an index row with no entry", []docsEdit{
			replace(fxArch, fxRow03, fxRow03+"\n| D-04 | Log every retry | accepted | live |"),
			replace(fxArch, "Next ID: D-04", "Next ID: D-05"),
		}, []string{"docs/architecture.md:15: D-04 has no entry"}},
		{"an entry with no index row", []docsEdit{
			replace(fxArch, "Next ID: D-04", "### D-04 Log every retry\n\nRejected: none.\nCost: none.\n\nNext ID: D-05"),
		}, []string{"docs/architecture.md:40: D-04 has no index row"}},
		{"an ID with one digit, which the citation scan also reads, reported once", []docsEdit{
			replace(fxArch, "| D-03 | Retry three", "| D-3 | Retry three"),
		}, []string{
			`docs/architecture.md:14: "D-3" is not written as a decision ID like D-03`,
			"docs/architecture.md:33: D-03 has no index row",
		}},
		{"an index with a column too many", []docsEdit{
			replace(fxArch, "| Held by |\n| --- | --- | --- | --- |", "| Held by | Links |\n| --- | --- | --- | --- | --- |"),
		}, []string{
			`docs/architecture.md:10: the decision index has the columns ["ID" "Decision" "Status" "Held by" "Links"], ` +
				`want ["ID" "Decision" "Status" "Held by"]`,
			"docs/architecture.md: no decisions in the index: this gate would be looking at nothing",
			"docs/architecture.md:16: D-01 has no index row",
			"docs/architecture.md:28: D-02 has no index row",
			"docs/architecture.md:33: D-03 has no index row",
		}},
		{"an index that is not directly under its heading", []docsEdit{
			replace(fxArch, "## Decisions\n\n|", "## Decisions\n\nThe index.\n\n|"),
		}, []string{
			`docs/architecture.md:8: the decision index is not a table directly under "## Decisions"`,
			"docs/architecture.md: no decisions in the index: this gate would be looking at nothing",
			"docs/architecture.md:18: D-01 has no index row",
			"docs/architecture.md:30: D-02 has no index row",
			"docs/architecture.md:35: D-03 has no index row",
		}},
	})
}

func TestDocsGateHoldsHeldByToWhatExists(t *testing.T) {
	const notAHolder = "which is not live, review, a make target, a gates command, an enabled linter or a test function"
	checkDocsCases(t, []docsCase{
		{"a test function that does not exist", []docsEdit{
			replace(fxArch, "| TestRetry, smoke |", "| TestRetryLater, smoke |"),
		}, []string{`docs/architecture.md:12: D-01 is held by "TestRetryLater", ` + notAHolder}},
		{"a linter configured but not enabled", []docsEdit{
			replace(fxArch, "| forbidigo, lint, review |", "| misspell, lint, review |"),
		}, []string{`docs/architecture.md:14: D-03 is held by "misspell", ` + notAHolder}},
		{"a linter enabled only inside another linter's settings", []docsEdit{
			replace(fxArch, "| forbidigo, lint, review |", "| nilness, lint, review |"),
		}, []string{`docs/architecture.md:14: D-03 is held by "nilness", ` + notAHolder}},
		{"an evidence entry held by a test that does not exist", []docsEdit{
			replace(fxEvidence, "Held by: live", "Held by: TestGone"),
		}, []string{`docs/evidence.md:13: E-001 is held by "TestGone", ` + notAHolder}},
		{"an accepted decision held by a dash", []docsEdit{
			replace(fxArch, "| forbidigo, lint, review |", "| — |"),
		}, []string{"docs/architecture.md:14: D-03 is accepted and held by —; an accepted decision names what holds it"}},
		{"a decision held by nothing", []docsEdit{
			replace(fxArch, "| TestRetry, smoke |", "| |"),
		}, []string{"docs/architecture.md:12: D-01 is held by nothing"}},
	})
}

func TestDocsGateHoldsEvidenceIntegrity(t *testing.T) {
	const observed = "Observed: a burst of 100 writes returns 429; Chat API v1, 2026-10-03."
	const source = `Source says: "429 means too many requests" ([limits](https://example.com/limits)).`
	checkDocsCases(t, []docsCase{
		{"an ID with two entries", []docsEdit{
			replace(fxEvidence, "Retired: E-003", "### E-001 Again\n\nRetired: E-003"),
		}, []string{"docs/evidence.md:23: E-001 has two entries"}},
		{"a line left out", []docsEdit{
			replace(fxEvidence, observed+"\n", ""),
		}, []string{`docs/evidence.md:7: E-001 has no "Observed:" line`}},
		{"lines out of order", []docsEdit{
			replace(fxEvidence, "Supports: D-01\n"+source+"\n", source+"\nSupports: D-01\n"),
		}, []string{"docs/evidence.md:7: E-001's lines are out of order; want Supports, Source says, Observed, Verdict, Held by"}},
		{"a verdict outside the set", []docsEdit{
			replace(fxEvidence, "Verdict: confirmed", "Verdict: plausible"),
		}, []string{`docs/evidence.md:12: E-001's verdict is "plausible", want confirmed, contradicted or refuted`}},
		{"Supports naming what is not a decision ID", []docsEdit{
			replace(fxEvidence, "Supports: D-01", "Supports: D-01, the retry rule"),
		}, []string{`docs/evidence.md:9: E-001 supports "the retry rule", which is not a D-ID or none`}},
		{"IDs at Next ID", []docsEdit{
			replace(fxEvidence, "Next ID: E-004", "Next ID: E-002"),
		}, []string{
			"docs/evidence.md:15: E-002 is not below Next ID E-002",
			"docs/evidence.md:23: Retired lists E-003, which is not below Next ID E-002",
		}},
		{"a retired ID that still has an entry", []docsEdit{
			replace(fxEvidence, "Retired: E-003", "Retired: E-002, E-003"),
		}, []string{"docs/evidence.md:23: E-002 is retired and still has an entry"}},
		{"a deleted ID that is not retired", []docsEdit{
			replace(fxEvidence, "Retired: E-003", "Retired: none"),
		}, []string{"docs/evidence.md:24: E-003 is below Next ID E-004 and is missing; list it under Retired"}},
		{"no Retired line", []docsEdit{
			replace(fxEvidence, "Retired: E-003\n", ""),
		}, []string{`docs/evidence.md:23: the last two lines are not "Retired: …" and "Next ID: E-NNN"`}},
		{"no evidence document", []docsEdit{
			drop(fxEvidence),
		}, []string{"docs/evidence.md is missing"}},
	})
}

func TestDocsGateFindsEvidenceWithNoHome(t *testing.T) {
	entry := textLines(
		"### E-004 A retry after 429 lands",
		"",
		"Supports: none",
		"Source says: nothing",
		"Observed: a retry after one second lands; Chat API v1, 2026-10-03.",
		"Verdict: confirmed — as observed.",
		"Held by: live",
		"",
		"Retired: E-003",
		"Next ID: E-005",
	)
	const orphan = "docs/evidence.md:23: E-004 supports no decision and is cited nowhere outside docs/evidence.md; " +
		"cite it where it is relied on"
	checkDocsCases(t, []docsCase{
		{"a confirmed entry that supports nothing and nobody cites", []docsEdit{
			replace(fxEvidence, "Retired: E-003\nNext ID: E-004\n", entry),
		}, []string{orphan}},
		{"an entry cited only from the evidence document", []docsEdit{
			replace(fxEvidence, "Retired: E-003\nNext ID: E-004\n", entry),
			replace(fxEvidence, "returns 429; Chat API v1", "returns 429, unlike E-004; Chat API v1"),
		}, []string{orphan}},
		{"a contradiction that supports nothing and nobody cites", []docsEdit{
			replace(fxEvidence, "Verdict: refuted", "Verdict: contradicted"),
		}, []string{"docs/evidence.md:15: E-002 supports no decision and is cited nowhere outside docs/evidence.md; " +
			"cite it where it is relied on"}},
		{"an entry that supports a decision and nothing else cites", []docsEdit{
			replace(fxClient, ", as measured in E-001.", "."),
		}, nil},
	})
}

func TestDocsGateResolvesCitations(t *testing.T) {
	const bySlug = "cite a D-ID, an E-ID or docs/<file>.md#<slug>"
	checkDocsCases(t, []docsCase{
		{"a superseded decision cited from a Go comment", []docsEdit{
			replace(fxClient, "(D-01)", "(D-02)"),
		}, []string{"internal/gchat/client.go:3: cites D-02, which is superseded by D-03"}},
		{"a deprecated decision cited from a doc", []docsEdit{
			replace(fxArch, fxRow03, fxRow03+"\n| D-04 | Log every retry | deprecated | — |"),
			replace(fxArch, "Next ID: D-04", "### D-04 Log every retry\n\nRejected: none.\nCost: none.\n\nNext ID: D-05"),
			replace(fxReadme, "#d-01-retries-back-off`.", "#d-01-retries-back-off`. Logging was D-04."),
		}, []string{"README.md:4: cites D-04, which is deprecated"}},
		{"an unknown decision cited twice on a line, reported once", []docsEdit{
			replace(fxClient, "(D-01)", "(D-07, and D-07 again)"),
		}, []string{"internal/gchat/client.go:3: cites D-07, which is not a decision"}},
		{"an unknown decision on a Supports line", []docsEdit{
			replace(fxEvidence, "Supports: D-01", "Supports: D-07"),
		}, []string{"docs/evidence.md:9: cites D-07, which is not a decision"}},
		{"a retired evidence ID cited from a doc", []docsEdit{
			replace(fxReadme, "says so too.", "says so too, as E-003 did."),
		}, []string{"README.md:6: cites E-003, which is not an evidence entry"}},
		{"a link slug no heading has", []docsEdit{
			replace(fxReadme, "docs/architecture.md#decisions", "docs/architecture.md#decision"),
		}, []string{"README.md:3: links to docs/architecture.md#decision, and docs/architecture.md has no heading with that slug"}},
		{"a code span slug no heading has", []docsEdit{
			replace(fxReadme, "#d-01-retries-back-off`", "#d-01-retry`"),
		}, []string{"README.md:4: names docs/architecture.md#d-01-retry, and docs/architecture.md has no heading with that slug"}},
		{"a repeated heading's suffix past the last repeat", []docsEdit{
			replace(fxReadme, "#steps-1", "#steps-2"),
		}, []string{"README.md:3: links to docs/runbook.md#steps-2, and docs/runbook.md has no heading with that slug"}},
		{"a same-file anchor no heading has", []docsEdit{
			replace(fxRunbook, "(#steps)", "(#step)"),
		}, []string{"docs/runbook.md:12: links to docs/runbook.md#step, and docs/runbook.md has no heading with that slug"}},
		{"a reference definition no heading answers", []docsEdit{
			replace(fxRunbook, "[map]: architecture.md#codemap", "[map]: architecture.md#codemaps"),
		}, []string{"docs/runbook.md:14: links to docs/architecture.md#codemaps, and docs/architecture.md has no heading with that slug"}},
		{"a link to a doc that does not exist", []docsEdit{
			replace(fxReadme, "says so too.", "says so too, in [the guide](docs/gone.md)."),
		}, []string{"README.md:6: links to docs/gone.md, which does not exist"}},
		{"a link written from the root instead of the linking file", []docsEdit{
			replace(fxRunbook, "](architecture.md#codemap)", "](docs/architecture.md#codemap)"),
		}, []string{"docs/runbook.md:5: links to docs/docs/architecture.md, which does not exist"}},
		{"a docs citation in a Go comment to a file that does not exist", []docsEdit{
			replace(fxClient, "as measured in E-001.", "as measured in E-001 and docs/retries.md#backoff."),
		}, []string{"internal/gchat/client.go:3: cites docs/retries.md, which does not exist"}},
		{"a bare section number in a Go comment", []docsEdit{
			replace(fxClient, "as RFC 9110 §15.4 allows", "as §15.4 allows"),
		}, []string{"internal/gchat/client.go:4: cites a section by number (§15); " + bySlug}},
		{"a bare section number in the changelog's Unreleased section", []docsEdit{
			replace(fxChangelog, "as `docs/architecture.md#codemap` says.", "as §3 says."),
		}, []string{"CHANGELOG.md:5: cites a section by number (§3); " + bySlug}},
		{"a bare section number in a released changelog section", []docsEdit{
			replace(fxChangelog, "Cites D-99 and", "Cites D-99, §3 and"),
		}, nil},
		{"IDs written at the wrong width, in a Go comment and a doc", []docsEdit{
			replace(fxClient, "(D-01)", "(D-1)"),
			replace(fxReadme, "says so too.", "says so too, as E-0001 did."),
		}, []string{
			`README.md:6: "E-0001" is not written as an evidence ID like E-001`,
			`internal/gchat/client.go:3: "D-1" is not written as a decision ID like D-01`,
		}},
		{"a superseded decision cited from the evidence that supported it", []docsEdit{
			replace(fxEvidence, "Supports: D-01", "Supports: D-01, D-02"),
		}, nil},
		{"a one-line code span in fence backticks opens no fence", []docsEdit{
			replace(fxReadme, "says so too.\n", "says so too.\n```go``` is a span, not a fence.\nAs D-07 said.\n"),
		}, []string{"README.md:8: cites D-07, which is not a decision"}},
		{"a root doc in a Go comment that does not exist, or has no such heading", []docsEdit{
			replace(fxClient, "as measured in E-001.", "as measured in E-001, RUNBOOK.md and README.md#retries."),
		}, []string{
			"internal/gchat/client.go:3: cites RUNBOOK.md, which does not exist",
			"internal/gchat/client.go:3: cites README.md#retries, and README.md has no heading with that slug",
		}},
		{"doc paths in a Go string that do not resolve", []docsEdit{
			replace(fxClient, "in a string are data", "in a string are data; docs/setup.md and README.md#setup are not"),
		}, []string{
			"internal/gchat/client.go:5: names docs/setup.md, which does not exist",
			"internal/gchat/client.go:5: names README.md#setup, and README.md has no heading with that slug",
		}},
	})
}

// The allowlist is a ratchet: each file's count of bare section citations
// may only fall, and the list has to follow it down.
func TestDocsGateHoldsTheAllowlistToItsCounts(t *testing.T) {
	const bySlug = "cite a D-ID, an E-ID or docs/<file>.md#<slug>"
	checkDocsCases(t, []docsCase{
		{"a count that rises", []docsEdit{
			replace(fxRunbook, "Back to [the first steps](#steps).", "Back to §1 and [the first steps](#steps)."),
		}, []string{"testdata/citation-allowlist.txt:2: lists docs/runbook.md at 1 bare section citations, and it has 2; " + bySlug}},
		{"a count that falls without the list following", []docsEdit{
			replace(fxAllowlist, "docs/runbook.md 1", "docs/runbook.md 2"),
		}, []string{"testdata/citation-allowlist.txt:2: lists docs/runbook.md at 2 bare section citations, and it has 1; lower the count"}},
		{"a file with no bare section number left", []docsEdit{
			replace(fxRunbook, "Follow §2 of the old runbook", "Follow the old runbook"),
		}, []string{"testdata/citation-allowlist.txt:2: lists docs/runbook.md, which has no bare section citation left; remove it"}},
		{"a file that does not exist", []docsEdit{
			replace(fxAllowlist, "docs/runbook.md 1", "docs/runbook.md 1\ndocs/gone.md 1"),
		}, []string{"testdata/citation-allowlist.txt:3: lists docs/gone.md, which does not exist"}},
		{"a line with a count of zero", []docsEdit{
			replace(fxAllowlist, "docs/runbook.md 1", "docs/runbook.md 0"),
		}, []string{
			`testdata/citation-allowlist.txt:2: "docs/runbook.md 0" is not a path and a count above zero`,
			"docs/runbook.md:10: cites a section by number (§2); " + bySlug,
		}},
	})
}

func TestDocsGateHoldsTheCodemapToTheTree(t *testing.T) {
	checkDocsCases(t, []docsCase{
		{"a package the codemap leaves out", []docsEdit{
			add("internal/redact/redact.go", "package redact\n"),
		}, []string{`docs/architecture.md:3: "## Codemap" does not name internal/redact`}},
		{"a directory named that holds no Go file", []docsEdit{
			add("internal/assets/logo.txt", "a logo\n"),
			replace(fxArch, "- `cmd/server` is the binary.", "- `cmd/server` is the binary.\n- `internal/assets` holds the logo."),
		}, []string{`docs/architecture.md:7: "## Codemap" names internal/assets, which holds no .go file`}},
		{"a package the tree does not have", []docsEdit{
			replace(fxArch, "- `cmd/server` is the binary.", "- `cmd/server` is the binary.\n- `internal/gone` was removed."),
		}, []string{"docs/architecture.md:7: names internal/gone, which does not exist"}},
		{"no codemap heading", []docsEdit{
			replace(fxArch, "## Codemap", "## Packages"),
		}, []string{
			"CHANGELOG.md:5: names docs/architecture.md#codemap, and docs/architecture.md has no heading with that slug",
			"CLAUDE.md:3: names docs/architecture.md#codemap, and docs/architecture.md has no heading with that slug",
			"docs/runbook.md:5: links to docs/architecture.md#codemap, and docs/architecture.md has no heading with that slug",
			"docs/runbook.md:14: links to docs/architecture.md#codemap, and docs/architecture.md has no heading with that slug",
			`docs/architecture.md: no "## Codemap" heading`,
		}},
	})
}

func TestDocsGateHoldsPathsToTheRepository(t *testing.T) {
	checkDocsCases(t, []docsCase{
		{"a path in CLAUDE.md that does not exist", []docsEdit{
			replace(fxAgent, "`internal/gchat/`", "`internal/chat/`"),
		}, []string{"CLAUDE.md:4: names internal/chat/, which does not exist"}},
		{"a path in another doc that does not exist", []docsEdit{
			replace(fxRunbook, "`internal/gchat/client.go`", "`internal/gchat/retry.go`"),
		}, []string{"docs/runbook.md:6: names internal/gchat/retry.go, which does not exist"}},
		{"a link to a file that does not exist", []docsEdit{
			replace(fxRunbook, "(../internal/gchat/client.go)", "(../internal/gchat/retry.go)"),
		}, []string{"docs/runbook.md:6: links to internal/gchat/retry.go, which does not exist"}},
		{"a link from the root to a directory that does not exist", []docsEdit{
			replace(fxRunbook, "(/cmd/server)", "(/cmd/client)"),
		}, []string{"docs/runbook.md:10: links to cmd/client, which does not exist"}},
		{"a Go name in a package that exists", []docsEdit{
			replace(fxRunbook, "`internal/gchat/client.go`", "`internal/gchat.Retry`"),
		}, nil},
		{"a Go name in a package that does not exist", []docsEdit{
			replace(fxRunbook, "`internal/gchat/client.go`", "`internal/chat.Retry`"),
		}, []string{"docs/runbook.md:6: names internal/chat.Retry, which does not exist"}},
		{"a file named as a directory", []docsEdit{
			replace(fxAgent, "`go.mod`", "`go.mod/`"),
		}, []string{"CLAUDE.md:4: names go.mod/, which is not a directory"}},
		{"a make target that does not exist", []docsEdit{
			replace(fxAgent, "`make check`", "`make verify`"),
		}, []string{"CLAUDE.md:5: names make verify, which is not a Makefile target"}},
		{"a gates command that is not registered", []docsEdit{
			replace(fxAgent, "`scripts/gates smoke`", "`scripts/gates smok`"),
		}, []string{"CLAUDE.md:5: names scripts/gates smok, which is not a gates command"}},
		{"no CLAUDE.md", []docsEdit{
			drop(fxAgent),
		}, []string{"CLAUDE.md is missing"}},
	})
}

// A gate that read nothing passes everything, so each input it depends on
// reading has a floor, and each row here sets one off.
func TestDocsGateRefusesToReadNothing(t *testing.T) {
	checkDocsCases(t, []docsCase{
		{"no Go files", []docsEdit{
			drop(fxClient), drop("internal/gchat/client_test.go"), drop("cmd/server/main.go"),
		}, []string{
			"no .go files read: this gate would be looking at nothing",
			`docs/architecture.md:12: D-01 is held by "TestRetry", which is not live, review, a make target, ` +
				"a gates command, an enabled linter or a test function",
			"docs/architecture.md:5: names internal/gchat/client.go, which does not exist",
			"docs/runbook.md:6: links to internal/gchat/client.go, which does not exist",
			"docs/runbook.md:6: names internal/gchat/client.go, which does not exist",
			"docs/runbook.md:10: links to cmd/server, which does not exist",
			"no package directories under internal/ or cmd/: the codemap check would be looking at nothing",
			`docs/architecture.md:5: "## Codemap" names internal/gchat, which holds no .go file`,
		}},
		{"an index with no rows", []docsEdit{
			replace(fxArch, fxRow01+"\n"+fxRow02+"\n"+fxRow03+"\n", ""),
		}, []string{
			"docs/architecture.md: no decisions in the index: this gate would be looking at nothing",
			"docs/architecture.md:13: D-01 has no index row",
			"docs/architecture.md:25: D-02 has no index row",
			"docs/architecture.md:30: D-03 has no index row",
		}},
		{"no evidence entries", []docsEdit{
			add(fxEvidence, textLines("# Evidence", "", "Retired: none", "Next ID: E-001")),
		}, []string{
			"docs/evidence.md: no evidence entries: this gate would be looking at nothing",
			"internal/gchat/client.go:3: cites E-001, which is not an evidence entry",
		}},
		{"linters listed in flow style", []docsEdit{
			replace(fxGolangci, "  enable:\n    - errcheck\n    - forbidigo # stdout carries only frames\n", "  enable: [errcheck, forbidigo]\n"),
		}, []string{
			".golangci.yml: no enabled linters read: a Held by naming one would never match",
			`docs/architecture.md:14: D-03 is held by "forbidigo", which is not live, review, a make target, ` +
				"a gates command, an enabled linter or a test function",
		}},
		{"a Makefile with no rules", []docsEdit{
			add(fxMakefile, "GO ?= go\n"),
		}, []string{
			"Makefile: no targets read: a Held by naming one would never match",
			`docs/architecture.md:14: D-03 is held by "lint", which is not live, review, a make target, ` +
				"a gates command, an enabled linter or a test function",
			"CLAUDE.md:5: names make check, which is not a Makefile target",
		}},
		{"no architecture document", []docsEdit{
			drop(fxArch),
		}, []string{
			"docs/architecture.md is missing",
			"CHANGELOG.md:5: names docs/architecture.md#codemap, which does not exist",
			"CLAUDE.md:3: names docs/architecture.md, which does not exist",
			"CLAUDE.md:3: names docs/architecture.md#codemap, which does not exist",
			"README.md:3: links to docs/architecture.md, which does not exist",
			"README.md:4: names docs/architecture.md#d-01-retries-back-off, which does not exist",
			"docs/runbook.md:5: links to docs/architecture.md, which does not exist",
			"docs/runbook.md:14: links to docs/architecture.md, which does not exist",
		}},
		{"a Go file that does not parse", []docsEdit{
			add("internal/gchat/broken.go", "package gchat\n\nfunc {\n"),
		}, []string{"gates cannot parse internal/gchat/broken.go: internal/gchat/broken.go:3:6: expected 'IDENT', found '{'"}},
	})
}

func TestDocsGateNeedsAGitCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	code, stdout, problems := runDocsGate(t)
	if code != 1 || stdout != "" || len(problems) != 1 || !strings.HasPrefix(problems[0], "git ls-files") {
		t.Errorf("exit %d, stdout %q, problems %q; want 1, nothing and one git ls-files failure", code, stdout, problems)
	}
}

// These slugs are what GitHub renders for the same headings.
func TestGitHubSlug(t *testing.T) {
	for _, tc := range []struct{ heading, want string }{
		{"Codemap", "codemap"},
		{"D-01 Stdout carries only MCP JSON-RPC frames", "d-01-stdout-carries-only-mcp-json-rpc-frames"},
		{"What a write may reach", "what-a-write-may-reach"},
		{"The `register` function", "the-register-function"},
		{"C# and F#?", "c-and-f"},
		{"snake_case stays", "snake_case-stays"},
		{"Two  spaces", "two--spaces"},
		{"Straße über alles", "straße-über-alles"},
		{"A [link](https://example.com) here", "a-link-here"},
	} {
		if got := githubSlug(tc.heading); got != tc.want {
			t.Errorf("githubSlug(%q) = %q, want %q", tc.heading, got, tc.want)
		}
	}
}
