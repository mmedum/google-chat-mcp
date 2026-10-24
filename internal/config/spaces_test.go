package config_test

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/v7/internal/config"
	"github.com/mmedum/google-chat-mcp/v7/internal/credentials"
	"github.com/mmedum/google-chat-mcp/v7/internal/userconfig"
)

// varsFloor is how many variables the server reads today, on purpose:
// the floor moves with it, so a registry that records nothing cannot
// pass the walk below.
const varsFloor = 21

// unconfigured is the configuration no environment gives, stated.
func unconfigured() config.Config {
	return config.Config{
		Profile:   "default",
		LogLevel:  config.LogInfo,
		LogFormat: config.LogJSON,
		Toolsets: []config.Toolset{"core", "sections", "pins", "emoji", "events", "readstate", "settings",
			"availability"},
		HTTPTimeout:       10 * time.Second,
		HTTPMaxRetries:    3,
		SearchMaxPages:    10,
		DirectoryCacheTTL: 24 * time.Hour,
		ChatAPIBase:       "https://chat.googleapis.com/v1",
		PeopleAPIBase:     "https://people.googleapis.com/v1",
		CloudIdentityBase: "https://cloudidentity.googleapis.com/v1",
	}
}

// outsideRow is a variable read outside Define: its reader, a value with
// spaces around it, what the reader gives for that value, and what it
// gives for spaces alone, its error included.
type outsideRow struct {
	read     func(t *testing.T, value string) (string, error)
	value    string
	want     string
	unset    string
	unsetErr error
}

