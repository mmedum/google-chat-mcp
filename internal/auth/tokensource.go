package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
)

// ErrReauthorize means the stored refresh token no longer works. The
// person has to sign in again; nothing the server retries will help.
var ErrReauthorize = errors.New("auth: the stored credentials are no longer valid")

// AccessTokens hands out access tokens for the calling user, refreshing
// through the stored refresh token when one expires.
type AccessTokens struct {
	src oauth2.TokenSource
}

// NewAccessTokens wraps a refresh token in a caching, refreshing source.
// The context bounds every refresh the source performs later, so it
// should outlive the server rather than one request.
func NewAccessTokens(ctx context.Context, cfg *oauth2.Config, refreshToken string) *AccessTokens {
	return &AccessTokens{src: TokenSource(ctx, cfg, refreshToken)}
}

// Token returns a valid access token, refreshing if needed.
//
// The context argument is ignored: oauth2 binds the context at
// construction. It is in the signature because the client calls this
// per request and would otherwise need a second interface.
func (a *AccessTokens) Token(context.Context) (string, error) {
	tok, err := a.src.Token()
	if err != nil {
		if isInvalidGrant(err) {
			return "", fmt.Errorf("%w: %w", ErrReauthorize, err)
		}
		return "", fmt.Errorf("auth: refresh access token: %w", err)
	}
	return tok.AccessToken, nil
}

// isInvalidGrant recognises the one refusal that re-signing in fixes: a
// revoked, expired or superseded refresh token. Google reports it as
// invalid_grant, and no amount of retrying changes the answer.
func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" {
			return true
		}
		if re.Response != nil && re.Response.StatusCode == 400 {
			return strings.Contains(string(re.Body), "invalid_grant")
		}
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

// StaticTokens is an access token supplied from outside, for tests and
// for automation that already holds one.
type StaticTokens string

// Token returns the fixed token.
func (s StaticTokens) Token(context.Context) (string, error) {
	if s == "" {
		return "", ErrReauthorize
	}
	return string(s), nil
}
