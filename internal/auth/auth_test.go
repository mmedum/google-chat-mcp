package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

const desktopJSON = `{"installed":{"client_id":"cid.apps.googleusercontent.com","client_secret":"sec","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","redirect_uris":["http://localhost"]}}`

func TestParseClientSecret(t *testing.T) {
	cfg, err := ParseClientSecret([]byte(desktopJSON), Scopes(false))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "cid.apps.googleusercontent.com" || cfg.ClientSecret != "sec" || len(cfg.Scopes) != len(scopes.All) || cfg.Endpoint.TokenURL != GoogleTokenURL {
		t.Fatalf("config wrong: %+v", cfg)
	}
	if _, err := ParseClientSecret([]byte(`{"web":{"client_id":"x"}}`), nil); !errors.Is(err, ErrNotDesktopClient) {
		t.Fatalf("web client should be refused: %v", err)
	}
	if _, err := ParseClientSecret([]byte(`{"installed":{}}`), nil); err == nil {
		t.Fatal("missing client_id should fail")
	}
	if _, err := ParseClientSecret([]byte(`nope`), nil); err == nil {
		t.Fatal("bad json should fail")
	}
	cfg, err = ParseClientSecret([]byte(`{"installed":{"client_id":"a","client_secret":"b"}}`), nil)
	if err != nil || cfg.Endpoint.AuthURL != GoogleAuthURL {
		t.Fatalf("defaults not applied: %+v %v", cfg, err)
	}
	if _, err := LoadClientSecret("/nonexistent/client.json", nil); err == nil {
		t.Fatal("missing file should fail")
	}
	if got := Scopes(false); len(got) != len(scopes.All) {
		t.Fatalf("Scopes(false) = %d scopes, want %d", len(got), len(scopes.All))
	}
	// The admin scope is the one login asks for conditionally, so the
	// consent screen of someone who never turns the admin toolset on
	// never carries it.
	if got := Scopes(true); len(got) != len(scopes.All)+1 || !slices.Contains(got, scopes.AdminSpacesReadonly) {
		t.Errorf("Scopes(true) = %v, want the admin scope added", got)
	}
}

// fakeGoogle plays the authorization and token endpoints.
func fakeGoogle(t *testing.T) (*httptest.Server, *oauth2.Config) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &oauth2.Config{ClientID: "cid", ClientSecret: "sec", Scopes: Scopes(false),
		Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
	return srv, cfg
}

