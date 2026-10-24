package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// define builds Settings the way the command line does, from a fake
// environment, then applies the given arguments.
func define(t *testing.T, env map[string]string, args ...string) *Settings {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	s := Define(fs, func(k string) string { return env[k] })
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

// definedFloor is how many variables Define reads today, on purpose: the
// floor moves with it, so a Define that reads nothing cannot pass.
const definedFloor = 18

// A profile name is held to ProfileRule, and the refusal says it. Only A
// to Z are lowered: a character Unicode folds into a to z, such as the
// Kelvin sign, is refused rather than renamed to another profile.
func TestProfileRule(t *testing.T) {
	if ProfileRule != "up to 64 characters from a to z, 0 to 9, - and _, starting with a letter or digit" {
		t.Errorf("ProfileRule = %q", ProfileRule)
	}
	for _, tc := range []struct{ name, want string }{
		{"Work", "work"}, {" work ", "work"}, {"a", "a"}, {"9-", "9-"}, {"a-b_c", "a-b_c"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	} {
		if c, err := define(t, map[string]string{"GCM_PROFILE": tc.name}).Build(); err != nil || c.Profile != tc.want {
			t.Errorf("GCM_PROFILE=%q: profile %q, %v; want %q", tc.name, c.Profile, err, tc.want)
		}
	}
	for _, name := range []string{strings.Repeat("a", 65), "-a", "_a", "a.b", "a b", "caf\u00e9", "\u212aey"} {
		_, err := define(t, map[string]string{"GCM_PROFILE": name}).Build()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must be "+ProfileRule) {
			t.Errorf("GCM_PROFILE=%q: %v; want it refused, naming the rule", name, err)
		}
	}
}

// Load is Define, Parse and Build: a flag is read, and an unknown one
// refused.
func TestLoad(t *testing.T) {
	c, err := Load([]string{"-read-only", "true"}, func(string) string { return "" })
	if err != nil || !c.ReadOnly {
		t.Errorf("Load = %+v, %v; want read-only", c, err)
	}
	if _, err := Load([]string{"-unknown"}, func(string) string { return "" }); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

// EnvVars is every variable the server reads, the removed ones and the
// three read outside Define included.
func TestEnvVars(t *testing.T) {
	want := []string{
		"GCM_ALLOW_DESTRUCTIVE", "GCM_ASK_BEFORE_SEND", "GCM_CHAT_API_BASE", "GCM_CLIENT_SECRET",
		"GCM_CLOUD_IDENTITY_API_BASE", "GCM_CONFIG_DIR", "GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME",
		"GCM_DIRECTORY_CACHE_TTL_SECONDS", "GCM_ENABLE_DESTRUCTIVE", "GCM_HTTP_MAX_RETRIES",
		"GCM_HTTP_TIMEOUT_SECONDS", "GCM_LOCAL_DIR", "GCM_LOG_FORMAT", "GCM_LOG_LEVEL", "GCM_PEOPLE_API_BASE",
		"GCM_PROFILE", "GCM_READ_ONLY", "GCM_REFRESH_TOKEN", "GCM_REQUIRE_PROMPT", "GCM_SEARCH_MAX_PAGES",
		"GCM_TOOLSETS",
	}
	if got := EnvVars(); !slices.Equal(got, want) {
		t.Errorf("EnvVars =\n%v\nwant\n%v", got, want)
	}
}

// notRead is the GCM_ names tests spell for a variable the server does
// not read, each without the prefix: a setting a gate is shown refusing,
// a near miss the staleness gate is shown catching, and one a release
// stopped reading. Each is joined to EnvPrefix through a variable, so
// this list spells none of them itself.
var notRead = []string{
	"ADDED_LATER", "ASKED", "CONFIG_DIRECTORY", "GONE", "INTERACTION_HINT", "LEFT_OUT", "LOCALDIR",
	"NOT_READ", "REMOVED", "TOOLSET",
}

// Every GCM_ name the code spells is one EnvVars lists, so a variable
// read outside Define cannot escape the bundle and staleness gates. Test
// files are read too: the smoke test, the live driver, the evals and the
// command tests set the variables of the server they start, and a
// misspelling there tests a configuration nobody asked for. A test may
// spell a name notRead lists, and nothing else may; each one is spelled
// by a test and is not in EnvVars, so the list cannot go stale. The scan
// must find a name outside this package in the code and in the tests of
// each root, or it read too little.
func TestEveryVariableTheCodeNamesIsInEnvVars(t *testing.T) {
	known := map[string]bool{}
	for _, name := range EnvVars() {
		known[name] = true
	}
	unread := map[string]bool{}
	for _, key := range notRead {
		unread[EnvPrefix+key] = true
	}
	spelledByTests := map[string]bool{}
	for _, root := range []string{"../../cmd", "../../internal", "../../scripts"} {
		elsewhere := map[bool]int{}
		for _, s := range spellings(t, root) {
			if s.dir != filepath.Join("..", "..", "internal", "config") {
				elsewhere[s.test]++
			}
			switch {
			case known[s.name]:
			case unread[s.name] && s.test:
				spelledByTests[s.name] = true
			case unread[s.name]:
				t.Errorf("%s names %s, which notRead lists for a test to spell", s.at, s.name)
			default:
				t.Errorf("%s names %s, which EnvVars does not list", s.at, s.name)
			}
		}
		if elsewhere[false] == 0 || elsewhere[true] == 0 {
			t.Errorf("the scan found %d GCM_ names in the code under %s and %d in its tests, outside "+
				"internal/config; it read too little", elsewhere[false], root, elsewhere[true])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(unread)) {
		if known[name] {
			t.Errorf("notRead lists %s, which the server reads", name)
		}
		if !spelledByTests[name] {
			t.Errorf("notRead lists %s, which no test spells", name)
		}
	}
}

// The scan reads a test file as it reads code, and a + chain in order: a
// run of literals that another operand continues ends in a prefix, not a
// name, unless something ends the name inside the run.
func TestTheNameScanReadsChainsInOrder(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"code.go": "package p\n" +
			"var (\n" +
			"\tprefix = EnvPrefix + \"ENABLE_\" + kind\n" +
			"\tended  = \"set \" + kind + EnvPrefix + \"GONE\"\n" +
			"\tspaced = config.EnvPrefix + \"NOT_READ \" + kind\n" +
			")\n",
		"code_test.go": "package p\n" +
			"func init() { os.Setenv(EnvPrefix+\"ADDED_LATER\", \"\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, s := range spellings(t, root) {
		got = append(got, fmt.Sprintf("%s:%d %s test=%t", filepath.Base(s.at.Filename), s.at.Line, s.name, s.test))
	}
	want := []string{"code.go:4 GCM_GONE test=false", "code.go:5 GCM_NOT_READ test=false",
		"code_test.go:2 GCM_ADDED_LATER test=true"}
	if !slices.Equal(got, want) {
		t.Errorf("spellings =\n%q\nwant\n%q", got, want)
	}
}

// spelling is a GCM_ name a Go file spells: where, in which directory,
// and whether the file is a test.
type spelling struct {
	name string
	at   token.Position
	dir  string
	test bool
}

// spellings is every GCM_ name the Go files under root spell, test files
// included, in the order the walk reads them. A name is seen as a string
// literal, or as a run of literals and EnvPrefix joined by +; one built
// from any other constant, or through a format verb, is not seen. A run
// that another operand continues is building a name, so its last word is
// a prefix rather than a name when nothing ends it inside the run:
// EnvPrefix + "ENABLE_" + kind spells none.
func spellings(t *testing.T, root string) []spelling {
	t.Helper()
	name := regexp.MustCompile(`^GCM_[A-Z0-9][A-Z0-9_]*$`)
	inName := func(r rune) bool { return r == '_' || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') }
	var out []spelling
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// spell records the names s spells; continued is another operand
		// following s in its chain.
		spell := func(s string, at token.Pos, continued bool) {
			words := strings.FieldsFunc(s, func(r rune) bool { return !inName(r) })
			if continued && s != "" && inName(rune(s[len(s)-1])) {
				words = words[:len(words)-1]
			}
			for _, word := range words {
				if name.MatchString(word) {
					out = append(out, spelling{name: word, at: fset.Position(at), dir: filepath.Dir(path),
						test: strings.HasSuffix(path, "_test.go")})
				}
			}
		}
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if n.Op != token.ADD {
					return true
				}
				// A + chain is read in order: each run of literals and
				// EnvPrefix is one string, and any other operand
				// continues the run before it and is read on its own.
				run, at := "", n.Pos()
				for _, op := range operands(n) {
					if s, ok := joined(op); ok {
						if run == "" {
							at = op.Pos()
						}
						run += s
						continue
					}
					spell(run, at, true)
					run = ""
					ast.Inspect(op, visit)
				}
				spell(run, at, false)
				return false
			case *ast.BasicLit:
				if s, err := strconv.Unquote(n.Value); n.Kind == token.STRING && err == nil {
					spell(s, n.Pos(), false)
				}
			}
			return true
		}
		ast.Inspect(f, visit)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// operands is a + chain's operands in order, through parentheses.
func operands(e ast.Expr) []ast.Expr {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return append(operands(e.X), operands(e.Y)...)
		}
	case *ast.ParenExpr:
		if b, ok := e.X.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			return operands(b)
		}
	}
	return []ast.Expr{e}
}

