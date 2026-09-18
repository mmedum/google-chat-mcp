package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each case is one line a driver could write. The gate reads files, so
// each is written to a tagged file and read back the way the real one is.
func TestTranscriptProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		// want is a fragment of the problem; empty means the line is
		// allowed through.
		want string
	}{
		{
			name: "a literal says nothing about an account",
			line: `t.Logf("posted %s", "ok")`,
		},
		{
			name: "two literals joined are still a literal",
			line: `t.Log("a message too long for one line " + "continues here")`,
		},
		{
			name: "a value through the redactor",
			line: `t.Errorf("body %s", d.redact(out.Text))`,
		},
		{
			name: "a value straight out of the account",
			line: `t.Errorf("body %s", out.Text)`,
			want: "out.Text reaches the transcript unredacted",
		},
		{
			// The hole this gate had: an unformatted call has no format
			// string, so skipping the first argument skipped the value.
			name: "an unformatted call's only argument",
			line: `t.Fatal(out.Text)`,
			want: "out.Text reaches the transcript unredacted",
		},
		{
			name: "an unformatted call whose argument is allowed",
			line: `t.Fatal(err)`,
		},
		{
			// Skip reaches the terminal like the rest, and was not in
			// the printer set before the two copies of this rule merged.
			name: "a skip carrying account content",
			line: `t.Skipf("nothing to do: %s", out.Text)`,
			want: "out.Text reaches the transcript unredacted",
		},
		{
			name: "a count cannot carry a message",
			line: `t.Logf("%d rows", len(out.Result))`,
		},
		{
			name: "the redactor reached through a wrapper",
			line: `t.Logf("%s", fmt.Sprintf("%s", clip(out.Text, 400)))`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := writeDriver(t, "func demo(t *testing.T) {\n\t"+tc.line+"\n}\n")
			calls, problems, err := transcriptProblems(file)
			if err != nil {
				t.Fatalf("transcriptProblems: %v", err)
			}
			if calls != 1 {
				t.Fatalf("read %d writes, want the one", calls)
			}
			if tc.want == "" {
				if len(problems) > 0 {
					t.Errorf("refused an allowed line: %v", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Errorf("problems = %v, want one mentioning %q", problems, tc.want)
			}
		})
	}
}

// A function named in beforeTheAccount is exempt, and only in the file
// the list names: the exemption is about what has run before the print,
// which is a property of that function and not of its name.
func TestBeforeTheAccountIsExemptWhereItIsNamed(t *testing.T) {
	const body = "func binPath(t *testing.T) {\n\tt.Fatal(out.Text)\n}\n"

	_, problems, err := transcriptProblems(writeDriver(t, body))
	if err != nil {
		t.Fatalf("transcriptProblems: %v", err)
	}
	if len(problems) != 1 {
		t.Errorf("problems = %v, want the print refused: this file is not the one the list names", problems)
	}
}

// The list is keyed on the function, and the functions it names have to
// exist: a renamed one would leave an exemption blessing nothing while
// the print it covered started failing somewhere else.
func TestBeforeTheAccountNamesFunctionsThatExist(t *testing.T) {
	for key := range beforeTheAccount {
		file, fn, ok := strings.Cut(key, ":")
		if !ok {
			t.Fatalf("%q is not file:function", key)
		}
		if !strings.Contains(readRepoFile(t, file), "func "+fn+"(") {
			t.Errorf("%s names no func %s", file, fn)
		}
	}
}

// writeDriver writes one tagged driver file, the shape the gate reads.
func writeDriver(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "driver_test.go")
	const header = "//go:build live\n\npackage demo\n\n"
	if err := os.WriteFile(file, []byte(header+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}
