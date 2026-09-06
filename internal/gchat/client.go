// Package gchat is a raw REST client for the Google Chat and People
// APIs, with its own wire types.
//
// It does not use google.golang.org/api: that package drags in gRPC and
// telemetry for a handful of JSON calls, and its generated types accept
// or reject fields on their own terms, which is the opposite of what the
// drift rule needs.
//
// Nothing here knows about MCP. Tool shaping lives in internal/tools and
// the rules live in internal/service.
package gchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Google's published per-user limits for the Chat API, checked
// 2026-09-05: 15 reads and 1 write per second. The defaults below sit
// under those, and a burst absorbs the fan-out a single tool call makes
// when it enriches a page of messages with sender names.
const (
	defaultReadRate   = rate.Limit(10)
	defaultReadBurst  = 15
	defaultWriteRate  = rate.Limit(1)
	defaultWriteBurst = 3
)

// maxBackoff caps one wait, not the whole call. The caller's context
// bounds the call.
const maxBackoff = 30 * time.Second

// TokenSource returns an access token for the calling user. The stdio
// server backs this with the stored refresh token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Options configure a Client. Zero values take the defaults.
type Options struct {
	// HTTP is the transport. Tests inject one that never leaves the
	// process; the zero value builds a client with the timeout below.
	HTTP *http.Client
	// ChatBase, PeopleBase and OIDCBase are API roots without a
	// trailing slash.
	ChatBase   string
	PeopleBase string
	OIDCBase   string
	// Timeout bounds one HTTP attempt.
	Timeout time.Duration
	// MaxRetries is the number of extra attempts after the first.
	MaxRetries int
	// Tokens supplies the access token per request.
	Tokens TokenSource
	// Logger receives drift and retry lines. Never nil after New.
	Logger *slog.Logger
	// UserAgent identifies this build to Google.
	UserAgent string
	// ReadLimiter, PeopleLimiter and WriteLimiter override the
	// per-user defaults.
	ReadLimiter, PeopleLimiter, WriteLimiter *rate.Limiter
	// Sleep is time.Sleep in production and a stub in tests, so a
	// backoff test does not actually wait.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client calls the Chat and People APIs.
type Client struct {
	http       *http.Client
	chatBase   string
	peopleBase string
	oidcBase   string
	uploadBase string
	maxRetries int
	tokens     TokenSource
	log        *slog.Logger
	userAgent  string
	readLim    *rate.Limiter
	peopleLim  *rate.Limiter
	writeLim   *rate.Limiter
	sleep      func(ctx context.Context, d time.Duration) error
	// timeout bounds one attempt: the whole call for a JSON request,
	// and the response headers plus each individual Read for a
	// transfer. See attemptStream.
	timeout time.Duration
	// stream is http without its whole-request timeout, which would cut
	// a large download off partway through.
	stream *http.Client

	// allowed is every origin this client may send an access token to,
	// built once from the base URLs it was configured with. See
	// allowURL.
	allowed map[string]bool

	// driftOnce keeps the log to one line per field path. The counter
	// beside it is not deduped: an operator alerting on a rate needs
	// every occurrence, while a log line per row of every page is noise.
	driftOnce sync.Map
	driftSeen atomic.Int64
}

// New builds a Client. It never returns an error: a bad base URL is a
// configuration problem caught in internal/config.
func New(o Options) *Client {
	c := &Client{
		http:       o.HTTP,
		chatBase:   strings.TrimRight(o.ChatBase, "/"),
		peopleBase: strings.TrimRight(o.PeopleBase, "/"),
		oidcBase:   strings.TrimRight(o.OIDCBase, "/"),
		maxRetries: o.MaxRetries,
		tokens:     o.Tokens,
		log:        o.Logger,
		userAgent:  o.UserAgent,
		readLim:    o.ReadLimiter,
		peopleLim:  o.PeopleLimiter,
		writeLim:   o.WriteLimiter,
		sleep:      o.Sleep,
	}
	c.timeout = o.Timeout
	if c.timeout <= 0 {
		c.timeout = 10 * time.Second
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: c.timeout}
	}
	if c.chatBase == "" {
		c.chatBase = "https://chat.googleapis.com/v1"
	}
	if c.peopleBase == "" {
		c.peopleBase = "https://people.googleapis.com/v1"
	}
	if c.oidcBase == "" {
		c.oidcBase = DefaultOIDCBase
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.userAgent == "" {
		c.userAgent = "google-chat-mcp"
	}
	if c.readLim == nil {
		c.readLim = rate.NewLimiter(defaultReadRate, defaultReadBurst)
	}
	if c.peopleLim == nil {
		c.peopleLim = rate.NewLimiter(defaultReadRate, defaultReadBurst)
	}
	if c.writeLim == nil {
		c.writeLim = rate.NewLimiter(defaultWriteRate, defaultWriteBurst)
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	c.uploadBase = uploadBaseOf(c.chatBase)
	c.allowed = map[string]bool{}
	for _, base := range []string{c.chatBase, c.peopleBase, c.oidcBase, c.uploadBase} {
		if u, err := url.Parse(base); err == nil {
			c.allowed[originKey(u)] = true
		}
	}
	// Both clients are copies, so the allowlist can be put on the
	// redirect hook without reaching into a client the caller handed in.
	// They share a Transport, which is what keeps one connection pool.
	withRedirects := *c.http
	withRedirects.CheckRedirect = c.checkRedirect
	c.http = &withRedirects
	streamClient := withRedirects
	streamClient.Timeout = 0
	c.stream = &streamClient
	return c
}

// maxRedirects is Go's own default, restated because setting
// CheckRedirect replaces the check that enforces it.
const maxRedirects = 10

// checkRedirect runs the host allowlist on every hop, not just the
// first.
//
// net/http drops the Authorization header on a redirect to a different
// domain, but keeps it for a subdomain of the one asked for. So a 302
// from chat.googleapis.com to anything under it would carry the access
// token to an origin this client was never configured to reach — which
// is the invariant the allowlist exists for, walked around one hop
// later.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("chat api: stopped after %d redirects", maxRedirects)
	}
	if !c.allowURL(req.URL) {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, req.URL.Host)
	}
	return nil
}

