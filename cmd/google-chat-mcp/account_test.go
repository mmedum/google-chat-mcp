package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/userconfig"
)

// A client going away is the ordinary end of a session, and the exit
// code says whether the host records it as a crash. The SDK reports it
// as a JSON-RPC error carrying the EOF in its message text rather than
// wrapping it, so errors.Is on io.EOF does not match and the code is
// the only stable signal.
//
// This test builds the error it matches, so on its own it proves the
// predicate and nothing about what the SDK actually sends. The smoke
// gate is what holds that half: its second run closes stdin the instant
// the last request is written and asserts the exit code, and it was
// checked against a build with this branch removed.
func TestIsShutdownMatchesTheDisconnectCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"server closing", &jsonrpc.Error{Code: codeServerClosing, Message: "server is closing"}, true},
		{"client closing", &jsonrpc.Error{Code: codeClientClosing, Message: "client is closing"}, true},
		{"wrapped server closing",
			fmt.Errorf("serve: %w", &jsonrpc.Error{Code: codeServerClosing, Message: "server is closing"}), true},
		{"EOF", io.EOF, true},
		{"closed pipe", io.ErrClosedPipe, true},
		{"closed file", os.ErrClosed, true},
		// A real failure must not be read as a graceful exit, or the
		// process reports success while the session died.
		{"an ordinary error", errors.New("connection refused"), false},
		{"another JSON-RPC error", &jsonrpc.Error{Code: -32603, Message: "internal error"}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isShutdown(tc.err); got != tc.want {
				t.Errorf("isShutdown(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// The message a person reads after login says which account, and an
// empty address must not produce a dangling " as ".
func TestAccountSuffix(t *testing.T) {
	if got := accountSuffix(""); got != "" {
		t.Errorf("accountSuffix(\"\") = %q, want empty", got)
	}
	if got := accountSuffix("janedoe@example.com"); got != " as janedoe@example.com" {
		t.Errorf("accountSuffix = %q", got)
	}
}

// "all" is a claim about the whole set, so it may only be printed when
// the set really is whole. Printing it for a narrowed server would tell
// a reader the opposite of what is registered.
func TestJoinToolsetsOnlySaysAllWhenItIsAll(t *testing.T) {
	if got := joinToolsets(config.AllToolsets); got != "all" {
		t.Errorf("every toolset joined to %q, want \"all\"", got)
	}
	got := joinToolsets([]config.Toolset{config.ToolsetCore, config.ToolsetPins})
	if got == "all" {
		t.Error("a narrowed set reported itself as all")
	}
	if !strings.Contains(got, string(config.ToolsetCore)) {
		t.Errorf("joinToolsets = %q, want it to name the sets", got)
	}
	if got := joinToolsets(nil); got == "all" {
		t.Error("an empty set reported itself as all")
	}
}

// Google may grant fewer scopes than were asked for, and the profile has
// to record what was granted rather than what was requested — the
// difference is what makes status able to say a tool will fail.
func TestGrantedScopesReadsWhatGoogleReturned(t *testing.T) {
	token := (&oauth2.Token{AccessToken: "x"}).WithExtra(map[string]any{
		"scope": "https://www.googleapis.com/auth/chat.messages email",
	})
	got := grantedScopes(token, false)
	if len(got) == 0 {
		t.Fatal("no scopes read from the response")
	}
	if slicesContains(got, "https://www.googleapis.com/auth/chat.spaces") {
		t.Error("a scope Google did not grant was recorded as granted")
	}

	// No scope member at all is Google saying "everything you asked
	// for", which is the documented shape and must not read as none.
	bare := grantedScopes(&oauth2.Token{AccessToken: "x"}, false)
	if len(bare) == 0 {
		t.Error("a response naming no scopes recorded none granted, rather than all requested")
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestWarnIsPrefixed(t *testing.T) {
	var b bytes.Buffer
	warn(&b, "the keyring is unavailable")
	if !strings.HasPrefix(b.String(), "warning: ") {
		t.Errorf("warn wrote %q, want a warning prefix", b.String())
	}
}

// confirm reads stdin, and anything that is not an explicit yes has to
// be a no: logout revokes access, so a stray newline must not take it.
func TestConfirmDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true}, {"YES\n", true},
		{"\n", false}, {"n\n", false}, {"no\n", false}, {"maybe\n", false},
		{"", false}, // EOF, as a closed or piped stdin gives
	} {
		t.Run(strings.TrimSpace(tc.in)+"|", func(t *testing.T) {
			restore := swapStdin(t, tc.in)
			defer restore()
			var prompt bytes.Buffer
			if got := confirm(&prompt, "Revoke? [y/N] "); got != tc.want {
				t.Errorf("confirm(%q) = %t, want %t", tc.in, got, tc.want)
			}
			if prompt.Len() == 0 {
				t.Error("confirm asked nothing")
			}
		})
	}
}

// swapStdin points os.Stdin at a pipe holding body, and hands back the
// restore. confirm reads the real os.Stdin, so there is no seam to
// inject through.
func swapStdin(t *testing.T, body string) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	saved := os.Stdin
	os.Stdin = r
	return func() { os.Stdin = saved; _ = r.Close() }
}

// Status is the command a person runs when something is wrong, so the
// signed-in shape has to name the account, where the token came from,
// and what the server will and will not do.
func TestStatusReportsTheSignedInProfile(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	if err := userconfig.Save("default", userconfig.Config{
		ClientSecretPath: "/tmp/client_secret.json",
		AccountEmail:     "janedoe@example.com",
		TokenStore:       "keyring",
		Scopes:           []string{"https://www.googleapis.com/auth/chat.messages"},
	}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	for _, want := range []string{
		"janedoe@example.com", "client json:", "token store:",
		"read only:", "deletes:", "toolsets:", "chat api:",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status did not report %q:\n%s", want, stdout)
		}
	}
	// Scopes were granted narrowly above, so status owes the reader the
	// list of what is missing rather than a silent partial install.
	if !strings.Contains(stdout, "not granted") {
		t.Errorf("status did not report the ungranted scopes:\n%s", stdout)
	}
}

// Not signed in is an ordinary state, not a failure, and the exit code
// says so — a wrapper script that treats it as an error is a worse
// outcome than the missing login.
func TestStatusWithNoProfileSucceedsAndSaysWhatToDo(t *testing.T) {
	tempProfile(t)
	code, stdout, stderr := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "not signed in") {
		t.Errorf("stdout = %q, want it to say the account is not signed in", stdout)
	}
	if !strings.Contains(stdout, "login") {
		t.Errorf("stdout = %q, want it to name the command that fixes this", stdout)
	}
}