// joined is a + chain of string literals and EnvPrefix as one string.
func joined(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := joined(e.X)
		if !ok {
			return "", false
		}
		y, ok := joined(e.Y)
		return x + y, ok
	case *ast.ParenExpr:
		return joined(e.X)
	case *ast.BasicLit:
		s, err := strconv.Unquote(e.Value)
		return s, e.Kind == token.STRING && err == nil
	case *ast.Ident:
		return EnvPrefix, e.Name == "EnvPrefix"
	case *ast.SelectorExpr:
		return EnvPrefix, e.Sel.Name == "EnvPrefix"
	}
	return "", false
}

func TestDefaults(t *testing.T) {
	c, err := define(t, nil).Build()
	if err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if c.Profile != "default" || c.LogLevel != LogInfo || c.LogFormat != LogJSON {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if c.ReadOnly {
		t.Error("read-only must default off")
	}
	if c.EnableDestructive {
		t.Error("the destructive tools must be unregistered by default: a deleted Chat message has no trash " +
			"behind it, and an opened space or a new manager cannot be taken back")
	}
	if !slices.Equal(c.Toolsets, defaultToolsets) {
		t.Errorf("toolsets = %v, want every toolset but admin", c.Toolsets)
	}
	if slices.Contains(c.Toolsets, ToolsetAdmin) {
		t.Error("admin is on by default, so every consent screen carries an administrator's scope")
	}
	if c.HTTPTimeout != 10*time.Second || c.HTTPMaxRetries != 3 {
		t.Errorf("http defaults = %s / %d", c.HTTPTimeout, c.HTTPMaxRetries)
	}
	if c.DirectoryCacheTTL != 24*time.Hour {
		t.Errorf("cache ttl = %s", c.DirectoryCacheTTL)
	}
	if c.SearchMaxPages != 10 {
		t.Errorf("search max pages = %d, want the default 10", c.SearchMaxPages)
	}
	if c.ChatAPIBase != DefaultChatAPIBase || c.PeopleAPIBase != DefaultPeopleAPIBase {
		t.Errorf("api bases = %s / %s", c.ChatAPIBase, c.PeopleAPIBase)
	}
}

func TestEnvironmentIsRead(t *testing.T) {
	env := map[string]string{
		"GCM_PROFILE":       "work",
		"GCM_LOG_LEVEL":     "debug",
		"GCM_LOG_FORMAT":    "text",
		"GCM_READ_ONLY":     "true",
		"GCM_CHAT_API_BASE": "http://127.0.0.1:8080/v1/",
	}
	c, err := define(t, env).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if c.Profile != "work" || c.LogLevel != LogDebug || c.LogFormat != LogText || !c.ReadOnly {
		t.Errorf("environment not applied: %+v", c)
	}
	if c.ChatAPIBase != "http://127.0.0.1:8080/v1" {
		t.Errorf("trailing slash not trimmed: %q", c.ChatAPIBase)
	}
}

// A flag on the command line beats the environment. Clients pass env,
// so a person debugging by hand needs the flag to win.
func TestFlagOverridesEnvironment(t *testing.T) {
	env := map[string]string{"GCM_LOG_LEVEL": "error"}
	c, err := define(t, env, "-log-level", "debug").Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if c.LogLevel != LogDebug {
		t.Errorf("log level = %q, want the flag to win", c.LogLevel)
	}
}

func TestSecondsAreAcceptedWithoutAUnit(t *testing.T) {
	// The variables are named ..._SECONDS and held integers before the
	// rewrite, so a bare number must keep working.
	env := map[string]string{
		"GCM_HTTP_TIMEOUT_SECONDS":        "30",
		"GCM_DIRECTORY_CACHE_TTL_SECONDS": "86400",
	}
	c, err := define(t, env).Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if c.HTTPTimeout != 30*time.Second {
		t.Errorf("timeout = %s, want 30s", c.HTTPTimeout)
	}
	if c.DirectoryCacheTTL != 24*time.Hour {
		t.Errorf("ttl = %s, want 24h", c.DirectoryCacheTTL)
	}
}

func TestToolsets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  []Toolset
	}{
		{"all", "all", defaultToolsets},
		{"empty means all", "", defaultToolsets},
		{"admin is had by naming it", "admin", []Toolset{ToolsetCore, ToolsetAdmin}},
		{"all and admin is everything", "all,admin", []Toolset{
			ToolsetCore, ToolsetSections, ToolsetPins, ToolsetEmoji, ToolsetEvents,
			ToolsetReadState, ToolsetSettings, ToolsetAvailability, ToolsetAdmin,
		}},
		{"named", "pins,emoji", []Toolset{ToolsetCore, ToolsetPins, ToolsetEmoji}},
		{"core is implied", "pins", []Toolset{ToolsetCore, ToolsetPins}},
		{"whitespace and case", " Pins , EMOJI ", []Toolset{ToolsetCore, ToolsetPins, ToolsetEmoji}},
		{"duplicates collapse", "pins,pins", []Toolset{ToolsetCore, ToolsetPins}},
		{"registration order, not input order", "emoji,pins", []Toolset{ToolsetCore, ToolsetPins, ToolsetEmoji}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := define(t, map[string]string{"GCM_TOOLSETS": tc.value}).Build()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !slices.Equal(c.Toolsets, tc.want) {
				t.Errorf("toolsets = %v, want %v", c.Toolsets, tc.want)
			}
			if !c.Enabled(ToolsetCore) {
				t.Error("core must always be enabled")
			}
		})
	}
}

func TestInvalidValuesAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"profile", map[string]string{"GCM_PROFILE": "Not Valid"}, "profile"},
		{"log level", map[string]string{"GCM_LOG_LEVEL": "verbose"}, "log level"},
		{"log format", map[string]string{"GCM_LOG_FORMAT": "xml"}, "log format"},
		{"read only", map[string]string{"GCM_READ_ONLY": "maybe"}, "read-only"},
		{"toolset", map[string]string{"GCM_TOOLSETS": "pins,nope"}, "unknown toolsets"},
		{"retries not a number", map[string]string{"GCM_HTTP_MAX_RETRIES": "many"}, "http max retries"},
		{"retries out of range", map[string]string{"GCM_HTTP_MAX_RETRIES": "99"}, "http max retries"},
		{"timeout out of range", map[string]string{"GCM_HTTP_TIMEOUT_SECONDS": "0"}, "http-timeout"},
		{"timeout unparsable", map[string]string{"GCM_HTTP_TIMEOUT_SECONDS": "soon"}, "http-timeout"},
		{"api base not a url", map[string]string{"GCM_CHAT_API_BASE": "chat.googleapis.com"}, "chat-api-base"},
		{"api base empty", map[string]string{"GCM_CHAT_API_BASE": "/"}, "chat-api-base must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := define(t, tc.env).Build()
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A removed setting starts when off or unset and is refused otherwise,
// saying what to do instead: ignored, it would quietly leave the server
// otherwise than its owner set it. GCM_INTERACTION_HINT is not read at
// all. The flags of the first two are gone.
func TestARemovedSettingIsRefusedUnlessOff(t *testing.T) {
	for _, env := range []map[string]string{
		nil,
		{"GCM_REQUIRE_PROMPT": "false", "GCM_ASK_BEFORE_SEND": " FALSE "},
		{"GCM_REQUIRE_PROMPT": "0", "GCM_ASK_BEFORE_SEND": ""},
		{"GCM_ALLOW_DESTRUCTIVE": "false"},
		{"GCM_ALLOW_DESTRUCTIVE": ""},
		{"GCM_INTERACTION_HINT": "false"},
		{"GCM_INTERACTION_HINT": "maybe"},
	} {
		if _, err := define(t, env).Build(); err != nil {
			t.Errorf("%v: %v", env, err)
		}
	}
	for _, tc := range []struct{ key, value, removed, instead string }{
		{"GCM_REQUIRE_PROMPT", "true", "GCM_REQUIRE_PROMPT was removed", "GCM_ENABLE_DESTRUCTIVE off"},
		{"GCM_REQUIRE_PROMPT", "maybe", "GCM_REQUIRE_PROMPT was removed", "GCM_ENABLE_DESTRUCTIVE off"},
		{"GCM_ASK_BEFORE_SEND", "true", "GCM_ASK_BEFORE_SEND was removed", "send_message and update_message"},
		{"GCM_ALLOW_DESTRUCTIVE", "true", "GCM_ALLOW_DESTRUCTIVE (--allow-destructive) was removed",
			"GCM_ENABLE_DESTRUCTIVE (--enable-destructive)"},
		{"GCM_ALLOW_DESTRUCTIVE", "maybe", "GCM_ALLOW_DESTRUCTIVE (--allow-destructive) was removed",
			"GCM_ENABLE_DESTRUCTIVE (--enable-destructive)"},
	} {
		_, err := define(t, map[string]string{tc.key: tc.value}).Build()
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.removed) ||
			!strings.Contains(err.Error(), tc.instead) {
			t.Errorf("%s=%s: %v", tc.key, tc.value, err)
		}
		if err != nil && strings.Contains(err.Error(), tc.value) {
			t.Errorf("%s=%s: the refusal echoes the value: %v", tc.key, tc.value, err)
		}
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	Define(fs, func(string) string { return "" })
	for _, name := range []string{"require-prompt", "ask-before-send", "interaction-hint"} {
		if fs.Lookup(name) != nil {
			t.Errorf("--%s is still a flag", name)
		}
	}
}

// The destructive switch's old names answer as a removed setting does,
// as a variable and as a flag: off starts and does nothing, and on or
// not a boolean is refused, beside the new name too, naming the new
// one. The flag beats the variable, as every flag does.
func TestTheDestructiveSwitchsOldNamesNameTheNewOnes(t *testing.T) {
	const refused = "config: invalid: GCM_ALLOW_DESTRUCTIVE (--allow-destructive) was removed: " +
		"it is GCM_ENABLE_DESTRUCTIVE (--enable-destructive) now. Unset it, and set that in its place"
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		// want is the refusal, or empty for a run that starts.
		want string
		// on is EnableDestructive in a run that starts: the new switch's.
		on bool
	}{
		{name: "the variable on, beside the new one",
			env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "true", "GCM_ALLOW_DESTRUCTIVE": "true"}, want: refused},
		{name: "the flag on", args: []string{"-allow-destructive=true"}, want: refused},
		{name: "the flag not a boolean", args: []string{"-allow-destructive=maybe"}, want: refused},
		{name: "the flag on, beside the new one", args: []string{"-enable-destructive=true", "-allow-destructive=1"},
			want: refused},
		{name: "the flag on over the variable off", env: map[string]string{"GCM_ALLOW_DESTRUCTIVE": "false"},
			args: []string{"-allow-destructive=true"}, want: refused},
		// Off, the old name is ignored, and the new switch decides, unset
		// as well as on.
		{name: "the flag off, the new one unset", args: []string{"-allow-destructive=false"}, on: false},
		{name: "the variable off, the new one unset", env: map[string]string{"GCM_ALLOW_DESTRUCTIVE": "false"},
			on: false},
		{name: "the flag off leaves the new one on", env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "true"},
			args: []string{"-allow-destructive=false"}, on: true},
		{name: "the flag off over the variable on is ignored, and the new one decides",
			env:  map[string]string{"GCM_ALLOW_DESTRUCTIVE": "true", "GCM_ENABLE_DESTRUCTIVE": "true"},
			args: []string{"-allow-destructive=false"}, on: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := define(t, tc.env, tc.args...).Build()
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Errorf("got %v\nwant %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if c.EnableDestructive != tc.on {
				t.Errorf("EnableDestructive = %t, want the new switch's %t", c.EnableDestructive, tc.on)
			}
		})
	}
}

