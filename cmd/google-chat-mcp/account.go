package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/google-chat-mcp/internal/auth"
	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/credentials"
	"github.com/mmedum/google-chat-mcp/internal/doctor"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
	"github.com/mmedum/google-chat-mcp/internal/scopes"
	"github.com/mmedum/google-chat-mcp/internal/service"
	"github.com/mmedum/google-chat-mcp/internal/userconfig"
)

// cmdLogin runs the loopback OAuth flow and stores the refresh token.
func cmdLogin(args []string, stdout, stderr io.Writer) int {
	var noBrowser bool
	cfg, err := loadConfig("login", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&noBrowser, "no-browser", false, "print the authorization URL instead of opening a browser")
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}

	stored, err := userconfig.Load(cfg.Profile)
	if err != nil && !errors.Is(err, userconfig.ErrNotFound) {
		return fail(stderr, "%v", err)
	}

	path := cfg.ClientSecretPath
	if path == "" {
		path = stored.ClientSecretPath
	}
	if path == "" {
		return fail(stderr, "no OAuth client. Pass --client-secret <path to the Desktop client JSON>, "+
			"or set %sCLIENT_SECRET. docs/gcp-setup.md walks through creating one.", config.EnvPrefix)
	}

	admin := cfg.Enabled(config.ToolsetAdmin)
	oauthCfg, err := auth.LoadClientSecret(path, auth.Scopes(admin))
	if err != nil {
		return fail(stderr, "%v", err)
	}

	// The prompt and the URL go to stderr. A person may pipe stdout,
	// and a swallowed authorization URL leaves the flow stuck with no
	// visible reason.
	opts := auth.LoginOptions{Out: stderr}
	if noBrowser {
		// A no-op keeps the URL on stderr and opens nothing, which is
		// what a headless host or a remote shell needs.
		opts.OpenBrowser = func(string) error { return nil }
	}
	token, err := auth.Login(context.Background(), oauthCfg, opts)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if token.RefreshToken == "" {
		return fail(stderr, "Google returned no refresh token. Revoke this app's access at "+
			"https://myaccount.google.com/permissions and run login again.")
	}

	store, err := credentialStore(cfg, func(msg string) { warn(stderr, msg) })
	if err != nil {
		return fail(stderr, "%v", err)
	}
	source, err := store.Save(token.RefreshToken)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	granted := grantedScopes(token, admin)
	profile := userconfig.Config{
		ClientSecretPath: path,
		TokenStore:       string(source),
		Scopes:           granted,
	}
	if email := accountEmail(context.Background(), cfg, token.AccessToken); email != "" {
		profile.AccountEmail = email
	}
	if err := userconfig.Save(cfg.Profile, profile); err != nil {
		return fail(stderr, "%v", err)
	}

	_, _ = fmt.Fprintf(stdout, "Signed in%s. Refresh token stored in the %s.\n",
		accountSuffix(profile.AccountEmail), source)
	if missing := scopes.Missing(granted, admin); len(missing) > 0 {
		_, _ = fmt.Fprintf(stdout, "\n%d scope(s) were not granted, so some tools will report a [scope] error:\n", len(missing))
		for _, s := range missing {
			_, _ = fmt.Fprintf(stdout, "  %s\n", s)
		}
		_, _ = fmt.Fprintln(stdout, "\nAdd them to the OAuth consent screen, then run login again.")
	}
	return 0
}

// cmdLogout revokes the refresh token at Google and deletes it locally.
func cmdLogout(args []string, stdout, stderr io.Writer) int {
	var assumeYes bool
	cfg, err := loadConfig("logout", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&assumeYes, "yes", false, "do not ask for confirmation")
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}

	store, err := credentialStore(cfg, nil)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	// ResolveStored, not Resolve: logout revokes and deletes what it
	// stored, and Resolve would hand back GCM_REFRESH_TOKEN instead. That
	// revokes a token logout cannot delete, then deletes the stored one
	// without revoking it — leaving the grant live with no local copy
	// left to withdraw it, which is what the comment below is about.
	refresh, source, resolveErr := store.ResolveStored()
	if resolveErr != nil {
		_, _ = fmt.Fprintln(stdout, "Nothing to do: no stored credentials.")
		return 0
	}

	if !assumeYes && !confirm(stderr, fmt.Sprintf(
		"Revoke this app's access and delete the refresh token from the %s? [y/N] ", source)) {
		_, _ = fmt.Fprintln(stdout, "Cancelled.")
		return 0
	}

	// Revoking first is deliberate. Deleting the local copy of a token
	// that still works at Google leaves access granted with no way to
	// withdraw it from here.
	// Bounded: Revoke with a nil client uses http.DefaultClient, which
	// has no timeout, and Background() gave it no cancellation either —
	// so a stalled connection hung `logout` with nothing to stop it but
	// the person at the keyboard.
	revokeCtx, cancelRevoke := context.WithTimeout(context.Background(), revokeTimeout)
	defer cancelRevoke()
	if err := auth.Revoke(revokeCtx, nil, refresh); err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: could not revoke at Google (%v). "+
			"Remove access at https://myaccount.google.com/permissions.\n", err)
	}
	if err := store.Delete(); err != nil {
		return fail(stderr, "%v", err)
	}
	// The override is outside this command's reach, and someone who set
	// it for automation would otherwise read "signed out" and still be
	// signed in. Borrowed from google-docs-mcp, which had this and the
	// resolver above right when we had neither.
	if os.Getenv(credentials.EnvVar) != "" {
		_, _ = fmt.Fprintf(stderr, "note: %s is set; logout cannot revoke or remove it, "+
			"and the server will keep using it.\n", credentials.EnvVar)
	}
	if err := userconfig.Remove(cfg.Profile); err != nil {
		return fail(stderr, "%v", err)
	}
	_, _ = fmt.Fprintln(stdout, "Signed out. Run `google-chat-mcp login` to sign in again.")
	return 0
}

