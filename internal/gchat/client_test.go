package gchat

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// staticToken is a TokenSource that never calls Google.
type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// failingToken stands in for an expired refresh token.
type failingToken struct{ err error }

func (f failingToken) Token(context.Context) (string, error) { return "", f.err }

// newTestClient points a Client at srv and makes every wait instant, so
// a retry test costs no wall-clock time.
func newTestClient(t *testing.T, srv *httptest.Server, opts ...func(*Options)) *Client {
	t.Helper()
	o := Options{
		HTTP:         srv.Client(),
		ChatBase:     srv.URL + "/v1",
		PeopleBase:   srv.URL + "/people",
		OIDCBase:     srv.URL + "/oidc",
		Tokens:       staticToken("test-token"),
		MaxRetries:   3,
		ReadLimiter:  rate.NewLimiter(rate.Inf, 1),
		WriteLimiter: rate.NewLimiter(rate.Inf, 1),
		Sleep:        func(context.Context, time.Duration) error { return nil },
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func TestListSpacesSendsTheRequestGoogleExpects(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/AAA","displayName":"Team"}],"nextPageToken":"tok"}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).ListSpaces(context.Background(), ListSpacesOptions{
		Filter: `spaceType = "SPACE"`, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if gotPath != "/v1/spaces" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "pageSize=50") || !strings.Contains(gotQuery, "filter=") {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer test-token" || gotAccept != "application/json" {
		t.Errorf("headers = %q / %q", gotAuth, gotAccept)
	}
	if len(got.Spaces) != 1 || got.Spaces[0].DisplayName != "Team" || got.NextPageToken != "tok" {
		t.Errorf("response = %+v", got)
	}
}

// Paging is the caller's decision. A tool that looped here could spend
// a person's whole quota on one request.
func TestListSpacesDoesNotFollowThePageToken(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/AAA"}],"nextPageToken":"more"}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).ListSpaces(context.Background(), ListSpacesOptions{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("made %d calls, want exactly 1", calls.Load())
	}
}

func TestRetriesOn5xxThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"spaces":[]}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).ListSpaces(context.Background(), ListSpacesOptions{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("made %d attempts, want 3", calls.Load())
	}
}

func TestRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"spaces":[]}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).ListSpaces(context.Background(), ListSpacesOptions{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("made %d attempts, want 2", calls.Load())
	}
}

// Retrying forever is worse than reporting. The last error reaches the
// caller unwrapped, so a tool can classify it.
func TestGivesUpAfterMaxRetriesAndReportsTheRealError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"try later"}}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).ListSpaces(context.Background(), ListSpacesOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not an *APIError: %v", err)
	}
	if apiErr.StatusCode != 503 || apiErr.Status != "UNAVAILABLE" {
		t.Errorf("error = %+v", apiErr)
	}
	if calls.Load() != 4 {
		t.Errorf("made %d attempts, want 1 plus 3 retries", calls.Load())
	}
}

func TestDoesNotRetryA4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no such space"}}`)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GetSpace(context.Background(), "spaces/AAA")
	if !IsNotFound(err) {
		t.Fatalf("want a not-found error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("made %d attempts, want 1", calls.Load())
	}
}

// A transport failure on a write may mean the write landed. Repeating it
// would post a second copy, so only an answer from Google is retried.
func TestAWriteIsNotRetriedAfterATransportFailure(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, httptest.NewServer(http.NotFoundHandler()), func(o *Options) {
		o.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("connection reset")
		})}
	})
	err := c.do(context.Background(), request{method: "POST", path: "spaces"}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 1 {
		t.Errorf("made %d attempts, want 1: a write must not be repeated blindly", calls.Load())
	}
}

// A read is safe to repeat, so a transport failure is worth another try.
func TestAReadIsRetriedAfterATransportFailure(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, httptest.NewServer(http.NotFoundHandler()), func(o *Options) {
		o.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("connection reset")
		})}
	})
	if err := c.do(context.Background(), request{method: "GET", path: "spaces"}, nil); err == nil {
		t.Fatal("want an error")
	}
	if calls.Load() != 4 {
		t.Errorf("made %d attempts, want 1 plus 3 retries", calls.Load())
	}
}

