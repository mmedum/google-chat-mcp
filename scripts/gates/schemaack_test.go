package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// renameBreak is the break renamed makes of plain.
var renameBreak = []change{{"send_message", "send_message: output field message_id removed", "output message_id"}}

// schema-ack fails a pull request unless its commits name every changed
// item in a footer, and every break of a release in a BREAKING CHANGE:
// footer.
func TestSchemaAckNeedsEveryItemNamed(t *testing.T) {
	relabeled := strings.Replace(plain, `"name":"space"`, `"name":"spaces"`, 1)
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

// The fix a refusal names is the footer that fixes it: BREAKING CHANGE:
// for an unnamed break, SCHEMA-CHANGE: for the rest, each naming its
// items, and a message that ends with it passes.
func TestARefusalNamesTheFooterThatFixesIt(t *testing.T) {
	both := strings.Replace(renamed, `"name":"space"`, `"name":"spaces"`, 1)
	for _, tc := range []struct{ name, built, wantFix string }{
		{"a change", flipped, "SCHEMA-CHANGE: send_message: what changed"},
		{"a break", renamed, "BREAKING CHANGE: send_message: what broke"},
		{"a break and a change", both, "BREAKING CHANGE: send_message: what broke\nSCHEMA-CHANGE: gchat://spaces/{id}: what changed"},
		{"a break and a change to one tool", strings.Replace(renamed, `"destructiveHint":false`, `"destructiveHint":true`, 1),
			"BREAKING CHANGE: send_message: what broke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := compareSurfaces(readTest(t, plain), readTest(t, tc.built))
			err := checkAck(io.Discard, c, []string{"Change it"}, "base")
			if err == nil {
				t.Fatal("checkAck passed with no footer")
			}
			_, fix, _ := strings.Cut(err.Error(), "\n\n")
			if fix != tc.wantFix {
				t.Errorf("the refusal's fix is %q, want %q", fix, tc.wantFix)
			}
			if err := checkAck(io.Discard, c, []string{"Change it", "Name it\n\n" + fix}, "base"); err != nil {
				t.Errorf("checkAck = %v after the fix it named", err)
			}
		})
	}
}

// A name in backticks is taken whole, so a URI with a comma in it can be
// named; split on its comma, it names nothing.
func TestAFooterCanNameAURIWithACommaInBackticks(t *testing.T) {
	const tmpl = "gchat://spaces/{id}{?view,page}"
	was := strings.Replace(plain, `"gchat://spaces/{id}"`, `"`+tmpl+`"`, 1)
	now := strings.Replace(was, `"name":"space"`, `"name":"spaces"`, 1)
	c := compareSurfaces(readTest(t, was), readTest(t, now))
	if err := checkAck(io.Discard, c, []string{"Rename\n\nSCHEMA-CHANGE: `" + tmpl + "`: renamed"}, "base"); err != nil {
		t.Errorf("checkAck = %v, want the backticked URI to name the template", err)
	}
	if err := checkAck(io.Discard, c, []string{"Rename\n\nSCHEMA-CHANGE: " + tmpl + ": renamed"}, "base"); err == nil {
		t.Error("checkAck passed with the URI split on its comma, which names nothing")
	}
}

// A break of the released surface needs BREAKING CHANGE:; removing a tool
// added since the release breaks no caller, so it is a plain change, and
// the report lists it as one.
func TestABreakIsABreakOfARelease(t *testing.T) {
	release := readTest(t, plain)
	base := readTest(t, strings.Replace(plain, `"tools":[`, `"tools":[{"name":"draft_tool"},`, 1))
	head := readTest(t, renamed)
	c := releaseBreaksOnly(compareSurfaces(base, head), compareSurfaces(release, head).breaking)
	if !slices.Equal(c.breaking, renameBreak) {
		t.Errorf("breaking = %v, want %v", c.breaking, renameBreak)
	}
	if want := []change{{item: "draft_tool", text: "draft_tool removed"}}; !slices.Equal(c.changed, want) {
		t.Errorf("changed = %v, want %v", c.changed, want)
	}
	var report strings.Builder
	footer := "Drop and rename\n\nSCHEMA-CHANGE: draft_tool: never released\nBREAKING CHANGE: send_message: message_id is id"
	if err := checkAck(&report, c, []string{footer}, "base"); err != nil {
		t.Errorf("checkAck = %v, want both acknowledged", err)
	}
	want := "against the merge-base base: 0 added, 1 changed, 1 breaking\n" +
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
}

// The release is the baseline the base branch records. Before the first
// release there is none, and nothing breaks one.
func TestReleasedBreaksReadTheBaseBranchBaseline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initRepo(t)
	gitRun(t, "commit", "--quiet", "--allow-empty", "-m", "Start")
	head := readTest(t, renamed)

	breaks, err := releasedBreaks("HEAD", head)
	if err != nil || len(breaks) != 0 {
		t.Fatalf("releasedBreaks with no baseline = %v, %v; want none", breaks, err)
	}

	putFile(t, baselinePath, plain)
	gitRun(t, "add", baselinePath)
	gitRun(t, "commit", "--quiet", "-m", "Release")
	// The working tree's baseline is a branch's, not the release's.
	putFile(t, baselinePath, `{"tools":[]}`)
	breaks, err = releasedBreaks("HEAD", head)
	if err != nil || !slices.Equal(breaks, renameBreak) {
		t.Errorf("releasedBreaks = %v, %v; want %v", breaks, err, renameBreak)
	}
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
// configuration, so a person's hooks or signing never reach a test.
func initRepo(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitRun(t, "init", "--quiet")
	gitRun(t, "config", "user.email", "fixture@example.test")
	gitRun(t, "config", "user.name", "Fixture")
	gitRun(t, "config", "commit.gpgsign", "false")
}

// gitRun runs git in the working directory, failing the test on an error.
func gitRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// putFile writes body to path, making its directory.
func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
