package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/mmedum/google-chat-mcp/internal/auth"
	"github.com/mmedum/google-chat-mcp/internal/userconfig"
)

// A Desktop OAuth client, in the shape Google hands out. Every value is
// invented; the leak gate would fail the build on a real one.
const exampleClientSecret = `{"installed":{
  "client_id":"example-client-id.apps.googleusercontent.com",
  "client_secret":"example-client-secret",
  "auth_uri":"https://accounts.google.com/o/oauth2/auth",
  "token_uri":"https://oauth2.googleapis.com/token",
  "redirect_uris":["http://localhost"]
}}`

// writeClientSecret puts an invented Desktop client in the profile
// directory and hands back its path.
func writeClientSecret(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "client_secret.json")
	if err := os.WriteFile(path, []byte(exampleClientSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Nobody has logged in yet, and the server still starts — that is
// deliberate, because a stdio server that exits on a missing login shows
// up in the client as "failed to connect" with no way to see why. What
// makes that trade honest is this message: every tool call has to say
// what to run.
func TestTheNotLoggedInSourceSaysWhatToRun(t *testing.T) {
	_, err := notLoggedIn{}.Token(context.Background())
	if err == nil {
		t.Fatal("a server with no credentials handed out a token")
	}
	if !errors.Is(err, auth.ErrReauthorize) {
		t.Errorf("err = %v, want it to classify as needing authorization", err)
	}
	if !strings.Contains(err.Error(), "login") {
		t.Errorf("err = %v, want it to name the command that fixes this", err)
	}
	if !strings.Contains(err.Error(), "--client-secret") {
		t.Errorf("err = %v, want it to name the argument login needs", err)
	}
}

// The server must come up with no credentials at all rather than refuse
// to start, and it must still register its tools.
func TestBuildSucceedsWithNoCredentials(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()

	d, err := build(context.Background(), loadedConfig(t), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build with no credentials: %v", err)
	}
	if d.Service == nil || d.Client == nil {
		t.Fatal("build returned no service")
	}
	if d.TokenSource == nil {
		t.Fatal("no token source: tool calls would panic rather than explain")
	}
	if _, err := d.TokenSource.Token(context.Background()); err == nil {
		t.Error("the stub source handed out a token")
	}
}

// With a client and a stored refresh token, the source is the real one
// rather than the stub, and it reports where the token came from. The
// distinction is what `status` and `doctor` print.
func TestLoadTokenSourceUsesTheStoredCredentials(t *testing.T) {
	dir := tempProfile(t)
	keyring.MockInit()
	path := writeClientSecret(t, dir)

	cfg := loadedConfig(t)
	store, err := credentialStore(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("an-invented-refresh-token"); err != nil {
		t.Fatal(err)
	}

	tokens, source, err := loadTokenSource(context.Background(), cfg,
		userconfig.Config{ClientSecretPath: path})
	if err != nil {
		t.Fatalf("loadTokenSource: %v", err)
	}
	if tokens == nil {
		t.Fatal("no token source")
	}
	if source == "" {
		t.Error("the source did not say where the token came from")
	}
	if _, isStub := tokens.(notLoggedIn); isStub {
		t.Error("stored credentials still produced the not-logged-in stub")
	}
}

// No client JSON anywhere is a refusal, not a stub: the flag, the
// environment and the stored profile were all empty, so there is
// nothing to refresh with and the reason has to survive to the caller.
func TestLoadTokenSourceWithoutAClientFails(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()

	if _, _, err := loadTokenSource(context.Background(), loadedConfig(t),
		userconfig.Config{}); err == nil {
		t.Error("a missing OAuth client produced a working token source")
	}
}

// The flag wins over the path recorded at login. Someone passing
// --client-secret is correcting the stored one, and silently preferring
// the stored path would ignore the correction.
func TestClientConfigPrefersTheFlagOverTheStoredPath(t *testing.T) {
	dir := tempProfile(t)
	path := writeClientSecret(t, dir)

	cfg := loadedConfig(t)
	cfg.ClientSecretPath = path
	got, err := clientConfig(cfg, userconfig.Config{ClientSecretPath: "/nonexistent/stored.json"})
	if err != nil {
		t.Fatalf("clientConfig: %v", err)
	}
	if got.ClientID == "" {
		t.Error("no client id read from the file the flag named")
	}
}
