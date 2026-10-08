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

	"github.com/mmedum/google-chat-mcp/v5/internal/config"
	"github.com/mmedum/google-chat-mcp/v5/internal/scopes"
	"github.com/mmedum/google-chat-mcp/v5/internal/userconfig"
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
	// Masked to the same shape as `status`: login is one command away from
	// it, so pasting either into an issue gives the same answer about what
	// is safe to share.
	if got := accountSuffix("janedoe@example.com"); got != " as …@example.com" {
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

	store, err := credentialStore(loadedConfig(t), nil)
	if err != nil {
		t.Fatalf("credentialStore: %v", err)
	}
	if _, err := store.Save("stored-refresh-token"); err != nil {
		t.Fatalf("save: %v", err)
	}

	code, stdout, stderr := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	// Where the token came from, what was granted, and a setting that is
	// off named as off rather than left blank.
	for _, want := range []string{
		"token store:    keyring\n",
		"scopes:         https://www.googleapis.com/auth/chat.messages\n",
		"local dir:      (unset)\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status did not print %q:\n%s", want, stdout)
		}
	}
	// The labels the four servers now share, in the shape they share —
	// and the account with its local part removed, keeping the domain,
	// which is the half that says whether shared drives and admin
	// policy apply at all.
	for _, want := range []string{
		"…@example.com", "client secret:", "token store:",
		"read-only:", "destructive:", "toolsets:", "chat api:",
		"profile:        ", "config dir:     ", "account:        ",
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

// With every scope granted there is nothing to add to the consent
// screen, and status says nothing about it rather than "0 scope(s)".
func TestStatusWithEveryScopeGrantedListsNoneMissing(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	if err := userconfig.Save("default", userconfig.Config{
		AccountEmail: "janedoe@example.com",
		TokenStore:   "keyring",
		Scopes:       scopes.All,
	}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCmd(t, "status")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "not granted") {
		t.Errorf("status reported missing scopes with every scope granted:\n%s", stdout)
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
func TestLogoutCanceledKeepsTheToken(t *testing.T) {
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
	if !strings.Contains(stdout, "Canceled") {
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

// One space is the smallest sample, and it gets past the argument check.
// Nobody is signed in, so the run stops at the login instead; nothing
// here can reach Google.
func TestDoctorAcceptsASampleOfOne(t *testing.T) {
	tempProfile(t)
	keyring.MockInit()
	t.Setenv("GCM_CHAT_API_BASE", "http://127.0.0.1:1/v1")
	t.Setenv("GCM_PEOPLE_API_BASE", "http://127.0.0.1:1/v1")
	t.Setenv("GCM_CLOUD_IDENTITY_API_BASE", "http://127.0.0.1:1/v1")
	_, _, stderr := runCmd(t, "doctor", "--spaces", "1")
	if strings.Contains(stderr, "at least 1") {
		t.Errorf("doctor refused a sample of one: %q", stderr)
	}
}

// A path given on the command line is the one login reads, even with
// nothing stored. The file is missing, so the error names it.
func TestLoginReadsTheClientSecretItWasGiven(t *testing.T) {
	tempProfile(t)
	code, _, stderr := runCmd(t, "login", "--client-secret", "/nonexistent/given-client.json")
	if code == 0 {
		t.Fatal("login succeeded with a client secret that does not exist")
	}
	if !strings.Contains(stderr, "given-client.json") {
		t.Errorf("stderr = %q, want the error to name the file it was given", stderr)
	}
}

// A profile that cannot be read stops login there. Going on would sign
// in over a profile nobody can see the contents of.
func TestLoginStopsAtAProfileItCannotRead(t *testing.T) {
	dir := tempProfile(t)
	path, err := userconfig.Path("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCmd(t, "login")
	if code == 0 {
		t.Fatal("login went on past a profile it could not parse")
	}
	if !strings.Contains(stderr, "parse") {
		t.Errorf("stderr = %q, want the parse failure, not a later error", stderr)
	}
}
