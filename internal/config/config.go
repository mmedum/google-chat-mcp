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
	"os"
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
// Fifty-eight tools is a lot for a client that loads them all, so a
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
	// EnableDestructive registers the Destructive tools: the deletes,
	// opening a space to an audience, making someone a manager and adding
	// someone from outside the organization. Off, they are not registered
	// at all, so no client mode and no model can call them
	// (docs/architecture.md, "What a write may reach").
	EnableDestructive bool
	Toolsets          []Toolset
	HTTPTimeout       time.Duration
	HTTPMaxRetries    int
	SearchMaxPages    int
	DirectoryCacheTTL time.Duration
	ChatAPIBase       string
	PeopleAPIBase     string
	CloudIdentityBase string
	ClientSecretPath  string
	// LocalDir is the one directory an attachment is written to and
	// read from. Unset means no file transfer at all, which is the
	// default: a server that can read and write anywhere on the machine
	// is a different thing from one that can talk to Chat.
	LocalDir string
}

// Enabled reports whether a toolset's tools should be registered.
func (c Config) Enabled(t Toolset) bool { return slices.Contains(c.Toolsets, t) }

// DestructiveRegisters reports whether the destructive tools register:
// the switch on, and read-only off, since read-only wins.
func (c Config) DestructiveRegisters() bool { return c.EnableDestructive && !c.ReadOnly }

// FullSurface is c with every tool registrable: writes on, every
// toolset, and the destructive tools. --dump-schemas writes this
// surface, because the schema diff holds every released tool to it and
// a tool a setting leaves out is no less part of the contract.
func (c Config) FullSurface() Config {
	c.ReadOnly = false
	c.EnableDestructive = true
	c.Toolsets = slices.Clone(AllToolsets)
	return c
}

// Settings holds the raw string values before validation. Flags and the
// environment both feed it; Build turns it into a Config.
type Settings struct {
	Profile           string
	LogLevel          string
	LogFormat         string
	ReadOnly          string
	EnableDestructive string
	Toolsets          string
	HTTPTimeout       string
	HTTPMaxRetries    string
	SearchMaxPages    string
	DirectoryCacheTTL string
	ChatAPIBase       string
	PeopleAPIBase     string
	CloudIdentityBase string
	ClientSecretPath  string
	LocalDir          string
	// removed are the settings a release took out, in removedSettings'
	// order, as a flag or the environment gives them, read only to
	// refuse one still set.
	removed []string
	// vars is every setting Define reads, in order.
	vars []Var
}

// Var is one variable the server reads.
type Var struct {
	// Name is the environment variable.
	Name string
	// Switch marks a setting that is on or off.
	Switch bool
	// Removed marks a setting a release took out, read only to refuse it.
	Removed bool
}

// The variables read outside Define. internal/userconfig reads the
// profile directory and its opt-in before any flag is parsed, and
// internal/credentials the refresh token; each takes its name from
// here, so config imports neither.
const (
	EnvConfigDir                 = EnvPrefix + "CONFIG_DIR"
	EnvConfigDirAllowOutsideHome = EnvPrefix + "CONFIG_DIR_ALLOW_OUTSIDE_HOME"
	EnvRefreshToken              = EnvPrefix + "REFRESH_TOKEN"
)

// envOnly is every variable read outside Define, for Vars.
var envOnly = []string{EnvConfigDir, EnvConfigDirAllowOutsideHome, EnvRefreshToken}

// Blank reports whether a variable's value leaves it unset: empty, or
// spaces alone, so a form field cleared to a space means the default
// rather than a value the server refuses.
func Blank(v string) bool { return strings.TrimSpace(v) == "" }

// removedSettings are the settings a release took out, each with why
// and what to do instead. Each was off by default, and unset or off is
// still accepted. On, or a value that is not a boolean, is refused at
// start: ignored, it would quietly leave the server otherwise than its
// owner set it. A setting with a flag keeps it, answering as its
// variable does, so the refusal says what to use instead, and --help
// says it is removed and names use. A setting renamed later goes the
// same way, rather than answering to two names.
//
// GCM_INTERACTION_HINT went too, and is not read. Off asked for fewer
// marks than the server now sets, and on was every install's default.
// The flags of the first two went in 6.0.0, and stay gone.
var removedSettings = []struct{ key, flag, use, why string }{
	{key: "REQUIRE_PROMPT",
		why: noQuestion + "leave " + EnvPrefix + "ENABLE_DESTRUCTIVE off for a client that runs with nobody watching"},
	{key: "ASK_BEFORE_SEND",
		why: noQuestion + "list send_message and update_message under your client's ask rules, as the README shows"},
	// The destructive switch's names until 6.1.0 renamed them.
	{key: "ALLOW_DESTRUCTIVE", flag: "allow-destructive", use: "--enable-destructive",
		why: "it is " + EnvPrefix + "ENABLE_DESTRUCTIVE (--enable-destructive) now. Unset it, and set that in its place"},
}