// uploadBaseOf is where a media upload goes: the Chat base with /upload
// in front of its path, which is how Google spells the upload endpoint
// — https://chat.googleapis.com/upload/v1/{parent}/attachments:upload
// against a v1 base of https://chat.googleapis.com/v1. Derived rather
// than configured, so there is no second setting to keep in step with
// the first, and resolved once in New rather than per upload.
func uploadBaseOf(chatBase string) string {
	u, err := url.Parse(chatBase)
	if err != nil {
		return chatBase
	}
	u.Path = "/upload" + u.Path
	return strings.TrimRight(u.String(), "/")
}

// ErrHostNotAllowed is returned when a request would send the access
// token somewhere this client was not configured to reach.
var ErrHostNotAllowed = errors.New("chat api: refusing to send credentials off the configured hosts")

// allowURL reports whether an access token may go to this URL.
//
// The allowlist is the origins this client was configured with, and
// nothing else. That is the point: Google hands out URLs in response
// bodies — an attachment's downloadUri and thumbnailUri, both on hosts
// this server never calls — and Google's own reference says not to
// fetch attachment content through them. A URL that arrived in a
// payload is therefore refused here rather than trusted for looking
// Google-ish.
//
// It is checked where the request is built, before the Authorization
// header goes on, so there is no path that attaches a credential first
// and validates after.
func (c *Client) allowURL(u *url.URL) bool { return c.allowed[originKey(u)] }

// originKey normalises a URL down to what the allowlist compares:
// scheme, hostname and port.
//
// The hostname is lowercased and a trailing dot trimmed, because
// "CHAT.googleapis.com." and "chat.googleapis.com" are one host and a
// comparison on the raw authority would call them two. The port is
// kept, not stripped: an unexpected port means a different endpoint,
// and this check decides whether an access token leaves the machine, so
// it is wrong in the direction of refusing. Both sibling servers had
// exactly one of these two halves.
func originKey(u *url.URL) string {
	key := u.Scheme + "://" + strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if port := u.Port(); port != "" {
		key += ":" + port
	}
	return key
}

