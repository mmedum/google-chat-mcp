package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// plain is a released surface: two tools and a resource template.
const plain = `{"version":"v1.0.0","tools":[
	{"name":"send_message","annotations":{"destructiveHint":false},
	 "inputSchema":{"type":"object","properties":{"text":{"type":"string"}}},
	 "outputSchema":{"type":"object","properties":{"message_id":{"type":"string"}}}},
	{"name":"get_space","annotations":{"readOnlyHint":true},
	 "inputSchema":{"type":"object","properties":{"space_id":{"type":"string"}}},
	 "outputSchema":{"type":"object","properties":{"space_id":{"type":"string"}}}}],
	"resourceTemplates":[{"uriTemplate":"gchat://spaces/{id}","name":"space"}]}`

// flipped is plain with send_message made destructive, and renamed is
// plain with send_message's message_id output renamed, which breaks it.
var (
	flipped = strings.Replace(plain, `"destructiveHint":false`, `"destructiveHint":true`, 1)
	renamed = strings.Replace(plain, `"properties":{"message_id"`, `"properties":{"id"`, 1)
)

// relabeled is plain with the resource template's name changed, and
// withDraft is plain with a tool added since the release.
var (
	relabeled = strings.Replace(plain, `"name":"space"`, `"name":"spaces"`, 1)
	withDraft = strings.Replace(plain, `"tools":[`, `"tools":[{"name":"draft_tool"},`, 1)
)

// refusalFix is the footer a refusal ends with, after its blank line.
func refusalFix(err error) string {
	_, fix, _ := strings.Cut(fmt.Sprint(err), "\n\n")
	return fix
}

// renameBreak is the break renamed makes of plain.
var renameBreak = []change{{"send_message", "send_message: output field message_id removed", "output message_id"}}

