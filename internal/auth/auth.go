// Package auth implements Google's documented OAuth flow for desktop
// applications: a loopback redirect on 127.0.0.1 with a random port and
// PKCE, then a refresh-token-backed token source for API calls.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// Scopes returns the scope set login requests. It is the full set
// whether or not the server is read-only: asking for a narrower set
// would force a second consent screen the day someone turns writes back
// on, and GCM_READ_ONLY controls which tools are registered, which is
// the enforcement that matters.
//
// The one scope that does vary is the administrator's, because it is
// the one nobody but an administrator can use. admin comes from the
// toolset of that name.
func Scopes(admin bool) []string { return scopes.Requested(admin) }

// ErrNotDesktopClient means the JSON is not a "Desktop app" OAuth client.
var ErrNotDesktopClient = errors.New("auth: client secret JSON is not a Desktop app client (expected an \"installed\" section)")

// LoadClientSecret reads a Desktop-app client JSON downloaded from the
// Google Cloud console and returns an oauth2.Config for the scopes.
func LoadClientSecret(path string, scopes []string) (*oauth2.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("auth: read client secret %s: %w", path, err)
	}
	return ParseClientSecret(data, scopes)
}

// ParseClientSecret is LoadClientSecret on bytes.
func ParseClientSecret(data []byte, scopes []string) (*oauth2.Config, error) {
	var file struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			AuthURI      string `json:"auth_uri"`
			TokenURI     string `json:"token_uri"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("auth: client secret is not valid JSON: %w", err)
	}
	if file.Installed == nil {
		return nil, ErrNotDesktopClient
	}
	in := file.Installed
	if in.ClientID == "" {
		return nil, errors.New("auth: client secret JSON has no client_id")
	}
	authURL, tokenURL := in.AuthURI, in.TokenURI
	if authURL == "" {
		authURL = GoogleAuthURL
	}
	if tokenURL == "" {
		tokenURL = GoogleTokenURL
	}
	return &oauth2.Config{
		ClientID:     in.ClientID,
		ClientSecret: in.ClientSecret,
		Scopes:       scopes,
		Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}, nil
}

// Google's OAuth endpoints, used when the client JSON omits them.
const (
	GoogleAuthURL  = "https://accounts.google.com/o/oauth2/auth"
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
)

// LoginOptions tune the interactive flow. Zero values are sensible.
type LoginOptions struct {
	// OpenBrowser is called with the authorization URL. nil uses the OS
	// default browser; a function that returns an error is not fatal, the
	// URL is always printed to Out as well.
	OpenBrowser func(url string) error
	// Out receives the URL and progress messages. nil discards them.
	Out io.Writer
	// Timeout bounds the wait for the browser. Default 5 minutes.
	Timeout time.Duration
	// Listener overrides the loopback listener (tests).
	Listener net.Listener
}

// Login runs the loopback authorization-code flow and returns a token
// that includes a refresh token.
func Login(ctx context.Context, cfg *oauth2.Config, opts LoginOptions) (*oauth2.Token, error) {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ln := opts.Listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("auth: listen on loopback: %w", err)
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port

	conf := *cfg
	conf.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	state, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := conf.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	)

	type outcome struct {
		code string
		err  error
	}
	results := make(chan outcome, 1)
	deliver := func(o outcome) {
		select {
		case results <- o:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch; start the login again", http.StatusBadRequest)
			deliver(outcome{err: errors.New("auth: state mismatch on callback")})
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization failed: "+e, http.StatusBadRequest)
			deliver(outcome{err: fmt.Errorf("auth: authorization denied: %s", e)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			deliver(outcome{err: errors.New("auth: callback without code")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, successPage)
		deliver(outcome{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	_, _ = fmt.Fprintf(out, "Open this URL in your browser to authorize google-chat-mcp:\n\n%s\n\n", authURL)
	open := opts.OpenBrowser
	if open == nil {
		open = OpenBrowser
	}
	if err := open(authURL); err != nil {
		_, _ = fmt.Fprintf(out, "(could not open a browser automatically: %v)\n", err)
	}
	_, _ = fmt.Fprintln(out, "Waiting for the browser to finish...")

	var code string
	select {
	case o := <-results:
		if o.err != nil {
			return nil, o.err
		}
		code = o.code
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, errors.New("auth: timed out waiting for the browser")
	}

	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("auth: exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("auth: Google returned no refresh token; remove the app at https://myaccount.google.com/permissions and log in again")
	}
	return tok, nil
}

// TokenSource returns a caching token source backed by the refresh token.
func TokenSource(ctx context.Context, cfg *oauth2.Config, refreshToken string) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}))
}

// WithBoundedHTTP puts a client with a timeout in ctx for oauth2 to
// refresh with.
//
// Without it a refresh runs on http.DefaultClient, which has no timeout
// at all: a token endpoint that accepts a connection and never answers
// hangs the first tool call of a session with nothing able to interrupt
// it. GCM_HTTP_TIMEOUT_SECONDS covers the Chat client and never reached
// this one. Raised by the Phase 3 review and by the google-docs-mcp
// session, which hit the same hole and fixed it the same way.
func WithBoundedHTTP(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: timeout})
}

// Endpoints used for revocation and token inspection. Vars so tests can
// point them at a local server.
var (
	RevokeURL    = "https://oauth2.googleapis.com/revoke"
	TokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"
)

// Revoke invalidates a refresh (or access) token at Google.
func Revoke(ctx context.Context, client *http.Client, token string) error {
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: revoke: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("auth: revoke returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// TokenInfo describes an access token as Google sees it.
type TokenInfo struct {
	Scopes    []string
	Email     string
	ExpiresIn time.Duration
	Audience  string
}

// Inspect calls the tokeninfo endpoint for an access token.
func Inspect(ctx context.Context, client *http.Client, accessToken string) (*TokenInfo, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u := TokenInfoURL + "?access_token=" + url.QueryEscape(accessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		// Without the strip, this leaks the token. Google's tokeninfo
		// endpoint takes the access token in the query string, and
		// net/http wraps every transport failure in a *url.Error whose
		// message renders the whole URL — so an ordinary timeout would
		// put a live credential into the caller's error and from there
		// into a log. internal/gchat strips the same way, for the search
		// term rather than for a token.
		return nil, fmt.Errorf("auth: tokeninfo: %w", withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: tokeninfo returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw struct {
		Scope     string `json:"scope"`
		Email     string `json:"email"`
		ExpiresIn string `json:"expires_in"`
		Aud       string `json:"aud"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("auth: tokeninfo: %w", err)
	}
	info := &TokenInfo{Email: raw.Email, Audience: raw.Aud}
	if raw.Scope != "" {
		info.Scopes = strings.Fields(raw.Scope)
	}
	if secs, err := strconv.Atoi(raw.ExpiresIn); err == nil {
		info.ExpiresIn = time.Duration(secs) * time.Second
	}
	return info, nil
}

// HasScopes reports whether every wanted scope was granted.
func HasScopes(granted, wanted []string) (missing []string) {
	have := make(map[string]bool, len(granted))
	for _, s := range granted {
		have[s] = true
	}
	for _, w := range wanted {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

// OpenBrowser opens url with the platform's default handler.
func OpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const successPage = `<!doctype html><meta charset="utf-8"><title>google-chat-mcp</title>
<body style="font-family:system-ui;margin:3rem"><h2>Signed in</h2>
<p>google-chat-mcp received the authorization. You can close this window.</p></body>`

// withoutURL strips the request URL out of a transport error.
//
// net/http wraps every transport failure in a *url.Error whose message
// renders the whole URL, query string included. The one caller here puts
// an access token in that query string.
func withoutURL(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Err == nil {
		return err
	}
	return fmt.Errorf("%s: %w", ue.Op, ue.Err)
}