// DriftCount is how many unknown response fields have been seen. The
// doctor command reports it; tests assert on it.
func (c *Client) DriftCount() int64 { return c.driftSeen.Load() }

// DriftPaths is every unknown field path seen so far, sorted. The
// doctor command prints them; a person comparing them against Google's
// release notes is how a new field becomes a modelled one.
func (c *Client) DriftPaths() []string {
	var out []string
	c.driftOnce.Range(func(k, _ any) bool {
		if path, ok := k.(string); ok {
			out = append(out, path)
		}
		return true
	})
	slices.Sort(out)
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pageQuery builds the paging parameters every listing shares.
func pageQuery(size int, token string) url.Values {
	q := url.Values{}
	if size > 0 {
		q.Set("pageSize", strconv.Itoa(size))
	}
	if token != "" {
		q.Set("pageToken", token)
	}
	return q
}

// api names which service a request goes to.
type api int

const (
	chatAPI api = iota
	peopleAPI
	oidcAPI
	// uploadAPI is Chat again, at the /upload prefix Google serves
	// media uploads from.
	uploadAPI
)

// DefaultOIDCBase serves the OpenID Connect userinfo endpoint, which is
// how the server learns whose token it holds.
const DefaultOIDCBase = "https://openidconnect.googleapis.com/v1"

// request is one call to Google.
type request struct {
	api    api
	method string
	// name is the resource this call addresses, as Google spells it:
	// "spaces/{s}/messages/{m}". It comes from a caller and is escaped
	// by resolvePath, so it is never escaped on the way in.
	name string
	// prefix is a literal fragment written in this package that comes
	// before the name: "media", for the one endpoint Google addresses
	// as a collection holding a whole resource name rather than the
	// other way round.
	prefix string
	// path is a literal fragment written in this package and never taken
	// from a caller: the collection under name, "messages", or the whole
	// path when there is no name, "spaces".
	path string
	// verb is a custom method, appended as ":setup" after everything
	// else.
	verb string
	// query is appended as the query string.
	query url.Values
	// body is marshalled as JSON when not nil.
	body any
	// accept overrides the Accept header. Empty asks for JSON, which is
	// every call but a media download.
	accept string
	// contentType overrides the request's own Content-Type. Empty says
	// JSON, which is every body but a media upload's multipart one.
	contentType string
	// readOnly marks a POST that changes nothing. Google models message
	// search as a POST because a filter is too long for a URL, and
	// deriving write-ness from the method alone would put a search on
	// the write limiter, refuse to retry it, and block it under the
	// context a dry run uses.
	//
	// It is the one thing here that can be set wrongly rather than
	// forgotten: leaving it off a search costs speed, and putting it on
	// a real write would let that write run during a dry run. So it is
	// spelled out, and TestNoPathIsBuiltByHand allows it only on a
	// method whose name says it searches.
	readOnly bool
	// scope is the OAuth scope Google documents for this endpoint. It
	// leaves on *APIError, so a refusal for want of consent names the
	// scope without every caller restating it. Where two scopes both
	// work, this is the narrower one: a deployer who declined the
	// restricted umbrella should not be pushed back into that tier by
	// the prompt that says what to grant.
	scope string
	// alsoScopes are further scopes the same call needs, beyond scope.
	// Always because an argument widens what the call reads: is_unread()
	// on a message search reads the caller's read state as well as the
	// messages, and a space-event listing needs the read scope of every
	// kind of event it asks for. A refusal names all of them, because
	// Google's answer does not say which one was declined.
	alsoScopes []string
	// idempotent lets a POST be repeated, and is the one thing here that
	// still has to be declared. Only a call carrying a client-chosen key
	// Google will refuse a duplicate of may set it.
	idempotent bool
}

// resolvePath renders the path this request addresses.
//
// A literal prefix comes first, the name is escaped segment by segment
// and the verb is appended after, which is why this is one function
// rather than a rule at every call site: escaping a name with the verb already on it would encode
// the colon and address a section called "{id}:position". Nineteen call
// sites kept that order by hand, and the names they passed all happened
// to be pattern-checked first — which the attachment, custom emoji and
// space event names of Phase 4 are not.
func (r request) resolvePath() string {
	p := escapeName(r.name)
	if r.prefix != "" {
		if p != "" {
			p = "/" + p
		}
		p = r.prefix + p
	}
	if r.path != "" {
		if p != "" {
			p += "/"
		}
		p += r.path
	}
	if r.verb != "" {
		p += ":" + r.verb
	}
	return p
}

// escapeName percent-escapes each segment of a resource name, leaving
// the separators alone. Chat resource ids can carry characters that
// would otherwise change the path.
//
// The colon is escaped here and not by url.PathEscape, which leaves it
// alone as a legal path character. It has to be: the colon separates a
// resource from a custom method, so a name carrying one addresses a
// different call than the caller asked for. AIP-122 keeps colons out of
// resource ids, so nothing legitimate is altered, and Phase 4's
// attachment, custom emoji and space event names are the first that
// reach here with no pattern check behind them.
//
// resolvePath is its only caller, and TestNoPathIsBuiltByHand keeps it
// that way: a second one is a path built by hand again.
func escapeName(name string) string {
	var out []byte
	start := 0
	for i := range len(name) {
		if name[i] != '/' {
			continue
		}
		out = append(out, escapeSegment(name[start:i])...)
		out = append(out, '/')
		start = i + 1
	}
	out = append(out, escapeSegment(name[start:])...)
	return string(out)
}

// escapeSegment escapes one segment of a resource name.
func escapeSegment(segment string) string {
	return strings.ReplaceAll(url.PathEscape(segment), ":", "%3A")
}

// isWrite reports whether this request changes something at Google.
//
// Derived from the method, with readOnly the one declared exception. It was a hand-set field,
// and it gates three things: the write rate limiter, what may be
// retried, and — the one that matters — the guard that makes a dry run
// structural instead of a promise. An unmarked POST took the read
// limiter, retried like a read, and wrote during a dry run. Phase 4 adds
// twenty-five more tools, so that flag would have been set by hand
// sixty-odd times, and it only had to be missed once.
func (r request) isWrite() bool { return r.method != http.MethodGet && !r.readOnly }

// safeToRepeat reports whether a second attempt cannot apply the call
// twice.
//
// GET, PATCH, PUT and DELETE say so by their own semantics: each names a
// fixed target and puts it in a fixed state, so doing it again lands
// where doing it once did. A POST creates, and qualifies only when the
// caller supplies a key that lets Google refuse the duplicate —
// send_message's client-chosen message id is the only one here.
//
// Taken from the google-drive-mcp session, which reached it from the
// other direction: deriving from the method makes a new POST fail closed,
// where marking the safe ones leaves someone to remember.
func (r request) safeToRepeat() bool {
	return r.method != http.MethodPost || r.idempotent || r.readOnly
}

// noWritesKey marks a context under which no write may leave the
// process.
type noWritesKey struct{}

// WithoutWrites returns a context that refuses every write.
//
// It is what makes a dry run structural rather than a promise. Each
// tool still shapes its own preview, but the guarantee that a preview
// changes nothing is enforced here, at the one place a request can
// reach Google — so a tool that declares dry_run and forgets to act on
// it fails loudly instead of writing.
func WithoutWrites(ctx context.Context) context.Context {
	return context.WithValue(ctx, noWritesKey{}, true)
}

// writesForbidden reports whether ctx refuses writes.
func writesForbidden(ctx context.Context) bool {
	forbidden, _ := ctx.Value(noWritesKey{}).(bool)
	return forbidden
}

// ErrWriteForbidden is returned when a write is attempted under a
// context that forbids one. Reaching it is a bug in this server, not
// something a caller can cause.
var ErrWriteForbidden = errors.New("chat api: this call may not write")

// deleteName removes one resource by its name.
//
// The four deletes in this package differ only in what they address and
// what they need consent for, and this is the one place the write flag
// decides that a transport failure is not retried.
func (c *Client) deleteName(ctx context.Context, name, scope string) error {
	return c.do(ctx, request{method: "DELETE", name: name, scope: scope}, nil)
}

// endpointFor renders the URL a request goes to and the path its errors
// and logs carry. The path never includes the query, which is where a
// People search would put the caller's search term.
func (c *Client) endpointFor(r request) (endpoint, path string) {
	base := c.chatBase
	switch r.api {
	case peopleAPI:
		base = c.peopleBase
	case oidcAPI:
		base = c.oidcBase
	case uploadAPI:
		base = c.uploadBase
	case chatAPI:
	}
	path = r.resolvePath()
	endpoint = base + "/" + strings.TrimLeft(path, "/")
	if len(r.query) > 0 {
		endpoint += "?" + r.query.Encode()
	}
	return endpoint, path
}

// newRequest builds one HTTP request and checks the host allowlist
// before the access token goes on it.
//
// Both the JSON path and the transfer path build their requests here,
// so there is one place where a credential is attached and one place
// that decides whether it may be.
func (c *Client) newRequest(ctx context.Context, r request, endpoint, path string, body io.Reader, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, r.method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("chat api: build %s %s: %w", r.method, path, err)
	}
	if !c.allowURL(req.URL) {
		return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, req.URL.Host)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	accept := r.accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		contentType := r.contentType
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// limiter is the one Google meters this call under.
//
// Chat and People are metered separately, so they get one each: a burst
// of name lookups must not throttle the message read waiting behind it.
// It is chosen from the request rather than passed in, because a send
// path that picks its own would sooner or later put a write on the read
// limiter.
func (c *Client) limiter(r request) *rate.Limiter {
	switch {
	case r.isWrite():
		return c.writeLim
	case r.api == peopleAPI:
		return c.peopleLim
	}
	return c.readLim
}

// send is the policy every call to Google goes through, whatever it
// does with the answer.
//
// Three send paths exist — a JSON call, a download handed back unread,
// and a streamed upload — and they differ only in what they do with the
// response. Everything before that is the same and belongs in one
// place: a write may not run under a dry run, the token is resolved
// once because it is the same for every attempt and a refusal is
// terminal, the limiter is chosen from the request, and a retry follows
// the retry rule. Written this way because the alternative is what the
// review found: a path that skipped the dry-run guard, and two that
// each picked a limiter by hand.
//
// attempt is called once per try and returns what it made, how long
// Google asked to wait, and the error. retries says whether this path
// may be tried again at all — a streamed upload may not, because its
// body is a file being read as it goes and there is no second pass
// over it.
func send[T any](c *Client, ctx context.Context, r request, retries bool,
	attempt func(endpoint, path, token string) (T, time.Duration, error),
) (T, error) {
	var zero T
	endpoint, path := c.endpointFor(r)
	if r.isWrite() && writesForbidden(ctx) {
		return zero, fmt.Errorf("%w: %s %s", ErrWriteForbidden, r.method, path)
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return zero, err
	}
	limiter := c.limiter(r)

	tries := c.maxRetries
	if !retries {
		tries = 0
	}
	var lastErr error
	for try := 0; try <= tries; try++ {
		if try > 0 {
			if err := c.sleep(ctx, c.backoff(try, lastErr)); err != nil {
				return zero, err
			}
		}
		if err := limiter.Wait(ctx); err != nil {
			return zero, err
		}

		got, retryAfter, err := attempt(endpoint, path, token)
		switch {
		case err == nil:
			return got, nil
		case !c.shouldRetry(err, r):
			return zero, err
		}
		lastErr = &retryHint{err: err, after: retryAfter}
		c.log.Debug("upstream_retry",
			"method", r.method, "path", path, "try", try+1, "error", err)
	}
	var hint *retryHint
	if errors.As(lastErr, &hint) {
		return zero, hint.err
	}
	return zero, lastErr
}

// do performs a request and decodes the response into out, which may be
// nil for a call whose body is not needed.
func (c *Client) do(ctx context.Context, r request, out any) error {
	var payload []byte
	if r.body != nil {
		encoded, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("chat api: encode %s %s: %w", r.method, r.resolvePath(), err)
		}
		payload = encoded
	}

	body, err := send(c, ctx, r, true, func(endpoint, path, token string) ([]byte, time.Duration, error) {
		return c.attempt(ctx, r, endpoint, path, payload, token)
	})
	if err != nil || out == nil {
		return err
	}
	_, path := c.endpointFor(r)
	return decode(body, out, c.driftReporter(path))
}