// schema-ack fails a pull request unless its commits name every changed
// item in a footer, and every break of a release in a BREAKING CHANGE:
// footer.
func TestSchemaAckNeedsEveryItemNamed(t *testing.T) {
	withoutTemplate := strings.Replace(plain, `{"uriTemplate":"gchat://spaces/{id}","name":"space"}`, ``, 1)
	reworded := strings.Replace(plain, `"version":"v1.0.0",`, `"version":"v1.0.0","instructions":"new",`, 1)
	const (
		wantUnnamed      = "no footer names send_message"
		wantUnnamedBreak = "no BREAKING CHANGE: footer names send_message"
	)
	for _, tc := range []struct {
		name, built, message, wantErr string
	}{
		{"an unchanged surface needs nothing", plain, "Tidy the README", ""},
		{"a change needs a footer", flipped, "Mark send_message destructive", wantUnnamed},
		{"a footer naming the tool acknowledges it", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message: destructiveHint true", ""},
		{"a footer naming another tool does not", flipped, "Mark it\n\nSCHEMA-CHANGE: get_space: destructiveHint true", wantUnnamed},
		{"a name is a whole token", flipped, "Mark it\n\nSCHEMA-CHANGE: send_messages: destructiveHint true", wantUnnamed},
		{"a continuation line can name it", flipped, "Mark them\n\nSCHEMA-CHANGE: get_space,\n  send_message: destructiveHint true", ""},
		{"an empty footer says nothing", flipped, "Mark it\n\nSCHEMA-CHANGE:", wantUnnamed},
		{"the next line is not the footer's text", flipped, "Mark it\n\nSCHEMA-CHANGE:\nCo-Authored-By: send_message", wantUnnamed},
		{"a footer must open its line", flipped, "Mark it, see SCHEMA-CHANGE: send_message", wantUnnamed},
		{"a line of prose is not a footer", flipped, "Mark it\n\nThe gate wants a\nSCHEMA-CHANGE: send_message footer, which this is not.", wantUnnamed},
		{"a subject is not a footer", flipped, "SCHEMA-CHANGE: send_message", wantUnnamed},
		{"an indented block is not a footer block", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message: hints\n\n    go run ./scripts/gates schema-ack", wantUnnamed},
		{"a footer above the co-author trailer", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message: hints\n\nCo-Authored-By: someone <x@example.com>", ""},
		{"a footer beside the co-author trailer", flipped, "Mark it\n\nCo-Authored-By: someone <x@example.com>\nSCHEMA-CHANGE: send_message: hints", ""},
		{"an input change is no break here", strings.Replace(plain, `"properties":{"text"`, `"properties":{"body"`, 1),
			"Rename the input\n\nSCHEMA-CHANGE: send_message: text is body", ""},
		{"a resource template by its URI", relabeled, "Rename it\n\nSCHEMA-CHANGE: gchat://spaces/{id}: renamed", ""},
		{"a resource template unnamed", relabeled, "Rename it\n\nSCHEMA-CHANGE: send_message: renamed", "no footer names gchat://spaces/{id}"},
		{"the instructions by their key", reworded, "Reword them\n\nSCHEMA-CHANGE: instructions: reworded", ""},
		{"the instructions unnamed", reworded, "Reword them", "no footer names instructions"},
		{"a description word names nothing", flipped, "Mark it\n\nSCHEMA-CHANGE: get_space: send_message is destructive", wantUnnamed},
		{"a tab after the colon starts the description too", flipped, "Mark it\n\nSCHEMA-CHANGE: get_space:\tdestructive, send_message", wantUnnamed},
		{"a value with no description names its whole list", flipped, "Mark them\n\nSCHEMA-CHANGE: get_space, send_message", ""},
		{"a value wrapped at the margin continues it", flipped, "Mark them\n\nSCHEMA-CHANGE: get_space,\nsend_message: destructiveHint true", ""},
		{"prose after a bare list names nothing", flipped, "Mark it\n\nSCHEMA-CHANGE: get_space\nsend_message is destructive now", wantUnnamed},
		{"a bare list ends at its line, and its name counts", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message\nhints flipped", ""},
		{"prose after a trailing comma names nothing", flipped, "Mark them\n\nSCHEMA-CHANGE: get_space,\nwhich send_message also reads", wantUnnamed},
		{"a list is split on commas, not spaces", flipped, "Mark them\n\nSCHEMA-CHANGE: get_space send_message: hints", wantUnnamed},
		{"a Token #value footer is a footer", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message: hints\n\nCloses #12", ""},
		{"a Token #value footer acknowledges nothing", flipped, "Mark it\n\nSCHEMA-CHANGE #send_message", wantUnnamed},
		{"nor does a BREAKING CHANGE #value one", renamed, "Rename it\n\nBREAKING CHANGE #send_message", wantUnnamedBreak},
		{"a footer ends the value before it", flipped, "Mark it\n\nSCHEMA-CHANGE: get_space,\nRefs #12, send_message", wantUnnamed},
		{"three blank lines end one paragraph", flipped, "Mark it\n\nSCHEMA-CHANGE: send_message: hints\n\n\n\nCo-Authored-By: someone <x@example.com>", ""},
		{"a break named only as a change", renamed, "Rename the field\n\nSCHEMA-CHANGE: send_message: message_id is id", wantUnnamedBreak},
		{"BREAKING CHANGE: acknowledges a break", renamed, "Rename the field\n\nBREAKING CHANGE: send_message: message_id is id", ""},
		{"BREAKING-CHANGE: is its synonym", renamed, "Rename the field\n\nBREAKING-CHANGE: send_message: message_id is id", ""},
		{"SCHEMA-CHANGE: in any case", flipped, "Mark it\n\nSchema-Change: send_message: destructiveHint true", ""},
		{"BREAKING CHANGE: only in uppercase", renamed, "Rename the field\n\nBreaking-Change: send_message: message_id is id", wantUnnamedBreak},
		{"a removed template needs BREAKING CHANGE:", withoutTemplate, "Drop it\n\nSCHEMA-CHANGE: gchat://spaces/{id}: gone",
			"no BREAKING CHANGE: footer names gchat://spaces/{id}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := compareSurfaces(readTest(t, plain), readTest(t, tc.built))
			err := checkAck(io.Discard, c, []string{"Tidy the docs", tc.message}, "base")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("checkAck = %v, want it to pass", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("checkAck = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// A refusal ends with the footer that fixes it, bare so it can be copied:
// BREAKING CHANGE: for the breaks, SCHEMA-CHANGE: for the rest, an item
// the first names left out of the second. The footer it prints passes.
func TestARefusalNamesTheFooterThatFixesIt(t *testing.T) {
	built := strings.Replace(relabeled, `"properties":{"message_id"`, `"properties":{"id"`, 1)
	built = strings.Replace(built, `"destructiveHint":false`, `"destructiveHint":true`, 1)
	c := compareSurfaces(readTest(t, plain), readTest(t, built))
	fix := refusalFix(checkAck(io.Discard, c, []string{"Change them"}, "base"))
	if want := "BREAKING CHANGE: send_message: what broke\nSCHEMA-CHANGE: gchat://spaces/{id}: what changed"; fix != want {
		t.Fatalf("the refusal's fix is %q, want %q", fix, want)
	}
	if err := checkAck(io.Discard, c, []string{"Change them", "Acknowledge them\n\n" + fix}, "base"); err != nil {
		t.Errorf("the printed fix does not pass: %v", err)
	}
}

// A name the footer would split, or trim, is printed in backticks, and
// the footer printed passes: a URI with a comma, and one ending in a full
// stop, which a bare name would lose. Bare, the URI with a comma names
// nothing.
func TestARefusalQuotesANameTheFooterWouldSplit(t *testing.T) {
	for _, tmpl := range []string{"gchat://spaces/{id}{?view,page}", "gchat://spaces/{id}."} {
		was := strings.Replace(plain, `"gchat://spaces/{id}"`, `"`+tmpl+`"`, 1)
		c := compareSurfaces(readTest(t, was), readTest(t, strings.Replace(was, `"name":"space"`, `"name":"spaces"`, 1)))
		fix := refusalFix(checkAck(io.Discard, c, nil, "base"))
		if want := "SCHEMA-CHANGE: `" + tmpl + "`: what changed"; fix != want {
			t.Errorf("the refusal's fix is %q, want %q", fix, want)
			continue
		}
		if err := checkAck(io.Discard, c, []string{"Rename it\n\n" + fix}, "base"); err != nil {
			t.Errorf("the printed fix for %s does not pass: %v", tmpl, err)
		}
		if bare := strings.ReplaceAll(fix, "`", ""); strings.Contains(tmpl, ",") && checkAck(io.Discard, c, []string{"Rename it\n\n" + bare}, "base") == nil {
			t.Errorf("%q passed, though split on its comma it names nothing", bare)
		}
	}
}

// A break no footer names is asked for once, as a break: the BREAKING
// CHANGE: footer that fixes it names it for both checks.
func TestARefusalAsksForAnUnnamedBreakOnce(t *testing.T) {
	err := checkAck(io.Discard, compareSurfaces(readTest(t, plain), readTest(t, renamed)), nil, "base")
	if want := "the tool surface changed since base, and no BREAKING CHANGE: footer names send_message. "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("checkAck = %v, want it to start %q", err, want)
	}
}

// A refusal exits 1 and a gate that could not run exits 2, and says so,
// so a reader can tell a missing footer from a broken run. The refusal's
// exit through the command is in TestSchemaAckBuildsBothSidesFromCommits.
func TestSchemaAckTellsARefusalFromAFailure(t *testing.T) {
	var r refusal
	if err := checkAck(io.Discard, compareSurfaces(readTest(t, plain), readTest(t, flipped)), nil, "base"); !errors.As(err, &r) {
		t.Errorf("checkAck = %v, want a refusal", err)
	}
	newDumpRepo(t).git("commit", "--quiet", "--allow-empty", "-m", "Start")
	var stdout, stderr strings.Builder
	if code := schemaAckCommand("no-such-ref", "HEAD", &stdout, &stderr); code != 2 || !strings.HasPrefix(stderr.String(), "gates: schema-ack: could not run: ") {
		t.Errorf("schemaAckCommand with no base = %d, %q; want 2 and a message saying the gate could not run", code, stderr.String())
	}
}

// A break of the released surface needs BREAKING CHANGE:; removing a tool
// added since the release breaks no caller, so it is a plain change, and
// the report lists it as one.
func TestABreakIsABreakOfARelease(t *testing.T) {
	release := readTest(t, plain)
	base := readTest(t, withDraft)
	head := readTest(t, renamed)
	c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking)
	if !slices.Equal(c.breaking, renameBreak) {
		t.Errorf("breaking = %v, want %v", c.breaking, renameBreak)
	}
	if want := []change{{item: "send_message", text: "send_message (outputSchema.properties)"},
		{item: "draft_tool", text: "draft_tool removed", field: "draft_tool"}}; !slices.Equal(c.changed, want) {
		t.Errorf("changed = %v, want %v", c.changed, want)
	}
	var report strings.Builder
	footer := "Drop and rename\n\nSCHEMA-CHANGE: draft_tool: never released\nBREAKING CHANGE: send_message: message_id is id"
	if err := checkAck(&report, c, []string{footer}, "base"); err != nil {
		t.Errorf("checkAck = %v, want both acknowledged", err)
	}
	want := "against the merge-base base: 0 added, 2 changed, 1 breaking\n" +
		"  ~ send_message (outputSchema.properties)\n" +
		"  ~ draft_tool removed\n" +
		"  ! send_message: output field message_id removed  BREAKING\n" +
		"the change since base is acknowledged\n"
	if report.String() != want {
		t.Errorf("the report is\n%s\nwant\n%s", report.String(), want)
	}
}

// A tool the base branch already broke since the release, in one field,
// does not make a pull request's break of another field, one no release
// shipped, a break of the release.
func TestABreakOfTheReleaseIsMatchedByItsField(t *testing.T) {
	withOutputs := func(props string) toolSurface {
		return readTest(t, strings.Replace(plain, `"properties":{"message_id":{"type":"string"}}`, `"properties":{`+props+`}`, 1))
	}
	release := withOutputs(`"message_id":{"type":"string"},"note":{"type":"string"}`)
	base := withOutputs(`"message_id":{"type":"string"},"draft":{"type":"string"}`)
	head := withOutputs(`"message_id":{"type":"string"}`)
	c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking)
	if len(c.breaking) != 0 {
		t.Errorf("breaking = %v, want draft, which no release shipped, as a change", c.breaking)
	}
	// Nor does a released tool of the name of a prompt the pull request
	// removes: they are two items, though a footer names both alike.
	release = readTest(t, `{"tools":[{"name":"get_space"},{"name":"summarize"}]}`)
	base = readTest(t, `{"tools":[{"name":"get_space"}],"prompts":[{"name":"summarize"}]}`)
	head = readTest(t, `{"tools":[{"name":"get_space"}],"prompts":[]}`)
	if c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking); len(c.breaking) != 0 {
		t.Errorf("breaking = %v, want the unreleased prompt's removal as a change", c.breaking)
	}
}

// The release is the baseline the base branch records. Before the first
// release there is none, and nothing breaks one.
func TestReleasedBreaksReadTheBaseBranchBaseline(t *testing.T) {
	r := newDumpRepo(t)
	r.git("commit", "--quiet", "--allow-empty", "-m", "Start")
	head := readTest(t, renamed)

	breaks, err := releasedBreaks("HEAD", head)
	if err != nil || len(breaks) != 0 {
		t.Fatalf("releasedBreaks with no baseline = %v, %v; want none", breaks, err)
	}

	put(t, ".", baselinePath, plain)
	r.git("add", baselinePath)
	r.git("commit", "--quiet", "-m", "Release")
	// The working tree's baseline is a branch's, not the release's.
	put(t, ".", baselinePath, `{"tools":[]}`)
	breaks, err = releasedBreaks("HEAD", head)
	if err != nil || !slices.Equal(breaks, renameBreak) {
		t.Errorf("releasedBreaks = %v, %v; want %v", breaks, err, renameBreak)
	}
}

// Both sides are built from commits: the merge-base and the head. The
// checkout is neither, in CI the merge commit and here main with a change
// nobody committed, so what it would serve is not what is judged.
func TestSchemaAckBuildsBothSidesFromCommits(t *testing.T) {
	r := newDumpRepo(t)
	r.commit(plain, "Base")
	r.git("switch", "--quiet", "-c", "feature")
	r.commit(flipped, "Flip send_message")
	r.git("switch", "--quiet", "main")
	r.write(renamed)

	var out, stderr strings.Builder
	if code := schemaAckCommand("main", "feature", &out, &stderr); code != 1 || !strings.HasSuffix(stderr.String(), "\n\nSCHEMA-CHANGE: send_message: what changed\n") {
		t.Fatalf("schemaAckCommand = %d, %q; want 1 and a refusal naming the flip alone", code, stderr.String())
	}
	if !strings.Contains(out.String(), "~ send_message (annotations.destructiveHint)\n") || strings.Contains(out.String(), "BREAKING") {
		t.Errorf("the report is\n%s\nwant the flip and no break", out.String())
	}
	r.git("checkout", "--", ".")
	r.git("switch", "--quiet", "feature")
	r.git("commit", "--quiet", "--allow-empty", "-m", "Acknowledge it\n\nSCHEMA-CHANGE: send_message: destructive")
	if err := schemaAck("main", "HEAD", io.Discard); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
	if n := r.worktrees(); n != 1 {
		t.Errorf("%d worktrees left, want the checkout alone", n)
	}
}

// Through the whole gate: a tool no release shipped, removed, is a plain
// change, and an output the released baseline lists, renamed, is a break.
func TestSchemaAckAsksForBreakingChangeOnlyForAReleasedBreak(t *testing.T) {
	r := newDumpRepo(t)
	r.release(plain, "Release")
	r.commit(withDraft, "Add draft_tool")
	r.git("switch", "--quiet", "-c", "feature")
	r.commit(renamed, "Drop draft_tool and rename message_id")
	c, messages, base, err := ackInputs("main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	fix := refusalFix(checkAck(io.Discard, c, messages, base))
	if want := "BREAKING CHANGE: send_message: what broke\nSCHEMA-CHANGE: draft_tool: what changed"; fix != want {
		t.Fatalf("the fix is %q, want %q", fix, want)
	}
	if err := checkAck(io.Discard, c, append(messages, "Acknowledge them\n\n"+fix), base); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
}

// The release is the baseline the base branch records, not the head's:
// a branch cut before the last release, renaming what that release
// already renamed, breaks no caller of it.
func TestTheReleaseIsTheBaseBranchsBaseline(t *testing.T) {
	r := newDumpRepo(t)
	r.release(plain, "Release 1")
	r.git("branch", "feature")
	r.release(renamed, "Release 2")
	r.git("switch", "--quiet", "feature")
	r.commit(renamed, "Rename message_id")
	if fix, want := refusalFix(schemaAck("main", "HEAD", io.Discard)), "SCHEMA-CHANGE: send_message: what changed"; fix != want {
		t.Errorf("the fix is %q, want %q, since release 2 shipped the rename", fix, want)
	}
}

// dumpRepo is a git repository, the working directory for the test,
// whose cmd/google-chat-mcp prints a fixed dump, so the gate's worktree
// builds are exercised without building the real server.
type dumpRepo struct{ t *testing.T }

func newDumpRepo(t *testing.T) *dumpRepo {
	t.Helper()
	t.Chdir(t.TempDir())
	initRepo(t)
	r := &dumpRepo{t}
	r.git("checkout", "--quiet", "-b", "main")
	put(t, ".", "go.mod", "module example.invalid/fake\n\ngo 1.21\n")
	r.git("add", "go.mod")
	return r
}

func (r *dumpRepo) git(args ...string) {
	r.t.Helper()
	gitRun(r.t, args...)
}

// commit commits a main that prints dump.
func (r *dumpRepo) commit(dump, message string) {
	r.t.Helper()
	r.write(dump)
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", message)
}

// release commits a main that prints dump with dump as the baseline, as
// a release commit records it.
func (r *dumpRepo) release(dump, message string) {
	r.t.Helper()
	put(r.t, ".", baselinePath, dump)
	r.commit(dump, message)
}

// write puts a main that prints dump in the working tree, uncommitted.
func (r *dumpRepo) write(dump string) {
	r.t.Helper()
	put(r.t, ".", "cmd/google-chat-mcp/main.go",
		fmt.Sprintf("package main\n\nimport \"os\"\n\nfunc main() { os.Stdout.WriteString(%q) }\n", dump))
}

func (r *dumpRepo) worktrees() int {
	r.t.Helper()
	out, err := git("worktree", "list", "--porcelain")
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.Count(out, "worktree ")
}

// readTest reads a dump for a test.
func readTest(t *testing.T, dump string) toolSurface {
	t.Helper()
	s, err := readToolSurface([]byte(dump), "the test dump")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// initRepo makes the working directory a git repository whose commits
// are Fixture's and never signed, with no global or system
// configuration, so a person's hooks or signing never reach a test. The
// environment carries the settings, which costs no git process.
func initRepo(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		// git reads $XDG_CONFIG_HOME/git/ignore even with the global
		// configuration off.
		"XDG_CONFIG_HOME":     t.TempDir(),
		"GIT_AUTHOR_NAME":     "Fixture",
		"GIT_AUTHOR_EMAIL":    "fixture@example.test",
		"GIT_COMMITTER_NAME":  "Fixture",
		"GIT_COMMITTER_EMAIL": "fixture@example.test",
		"GIT_CONFIG_COUNT":    "1",
		"GIT_CONFIG_KEY_0":    "commit.gpgsign",
		"GIT_CONFIG_VALUE_0":  "false",
	} {
		t.Setenv(key, value)
	}
	gitRun(t, "init", "--quiet")
}

// gitRun runs git in the working directory, failing the test on an error.
func gitRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
