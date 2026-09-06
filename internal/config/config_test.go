package config

import (
	"bytes"
	"errors"
	"flag"
	"log/slog"
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
	if c.RefuseDeletes {
		t.Error("the delete tools must be registered by default")
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

func TestNewLoggerHonoursFormatAndLevel(t *testing.T) {
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

func TestSearchMaxPagesDefaults(t *testing.T) {
	cfg, err := define(t, nil).Build()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SearchMaxPages != 10 {
		t.Errorf("search max pages = %d, want the default 10", cfg.SearchMaxPages)
	}
}