// retryHint carries a Retry-After alongside the error that produced it.
type retryHint struct {
	err   error
	after time.Duration
}

func (r *retryHint) Error() string { return r.err.Error() }
func (r *retryHint) Unwrap() error { return r.err }

// shouldRetry decides whether another attempt is worth making.
//
// A read retries on any transient failure. A write retries only when
// Google answered, and only on the statuses it marks transient: a
// transport error on a write may mean the write landed, and repeating
// it would do the thing twice.
//
// An idempotent write is the exception, and it has to be declared
// rather than assumed. send_message carries a client-chosen message id,
// so Google refuses the second copy; that is what earns it the
// transport retry a plain write does not get.
func (c *Client) shouldRetry(err error, r request) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// A write with no idempotency key gets the narrower set. "Google
		// answered" is not the same as "Google did not do it": a 500 from
		// the backend or a 504 from a gateway can arrive after the write
		// landed, and repeating a create then makes two of whatever it
		// created. Only the answers that mean the request was turned away
		// before it was applied are safe here.
		if !r.safeToRepeat() {
			return turnedAway(apiErr.StatusCode)
		}
		return retryable(apiErr.StatusCode)
	}
	return r.safeToRepeat()
}

// attempt makes one HTTP call. path is the resolved path, which the
// error messages carry: it names the resource, never the query, where
// a People search would put the caller's search term.
func (c *Client) attempt(ctx context.Context, r request, endpoint, path string, payload []byte, token string) ([]byte, time.Duration, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := c.newRequest(ctx, r, endpoint, path, reader, token)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("chat api: %s %s: %w", r.method, path, withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("chat api: read %s %s: %w", r.method, path, err)
	}
	if resp.StatusCode >= 300 {
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")), r.apiError(resp.StatusCode, body, path)
	}
	return body, 0, nil
}

