package main

import (
	"encoding/json"
	"maps"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The go command's words are read as the shell splits them, its flags
// and their values skipped, so a flag never reads as the package.
func TestGoRunsInSkipsTheFlags(t *testing.T) {
	for line, want := range map[string][]goRun{
		"go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...": {
			{pkg: "golang.org/x/vuln/cmd/govulncheck@v1.8.0", args: []string{"./..."}}},
		"go run -mod=mod -modfile tools.mod -trimpath ./scripts/gates pins": {
			{pkg: "./scripts/gates", args: []string{"pins"}}},
		`CGO_ENABLED=0 go run "x.org/y@v1" && go run -C dir ./z a`: {
			{pkg: "x.org/y@v1"},
			{pkg: "./z", args: []string{"a"}}},
		`go run -ldflags "-s -w" ./scripts/gates docs`: {
			{pkg: "./scripts/gates", args: []string{"docs"}}},
		`go run -ldflags="-X a=b -s" tool.org/x@v1 x`: {
			{pkg: "tool.org/x@v1", args: []string{"x"}}},
		`go run -ldflags=-X\ a=b ./scripts/gates pins`: {
			{pkg: "./scripts/gates", args: []string{"pins"}}},
		`go run -ldflags '-X a=b c' ./scripts/gates pins`: {
			{pkg: "./scripts/gates", args: []string{"pins"}}},
		`go run -gcflags 'all=-N -l' ./cmd/x; echo done`: {
			{pkg: "./cmd/x"}},
		`$GOROOT/bin/go run x.org/y@main`: {
			{pkg: "x.org/y@main"}},
		"go run -mod":              nil,
		"cargo run x":              nil,
		`echo "go run x.org/y@v1"`: nil,
		"echo go run":              nil,
		"go vet ./...":             nil,
		// A command substitution and a subshell hold commands of their own,
		// and a quoted ")" is a word, not the end of one.
		"OUT=$(go run x.org/y@v1 ./...)": {
			{pkg: "x.org/y@v1", args: []string{"./..."}}},
		"OUT=`go run x.org/y@v1` && echo \"$(go run ./scripts/gates docs)\"": {
			{pkg: "x.org/y@v1"},
			{pkg: "./scripts/gates", args: []string{"docs"}}},
		"echo $(echo $(go run x.org/y@v1 a) b)": {
			{pkg: "x.org/y@v1", args: []string{"a"}}},
		"(go run x.org/y@v1)": {
			{pkg: "x.org/y@v1"}},
		`go run x.org/y@v1 ")" a`: {
			{pkg: "x.org/y@v1", args: []string{")", "a"}}},
		`echo '$(go run x.org/y@v1)' "\$(go run x.org/y@v2)"`: nil,
		// A substitution's end goes back to the quote it was opened in.
		`echo "$(go version) go run x.org/y@v1"`: nil,
		`X="$(date)a" go run x.org/y@v1`: {
			{pkg: "x.org/y@v1"}},
		"go run x.org/y@v1 # go run x.org/z@v2": {
			{pkg: "x.org/y@v1"}},
		// A redirection is neither an argument nor the end of the command.
		"go run ./scripts/gates smoke ./google-drive-mcp 2>&1 | tee smoke.log": {
			{pkg: "./scripts/gates", args: []string{"smoke", "./google-drive-mcp"}}},
		"2>/dev/null go run x.org/y@v1 >&2 a &>> log b": {
			{pkg: "x.org/y@v1", args: []string{"a", "b"}}},
	} {
		got := goRunsIn(line)
		if !slices.EqualFunc(got, want, func(a, b goRun) bool { return a.pkg == b.pkg && slices.Equal(a.args, b.args) }) {
			t.Errorf("goRunsIn(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestGoCommandsEndAtTheCommand(t *testing.T) {
	same := func(a, b goCommand) bool { return a.sub == b.sub && slices.Equal(a.args, b.args) }
	got := goCommands(`go install -v a.org/b@v1 c.org/d@v2 | tee log && go vet -tags=live ./...; echo go; go`)
	want := []goCommand{{"install", []string{"-v", "a.org/b@v1", "c.org/d@v2"}}, {"vet", []string{"-tags=live", "./..."}}}
	if !slices.EqualFunc(got, want, same) {
		t.Errorf("goCommands = %q, want %q", got, want)
	}
	got = goCommands("X=$(go env GOPATH)/bin; go vet ./...")
	want = []goCommand{{"env", []string{"GOPATH"}}, {"vet", []string{"./..."}}}
	if !slices.EqualFunc(got, want, same) {
		t.Errorf("goCommands = %q, want %q: a substitution's ) ends its command", got, want)
	}
}

// A flag build, run or vet reads a value for takes the next word when the
// value is written apart from it.
func TestGoAfterFlagsSkipsTheValues(t *testing.T) {
	for _, words := range [][]string{
		{"-tags", "x", "-v", "./scripts/gates"},
		{"-o", "bin/gates", "./scripts/gates"},
		{"-vettool", "/usr/local/bin/shadow", "./scripts/gates"},
		{"-c", "2", "./scripts/gates"},
		{"-printf.funcs", "Wrapf", "./scripts/gates"},
		{"-unusedresult.funcs", "pkg.F", "./scripts/gates"},
		{"-exec", "wrapper", "./scripts/gates"},
		{"-debug-trace", "/tmp/trace.json", "./scripts/gates"},
		{"-debug-actiongraph", "graph.json", "./scripts/gates"},
		{"-debug-runtime-trace", "runtime.trace", "./scripts/gates"},
	} {
		if got := goAfterFlags(words); !slices.Equal(got, []string{"./scripts/gates"}) {
			t.Errorf("goAfterFlags(%q) = %q, want [./scripts/gates]", words, got)
		}
	}
}

// goValueFlags is exactly the flags the go command reads a value for on
// the commands the gates read, run, install and vet, at the toolchain
// go.mod names: `go help build`, `go help run`, `go help vet` and the vet
// tool's own. A value flag it lacks reads its value as the package, so
// `go run -pgo file tool@v1` would run file; a boolean it holds reads the
// package as the flag's value, so `go run -race tool@v1` would run nothing.
// `go help test` is not read: no gate reads a go test's words, and there
// -c is a boolean where vet's -c takes a value.
func TestGoValueFlagsAreTheFlagsTheHelpGivesAValue(t *testing.T) {
	gomod := repoFile(t, "go.mod")
	goCmd := func(args ...string) string {
		cmd := exec.Command("go", args...)
		cmd.Dir = parityRepoRoot
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s: %v", strings.Join(args, " "), err)
		}
		return string(out)
	}
	// A GOEXPERIMENT build carries its experiments after the release.
	version := strings.FieldsFunc(goCmd("env", "GOVERSION"), func(r rune) bool { return r == '-' || r == ' ' || r == '\n' })
	if want := "go" + pinsGoVersion(string(gomod)); len(version) == 0 || version[0] != want {
		t.Skipf("go is %v, not go.mod's %s, whose help the map is held to; GOTOOLCHAIN=auto runs it", version, want)
	}
	// A flag line in the help, with the word that names its value or with
	// none, or a flag in a usage line, with the word that names its value.
	valueLine := regexp.MustCompile(`(?m)^\t-([\w.-]+) \S|\[-([\w.-]+) \w+\]`)
	boolLine := regexp.MustCompile(`(?m)^\t-([\w.-]+)$`)
	value, boolean := map[string]bool{}, map[string]bool{}
	for _, help := range []string{goCmd("help", "build"), goCmd("help", "run"), goCmd("help", "vet")} {
		for _, m := range valueLine.FindAllStringSubmatch(help, -1) {
			value[m[1]+m[2]] = true
		}
		for _, m := range boolLine.FindAllStringSubmatch(help, -1) {
			boolean[m[1]] = true
		}
	}
	var vetFlags []struct {
		Name string
		Bool bool
	}
	if err := json.Unmarshal([]byte(goCmd("tool", "vet", "-flags")), &vetFlags); err != nil {
		t.Fatal(err)
	}
	for _, f := range vetFlags {
		if f.Bool {
			boolean[f.Name] = true
		} else {
			value[f.Name] = true
		}
	}
	// The help leaves out three debug flags that take a value
	// (https://github.com/golang/go/blob/go1.27.3/src/cmd/go/internal/work/build.go#L358-L361).
	for _, name := range []string{"debug-actiongraph", "debug-runtime-trace", "debug-trace"} {
		value[name] = true
	}
	if len(value) < 25 || len(boolean) < 20 {
		t.Fatalf("read %d value flags and %d boolean flags from the help, want at least 25 and 20", len(value), len(boolean))
	}
	for _, name := range slices.Sorted(maps.Keys(value)) {
		if !goValueFlags[name] {
			t.Errorf("the help gives -%s a value, and goValueFlags does not have it", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(goValueFlags)) {
		switch {
		case value[name]:
		case boolean[name]:
			t.Errorf("goValueFlags has -%s, which the help lists as a boolean", name)
		default:
			t.Errorf("goValueFlags has -%s, which no help lists", name)
		}
	}
}

func TestShellSplit(t *testing.T) {
	for line, want := range map[string][]string{
		`a "b c" 'd e' f\ g`: {"a", "b c", "d e", "f g"},
		`"a\"b" 'a\b' "a\b"`: {`a"b`, `a\b`, `a\b`},
		"a;b&&c||d|e":        {"a", ";", "b", "&&", "c", "||", "d", "|", "e"},
		`x="" y`:             {"x=", "y"},
		"\t a \t":            {"a"},
		// A # that starts a word starts a comment; inside a word, quoted or
		// escaped, it is a character.
		"a # b c":        {"a"},
		"a;#b":           {"a", ";"},
		`a#b "#" \# ""#`: {"a#b", "#", "#", "#"},
		"${#x} y":        {"${#x}", "y"},
		"# all of it":    nil,
		"a $(b) `c` (d)": {"a", "$(", "b", ")", "`", "c", "`", "(", "d", ")"},
	} {
		if got := shellTexts(shellSplit(line)); !slices.Equal(got, want) {
			t.Errorf("shellSplit(%q) = %q, want %q", line, got, want)
		}
	}
}

// A redirection is one word of its own, its operator and its target
// joined, and neither a word of the command nor an operator that ends it
// (https://pubs.opengroup.org/onlinepubs/9799919799/utilities/V3_chap02.html#tag_19_07).
// Digits are its file descriptor only when they are the whole word, unquoted.
func TestShellSplitReadsARedirection(t *testing.T) {
	word := func(s string) shellWord { return shellWord{text: s} }
	redir := func(s string) shellWord { return shellWord{text: s, redir: true} }
	op := func(s string) shellWord { return shellWord{text: s, op: true} }
	for line, want := range map[string][]shellWord{
		"a 2>&1 b":           {word("a"), redir("2>&1"), word("b")},
		"a >&2":              {word("a"), redir(">&2")},
		"a 0<&3 <&4":         {word("a"), redir("0<&3"), redir("<&4")},
		"a &> log":           {word("a"), redir("&>log")},
		"a &>>log b":         {word("a"), redir("&>>log"), word("b")},
		"a > out >>all < in": {word("a"), redir(">out"), redir(">>all"), redir("<in")},
		"a 2> err 1>>out":    {word("a"), redir("2>err"), redir("1>>out")},
		`a> "x y"`:           {word("a"), redir(">x y")},
		`a2>x "2">y 2\>z`:    {word("a2"), redir(">x"), word("2"), redir(">y"), word("2>z")},
		"a 2>&1 | b; c":      {word("a"), redir("2>&1"), op("|"), word("b"), op(";"), word("c")},
		"a &>b && c &":       {word("a"), redir("&>b"), op("&&"), word("c"), op("&")},
		`echo ">" '2>&1'`:    {word("echo"), word(">"), word("2>&1")},
		"a > # b":            {word("a"), redir(">")},
		"a >":                {word("a"), redir(">")},
	} {
		if got := shellSplit(line); !slices.Equal(got, want) {
			t.Errorf("shellSplit(%q) = %+v, want %+v", line, got, want)
		}
	}
}

// A line's lists split at ;, &, && and ||, and a pipeline, a substitution
// and a quoted operator stay inside the command that holds them.
func TestShellListsSplitAtTheListOperators(t *testing.T) {
	for line, want := range map[string][]string{
		"a && b || c; d & e | f": {"a", "b", "c", "d", "e | f"},
		"x=$(a b) y; ; z;":       {"x= $( a b ) y", "z"},
		`echo "a && b"`:          {"echo a && b"},
		"a &> log; b 2>&1 & c":   {"a &>log", "b 2>&1", "c"},
		"":                       nil,
	} {
		var got []string
		for _, c := range shellLists(shellSplit(line)) {
			got = append(got, strings.Join(shellTexts(c), " "))
		}
		if !slices.Equal(got, want) {
			t.Errorf("shellLists(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestShellSwallowed(t *testing.T) {
	for line, want := range map[string]string{
		"make leaks":                     "",
		"make leaks && make pins":        "",
		"make leaks || true":             "`|| true` passes whatever came before",
		"make leaks || :":                "`|| :` passes whatever came before",
		"make leaks || exit 0":           "`|| exit 0` passes whatever came before",
		"make leaks || exit 1":           "",
		"make leaks; true":               "`; true` ends it with success whatever came before",
		"make leaks; make pins":          "",
		"true; make leaks":               "",
		"make leaks &":                   "& runs a command in the background, where its failure is never seen",
		"! make leaks":                   "! turns a failure into success",
		"set +e; make leaks":             "set +e lets the commands after it fail",
		"set -euo pipefail; make leaks":  "",
		"make leaks 2>&1 | tee log":      "",
		"x=$(make leaks || true)":        "`|| true` passes whatever came before",
		"out=$(gofmt -l .); exit 1":      "",
		"echo 'make x || true'":          "",
		"make leaks # || true, not here": "",
	} {
		if got := shellSwallowed(line); got != want {
			t.Errorf("shellSwallowed(%q) = %q, want %q", line, got, want)
		}
	}
}