// cmdStatus prints where things are and what is missing.
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	cfg, err := loadConfig("status", args, stderr, nil)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	_, _ = fmt.Fprintf(stdout, "profile:      %s\n", cfg.Profile)
	if dir, err := userconfig.ProfileDir(cfg.Profile); err == nil {
		_, _ = fmt.Fprintf(stdout, "config dir:   %s\n", dir)
	}

	stored, err := userconfig.Load(cfg.Profile)
	switch {
	case errors.Is(err, userconfig.ErrNotFound):
		_, _ = fmt.Fprintln(stdout, "account:      not signed in")
		_, _ = fmt.Fprintln(stdout, "\nRun `google-chat-mcp login --client-secret <path>` to sign in.")
		return 0
	case err != nil:
		return fail(stderr, "%v", err)
	}

	_, _ = fmt.Fprintf(stdout, "account:      %s\n", cmp.Or(stored.AccountEmail, "(none)"))
	_, _ = fmt.Fprintf(stdout, "client json:  %s\n", cmp.Or(stored.ClientSecretPath, "(none)"))

	store, err := credentialStore(cfg, nil)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if _, source, err := store.Resolve(); err == nil {
		_, _ = fmt.Fprintf(stdout, "token store:  %s\n", source)
	} else {
		_, _ = fmt.Fprintf(stdout, "token store:  none (%v)\n", err)
	}

	_, _ = fmt.Fprintf(stdout, "read only:    %t\n", cfg.ReadOnly)
	_, _ = fmt.Fprintf(stdout, "deletes:      %t\n", !cfg.RefuseDeletes)
	_, _ = fmt.Fprintf(stdout, "toolsets:     %s\n", joinToolsets(cfg.Toolsets))
	_, _ = fmt.Fprintf(stdout, "chat api:     %s\n", cfg.ChatAPIBase)
	_, _ = fmt.Fprintf(stdout, "log:          %s %s\n", cfg.LogLevel, cfg.LogFormat)

	if missing := scopes.Missing(stored.Scopes, cfg.Enabled(config.ToolsetAdmin)); len(missing) > 0 {
		_, _ = fmt.Fprintf(stdout, "\n%d scope(s) not granted; tools needing them report a [scope] error:\n", len(missing))
		for _, s := range missing {
			_, _ = fmt.Fprintf(stdout, "  %s\n", s)
		}
		_, _ = fmt.Fprintln(stdout, "\nAdd them to the consent screen, then run `google-chat-mcp login` again.")
	}
	return 0
}

// cmdDoctor checks live responses against the models this server holds.
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	var sampleSpaces int
	cfg, err := loadConfig("doctor", args, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&sampleSpaces, "spaces", 3, "how many spaces to sample")
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if sampleSpaces < 1 {
		return fail(stderr, "--spaces must be at least 1")
	}

	// Output goes to stdout on purpose: this is a CLI command, not the
	// server, so the stdout-only-JSON-RPC rule does not apply here.
	log := config.NewLogger(cfg, stderr)
	d, err := build(context.Background(), cfg, log)
	if err != nil {
		return fail(stderr, "%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), doctorTimeout)
	defer cancel()

	// A Service with no email lookup: doctor reads Chat's wire fields,
	// and enriching every sampled row would add one People request per
	// person to a command that never looks at the answer.
	report, err := doctor.Run(ctx, service.New(d.Client, nil, cfg, log), sampleSpaces)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if _, err := report.WriteTo(stdout); err != nil {
		return fail(stderr, "%v", err)
	}
	return 0
}

// doctorTimeout bounds the whole walk. It samples a few spaces, so a
// run that has not finished by then is a sign of something wrong rather
// than of a large account.
const doctorTimeout = 2 * time.Minute

// revokeTimeout bounds the one network call `logout` makes.
const revokeTimeout = 30 * time.Second

// grantedScopes reads the scopes Google actually granted, which can be
// fewer than were asked for. A response that names none is taken at its
// word: everything asked for was granted.
func grantedScopes(token *oauth2.Token, admin bool) []string {
	raw, _ := token.Extra("scope").(string)
	if raw == "" {
		return scopes.Canonical(scopes.Requested(admin))
	}
	return scopes.Canonical(strings.Fields(raw))
}

// accountEmail asks Google who just signed in. A failure here is not
// worth failing a login over; the address is a convenience.
func accountEmail(ctx context.Context, cfg config.Config, accessToken string) string {
	client := gchat.New(gchat.Options{
		ChatBase:   cfg.ChatAPIBase,
		PeopleBase: cfg.PeopleAPIBase,
		Timeout:    cfg.HTTPTimeout,
		Tokens:     auth.StaticTokens(accessToken),
	})
	info, err := client.Userinfo(ctx)
	if err != nil {
		return ""
	}
	return info.Email
}

func accountSuffix(email string) string {
	if email == "" {
		return ""
	}
	return " as " + email
}

func joinToolsets(ts []config.Toolset) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = string(t)
	}
	if slices.Equal(names, toolsetNames()) {
		return "all"
	}
	return strings.Join(names, ", ")
}

func toolsetNames() []string {
	names := make([]string, len(config.AllToolsets))
	for i, t := range config.AllToolsets {
		names[i] = string(t)
	}
	return names
}

// warn reports something the person should know but that does not stop
// the command, such as the keyring being unavailable at login.
func warn(w io.Writer, msg string) { _, _ = fmt.Fprintln(w, "warning: "+msg) }

// confirm asks a yes-or-no question on stderr and reads stdin.
func confirm(w io.Writer, prompt string) bool {
	_, _ = fmt.Fprint(w, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
