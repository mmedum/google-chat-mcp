package config_test

import (
	"flag"
	"fmt"
	"io"
	"testing"

	"github.com/mmedum/google-chat-mcp/v7/internal/config"
	"github.com/mmedum/google-chat-mcp/v7/internal/credentials"
	"github.com/mmedum/google-chat-mcp/v7/internal/userconfig"
)

// varsFloor is how many variables the server reads today, on purpose:
// the floor moves with it, so a registry that records nothing cannot
// pass the walk below.
const varsFloor = 21

// A variable of spaces alone is unset, as an empty one is, and the spaces
// around a value are dropped, so a form field cleared to a space starts
// the server with the default. That holds for every variable the server
// reads: Define's through Load, and each one read outside Define through
// its reader. One read outside Define with no reader here fails, so a
// variable added there cannot miss the rule. This is a test of its own
// package because those readers import config.
func TestASettingOfSpacesIsUnset(t *testing.T) {
	// The opt-in is read only for a directory outside the home one.
	home, elsewhere := t.TempDir(), t.TempDir()
	outside := map[string]func(t *testing.T, value string) string{
		config.EnvConfigDir: func(t *testing.T, value string) string {
			t.Setenv(config.EnvConfigDirAllowOutsideHome, "")
			t.Setenv(config.EnvConfigDir, value)
			dir, err := userconfig.BaseDir()
			return fmt.Sprintf("%q %v", dir, err)
		},
		config.EnvConfigDirAllowOutsideHome: func(t *testing.T, value string) string {
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv(config.EnvConfigDir, elsewhere)
			t.Setenv(config.EnvConfigDirAllowOutsideHome, value)
			dir, err := userconfig.BaseDir()
			return fmt.Sprintf("%q %v", dir, err)
		},
		config.EnvRefreshToken: func(_ *testing.T, value string) string {
			s := credentials.Store{Profile: "p", Env: func(string) string { return value }}
			tok, src, err := s.Resolve()
			return fmt.Sprintf("%q %q %v", tok, src, err)
		},
	}
	defined := map[string]bool{}
	fs := flag.NewFlagSet("vars", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config.Define(fs, func(name string) string { defined[name] = true; return "" })

	vars := config.Vars()
	if len(vars) < varsFloor {
		t.Fatalf("Vars has %d variables, want at least %d", len(vars), varsFloor)
	}
	for _, v := range vars {
		read := func(_ *testing.T, value string) string {
			c, err := config.Load(nil, func(name string) string {
				if name == v.Name {
					return value
				}
				return ""
			})
			return fmt.Sprintf("%+v %v", c, err)
		}
		if !defined[v.Name] {
			var ok bool
			if read, ok = outside[v.Name]; !ok {
				t.Errorf("%s is read outside Define, and this test has no reader for it", v.Name)
				continue
			}
			delete(outside, v.Name)
		}
		t.Run(v.Name, func(t *testing.T) {
			// 1 is the value the config directory's opt-in takes.
			for _, pair := range [][2]string{{"  ", ""}, {" x ", "x"}, {" 1 ", "1"}} {
				if got, want := read(t, pair[0]), read(t, pair[1]); got != want {
					t.Errorf("%s=%q: %s\nwant %s, as %s=%q", v.Name, pair[0], got, want, v.Name, pair[1])
				}
			}
		})
	}
	for name := range outside {
		t.Errorf("this test has a reader for %s, which Vars does not list as read outside Define", name)
	}
}