// noQuestion is why the settings that made the server ask were removed.
const noQuestion = "the server no longer asks the person, and the client's own approval decides each call. Unset it, and "

// removedSet refuses each removed setting that is set to anything but
// off.
func (s *Settings) removedSet() []error {
	var errs []error
	for i, r := range removedSettings {
		if on, err := parseBool(r.key, s.removed[i]); err == nil && !on {
			continue
		}
		name := EnvPrefix + r.key
		if r.flag != "" {
			name += " (--" + r.flag + ")"
		}
		errs = append(errs, fmt.Errorf("%w: %s was removed: %s", ErrInvalid, name, r.why))
	}
	return errs
}

// Defaults for the API endpoints. Overridable so tests can point the
// client at a local server without a network round trip.
const (
	DefaultChatAPIBase   = "https://chat.googleapis.com/v1"
	DefaultPeopleAPIBase = "https://people.googleapis.com/v1"
	// DefaultCloudIdentityBase is where a group's id is looked up.
	DefaultCloudIdentityBase = "https://cloudidentity.googleapis.com/v1"
)

// Define registers one flag per setting on fs, defaulting to the
// matching GCM_* variable read through env.
func Define(fs *flag.FlagSet, env func(string) string) *Settings {
	s := &Settings{removed: make([]string, len(removedSettings))}
	for i, r := range removedSettings {
		// The flag is registered empty and the variable read after, so
		// --help shows no default for a setting nobody should set.
		if r.flag != "" {
			fs.StringVar(&s.removed[i], r.flag, "", "removed; use "+r.use+" [env "+EnvPrefix+r.key+"]")
		}
		s.removed[i] = env(EnvPrefix + r.key)
		s.vars = append(s.vars, Var{Name: EnvPrefix + r.key, Removed: true})
	}
	bind := func(p *string, name, fallback, usage string, v Var) {
		value := env(v.Name)
		if Blank(value) {
			value = fallback
		}
		fs.StringVar(p, name, value, usage+" [env "+v.Name+"]")
		s.vars = append(s.vars, v)
	}
	def := func(p *string, name, key, fallback, usage string) {
		bind(p, name, fallback, usage, Var{Name: EnvPrefix + key})
	}
	defSwitch := func(p *string, name, key, usage string) {
		bind(p, name, "false", usage, Var{Name: EnvPrefix + key, Switch: true})
	}
	def(&s.Profile, "profile", "PROFILE", "default", "named configuration profile")
	def(&s.LogLevel, "log-level", "LOG_LEVEL", string(LogInfo), "log level: debug, info, warn, error")
	def(&s.LogFormat, "log-format", "LOG_FORMAT", string(LogJSON), "log format: text, json")
	defSwitch(&s.ReadOnly, "read-only", "READ_ONLY", "register only the read-only tools")
	defSwitch(&s.EnableDestructive, "enable-destructive", "ENABLE_DESTRUCTIVE",
		"register the tools that delete, open a space, make a manager or add someone from outside the organization")
	def(&s.Toolsets, "toolsets", "TOOLSETS", "all", "comma-separated tool groups to register, or all: "+ToolsetList())
	def(&s.HTTPTimeout, "http-timeout", "HTTP_TIMEOUT_SECONDS", "10s", "per-request timeout for Google API calls")
	def(&s.HTTPMaxRetries, "http-max-retries", "HTTP_MAX_RETRIES", "3", "retries for a 429 or 5xx from Google")
	def(&s.SearchMaxPages, "search-max-pages", "SEARCH_MAX_PAGES", "10", "how many pages search_messages may scan before reporting a partial result")
	def(&s.DirectoryCacheTTL, "directory-cache-ttl", "DIRECTORY_CACHE_TTL_SECONDS", "24h", "how long a resolved email stays cached")
	def(&s.ChatAPIBase, "chat-api-base", "CHAT_API_BASE", DefaultChatAPIBase, "Chat API base URL")
	def(&s.PeopleAPIBase, "people-api-base", "PEOPLE_API_BASE", DefaultPeopleAPIBase, "People API base URL")
	def(&s.CloudIdentityBase, "cloud-identity-api-base", "CLOUD_IDENTITY_API_BASE", DefaultCloudIdentityBase, "Cloud Identity API base URL")
	def(&s.ClientSecretPath, "client-secret", "CLIENT_SECRET", "", "path to the OAuth Desktop client JSON (overrides the stored profile setting)")
	def(&s.LocalDir, "local-dir", "LOCAL_DIR", "", "the one directory attachments are downloaded to and uploaded from (unset turns file transfer off)")
	return s
}