// browser simulates the user: it parses the auth URL and hits the
// loopback callback the way Google would redirect.
func browser(t *testing.T, mutate func(q url.Values)) func(string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
			t.Errorf("auth url lacks PKCE/offline/consent: %s", authURL)
		}
		cb, _ := url.Parse(q.Get("redirect_uri"))
		params := url.Values{"state": {q.Get("state")}, "code": {"the-code"}}
		if mutate != nil {
			mutate(params)
		}
		cb.RawQuery = params.Encode()
		go func() {
			resp, err := http.Get(cb.String())
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoginHappyPath(t *testing.T) {
	_, cfg := fakeGoogle(t)
	var out strings.Builder
	tok, err := Login(context.Background(), cfg, LoginOptions{OpenBrowser: browser(t, nil), Out: &out, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshToken != "rt" || tok.AccessToken != "at" {
		t.Fatalf("token = %+v", tok)
	}
	if !strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("URL not printed: %s", out.String())
	}
}

func TestLoginRejectsBadStateAndDenial(t *testing.T) {
	_, cfg := fakeGoogle(t)
	_, err := Login(context.Background(), cfg, LoginOptions{OpenBrowser: browser(t, func(q url.Values) { q.Set("state", "wrong") }), Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("bad state: %v", err)
	}
	_, err = Login(context.Background(), cfg, LoginOptions{OpenBrowser: browser(t, func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }), Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denial: %v", err)
	}
}

func TestLoginTimeoutAndCancel(t *testing.T) {
	_, cfg := fakeGoogle(t)
	noop := func(string) error { return errors.New("no browser here") }
	_, err := Login(context.Background(), cfg, LoginOptions{OpenBrowser: noop, Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err = Login(ctx, cfg, LoginOptions{OpenBrowser: noop, Timeout: 5 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestLoginWithoutRefreshToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","token_type":"Bearer","expires_in":3600}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Login(context.Background(), cfg, LoginOptions{OpenBrowser: browser(t, nil), Timeout: 5 * time.Second, Listener: ln})
	if err == nil || !strings.Contains(err.Error(), "no refresh token") {
		t.Fatalf("got %v", err)
	}
}

func TestTokenSourceRevokeInspect(t *testing.T) {
	var revoked string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt" {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		revoked = r.Form.Get("token")
		if revoked == "bad" {
			http.Error(w, `{"error":"invalid_token"}`, 400)
		}
	})
	mux.HandleFunc("/tokeninfo", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "fresh" {
			http.Error(w, `{"error":"invalid_token"}`, 400)
			return
		}
		_, _ = w.Write([]byte(`{"scope":"a b","email":"me@b.test","expires_in":"3599","aud":"cid"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old1, old2 := RevokeURL, TokenInfoURL
	RevokeURL, TokenInfoURL = srv.URL+"/revoke", srv.URL+"/tokeninfo"
	defer func() { RevokeURL, TokenInfoURL = old1, old2 }()

	cfg := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
	ts := TokenSource(context.Background(), cfg, "rt")
	tok, err := ts.Token()
	if err != nil || tok.AccessToken != "fresh" {
		t.Fatalf("token source: %+v %v", tok, err)
	}
	if _, err := TokenSource(context.Background(), cfg, "wrong").Token(); err == nil {
		t.Fatal("bad refresh token should fail")
	}
	if err := Revoke(context.Background(), nil, "rt"); err != nil || revoked != "rt" {
		t.Fatalf("revoke: %v", err)
	}
	if err := Revoke(context.Background(), nil, "bad"); err == nil {
		t.Fatal("revoke failure should surface")
	}
	info, err := Inspect(context.Background(), nil, "fresh")
	if err != nil || info.Email != "me@b.test" || len(info.Scopes) != 2 || info.ExpiresIn != 3599*time.Second || info.Audience != "cid" {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	if _, err := Inspect(context.Background(), nil, "stale"); err == nil {
		t.Fatal("inspect failure should surface")
	}
	if missing := HasScopes([]string{"a", "b"}, []string{"a", "c"}); len(missing) != 1 || missing[0] != "c" {
		t.Fatalf("HasScopes = %v", missing)
	}
}

// A transport failure against the tokeninfo endpoint must not carry the
// access token out with it. Google takes that token in the query string,
// and net/http renders the whole URL into every *url.Error.
func TestInspectDoesNotLeakTheTokenOnATransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	old := TokenInfoURL
	TokenInfoURL = srv.URL
	defer func() { TokenInfoURL = old }()

	const secret = "CANARY-access-token-must-not-appear"
	_, err := Inspect(context.Background(), srv.Client(), secret)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error carries the access token: %v", err)
	}
}

// A token endpoint that accepts a connection and never answers used to
// hang the first tool call of a session with nothing able to interrupt
// it: the refresh ran on http.DefaultClient, which has no timeout.
//
// A raw listener, not an httptest.Server: Close on one of those waits
// for the connection this test deliberately leaves open. The detail
// came from the google-docs-mcp session, which lost three attempts to
// it.
func TestARefreshGivesUpOnASilentTokenEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Accepted and ignored, which is the case a deadline on the
			// request cannot see and only a client timeout catches.
			_ = conn
		}
	}()

	cfg := &oauth2.Config{
		ClientID: "cid", ClientSecret: "sec",
		Endpoint: oauth2.Endpoint{TokenURL: "http://" + listener.Addr().String() + "/token"},
	}
	ctx := WithBoundedHTTP(context.Background(), 200*time.Millisecond)
	tokens := NewAccessTokens(ctx, cfg, "refresh-token")

	done := make(chan error, 1)
	go func() {
		_, err := tokens.Token(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent token endpoint should not yield a token")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh never gave up: it is running without a timeout")
	}
}
