package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/credentials"
	"github.com/mmedum/google-chat-mcp/v2/internal/userconfig"
)

// tempProfile points the config directory at a temporary one. No test
// here may read or write the real profile, and none may touch the real
// keyring: a Phase 0 sibling of this suite once destroyed a live
// refresh token.
func tempProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(userconfig.EnvDir, dir)
	t.Setenv(userconfig.EnvAllowOutsideHome, "1")
	return dir
}

// run drives the command line the way main does.
func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// The regression guard for the wiring itself. `credentials.OSKeyring`
// having no caller meant every login wrote the refresh token to a
// plaintext file while reporting nothing unusual, because the warning
// only fires when a keyring was tried and failed.
func TestTheStoreUsesTheOSKeyring(t *testing.T) {
	tempProfile(t)
	store, err := credentialStore(loadedConfig(t), nil)
	if err != nil {
		t.Fatalf("credentialStore: %v", err)
	}
	if store.Keyring == nil {
		t.Fatal("no keyring backend: the refresh token would go to a plaintext file with no warning")
	}
	if store.FilePath == "" {
		t.Error("no file fallback for a machine with no keyring")
	}
	if store.Profile == "" {
		t.Error("the keyring entry has no account to file itself under")
	}
}

// A mistyped subcommand used to start the server, which blocks on stdin
// and looks like a hang.
func TestAnUnknownCommandIsReported(t *testing.T) {
	tempProfile(t)
	code, _, stderr := runCmd(t, "stauts")
	if code == 0 {
		t.Error("an unknown command should not succeed")
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %q, want it to say what went wrong", stderr)
	}
	if !strings.Contains(stderr, "google-chat-mcp login") {
		t.Errorf("stderr = %q, want the usage to follow", stderr)
	}
}

func TestVersionAndHelp(t *testing.T) {
	tempProfile(t)
	for _, arg := range []string{"--version", "--help", "-h", "help"} {
		code, stdout, stderr := runCmd(t, arg)
		if code != 0 {
			t.Errorf("%s exited %d (%s)", arg, code, stderr)
		}
		if stdout == "" {
			t.Errorf("%s printed nothing", arg)
		}
	}
}

// status is what a person runs when a client cannot connect, so it has
// to work before anyone has logged in.
func TestStatusBeforeLogin(t *testing.T) {
	tempProfile(t)
	code, stdout, stderr := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d (%s)", code, stderr)
	}
	if !strings.Contains(stdout, "not signed in") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "login") {
		t.Errorf("stdout = %q, want it to say what to run", stdout)
	}
}

// The schema dump needs no credentials: registering a tool does not
// call Google. The gates depend on that.
func TestDumpSchemasNeedsNoCredentials(t *testing.T) {
	tempProfile(t)
	code, stdout, stderr := runCmd(t, "--dump-schemas")
	if code != 0 {
		t.Fatalf("--dump-schemas exited %d (%s)", code, stderr)
	}
	for _, want := range []string{`"list_spaces"`, `"gchat://spaces/{space_id}"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the dump does not contain %s", want)
		}
	}
}

// A bad setting has to be reported, not worked around.
func TestABadSettingFailsTheCommand(t *testing.T) {
	tempProfile(t)
	code, _, stderr := runCmd(t, "status", "-log-level", "chatty")
	if code == 0 {
		t.Error("an invalid log level should not be accepted")
	}
	if !strings.Contains(stderr, "log level") {
		t.Errorf("stderr = %q", stderr)
	}
}

// loadedConfig builds the configuration the way a subcommand does.
func loadedConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := loadConfig("test", nil, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return cfg
}

// logout must act on what it stored, not on GCM_REFRESH_TOKEN. Reading
// the environment override would revoke a token logout cannot delete and
// then delete the stored one without revoking it — leaving the grant
// live with nothing local left to withdraw it.
func TestLogoutIgnoresTheEnvironmentOverride(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	store, err := credentialStore(loadedConfig(t), nil)
	if err != nil {
		t.Fatalf("credentialStore: %v", err)
	}
	if _, err := store.Save("stored-refresh-token"); err != nil {
		t.Fatalf("save: %v", err)
	}
	t.Setenv(credentials.EnvVar, "environment-refresh-token")

	got, _, err := store.ResolveStored()
	if err != nil {
		t.Fatalf("ResolveStored: %v", err)
	}
	if got != "stored-refresh-token" {
		t.Errorf("logout would act on %q, not the token it stored", got)
	}
	// The override still wins for serving, which is what it is for.
	if serving, _, err := store.Resolve(); err != nil || serving != "environment-refresh-token" {
		t.Errorf("Resolve = %q, %v; the override must still win for tool calls", serving, err)
	}
}
