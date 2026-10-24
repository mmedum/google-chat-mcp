package config

import (
	"bytes"
	"errors"
	"flag"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
		{"api base empty", map[string]string{"GCM_CHAT_API_BASE": "  "}, "chat-api-base"},
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

// Define reads each variable once, and Vars lists each once. Two
// settings on one variable would leave it to whichever Build reads
// last, and a duplicate in Vars would ask the bundle for it twice.
func TestDefineReadsEachVariableOnce(t *testing.T) {
	reads := map[string]int{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	Define(fs, func(name string) string { reads[name]++; return "" })
	var listed []string
	for _, v := range Vars() {
		listed = append(listed, v.Name)
	}
	// 18 is the setting count today, on purpose: the floor moves with it,
	// so a Define that reads nothing cannot pass.
	if len(listed) < 18 {
		t.Fatalf("Vars lists %d settings, want at least 18", len(listed))
	}
	if read := slices.Sorted(maps.Keys(reads)); !slices.Equal(listed, read) {
		t.Errorf("Vars lists %v\nDefine reads %v", listed, read)
	}
	for name, n := range reads {
		if n != 1 {
			t.Errorf("Define reads %s %d times", name, n)
		}
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
