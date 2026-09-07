// Package config loads and validates runtime configuration.
//
// Environment variables (GCM_*) are the source of truth because every MCP
// client passes only command, args and env to a stdio server. Each
// setting also has a flag bound to the same name; a flag given on the
// command line overrides the environment, and the environment overrides
// the built-in default. Validation runs once at start so a misconfigured
// server fails before it announces itself.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is prepended to every environment variable name.
const EnvPrefix = "GCM_"

// LogLevel is a typed enum constrained at load time.
type LogLevel string

// Allowed LogLevel values.
const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Slog returns the slog.Level for this level.
func (l LogLevel) Slog() slog.Level {
	switch l {
	case LogDebug:
		return slog.LevelDebug
	case LogWarn:
		return slog.LevelWarn
	case LogError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LogFormat is a typed enum constrained at load time.
type LogFormat string

// Allowed LogFormat values.
const (
	LogText LogFormat = "text"
	LogJSON LogFormat = "json"
)

// Toolset names a group of tools that can be registered or left out.
// Fifty-three tools is a lot for a client that loads them all, so a
// person can narrow the surface without losing the binary.
type Toolset string

// Toolsets. Core is the surface this server has always had.
const (
	ToolsetCore         Toolset = "core"
	ToolsetSections     Toolset = "sections"
	ToolsetPins         Toolset = "pins"
	ToolsetEmoji        Toolset = "emoji"
	ToolsetEvents       Toolset = "events"
	ToolsetReadState    Toolset = "readstate"
	ToolsetSettings     Toolset = "settings"
	ToolsetAvailability Toolset = "availability"
	ToolsetAdmin        Toolset = "admin"
)

// AllToolsets is every toolset a person may name, in registration
// order. It is not what "all" registers: see defaultToolsets.
var AllToolsets = []Toolset{
	ToolsetCore,
	ToolsetSections,
	ToolsetPins,
	ToolsetEmoji,
	ToolsetEvents,
	ToolsetReadState,
	ToolsetSettings,
	ToolsetAvailability,
	ToolsetAdmin,
}

// defaultToolsets is what "all" means, which is everything but admin.
//
// The admin tools search spaces the caller is not in, and only a
// Workspace administrator can use them. Turning the set on also adds an
// administrator's scope to what login asks for, so leaving it in "all"
// would put that scope on the consent screen of everyone who installs
// this binary for a feature almost none of them can run. Naming it
// explicitly is the opt-in.
var defaultToolsets = AllToolsets[:len(AllToolsets)-1]

// DefaultToolsets is what "all" registers, which is every set but
// admin. Anything that wants the surface a person gets by default —
// a test harness above all — asks here rather than reaching for
// AllToolsets, which includes the set nobody has unless they name it.
func DefaultToolsets() []Toolset { return slices.Clone(defaultToolsets) }

// Config is the validated runtime configuration.
type Config struct {
	Profile   string
	LogLevel  LogLevel
	LogFormat LogFormat
	ReadOnly  bool
	// RefuseDeletes is negative where its variable is positive. It used
	// to keep the zero value registering all four delete tools, back
	// when the flag decided registration; it decides only whether the
	// call goes through now, so nothing is lost from the surface either
	// way and the safe answer is the one a zero value should give.
	//
	// It is on by default: a deleted Chat message has no trash behind
	// it, unlike the Drive and Docs servers whose deletes are
	// recoverable and whose equivalent flags are off by default anyway.
	RefuseDeletes bool
	// SuppressInteractionHint is negative for the same reason as
	// RefuseDeletes: the zero value has to be the ordinary server, which
	// asks a client to put a person in front of a write.
	SuppressInteractionHint bool
	Toolsets                []Toolset
	HTTPTimeout             time.Duration
	HTTPMaxRetries          int
	SearchMaxPages          int
	DirectoryCacheTTL       time.Duration
	ChatAPIBase             string
	PeopleAPIBase           string
	ClientSecretPath        string
	// LocalDir is the one directory an attachment is written to and
	// read from. Unset means no file transfer at all, which is the
	// default: a server that can read and write anywhere on the machine
	// is a different thing from one that can talk to Chat.
	LocalDir string
}

// Enabled reports whether a toolset's tools should be registered.
func (c Config) Enabled(t Toolset) bool { return slices.Contains(c.Toolsets, t) }

// Settings holds the raw string values before validation. Flags and the
// environment both feed it; Build turns it into a Config.
type Settings struct {
	Profile           string
	LogLevel          string
	LogFormat         string
	ReadOnly          string
	AllowDestructive  string
	InteractionHint   string
	Toolsets          string
	HTTPTimeout       string
	HTTPMaxRetries    string
	SearchMaxPages    string
	DirectoryCacheTTL string
	ChatAPIBase       string
	PeopleAPIBase     string
	ClientSecretPath  string
	LocalDir          string
}

// Defaults for the API endpoints. Overridable so tests can point the
// client at a local server without a network round trip.
const (
	DefaultChatAPIBase   = "https://chat.googleapis.com/v1"
	DefaultPeopleAPIBase = "https://people.googleapis.com/v1"
)

// Define registers one flag per setting on fs, defaulting to the
// matching GCM_* variable read through env.
func Define(fs *flag.FlagSet, env func(string) string) *Settings {
	s := &Settings{}
	def := func(p *string, name, key, fallback, usage string) {
		v := env(EnvPrefix + key)
		if v == "" {
			v = fallback
		}
		fs.StringVar(p, name, v, usage+" [env "+EnvPrefix+key+"]")
	}
	def(&s.Profile, "profile", "PROFILE", "default", "named configuration profile")
	def(&s.LogLevel, "log-level", "LOG_LEVEL", string(LogInfo), "log level: debug, info, warn, error")
	def(&s.LogFormat, "log-format", "LOG_FORMAT", string(LogJSON), "log format: text, json")
	def(&s.ReadOnly, "read-only", "READ_ONLY", "false", "register only the read-only tools")
	def(&s.AllowDestructive, "allow-destructive", "ALLOW_DESTRUCTIVE", "false",
		"allow the tools that delete things to actually delete; they stay registered either way")
	def(&s.InteractionHint, "interaction-hint", "INTERACTION_HINT", "true",
		"ask the client to put a person in front of every write")
	def(&s.Toolsets, "toolsets", "TOOLSETS", "all", "comma-separated tool groups to register, or all: "+toolsetNames())
	def(&s.HTTPTimeout, "http-timeout", "HTTP_TIMEOUT_SECONDS", "10s", "per-request timeout for Google API calls")
	def(&s.HTTPMaxRetries, "http-max-retries", "HTTP_MAX_RETRIES", "3", "retries for a 429 or 5xx from Google")
	def(&s.SearchMaxPages, "search-max-pages", "SEARCH_MAX_PAGES", "10", "how many pages search_messages may scan before reporting a partial result")
	def(&s.DirectoryCacheTTL, "directory-cache-ttl", "DIRECTORY_CACHE_TTL_SECONDS", "24h", "how long a resolved email stays cached")
	def(&s.ChatAPIBase, "chat-api-base", "CHAT_API_BASE", DefaultChatAPIBase, "Chat API base URL")
	def(&s.PeopleAPIBase, "people-api-base", "PEOPLE_API_BASE", DefaultPeopleAPIBase, "People API base URL")
	def(&s.ClientSecretPath, "client-secret", "CLIENT_SECRET", "", "path to the OAuth Desktop client JSON (overrides the stored profile setting)")
	def(&s.LocalDir, "local-dir", "LOCAL_DIR", "", "the one directory attachments are downloaded to and uploaded from (unset turns file transfer off)")
	return s
}

// EnvVars is every GCM_ variable Define reads, sorted.
//
// It runs Define against a recording lookup rather than keeping a second
// list, so a setting cannot be added without appearing here. The
// staleness gate uses it to check that docs/configuration.md documents
// every setting the server actually has.
func EnvVars() []string {
	var seen []string
	fs := flag.NewFlagSet("envvars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	Define(fs, func(name string) string {
		seen = append(seen, name)
		return ""
	})
	slices.Sort(seen)
	return slices.Compact(seen)
}

func toolsetNames() string {
	names := make([]string, len(AllToolsets))
	for i, t := range AllToolsets {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

var (
	profilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	logLevels      = map[LogLevel]bool{LogDebug: true, LogInfo: true, LogWarn: true, LogError: true}
	logFormats     = map[LogFormat]bool{LogText: true, LogJSON: true}
)

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("config: invalid")

// Build validates the settings and returns a Config. Every problem is
// reported, not just the first, so one run fixes a whole broken config.
func (s *Settings) Build() (Config, error) {
	var c Config
	var errs []error

	c.Profile = strings.ToLower(strings.TrimSpace(s.Profile))
	if !profilePattern.MatchString(c.Profile) {
		errs = append(errs, fmt.Errorf("%w: profile %q must match %s", ErrInvalid, s.Profile, profilePattern))
	}

	c.LogLevel = LogLevel(strings.ToLower(strings.TrimSpace(s.LogLevel)))
	if !logLevels[c.LogLevel] {
		errs = append(errs, fmt.Errorf("%w: log level %q (want debug, info, warn, error)", ErrInvalid, s.LogLevel))
	}
	c.LogFormat = LogFormat(strings.ToLower(strings.TrimSpace(s.LogFormat)))
	if !logFormats[c.LogFormat] {
		errs = append(errs, fmt.Errorf("%w: log format %q (want text, json)", ErrInvalid, s.LogFormat))
	}

	var err error
	if c.ReadOnly, err = parseBool("read-only", s.ReadOnly); err != nil {
		errs = append(errs, err)
	}

	allowDestructive, err := parseBool("allow-destructive", s.AllowDestructive)
	if err != nil {
		errs = append(errs, err)
	}
	c.RefuseDeletes = !allowDestructive

	hint, err := parseBool("interaction-hint", s.InteractionHint)
	if err != nil {
		errs = append(errs, err)
	}
	c.SuppressInteractionHint = !hint

	if c.Toolsets, err = parseToolsets(s.Toolsets); err != nil {
		errs = append(errs, err)
	}

	if c.HTTPTimeout, err = parseDuration("http-timeout", s.HTTPTimeout, time.Second, 10*time.Minute); err != nil {
		errs = append(errs, err)
	}
	if c.DirectoryCacheTTL, err = parseDuration("directory-cache-ttl", s.DirectoryCacheTTL, time.Minute, 365*24*time.Hour); err != nil {
		errs = append(errs, err)
	}

	if c.HTTPMaxRetries, err = strconv.Atoi(strings.TrimSpace(s.HTTPMaxRetries)); err != nil {
		errs = append(errs, fmt.Errorf("%w: http max retries %q: %w", ErrInvalid, s.HTTPMaxRetries, err))
	} else if c.HTTPMaxRetries < 0 || c.HTTPMaxRetries > 10 {
		errs = append(errs, fmt.Errorf("%w: http max retries %d must be between 0 and 10", ErrInvalid, c.HTTPMaxRetries))
	}

	if c.SearchMaxPages, err = strconv.Atoi(strings.TrimSpace(s.SearchMaxPages)); err != nil {
		errs = append(errs, fmt.Errorf("%w: search max pages %q: %w", ErrInvalid, s.SearchMaxPages, err))
	} else if c.SearchMaxPages < 1 || c.SearchMaxPages > 50 {
		errs = append(errs, fmt.Errorf("%w: search max pages %d must be between 1 and 50", ErrInvalid, c.SearchMaxPages))
	}

	if c.ChatAPIBase, err = parseBase("chat-api-base", s.ChatAPIBase); err != nil {
		errs = append(errs, err)
	}
	if c.PeopleAPIBase, err = parseBase("people-api-base", s.PeopleAPIBase); err != nil {
		errs = append(errs, err)
	}

	c.ClientSecretPath = strings.TrimSpace(s.ClientSecretPath)

	// A relative path is refused rather than resolved. This server's
	// working directory is whatever the MCP client happened to launch it
	// from, so "attachments" would name a different place depending on
	// who started it — and the directory bounds where a download may
	// land.
	if dir := strings.TrimSpace(s.LocalDir); dir != "" {
		if !filepath.IsAbs(dir) {
			errs = append(errs, fmt.Errorf("%w: local dir %q must be an absolute path", ErrInvalid, dir))
		} else {
			c.LocalDir = filepath.Clean(dir)
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// parseToolsets accepts "all", or a comma-separated list of names. Core
// is always included: without it there is no server worth running.
// "all" is every set but admin, which is named to be had.
func parseToolsets(v string) ([]Toolset, error) {
	raw := strings.TrimSpace(v)
	if raw == "" || strings.EqualFold(raw, "all") {
		return slices.Clone(defaultToolsets), nil
	}
	seen := map[Toolset]bool{ToolsetCore: true}
	var unknown []string
	for _, part := range strings.Split(raw, ",") {
		name := Toolset(strings.ToLower(strings.TrimSpace(part)))
		if name == "" {
			continue
		}
		if !slices.Contains(AllToolsets, name) {
			unknown = append(unknown, string(name))
			continue
		}
		seen[name] = true
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: unknown toolsets %s (want %s, or all)",
			ErrInvalid, strings.Join(unknown, ", "), toolsetNames())
	}
	out := make([]Toolset, 0, len(seen))
	for _, t := range AllToolsets {
		if seen[t] {
			out = append(out, t)
		}
	}
	return out, nil
}

// parseDuration accepts a Go duration or a bare number of seconds. The
// bare form exists because the variables are named ..._SECONDS and were
// integers, which some deployments still set.
func parseDuration(name, v string, minimum, maximum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(v)
	d, err := time.ParseDuration(raw)
	if err != nil {
		secs, numErr := strconv.ParseFloat(raw, 64)
		if numErr != nil {
			return 0, fmt.Errorf("%w: %s %q: %w", ErrInvalid, name, v, err)
		}
		d = time.Duration(secs * float64(time.Second))
	}
	if d < minimum || d > maximum {
		return 0, fmt.Errorf("%w: %s %s must be between %s and %s", ErrInvalid, name, d, minimum, maximum)
	}
	return d, nil
}

func parseBase(name, v string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(v), "/")
	if base == "" {
		return "", fmt.Errorf("%w: %s must not be empty", ErrInvalid, name)
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return "", fmt.Errorf("%w: %s %q must be an http or https URL", ErrInvalid, name, v)
	}
	return base, nil
}

func parseBool(name, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("%w: %s %q (want true or false)", ErrInvalid, name, v)
}

// NewLogger builds the process logger. It writes to w, which must be
// stderr on the server path: stdout carries only JSON-RPC frames.
func NewLogger(c Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel.Slog()}
	if c.LogFormat == LogText {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