// --help shows the old flag as removed and what to use, with no
// default: not a live option, and not the variable's value.
func TestTheRemovedFlagsHelpSaysWhatToUse(t *testing.T) {
	var help bytes.Buffer
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&help)
	Define(fs, func(name string) string {
		if name == "GCM_ALLOW_DESTRUCTIVE" {
			return "true"
		}
		return ""
	})
	fs.PrintDefaults()
	const want = "  -allow-destructive string\n    \tremoved; use --enable-destructive [env GCM_ALLOW_DESTRUCTIVE]\n"
	if !strings.Contains(help.String(), want) {
		t.Errorf("--help does not say %q:\n%s", want, help.String())
	}
}

// The destructive tools register with the switch on and read-only off.
// Read-only wins over the switch, so registration, the server's
// instructions and status read this one answer.
func TestDestructiveRegisters(t *testing.T) {
	for _, tc := range []struct {
		enable, readOnly, want bool
	}{
		{enable: false, readOnly: false, want: false},
		{enable: true, readOnly: false, want: true},
		{enable: true, readOnly: true, want: false},
		{enable: false, readOnly: true, want: false},
	} {
		c := Config{EnableDestructive: tc.enable, ReadOnly: tc.readOnly}
		if got := c.DestructiveRegisters(); got != tc.want {
			t.Errorf("EnableDestructive %t, ReadOnly %t: DestructiveRegisters = %t, want %t",
				tc.enable, tc.readOnly, got, tc.want)
		}
	}
}

