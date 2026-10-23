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
	// Deprecated is each old setting name this run was given, for the
	// caller to warn about once it has a logger.
	Deprecated []Rename
}

// Rename is a setting that took a new name and still answers to the
// old one, as a variable and as a flag.
type Rename struct {
	Old, New         string
	OldFlag, NewFlag string
}

// renamedDestructive is the destructive switch's name until 6.1.0,
// renamed to match the sibling servers.
var renamedDestructive = Rename{
	Old: EnvPrefix + "ALLOW_DESTRUCTIVE", New: EnvPrefix + "ENABLE_DESTRUCTIVE",
	OldFlag: "--allow-destructive", NewFlag: "--enable-destructive",
}

// Enabled reports whether a toolset's tools should be registered.
func (c Config) Enabled(t Toolset) bool { return slices.Contains(c.Toolsets, t) }

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
	// AllowDestructive is EnableDestructive's old name, still read.
	AllowDestructive  string
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
	// removed are the settings a release took out, as the environment
	// gives them, read only to refuse one still set.
	removed map[string]string
	// vars is every setting Define reads, in order.
	vars []Var
}

// Var is one setting Define reads.
type Var struct {
	// Name is the environment variable.
	Name string
	// Default is what the server uses when neither it nor its flag is
	// set, written as a person would set it.
	Default string
	// Switch marks a setting that is on or off.
	Switch bool
	// ReplacedBy names the variable that took this one's place. The old
	// name still works, and nothing should offer it.
	ReplacedBy string
	// Removed marks a setting a release took out, read only to refuse it.
	Removed bool
}

// removedSettings are the settings that made the server ask the person,
// each with what to do instead. Both were off by default, and off is
// still accepted. On, or a value that is not a boolean, is refused at
// start: ignored, it would quietly allow what its owner set it to stop.
//
// GCM_INTERACTION_HINT went too, and is not read. Off asked for fewer
// marks than the server now sets, and on was every install's default.
var removedSettings = []struct{ key, instead string }{
	{"REQUIRE_PROMPT", "leave " + EnvPrefix + "ENABLE_DESTRUCTIVE off for a client that runs with nobody watching"},
	{"ASK_BEFORE_SEND", "list send_message and update_message under your client's ask rules, as the README shows"},
}

// removedSet refuses each removed setting that is set to anything but
// off.
func (s *Settings) removedSet() []error {
	var errs []error
	for _, r := range removedSettings {
		if on, err := parseBool(r.key, s.removed[r.key]); err == nil && !on {
			continue
		}
		errs = append(errs, fmt.Errorf("%w: %s%s was removed: the server no longer asks the person, and the client's "+
			"own approval decides each call. Unset it, and %s", ErrInvalid, EnvPrefix, r.key, r.instead))
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
	s := &Settings{removed: map[string]string{}}
	for _, r := range removedSettings {
		s.removed[r.key] = env(EnvPrefix + r.key)
		s.vars = append(s.vars, Var{Name: EnvPrefix + r.key, Removed: true})
	}
	bind := func(p *string, name, fallback, usage string, v Var) {
		value := env(v.Name)
		if value == "" {
			value = fallback
		}
		fs.StringVar(p, name, value, usage+" [env "+v.Name+"]")
		s.vars = append(s.vars, v)
	}
	def := func(p *string, name, key, fallback, usage string) {
		bind(p, name, fallback, usage, Var{Name: EnvPrefix + key, Default: fallback})
	}
	// A switch is registered empty rather than "false", so Build can
	// tell one nobody set from one set off. Empty is off.
	defSwitch := func(p *string, name, key, replacedBy, usage string) {
		bind(p, name, "", usage, Var{Name: EnvPrefix + key, Default: "false", Switch: true, ReplacedBy: replacedBy})
	}
	def(&s.Profile, "profile", "PROFILE", "default", "named configuration profile")
	def(&s.LogLevel, "log-level", "LOG_LEVEL", string(LogInfo), "log level: debug, info, warn, error")
	def(&s.LogFormat, "log-format", "LOG_FORMAT", string(LogJSON), "log format: text, json")
	defSwitch(&s.ReadOnly, "read-only", "READ_ONLY", "", "register only the read-only tools")
	defSwitch(&s.EnableDestructive, "enable-destructive", "ENABLE_DESTRUCTIVE", "",
		"register the tools that delete, open a space, make a manager or add someone from outside the organization")
	defSwitch(&s.AllowDestructive, "allow-destructive", "ALLOW_DESTRUCTIVE", renamedDestructive.New,
		"deprecated: the old name of --enable-destructive, which still works")
	def(&s.Toolsets, "toolsets", "TOOLSETS", "all", "comma-separated tool groups to register, or all: "+toolsetNames())
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

// Vars is every setting Define reads, sorted by variable.
func Vars() []Var {
	fs := flag.NewFlagSet("vars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	vars := Define(fs, func(string) string { return "" }).vars
	slices.SortFunc(vars, func(a, b Var) int { return strings.Compare(a.Name, b.Name) })
	return vars
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

	if c.EnableDestructive, err = s.enableDestructive(); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(s.AllowDestructive) != "" {
		c.Deprecated = append(c.Deprecated, renamedDestructive)
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

// enableDestructive reads the destructive switch under its name and
// under its old one. Either alone decides. Both set have to agree,
// because a run that took one over the other would turn on or leave
// off what its owner set the other to say.
func (s *Settings) enableDestructive() (bool, error) {
	on, err := parseBool("enable-destructive", s.EnableDestructive)
	if err != nil || strings.TrimSpace(s.AllowDestructive) == "" {
		return on, err
	}
	old, err := parseBool("allow-destructive", s.AllowDestructive)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(s.EnableDestructive) != "" && old != on {
		return false, fmt.Errorf("%w: %s is %t and %s, its old name, is %t: unset %s",
			ErrInvalid, renamedDestructive.New, on, renamedDestructive.Old, old, renamedDestructive.Old)
	}
	return old, nil
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
