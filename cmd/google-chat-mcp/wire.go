package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/oauth2"

	"github.com/mmedum/google-chat-mcp/internal/auth"
	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/credentials"
	"github.com/mmedum/google-chat-mcp/internal/directory"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
	"github.com/mmedum/google-chat-mcp/internal/service"
	"github.com/mmedum/google-chat-mcp/internal/userconfig"
	"github.com/mmedum/google-chat-mcp/internal/version"
)

// deps is everything the server needs, assembled once at start.
type deps struct {
	Service *service.Service
	Client  *gchat.Client
	// Profile is the stored, non-secret profile state. Zero when the
	// person has not logged in yet.
	Profile userconfig.Config
	// TokenSource is nil when no credentials are stored.
	TokenSource gchat.TokenSource
	// CredentialSource says where the refresh token came from.
	CredentialSource credentials.Source
}

// build assembles the service.
//
// Missing credentials are not a startup failure. A stdio server that
// exits because nobody has logged in yet shows up in the client as
// "failed to connect", with no way to see why; one that starts and
// answers every tool call with an [auth] error tells the person exactly
// what to run. So the token source is allowed to be a stub that reports
// the problem on first use.
func build(ctx context.Context, cfg config.Config, log *slog.Logger) (*deps, error) {
	d := &deps{}

	profile, err := userconfig.Load(cfg.Profile)
	switch {
	case err == nil:
		d.Profile = profile
	case errors.Is(err, userconfig.ErrNotFound):
		log.Debug("no stored profile", "profile", cfg.Profile)
	default:
		return nil, err
	}

	tokens, source, err := loadTokenSource(ctx, cfg, d.Profile)
	if err != nil {
		log.Debug("no usable credentials", "error", err)
		tokens = notLoggedIn{}
	}
	d.TokenSource, d.CredentialSource = tokens, source

	d.Client = gchat.New(gchat.Options{
		ChatBase:   cfg.ChatAPIBase,
		PeopleBase: cfg.PeopleAPIBase,
		Timeout:    cfg.HTTPTimeout,
		MaxRetries: cfg.HTTPMaxRetries,
		Tokens:     d.TokenSource,
		Logger:     log,
		UserAgent:  "google-chat-mcp/" + version.String(),
	})
	d.Service = service.New(d.Client, directoryResolver(cfg, d.Client, log), cfg, log)
	return d, nil
}

// directoryResolver builds the email lookup, cached on disk.
//
// A cache path that cannot be resolved is not a startup failure. The
// cache saves a round trip; losing it costs latency, and refusing to
// start over it would cost the whole server.
func directoryResolver(cfg config.Config, client *gchat.Client, log *slog.Logger) *directory.Resolver {
	path, err := userconfig.DirectoryCachePath(cfg.Profile)
	if err != nil {
		log.Debug("directory cache disabled", "error", err)
	}
	return directory.NewResolver(client, directory.NewCache(path, cfg.DirectoryCacheTTL, log), log)
}

// loadTokenSource resolves the refresh token and wraps it in a source
// that refreshes on demand.
func loadTokenSource(ctx context.Context, cfg config.Config, profile userconfig.Config) (gchat.TokenSource, credentials.Source, error) {
	oauthCfg, err := clientConfig(cfg, profile)
	if err != nil {
		return nil, "", err
	}
	store, err := credentialStore(cfg, nil)
	if err != nil {
		return nil, "", err
	}
	refresh, source, err := store.Resolve()
	if err != nil {
		return nil, "", err
	}
	// The refresh gets the same bound as a Chat call. It is a different
	// client from the one the Chat requests use, and it had none.
	return auth.NewAccessTokens(auth.WithBoundedHTTP(ctx, cfg.HTTPTimeout), oauthCfg, refresh), source, nil
}

// clientConfig loads the OAuth Desktop client JSON. The flag or
// environment wins over the path recorded at login.
func clientConfig(cfg config.Config, profile userconfig.Config) (*oauth2.Config, error) {
	path := cfg.ClientSecretPath
	if path == "" {
		path = profile.ClientSecretPath
	}
	if path == "" {
		var err error
		if path, err = userconfig.DefaultClientSecretPath(cfg.Profile); err != nil {
			return nil, err
		}
	}
	return auth.LoadClientSecret(path, auth.Scopes(cfg.Enabled(config.ToolsetAdmin)))
}

// credentialStore resolves the refresh token: the environment first for
// automation, then the OS keyring, then a 0600 file written only when
// the keyring was unavailable at login.
func credentialStore(cfg config.Config, warn func(string)) (*credentials.Store, error) {
	tokenPath, err := userconfig.TokenFilePath(cfg.Profile)
	if err != nil {
		return nil, err
	}
	return &credentials.Store{
		Profile:  cfg.Profile,
		Keyring:  credentials.OSKeyring(),
		FilePath: tokenPath,
		Warn:     warn,
	}, nil
}

// notLoggedIn stands in when no credentials are stored, so the server
// still starts and every tool call explains what to do.
type notLoggedIn struct{}

func (notLoggedIn) Token(context.Context) (string, error) {
	return "", fmt.Errorf("%w. Run `google-chat-mcp login --client-secret <path>` first", auth.ErrReauthorize)
}