func TestBackoffGrowsAndStaysWithinBounds(t *testing.T) {
	c := New(Options{})
	for attempt := 1; attempt <= 8; attempt++ {
		d := c.backoff(attempt, nil)
		if d <= 0 || d > maxBackoff {
			t.Fatalf("attempt %d: backoff %s out of bounds", attempt, d)
		}
	}
	// A Retry-After Google sent raises the wait but cannot exceed the cap.
	long := c.backoff(1, &retryHint{after: time.Hour})
	if long != maxBackoff {
		t.Errorf("a huge Retry-After gave %s, want the cap %s", long, maxBackoff)
	}
	// Jitter keeps a floor, so a retry storm does not collapse to zero.
	if d := c.backoff(1, &retryHint{after: 4 * time.Second}); d < 2*time.Second {
		t.Errorf("backoff %s fell below half of the Retry-After", d)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("2.5"); got != 2500*time.Millisecond {
		t.Errorf("seconds form = %s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %s", got)
	}
	if got := parseRetryAfter("not a number"); got != 0 {
		t.Errorf("garbage = %s", got)
	}
	if got := parseRetryAfter(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)); got <= 0 {
		t.Error("an HTTP date in the future should give a positive wait")
	}
	if got := parseRetryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("a past date should give no wait, got %s", got)
	}
}

// A token that cannot be refreshed must surface, not be retried into a
// timeout: the person has to log in again.
func TestATokenFailureIsReportedImmediately(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	want := errors.New("refresh token expired")
	c := newTestClient(t, srv, func(o *Options) { o.Tokens = failingToken{err: want} })

	_, err := c.ListSpaces(context.Background(), ListSpacesOptions{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the token error", err)
	}
}

func TestDriftIsCountedEveryTimeAndLoggedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/A","newField":1},{"name":"spaces/B","newField":2}]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.ListSpaces(context.Background(), ListSpacesOptions{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if got := c.DriftCount(); got != 2 {
		t.Errorf("drift count = %d, want one per row", got)
	}
	// A second page of the same drift keeps counting.
	if _, err := c.ListSpaces(context.Background(), ListSpacesOptions{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if got := c.DriftCount(); got != 4 {
		t.Errorf("drift count = %d after a second page, want 4", got)
	}
}

// Drift must never fail the call. This has been a total outage twice:
// one new field from Google turned every request into an error.
func TestDriftDoesNotFailTheCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/AAA","displayName":"Team","brandNewField":{"nested":true}}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).GetSpace(context.Background(), "spaces/AAA")
	if err != nil {
		t.Fatalf("an unknown field must not fail the call: %v", err)
	}
	if got.DisplayName != "Team" {
		t.Errorf("space = %+v", got)
	}
}

func TestUserinfoUsesTheOIDCBase(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"sub":"1","email":"janedoe@example.com","name":"Jane Doe"}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).Userinfo(context.Background())
	if err != nil {
		t.Fatalf("Userinfo: %v", err)
	}
	if gotPath != "/oidc/userinfo" {
		t.Errorf("path = %q, want the OIDC base", gotPath)
	}
	if got.Email != "janedoe@example.com" {
		t.Errorf("userinfo = %+v", got)
	}
}