// The destructive switch is GCM_ENABLE_DESTRUCTIVE and
// --enable-destructive alone. Its old name off does nothing.
func TestTheDestructiveSwitch(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		on   bool
	}{
		{name: "unset", on: false},
		{name: "the variable on", env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "true"}, on: true},
		{name: "the variable off", env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "false"}, on: false},
		{name: "the flag", args: []string{"-enable-destructive=true"}, on: true},
		{name: "the flag over the variable", env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "true"},
			args: []string{"-enable-destructive=false"}, on: false},
		{name: "the old name off beside the new on",
			env: map[string]string{"GCM_ENABLE_DESTRUCTIVE": "true", "GCM_ALLOW_DESTRUCTIVE": "false"}, on: true},
		{name: "the old name off alone", env: map[string]string{"GCM_ALLOW_DESTRUCTIVE": "false"}, on: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := define(t, tc.env, tc.args...).Build()
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if c.EnableDestructive != tc.on {
				t.Errorf("EnableDestructive = %t, want %t", c.EnableDestructive, tc.on)
			}
		})
	}
}

// One run should report every problem, not just the first.
func TestAllProblemsAreReportedAtOnce(t *testing.T) {
	_, err := define(t, map[string]string{
		"GCM_LOG_LEVEL":  "verbose",
		"GCM_LOG_FORMAT": "xml",
		"GCM_PROFILE":    "Not Valid",
	}).Build()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"log level", "log format", "profile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestLogLevelSlog(t *testing.T) {
	for level, want := range map[LogLevel]slog.Level{
		LogDebug: slog.LevelDebug,
		LogInfo:  slog.LevelInfo,
		LogWarn:  slog.LevelWarn,
		LogError: slog.LevelError,
		"":       slog.LevelInfo,
	} {
		if got := level.Slog(); got != want {
			t.Errorf("%q.Slog() = %v, want %v", level, got, want)
		}
	}
}

func TestNewLoggerHonorsFormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(Config{LogLevel: LogInfo, LogFormat: LogJSON}, &buf).Info("hello", "k", "v")
	if !strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("json format did not produce JSON: %q", buf.String())
	}

	buf.Reset()
	NewLogger(Config{LogLevel: LogInfo, LogFormat: LogText}, &buf).Info("hello")
	if strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("text format produced JSON: %q", buf.String())
	}

	buf.Reset()
	NewLogger(Config{LogLevel: LogError, LogFormat: LogJSON}, &buf).Info("suppressed")
	if buf.Len() != 0 {
		t.Errorf("info logged at error level: %q", buf.String())
	}
}

// The page cap is what stops one search from spending a caller's whole
// quota, so a value outside the range is a configuration error rather
// than something to clamp silently.
func TestSearchMaxPagesIsValidated(t *testing.T) {
	base := map[string]string{}
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"1", true}, {"10", true}, {"50", true},
		{"0", false}, {"51", false}, {"-1", false}, {"lots", false},
	} {
		cfg, err := define(t, base, "-search-max-pages", tc.value).Build()
		if tc.ok {
			if err != nil {
				t.Errorf("search-max-pages %q: %v", tc.value, err)
			} else if cfg.SearchMaxPages == 0 {
				t.Errorf("search-max-pages %q was not read", tc.value)
			}
			continue
		}
		if err == nil {
			t.Errorf("search-max-pages %q was accepted", tc.value)
		}
	}
}