// Vars is every variable the server reads, sorted by name: Define's,
// the removed ones included, and the ones read outside it. Define's are
// recorded rather than listed, so they cannot fall behind. The bundle
// and staleness gates derive what they hold from these.
func Vars() []Var {
	vars := defined()
	for _, name := range envOnly {
		vars = append(vars, Var{Name: name})
	}
	slices.SortFunc(vars, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	return vars
}

// defined is every variable Define reads, in the order it reads them.
func defined() []Var {
	fs := flag.NewFlagSet("vars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return Define(fs, func(string) string { return "" }).vars
}

// EnvVars is the names of Vars, sorted.
func EnvVars() []string {
	vars := Vars()
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

// Load is Define, Parse and Build for a caller with no flags of its own.
func Load(args []string, env func(string) string) (Config, error) {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := Define(fs, env)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return s.Build()
}

// ToolsetList is every toolset a person may name, in registration
// order, comma-separated.
func ToolsetList() string {
	names := make([]string, len(AllToolsets))
	for i, t := range AllToolsets {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

// ProfileRule is profilePattern in words, which Build's refusal and the
// bundle's profile field both give.
const ProfileRule = "up to 64 characters from a to z, 0 to 9, - and _, starting with a letter or digit"

var (
	profilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	logLevels      = map[LogLevel]bool{LogDebug: true, LogInfo: true, LogWarn: true, LogError: true}
	logFormats     = map[LogFormat]bool{LogText: true, LogJSON: true}
)

// asciiLower lowers A to Z alone, so a character Unicode folds into a
// to z, such as the Kelvin sign, is refused rather than renamed.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("config: invalid")

// Build validates the settings and returns a Config. Every problem is
// reported, not just the first, so one run fixes a whole broken config.
func (s *Settings) Build() (Config, error) {
	var c Config
	var errs []error

	c.Profile = asciiLower(strings.TrimSpace(s.Profile))
	if !profilePattern.MatchString(c.Profile) {
		errs = append(errs, fmt.Errorf("%w: profile %q must be %s", ErrInvalid, s.Profile, ProfileRule))
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

	if c.EnableDestructive, err = parseBool("enable-destructive", s.EnableDestructive); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, s.removedSet()...)

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

	for _, b := range []struct {
		name string
		in   string
		out  *string
	}{
		{"chat-api-base", s.ChatAPIBase, &c.ChatAPIBase},
		{"people-api-base", s.PeopleAPIBase, &c.PeopleAPIBase},
		{"cloud-identity-api-base", s.CloudIdentityBase, &c.CloudIdentityBase},
	} {
		if *b.out, err = parseBase(b.name, b.in); err != nil {
			errs = append(errs, err)
		}
	}

	c.ClientSecretPath = strings.TrimSpace(s.ClientSecretPath)

	// A relative path is refused rather than resolved. This server's
	// working directory is whatever the MCP client happened to launch it
	// from, so "attachments" would name a different place depending on
	// who started it — and the directory bounds where a download may
	// land.
	if dir := strings.TrimSpace(s.LocalDir); dir != "" {
		// Existence is part of the rule, not a detail. A relative path was
		// always refused here; an absolute one that does not exist was not,
		// so a typo was accepted at startup and surfaced later as a file
		// operation failing for reasons nobody could see from `status`.
		var info os.FileInfo
		var statErr error
		switch {
		case !filepath.IsAbs(dir):
			errs = append(errs, fmt.Errorf("%w: local dir %q must be an absolute path", ErrInvalid, dir))
		default:
			if info, statErr = os.Stat(dir); statErr != nil {
				errs = append(errs, fmt.Errorf("%w: local dir %q cannot be read: %w", ErrInvalid, dir, statErr))
			} else if !info.IsDir() {
				errs = append(errs, fmt.Errorf("%w: local dir %q is not a directory", ErrInvalid, dir))
			} else {
				c.LocalDir = filepath.Clean(dir)
			}
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// parseToolsets accepts a comma-separated list of names, any of which may
// be "all". Core is always included: without it there is no server worth
// running. "all" is every set but admin, which is named to be had, so
// "all,admin" is everything.
func parseToolsets(v string) ([]Toolset, error) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return slices.Clone(defaultToolsets), nil
	}
	seen := map[Toolset]bool{ToolsetCore: true}
	var unknown []string
	for _, part := range strings.Split(raw, ",") {
		name := Toolset(strings.ToLower(strings.TrimSpace(part)))
		if name == "" {
			continue
		}
		if name == "all" {
			for _, t := range defaultToolsets {
				seen[t] = true
			}
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
			ErrInvalid, strings.Join(unknown, ", "), ToolsetList())
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