// backoff is exponential with full jitter, honouring a Retry-After that
// Google sent but bounding it on both sides.
//
// Jitter matters more than it looks: without it, every tool call that a
// single client fired in parallel wakes at the same moment and trips the
// same limit again.
func (c *Client) backoff(attempt int, last error) time.Duration {
	base := 500 * time.Millisecond << (attempt - 1)
	if base > maxBackoff {
		base = maxBackoff
	}
	// A Retry-After is a minimum, not a target, so it is honoured as sent
	// rather than jittered. Taking it as a base and jittering downward —
	// which this did — turned "wait 10 seconds" into a wait of five, and
	// three early retries fail a call that honouring the header would
	// have completed. Both sibling servers had this right.
	var hint *retryHint
	if errors.As(last, &hint) && hint.after > 0 {
		return min(hint.after, maxBackoff)
	}
	// Full jitter over the lower half keeps a floor, so a retry storm
	// does not collapse back onto zero.
	return base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
}

// withoutURL strips the request URL out of a transport error.
//
// net/http wraps every transport failure in a *url.Error, whose message
// renders the whole URL, query string included. A People search puts the
// caller's search term in that query string, so an ordinary timeout
// would write "who did this person look up" into the logs. The method
// and path are already in the wrapper around this, and they carry no
// query.
func withoutURL(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) || ue.Err == nil {
		return err
	}
	return fmt.Errorf("%s: %w", ue.Op, ue.Err)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// driftReporter counts every unknown field and logs each path once.
// Field names only: Google's names are safe to log, the values behind
// them are message text and email addresses.
func (c *Client) driftReporter(path string) DriftFunc {
	return func(field string) {
		c.driftSeen.Add(1)
		if _, seen := c.driftOnce.LoadOrStore(field, struct{}{}); seen {
			return
		}
		c.log.Warn("schema_drift", "field", field, "path", path)
	}
}