// The numeric settings take their bounds and refuse one step past them.
func TestNumericSettingsTakeTheirExactBounds(t *testing.T) {
	for _, tc := range []struct {
		env, value string
		ok         bool
	}{
		{"GCM_HTTP_MAX_RETRIES", "0", true},
		{"GCM_HTTP_MAX_RETRIES", "10", true},
		{"GCM_HTTP_MAX_RETRIES", "-1", false},
		{"GCM_HTTP_MAX_RETRIES", "11", false},
		{"GCM_HTTP_TIMEOUT_SECONDS", "1", true},
		{"GCM_HTTP_TIMEOUT_SECONDS", "600", true},
		{"GCM_HTTP_TIMEOUT_SECONDS", "0.5", false},
		{"GCM_HTTP_TIMEOUT_SECONDS", "601", false},
		{"GCM_DIRECTORY_CACHE_TTL_SECONDS", "60", true},
		{"GCM_DIRECTORY_CACHE_TTL_SECONDS", "31536000", true},
		{"GCM_DIRECTORY_CACHE_TTL_SECONDS", "59", false},
		{"GCM_DIRECTORY_CACHE_TTL_SECONDS", "31536001", false},
	} {
		_, err := define(t, map[string]string{tc.env: tc.value}).Build()
		if (err == nil) != tc.ok {
			t.Errorf("%s=%s: err = %v, want accepted %v", tc.env, tc.value, err, tc.ok)
		}
	}
}