// A variable of spaces alone is unset, as an empty one is, and the spaces
// around a value are dropped, so a form field cleared to a space starts
// the server with the default. That holds for every variable the server
// reads: Define's through Load, and each one read outside Define through
// its reader. Each variable has a row, and one with none fails, so a
// variable added cannot miss the rule. This is a test of its own package
// because the readers outside Define import config.
func TestASettingOfSpacesIsUnset(t *testing.T) {
	home, elsewhere, local := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	userDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}

	// defined is, for each variable Define reads, a value with spaces
	// around it and what that value changes in the unconfigured
	// configuration. A removed setting given off changes nothing.
	nothing := func(*config.Config) {}
	defined := map[string]struct {
		value string
		set   func(c *config.Config)
	}{
		"GCM_PROFILE":                     {" work ", func(c *config.Config) { c.Profile = "work" }},
		"GCM_LOG_LEVEL":                   {" debug ", func(c *config.Config) { c.LogLevel = config.LogDebug }},
		"GCM_LOG_FORMAT":                  {" text ", func(c *config.Config) { c.LogFormat = config.LogText }},
		"GCM_READ_ONLY":                   {" true ", func(c *config.Config) { c.ReadOnly = true }},
		"GCM_ENABLE_DESTRUCTIVE":          {" true ", func(c *config.Config) { c.EnableDestructive = true }},
		"GCM_TOOLSETS":                    {" core ", func(c *config.Config) { c.Toolsets = []config.Toolset{"core"} }},
		"GCM_HTTP_TIMEOUT_SECONDS":        {" 5 ", func(c *config.Config) { c.HTTPTimeout = 5 * time.Second }},
		"GCM_HTTP_MAX_RETRIES":            {" 1 ", func(c *config.Config) { c.HTTPMaxRetries = 1 }},
		"GCM_SEARCH_MAX_PAGES":            {" 2 ", func(c *config.Config) { c.SearchMaxPages = 2 }},
		"GCM_DIRECTORY_CACHE_TTL_SECONDS": {" 60 ", func(c *config.Config) { c.DirectoryCacheTTL = time.Minute }},
		"GCM_CHAT_API_BASE":               {" http://127.0.0.1:1 ", func(c *config.Config) { c.ChatAPIBase = "http://127.0.0.1:1" }},
		"GCM_PEOPLE_API_BASE":             {" http://127.0.0.1:2 ", func(c *config.Config) { c.PeopleAPIBase = "http://127.0.0.1:2" }},
		"GCM_CLOUD_IDENTITY_API_BASE": {" http://127.0.0.1:3 ", func(c *config.Config) {
			c.CloudIdentityBase = "http://127.0.0.1:3"
		}},
		"GCM_CLIENT_SECRET":     {" client.json ", func(c *config.Config) { c.ClientSecretPath = "client.json" }},
		"GCM_LOCAL_DIR":         {" " + local + " ", func(c *config.Config) { c.LocalDir = local }},
		"GCM_REQUIRE_PROMPT":    {" false ", nothing},
		"GCM_ASK_BEFORE_SEND":   {" false ", nothing},
		"GCM_ALLOW_DESTRUCTIVE": {" false ", nothing},
	}
	baseDir := func(t *testing.T, dir, allow string) (string, error) {
		t.Setenv(config.EnvConfigDir, dir)
		t.Setenv(config.EnvConfigDirAllowOutsideHome, allow)
		return userconfig.BaseDir()
	}
	outside := map[string]outsideRow{
		config.EnvConfigDir: {
			read:  func(t *testing.T, value string) (string, error) { return baseDir(t, value, "") },
			value: " " + filepath.Join(home, "cfg") + " ", want: filepath.Join(home, "cfg"),
			unset: filepath.Join(userDir, "google-chat-mcp"),
		},
		// The opt-in is read only for a directory outside the home one.
		config.EnvConfigDirAllowOutsideHome: {
			read:  func(t *testing.T, value string) (string, error) { return baseDir(t, elsewhere, value) },
			value: " 1 ", want: elsewhere,
			unset: "", unsetErr: userconfig.ErrOutsideHome,
		},
		config.EnvRefreshToken: {
			read: func(_ *testing.T, value string) (string, error) {
				s := credentials.Store{Profile: "p", Env: func(string) string { return value }}
				tok, src, err := s.Resolve()
				return tok + " from " + string(src), err
			},
			value: " a-token ", want: "a-token from env",
			unset: " from ", unsetErr: credentials.ErrNotFound,
		},
	}

	read := map[string]bool{}
	fs := flag.NewFlagSet("vars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config.Define(fs, func(name string) string { read[name] = true; return "" })
	vars := config.Vars()
	if len(vars) < varsFloor {
		t.Fatalf("Vars has %d variables, want at least %d", len(vars), varsFloor)
	}
	for _, v := range vars {
		t.Run(v.Name, func(t *testing.T) {
			row, isOutside := outside[v.Name]
			delete(outside, v.Name)
			switch {
			case isOutside && read[v.Name]:
				t.Errorf("%s is read by Define, and this test has a reader outside Define for it", v.Name)
			case !isOutside && !read[v.Name]:
				t.Errorf("%s is read outside Define, and this test has no reader for it", v.Name)
			case isOutside:
				if got, err := row.read(t, row.value); got != row.want || err != nil {
					t.Errorf("%s=%q: %q, %v; want %q", v.Name, row.value, got, err, row.want)
				}
				got, err := row.read(t, "  ")
				if got != row.unset || !errors.Is(err, row.unsetErr) {
					t.Errorf("%s=\"  \": %q, %v; want %q, %v, as unset", v.Name, got, err, row.unset, row.unsetErr)
				}
			default:
				d, ok := defined[v.Name]
				delete(defined, v.Name)
				if !ok {
					t.Fatalf("%s is read by Define, and this test has no row for it", v.Name)
				}
				load := func(value string) (config.Config, error) {
					return config.Load(nil, func(name string) string {
						if name == v.Name {
							return value
						}
						return ""
					})
				}
				want := unconfigured()
				d.set(&want)
				if got, err := load(d.value); err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("%s=%q: %+v, %v\nwant %+v", v.Name, d.value, got, err, want)
				}
				if got, err := load("  "); err != nil || !reflect.DeepEqual(got, unconfigured()) {
					t.Errorf("%s=\"  \": %+v, %v\nwant the unconfigured %+v", v.Name, got, err, unconfigured())
				}
			}
		})
	}
	for name := range defined {
		t.Errorf("this test has a row for %s, which Vars does not list", name)
	}
	for name := range outside {
		t.Errorf("this test has a reader for %s, which Vars does not list", name)
	}
}