func TestFindDirectMessagePassesTheUserAsAQueryParameter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("name")
		fmt.Fprint(w, `{"name":"spaces/DM"}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).FindDirectMessage(context.Background(), "users/janedoe@example.com"); err != nil {
		t.Fatalf("FindDirectMessage: %v", err)
	}
	if gotQuery != "users/janedoe@example.com" {
		t.Errorf("name = %q", gotQuery)
	}
}

// The limiter must bound calls, not just exist. An unlimited client
// would trip Google's per-user quota on the first fan-out.
func TestTheReadLimiterIsApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, func(o *Options) {
		o.ReadLimiter = rate.NewLimiter(rate.Limit(1000), 1)
	})
	ctx := context.Background()
	start := time.Now()
	for range 3 {
		if _, err := c.ListSpaces(ctx, ListSpacesOptions{}); err != nil {
			t.Fatalf("ListSpaces: %v", err)
		}
	}
	if time.Since(start) == 0 {
		t.Error("three calls through a limiter took no time at all")
	}
}

// A cancelled context stops the work rather than finishing the retries.
func TestACancelledContextStopsRetrying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := newTestClient(t, srv, func(o *Options) {
		o.Sleep = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
	})
	if _, err := c.ListSpaces(ctx, ListSpacesOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestNewFillsInDefaults(t *testing.T) {
	c := New(Options{})
	if c.chatBase == "" || c.peopleBase == "" || c.oidcBase != DefaultOIDCBase {
		t.Errorf("bases = %q %q %q", c.chatBase, c.peopleBase, c.oidcBase)
	}
	if c.http == nil || c.log == nil || c.readLim == nil || c.writeLim == nil || c.sleep == nil {
		t.Error("New left a required field nil")
	}
	if c.userAgent == "" {
		t.Error("New left the user agent empty")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A transport failure must not carry the request URL into the logs.
//
// net/http wraps one in a *url.Error, which renders the whole URL. A
// People search puts the caller's search term in the query string, so
// an ordinary timeout would write down who they looked someone up as.
func TestATransportErrorDoesNotCarryTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening now, so the dial fails

	c := New(Options{
		PeopleBase: base + "/v1",
		Tokens:     staticToken("test"),
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	_, err := c.SearchDirectoryPeople(context.Background(), "janedoe@example.com", 10)
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "janedoe@example.com") {
		t.Errorf("the error carries the search term: %v", err)
	}
	if strings.Contains(err.Error(), "query=") {
		t.Errorf("the error carries the query string: %v", err)
	}
	// The method and path still have to be there, or the log says
	// nothing about which call failed.
	if !strings.Contains(err.Error(), "searchDirectoryPeople") {
		t.Errorf("the error does not name the call: %v", err)
	}
}

// Retry-After is a minimum, not a target. Jittering it downward — which
// this did — turns "wait 10 seconds" into a wait of five, so three early
// retries fail a call that honouring the header would have completed.
func TestRetryAfterIsHonouredNotJittered(t *testing.T) {
	c := newTestClient(t, httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {})))
	hint := &retryHint{err: errors.New("429"), after: 10 * time.Second}
	for range 20 {
		if got := c.backoff(1, hint); got != 10*time.Second {
			t.Fatalf("backoff = %s, want the 10s Google asked for", got)
		}
	}
	// Still bounded, so a hostile or mistaken header cannot park a call.
	long := &retryHint{err: errors.New("429"), after: time.Hour}
	if got := c.backoff(1, long); got != maxBackoff {
		t.Errorf("backoff = %s, want it capped at %s", got, maxBackoff)
	}
	// Without a hint the exponential is still jittered, or a burst of
	// retries all wakes at once.
	seen := map[time.Duration]bool{}
	for range 40 {
		seen[c.backoff(3, errors.New("plain"))] = true
	}
	if len(seen) < 2 {
		t.Error("the exponential backoff lost its jitter")
	}
}

func TestResolvePathEscapesTheNameAndNotTheVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  request
		want string
	}{
		{"a collection", request{path: "spaces"}, "spaces"},
		{"one resource", request{name: "spaces/AAAAspace1"}, "spaces/AAAAspace1"},
		{"a sub-collection", request{name: "spaces/AAAAspace1", path: "messages"}, "spaces/AAAAspace1/messages"},
		{"a verb on a collection", request{path: "spaces", verb: "setup"}, "spaces:setup"},
		{"a verb on a resource", request{name: "users/me/sections/S1", verb: "position"}, "users/me/sections/S1:position"},
		{"separators survive", request{name: "users/me/sections/S1/items/c3BhY2Vz"}, "users/me/sections/S1/items/c3BhY2Vz"},
		{"a segment is escaped", request{name: "spaces/a b#c"}, "spaces/a%20b%23c"},
		{"and a question mark, which would start a query", request{name: "spaces/with?question"}, "spaces/with%3Fquestion"},
		{"an address is left alone", request{name: "users/janedoe@example.com"}, "users/janedoe@example.com"},
		{
			// The order is the point, and so is escaping the colon: a
			// name carrying one would otherwise address a custom method
			// nobody asked for, and appending the verb after escaping is
			// what keeps the one the caller did ask for readable.
			"escaped, then the verb",
			request{name: "users/me/sections/a b:evil", verb: "position"},
			"users/me/sections/a%20b%3Aevil:position",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.resolvePath(); got != tc.want {
				t.Errorf("resolvePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// No path is built by hand. Two rules hold that, and both are read off
// the package's own syntax tree rather than trusted:
//
// A request's path and verb are written here, so they are literals. The
// failure this prevents is `path: name + "/messages"`, which reaches
// Google unescaped — harmless for the ids these tools pass, and
// not for the attachment, custom emoji and space event names of Phase 4.
//
// And escapeName has one caller, resolvePath, which is where the rule
// that the verb goes on after escaping lives.
func TestNoPathIsBuiltByHand(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package: %v", err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	constants := packageConstants(files)

	var callers []string
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if id, ok := node.Fun.(*ast.Ident); ok && id.Name == "escapeName" {
						callers = append(callers, fn.Name.Name)
					}
				case *ast.CompositeLit:
					if id, ok := node.Type.(*ast.Ident); ok && id.Name == "request" {
						assertLiteralPath(t, fset, constants, node)
						assertReadOnlyIsASearch(t, fset, fn.Name.Name, node)
					}
				}
				return true
			})
		}
	}
	if len(callers) != 1 || callers[0] != "resolvePath" {
		t.Errorf("escapeName is called from %v, want resolvePath alone", callers)
	}
}

// readOnlyMethods is every client method allowed to declare a POST
// read-only. It is a list of names rather than a pattern.
//
// A pattern was the first version — "the method name contains Search" —
// and the google-sheets-mcp session found the hole while adopting this
// rule: Sheets has three POSTs that only read and two beside them that
// write, and both sets are spelled with the same words. "get" reads
// like a read on a method that is a POST only because a filter does not
// fit in a URL. A name cannot be trusted to say what a call does, so
// each exception is named here and adding one is a deliberate edit.
var readOnlyMethods = map[string]bool{"SearchMessages": true}

// assertReadOnlyIsASearch fails when a request declares itself
// read-only outside the methods allowed to.
//
// readOnly turns off the write limiter, the retry rule and the guard
// that keeps a dry run off the network. Google models search as a POST,
// which is the only reason it exists; on anything else it would let a
// write run during a dry run.
func assertReadOnlyIsASearch(t *testing.T, fset *token.FileSet, fn string, lit *ast.CompositeLit) {
	t.Helper()
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "readOnly" {
			continue
		}
		value, ok := kv.Value.(*ast.Ident)
		if !ok || value.Name != "true" {
			continue
		}
		if !readOnlyMethods[fn] {
			t.Errorf("%s: %s declares readOnly, which only %v may do",
				fset.Position(kv.Pos()), fn, readOnlyMethods)
		}
	}
}

// packageConstants is every constant the package declares, by name.
func packageConstants(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					out[name.Name] = true
				}
			}
		}
	}
	return out
}

// assertLiteralPath fails when a request's path or verb is anything but
// a string written in this package: a literal, or a constant naming one.
//
// A plain variable is refused along with an expression, because it is
// the same mistake one line further up: `parent := o.Space + "/items"`
// reads as a name and reaches Google unescaped.
func assertLiteralPath(t *testing.T, fset *token.FileSet, constants map[string]bool, lit *ast.CompositeLit) {
	t.Helper()
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || (key.Name != "prefix" && key.Name != "path" && key.Name != "verb") {
			continue
		}
		switch value := kv.Value.(type) {
		case *ast.BasicLit:
		case *ast.Ident:
			if !constants[value.Name] {
				t.Errorf("%s: %s names %s, which is not a constant written here",
					fset.Position(kv.Pos()), key.Name, value.Name)
			}
		default:
			t.Errorf("%s: %s is built rather than written; pass the resource name as name",
				fset.Position(kv.Pos()), key.Name)
		}
	}
}

// Every call to Google names the scope that call needs, and the name is
// what a refused person is told to grant. Reflection over the client is
// what keeps that true as Phase 4 adds twenty-five more methods: one
// with no scope on its request fails here, rather than asking someone
// to grant "".
func TestEveryCallNamesItsScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, func(o *Options) { o.MaxRetries = 0 })

	// Membership, not scopes.Satisfied: the error tells a person to run
	// login and grant this exact string, so an umbrella covering it is
	// not enough — login has to ask for it.
	asked := map[string]bool{}
	for _, s := range scopes.All {
		asked[s] = true
	}

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errType := reflect.TypeOf((*error)(nil)).Elem()
	client := reflect.ValueOf(c)

	// Nothing on the client but these two stays out of it, and they are
	// named rather than counted: a floor to clear leaves room for a
	// method to fall out of the filter unnoticed.
	notCalls := map[string]bool{"DriftCount": true, "DriftPaths": true}

	var checked int
	for i := range client.NumMethod() {
		name := client.Type().Method(i).Name
		fn := client.Method(i)
		sig := fn.Type()
		// A call to Google takes a context first and returns an error
		// last. DriftCount and DriftPaths do neither.
		if sig.NumIn() == 0 || sig.In(0) != ctxType ||
			sig.NumOut() == 0 || sig.Out(sig.NumOut()-1) != errType {
			if !notCalls[name] {
				t.Errorf("%s is neither a call to Google nor one of %v", name, notCalls)
			}
			continue
		}
		args := []reflect.Value{reflect.ValueOf(context.Background())}
		for j := 1; j < sig.NumIn(); j++ {
			args = append(args, reflect.New(sig.In(j)).Elem())
		}
		results := fn.Call(args)
		err, _ := results[len(results)-1].Interface().(error)

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("%s: err = %v, want the stub's 403", name, err)
			continue
		}
		if !asked[apiErr.Scope] {
			t.Errorf("%s: scope = %q, which login does not ask for", name, apiErr.Scope)
		}
		checked++
	}
	if want := client.NumMethod() - len(notCalls); checked != want {
		t.Errorf("checked %d calls, want all %d", checked, want)
	}
}

func TestCredentialsGoOnlyToTheConfiguredHosts(t *testing.T) {
	// The production client, with the real Google bases.
	c := New(Options{Tokens: staticToken("t")})
	cases := []struct {
		raw  string
		want bool
		why  string
	}{
		{"https://chat.googleapis.com/v1/spaces", true, "the configured Chat base"},
		{"https://people.googleapis.com/v1/people:searchDirectoryPeople", true, "the configured People base"},
		{"https://chat.googleapis.com/upload/v1/spaces/AAAAspace1/attachments:upload", true, "the derived upload base"},
		{"https://CHAT.googleapis.com/v1/spaces", true, "the same host in another case"},
		{"https://chat.googleapis.com./v1/spaces", true, "the same host as a fully qualified name"},
		{"https://chat.googleapis.com:8443/v1/spaces", false, "a port is refused, not stripped"},
		{"https://chat.googleapis.com:443/v1/spaces", false, "the default port spelled out is still a port"},
		{"http://chat.googleapis.com/v1/spaces", false, "plain HTTP to the right host"},
		{"https://chat.google.com/api/get_attachment_url", false, "the host an attachment's downloadUri points at"},
		{"https://chat.googleapis.com.evil.example/v1/spaces", false, "a suffix that ends in the allowed host"},
		{"https://evil.example/v1/spaces", false, "somewhere else entirely"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got := c.allowURL(u); got != tc.want {
			t.Errorf("allowURL(%s) = %v, want %v: %s", tc.raw, got, tc.want, tc.why)
		}
	}
}

func TestARequestOffTheAllowedHostsSendsNoToken(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer srv.Close()

	// A client configured for Google, pointed at the test server by a
	// name it does not allow. Nothing may leave.
	c := New(Options{
		HTTP:        srv.Client(),
		ChatBase:    srv.URL + "/v1",
		Tokens:      staticToken("test-token"),
		ReadLimiter: rate.NewLimiter(rate.Inf, 1),
	})
	c.allowed = map[string]bool{"https://chat.googleapis.com": true}

	_, err := c.GetSpace(context.Background(), "spaces/AAAAspace1")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("GetSpace error = %v, want ErrHostNotAllowed", err)
	}
	if reached.Load() {
		t.Error("the request reached the server; the check must happen before it is sent")
	}
}

// A redirect is a second chance to send the token somewhere else, and
// net/http keeps the Authorization header across a redirect to a
// subdomain of the host asked for. The allowlist has to run on every
// hop, not only the first.
func TestARedirectOffTheAllowedHostsIsRefused(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if r.Header.Get("Authorization") != "" {
			t.Error("the access token followed the redirect")
		}
		fmt.Fprint(w, `{}`)
	}))
	defer elsewhere.Close()

	var redirects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, elsewhere.URL+"/v1/spaces/AAAAspace1", http.StatusFound)
	}))
	defer srv.Close()

	// Configured for srv alone, so elsewhere is off the allowlist.
	_, err := newTestClient(t, srv).GetSpace(context.Background(), "spaces/AAAAspace1")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("GetSpace error = %v, want ErrHostNotAllowed", err)
	}
	if redirects.Load() == 0 {
		t.Error("the redirect was never served, so the test proved nothing")
	}
	if reached.Load() {
		t.Error("the request followed a redirect off the configured hosts")
	}
}

// A redirect that stays on a configured host is followed as before.
func TestARedirectWithinTheAllowedHostsIsFollowed(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hops.Add(1) == 1 {
			http.Redirect(w, r, "/v1/spaces/AAAAspace1", http.StatusFound)
			return
		}
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1","displayName":"Team"}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).GetSpace(context.Background(), "spaces/AAAAspace1")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if got.DisplayName != "Team" || hops.Load() != 2 {
		t.Errorf("space = %+v after %d hops", got, hops.Load())
	}
}