// A directory that does not exist used to be accepted, because only a
// relative path was refused. The typo then surfaced at the moment
// somebody tried to move a file, a long way from the setting that caused
// it and invisible to `status`.
func TestTheDirectoryMustExistAndBeADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir string
		ok        bool
	}{
		{"a real directory", dir, true},
		{"unset is allowed and turns the feature off", "", true},
		{"a path that does not exist", filepath.Join(dir, "nope"), false},
		{"a file rather than a directory", file, false},
		{"a relative path", filepath.Join("relative", "path"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := define(t, map[string]string{"GCM_LOCAL_DIR": tc.dir}).Build()
			if tc.ok {
				if err != nil {
					t.Errorf("%q was refused: %v", tc.dir, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q was accepted; the failure would surface at the first file operation", tc.dir)
			}
			if !strings.Contains(err.Error(), "local dir") {
				t.Errorf("the error does not name the setting: %v", err)
			}
		})
	}
}

// A removed setting that keeps its flag says what to use in its place,
// in --help and in its refusal alike, and only such a setting names one.
func TestARemovedFlagNamesItsReplacement(t *testing.T) {
	flags := 0
	for _, r := range removedSettings {
		switch {
		case r.flag == "" && r.use != "":
			t.Errorf("%s names %s to use, and has no flag whose help would say so", r.key, r.use)
		case r.flag == "":
		case r.use == "":
			t.Errorf("--%s has no use, so --help would say %q", r.flag, "removed; use ")
		case !strings.Contains(r.why, r.use):
			t.Errorf("--%s's refusal does not name %s, which its help does: %q", r.flag, r.use, r.why)
		default:
			flags++
		}
	}
	if flags != 1 {
		t.Errorf("%d removed settings keep a flag, want the one: --allow-destructive", flags)
	}
}

// Define reads each variable once, and records each once for Vars. Two
// settings on one variable would leave it to whichever Build reads
// last.
func TestDefineReadsEachVariableOnce(t *testing.T) {
	reads := map[string]int{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	Define(fs, func(name string) string { reads[name]++; return "" })
	var listed []string
	for _, v := range defined() {
		listed = append(listed, v.Name)
	}
	slices.Sort(listed)
	if len(listed) < definedFloor {
		t.Fatalf("Define records %d settings, want at least %d", len(listed), definedFloor)
	}
	if read := slices.Sorted(maps.Keys(reads)); !slices.Equal(listed, read) {
		t.Errorf("Define records %v\nDefine reads %v", listed, read)
	}
	for name, n := range reads {
		if n != 1 {
			t.Errorf("Define reads %s %d times", name, n)
		}
	}
	// A variable Define reads and something outside it reads too would
	// be read under two rules; EnvVars lists it twice, so this sees it.
	if names := EnvVars(); !slices.Equal(names, slices.Compact(slices.Clone(names))) {
		t.Errorf("EnvVars names a variable twice: %v", names)
	}
}

// Vars is what the bundle gate reads a setting's kind from, so each
// claim it makes is held to what Build does. A switch takes true and
// false and refuses anything else, and nothing else does that.
func TestVarsDescribeWhatBuildDoes(t *testing.T) {
	vars := Vars()
	if len(vars) < 15 {
		t.Fatalf("Vars has %d settings; Define registers more than that", len(vars))
	}
	switches := 0
	for _, v := range vars {
		accepts := func(value string) bool {
			_, err := define(t, map[string]string{v.Name: value}).Build()
			return err == nil
		}
		if boolean := accepts("true") && accepts("false") && !accepts("maybe"); boolean != v.Switch {
			t.Errorf("%s: Switch is %t, and Build reads it as a boolean: %t", v.Name, v.Switch, boolean)
		}
		if v.Switch {
			switches++
		}
		if removed := slices.ContainsFunc(removedSettings, func(r struct{ key, flag, use, why string }) bool {
			return v.Name == EnvPrefix+r.key
		}); removed != v.Removed {
			t.Errorf("%s: Removed is %t, and it is a removed setting: %t", v.Name, v.Removed, removed)
		}
	}
	if switches != 2 {
		t.Errorf("%d switches, want read-only and the destructive switch", switches)
	}
}
