package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

// tokenServer stands in for Google's token endpoint.
func tokenServer(t *testing.T, handler http.HandlerFunc) *oauth2.Config {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &oauth2.Config{
		ClientID:     "cid",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
	}
}

func TestAccessTokensRefreshes(t *testing.T) {
	cfg := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`)
	})
	got, err := NewAccessTokens(context.Background(), cfg, "refresh-token").Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "fresh" {
		t.Errorf("token = %q", got)
	}
}

// A revoked or superseded refresh token is the one failure that signing
// in again fixes, so it has to be recognisable rather than generic.
func TestARevokedRefreshTokenAsksForANewLogin(t *testing.T) {
	cfg := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	})
	_, err := NewAccessTokens(context.Background(), cfg, "revoked").Token(context.Background())
	if !errors.Is(err, ErrReauthorize) {
		t.Fatalf("error = %v, want ErrReauthorize", err)
	}
}

// A server that is merely down must not read as a revoked token: it
// would send the person through a login they do not need.
func TestATransientFailureIsNotAReauthorize(t *testing.T) {
	cfg := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := NewAccessTokens(context.Background(), cfg, "still-good").Token(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrReauthorize) {
		t.Errorf("a 503 read as a revoked token: %v", err)
	}
}

func TestStaticTokens(t *testing.T) {
	got, err := StaticTokens("abc").Token(context.Background())
	if err != nil || got != "abc" {
		t.Errorf("Token = %q, %v", got, err)
	}
	if _, err := StaticTokens("").Token(context.Background()); !errors.Is(err, ErrReauthorize) {
		t.Errorf("an empty token should ask for a login, got %v", err)
	}
}

func TestIsInvalidGrant(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"typed code", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, true},
		{"body only", &oauth2.RetrieveError{
			Response: &http.Response{StatusCode: 400},
			Body:     []byte(`{"error":"invalid_grant"}`),
		}, true},
		{"other oauth error", &oauth2.RetrieveError{ErrorCode: "invalid_scope"}, false},
		{"plain error", errors.New("connection reset"), false},
		{"text fallback", errors.New(`oauth2: "invalid_grant" bad token`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInvalidGrant(tc.err); got != tc.want {
				t.Errorf("isInvalidGrant = %v, want %v", got, tc.want)
			}
		})
	}
}