// Logout with nothing stored must not fail, and must not ask Google to
// revoke a token it does not have.
func TestLogoutWithNothingStoredIsNotAnError(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	code, stdout, stderr := runCmd(t, "logout")
	if code != 0 {
		t.Fatalf("logout exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Nothing to do") {
		t.Errorf("stdout = %q, want it to say there was nothing stored", stdout)
	}
}

// Declining the prompt has to leave the token where it is. The opposite
// bug — revoking on a stray keypress — is unrecoverable without a fresh
// login, and the person said no.
func TestLogoutCancelledKeepsTheToken(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	store, err := credentialStore(loadedConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("refresh-token-value"); err != nil {
		t.Fatal(err)
	}

	restore := swapStdin(t, "n\n")
	defer restore()

	code, stdout, stderr := runCmd(t, "logout")
	if code != 0 {
		t.Fatalf("logout exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Cancelled") {
		t.Errorf("stdout = %q, want it to report the cancellation", stdout)
	}
	if _, _, err := store.ResolveStored(); err != nil {
		t.Error("declining the prompt deleted the refresh token anyway")
	}
}

// Login with no OAuth client anywhere has to name the flag and point at
// the setup document. This is the first thing a new person hits, and a
// bare "failed" leaves them with nowhere to go.
func TestLoginWithoutAClientSaysWhereToGetOne(t *testing.T) {
	tempProfile(t)
	code, _, stderr := runCmd(t, "login")
	if code == 0 {
		t.Fatal("login succeeded with no OAuth client")
	}
	if !strings.Contains(stderr, "--client-secret") {
		t.Errorf("stderr = %q, want it to name the flag", stderr)
	}
	if !strings.Contains(stderr, "gcp-setup") {
		t.Errorf("stderr = %q, want it to point at the setup document", stderr)
	}
}

// The argument is checked before anything reaches Google, so a mistyped
// sample size fails in a tenth of a second rather than after a login.
func TestDoctorRefusesAnEmptySampleBeforeCallingGoogle(t *testing.T) {
	tempProfile(t)
	code, _, stderr := runCmd(t, "doctor", "--spaces", "0")
	if code == 0 {
		t.Fatal("doctor accepted a sample of no spaces")
	}
	if !strings.Contains(stderr, "at least 1") {
		t.Errorf("stderr = %q, want it to say what the bound is", stderr)
	}
}
