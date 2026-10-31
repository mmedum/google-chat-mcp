package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/v9/internal/auth"
	"github.com/mmedum/google-chat-mcp/v9/internal/config"
	"github.com/mmedum/google-chat-mcp/v9/internal/directory"
	"github.com/mmedum/google-chat-mcp/v9/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v9/internal/scopes"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// newService points a Service at srv, so no test reaches Google.
func newService(t *testing.T, handler http.HandlerFunc) *Service {
	svc, _ := newServiceCached(t, handler)
	return svc
}

// newServiceCached is newService with the email cache handed back, for
// the tests that assert on what a call remembered.
func newServiceCached(t *testing.T, handler http.HandlerFunc) (*Service, *directory.Cache) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := gchat.New(gchat.Options{
		HTTP:              srv.Client(),
		ChatBase:          srv.URL + "/v1",
		PeopleBase:        srv.URL + "/people",
		OIDCBase:          srv.URL + "/oidc",
		CloudIdentityBase: srv.URL + "/cloudidentity",
		Tokens:            staticToken("test"),
	})
	// An empty path keeps the cache in memory: no test in this package
	// may write to the real profile directory.
	cache := directory.NewCache("", time.Hour, nil)
	return New(client, directory.NewResolver(client, cache, nil), config.Config{}, slog.New(slog.DiscardHandler)), cache
}

func TestWhoami(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"sub":"12345","email":"janedoe@example.com","name":"Jane Doe","hd":"example.com"}`)
	})
	got, err := s.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if got.UserSub != "12345" {
		t.Errorf("user sub = %q", got.UserSub)
	}
	if got.Email != "janedoe@example.com" || got.DisplayName != "Jane Doe" {
		t.Errorf("identity = %+v", got)
	}
}

func TestListSpacesMapsGooglesSpelling(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[
		  {"name":"spaces/A","spaceType":"SPACE","displayName":"Team","threaded":true},
		  {"name":"spaces/B","spaceType":"DIRECT_MESSAGE"},
		  {"name":"spaces/C","spaceType":"GROUP_CHAT"},
		  {"name":"spaces/D","type":"ROOM","displayName":"Legacy"}
		],"nextPageToken":"next"}`)
	})
	got, err := s.ListSpaces(context.Background(), ListSpacesInput{})
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if got.NextPageToken != "next" {
		t.Errorf("page token = %q", got.NextPageToken)
	}
	want := []struct {
		name string
		kind SpaceKind
		disp string
	}{
		{"spaces/A", KindSpace, "Team"},
		{"spaces/B", KindDirectMessage, "(direct message)"},
		{"spaces/C", KindGroupChat, "(group chat)"},
		{"spaces/D", KindSpace, "Legacy"},
	}
	if len(got.Spaces) != len(want) {
		t.Fatalf("got %d spaces, want %d", len(got.Spaces), len(want))
	}
	for i, w := range want {
		if got.Spaces[i].Name != w.name || got.Spaces[i].Kind != w.kind || got.Spaces[i].DisplayName != w.disp {
			t.Errorf("space %d = %+v, want %s/%s/%s", i, got.Spaces[i], w.name, w.kind, w.disp)
		}
	}
}

// A space type Google adds must not take down the listing. The caller
// can still act on a space whose kind this server has not learned.
func TestListSpacesDegradesAnUnknownKind(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/X","spaceType":"HUDDLE","displayName":"New thing"}]}`)
	})
	got, err := s.ListSpaces(context.Background(), ListSpacesInput{})
	if err != nil {
		t.Fatalf("an unknown space type must not fail the listing: %v", err)
	}
	if got.Spaces[0].Kind != KindUnknown {
		t.Errorf("kind = %q, want unknown", got.Spaces[0].Kind)
	}
	if got.Spaces[0].Name != "spaces/X" {
		t.Errorf("the row must survive: %+v", got.Spaces[0])
	}
}

// A space with no display name would otherwise render as an empty
// string, which reads as a bug to whoever is looking at the list.
func TestListSpacesAlwaysHasSomethingToCallASpace(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/Y","spaceType":"HUDDLE"}]}`)
	})
	got, err := s.ListSpaces(context.Background(), ListSpacesInput{})
	if err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if got.Spaces[0].DisplayName == "" {
		t.Error("display name is empty with no fallback")
	}
}

func TestListSpacesSendsTheFilterForAKind(t *testing.T) {
	var gotFilter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"spaces":[]}`)
	})
	if _, err := s.ListSpaces(context.Background(), ListSpacesInput{Kind: KindSpace}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if gotFilter != `spaceType = "SPACE"` {
		t.Errorf("filter = %q", gotFilter)
	}
}

func TestListSpacesRejectsBadInput(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[]}`)
	})
	for _, tc := range []struct {
		name string
		in   ListSpacesInput
	}{
		{"unknown kind", ListSpacesInput{Kind: "channel"}},
		{"limit above Google's maximum", ListSpacesInput{Limit: 5000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ListSpaces(context.Background(), tc.in)
			var se *Error
			if !errors.As(err, &se) || se.Class != ClassInvalid {
				t.Fatalf("error = %v, want an invalid-argument error", err)
			}
		})
	}
}

func TestListSpacesDefaultsTheLimit(t *testing.T) {
	var gotSize string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotSize = r.URL.Query().Get("pageSize")
		fmt.Fprint(w, `{"spaces":[]}`)
	})
	if _, err := s.ListSpaces(context.Background(), ListSpacesInput{}); err != nil {
		t.Fatalf("ListSpaces: %v", err)
	}
	if gotSize != "50" {
		t.Errorf("page size = %q, want the default 50", gotSize)
	}
}

func TestClassifyMapsGooglesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   Class
	}{
		{"missing scope", 403, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`, ClassScope},
		{"plain forbidden", 403, `{"error":{"status":"PERMISSION_DENIED","message":"not a member"}}`, ClassForbidden},
		{
			// A 403 that is really a rate limit, which Google's classic
			// shape reports this way. It read as a plain refusal until
			// the reason was looked at.
			"a rate limit wearing a 403", 403,
			`{"error":{"code":403,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`,
			ClassRateLimited,
		},
		{
			// And a quota, where waiting a moment is the wrong advice.
			"a spent quota", 403,
			`{"error":{"code":403,"message":"Daily Limit Exceeded","errors":[{"reason":"dailyLimitExceeded"}]}}`,
			ClassQuota,
		},
		{"expired token", 401, `{"error":{"status":"UNAUTHENTICATED","message":"invalid credentials"}}`, ClassAuth},
		{"not found", 404, `{"error":{"status":"NOT_FOUND","message":"no such space"}}`, ClassNotFound},
		{"bad argument", 400, `{"error":{"status":"INVALID_ARGUMENT","message":"bad filter"}}`, ClassInvalid},
		{"rate limited", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"slow down"}}`, ClassRateLimited},
		{"server error", 500, `{"error":{"status":"INTERNAL","message":"oops"}}`, ClassServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := s.ListSpaces(context.Background(), ListSpacesInput{})
			var se *Error
			if !errors.As(err, &se) {
				t.Fatalf("error is not a service error: %v", err)
			}
			if se.Class != tc.want {
				t.Errorf("class = %q, want %q (%v)", se.Class, tc.want, err)
			}
		})
	}
}

// The scope URL has to reach the person: it is the exact string they
// must grant, and an error result has nowhere else to carry it.
func TestAMissingScopeErrorNamesTheScope(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	})
	_, err := s.ListSpaces(context.Background(), ListSpacesInput{})
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("error = %v", err)
	}
	// The scope comes from the endpoint the call went to, not from a
	// caller restating it, so this asserts the value and not just that
	// there is one.
	if se.Scope != scopes.SpacesReadonly {
		t.Fatalf("scope = %q, want %q", se.Scope, scopes.SpacesReadonly)
	}
	if !strings.Contains(se.Error(), se.Scope) {
		t.Errorf("the message %q does not name the scope %q", se.Error(), se.Scope)
	}
	if !strings.Contains(se.Error(), "login") {
		t.Errorf("the message %q does not say what to run", se.Error())
	}
}

// Not being signed in is the most common failure. It never reaches
// Google, so it has to be recognized on its own.
func TestClassifyRecognizesAMissingLogin(t *testing.T) {
	err := Classify(fmt.Errorf("wrapped: %w", auth.ErrReauthorize))
	var se *Error
	if !errors.As(err, &se) || se.Class != ClassAuth {
		t.Fatalf("error = %v, want an auth error", err)
	}
	if !strings.Contains(se.Error(), "login") {
		t.Errorf("the message %q does not say what to run", se.Error())
	}
}

func TestClassifyPassesThroughAndHandlesNil(t *testing.T) {
	if got := Classify(nil); got != nil {
		t.Errorf("Classify(nil) = %v", got)
	}
	own := Invalidf("bad input")
	got := Classify(own)
	var se *Error
	if !errors.As(got, &se) || se != own {
		t.Errorf("an existing service error should pass through, got %v", got)
	}
}

func TestErrorTextLeadsWithTheClass(t *testing.T) {
	if got := Failf(ClassNotFound, "no such space").Error(); got != "[not_found] no such space" {
		t.Errorf("Error() = %q", got)
	}
}

func TestGetSpaceKeepsWhatGoogleDidNotSay(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/A","spaceType":"SPACE","displayName":"Team","createTime":"2026-01-02T03:04:05Z"}`)
	})
	got, err := s.GetSpace(context.Background(), "spaces/A")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if got.Kind != KindSpace || got.DisplayName != "Team" {
		t.Errorf("space = %+v", got)
	}
	if got.CreateTime.IsZero() {
		t.Error("create time should be read")
	}
	// Google omits these far more often than it sends false, so
	// reporting false would assert something it never said.
	if got.SingleUserBotDM != nil || got.ExternalUserAllowed != nil {
		t.Errorf("flags = %v / %v, want them absent", got.SingleUserBotDM, got.ExternalUserAllowed)
	}
	if got.AccessState != "" || got.Audience != "" {
		t.Errorf("access = %q / %q, want both absent", got.AccessState, got.Audience)
	}
}

// Google writes a time it does not have as the Unix epoch, as an
// administrator's read of a space did for its last active time.
func TestGetSpaceReadsTheEpochAsNoTime(t *testing.T) {
	for _, tc := range []struct {
		sent string
		want time.Time
	}{
		{"1970-01-01T00:00:00Z", time.Time{}},
		{"1970-01-01T00:00:00.5Z", time.Date(1970, 1, 1, 0, 0, 0, 500000000, time.UTC)},
		{"1970-01-01T00:00:01Z", time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)},
		{"2026-01-03T09:00:00Z", time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)},
	} {
		s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"name":"spaces/A","spaceType":"SPACE","displayName":"Team","lastActiveTime":%q}`, tc.sent)
		})
		got, err := s.GetSpace(context.Background(), "spaces/A")
		if err != nil {
			t.Fatalf("GetSpace with lastActiveTime %s: %v", tc.sent, err)
		}
		if !got.LastActiveTime.Equal(tc.want) {
			t.Errorf("lastActiveTime %s read as %v, want %v", tc.sent, got.LastActiveTime, tc.want)
		}
	}
}

func TestGetSpaceReadsWhoCanFindIt(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/A","spaceType":"SPACE","displayName":"Team",
		  "accessSettings":{"accessState":"DISCOVERABLE","audience":"audiences/default"}}`)
	})
	got, err := s.GetSpace(context.Background(), "spaces/A")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if got.AccessState != "DISCOVERABLE" || got.Audience != "audiences/default" {
		t.Errorf("access = %q / %q", got.AccessState, got.Audience)
	}
}

func TestGetSpaceReadsTheFlagsWhenGoogleSendsThem(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/B","spaceType":"DIRECT_MESSAGE","singleUserBotDm":true,"externalUserAllowed":false}`)
	})
	got, err := s.GetSpace(context.Background(), "spaces/B")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if got.SingleUserBotDM == nil || !*got.SingleUserBotDM {
		t.Errorf("single user bot dm = %v", got.SingleUserBotDM)
	}
	if got.ExternalUserAllowed == nil || *got.ExternalUserAllowed {
		t.Errorf("external user allowed = %v", got.ExternalUserAllowed)
	}
	if got.DisplayName == "" {
		t.Error("a direct message still needs something to call it")
	}
}

func TestGetSpaceRejectsAMissingID(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	for _, id := range []string{"", "  ", "users/me"} {
		_, err := s.GetSpace(context.Background(), id)
		var se *Error
		if !errors.As(err, &se) || se.Class != ClassInvalid {
			t.Errorf("GetSpace(%q) = %v, want an invalid-argument error", id, err)
		}
	}
}

// Drift is counted so an operator can alert on it, and listed so a
// person can see which field Google added.
func TestDriftIsVisible(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/A","spaceType":"SPACE","huddleEnabled":true}]}`)
	})
	if _, err := s.ListSpaces(context.Background(), ListSpacesInput{}); err != nil {
		t.Fatalf("drift must not fail the call: %v", err)
	}
	if s.DriftCount() == 0 {
		t.Error("an unknown field should be counted")
	}
	paths := s.DriftPaths()
	if len(paths) == 0 || !strings.Contains(strings.Join(paths, ","), "huddleEnabled") {
		t.Errorf("drift paths = %v, want the new field named", paths)
	}
}

// call is one request a write test saw.
type call struct{ Method, Path, Query, Body string }

// calls records what a write actually sent.
//
// The lock is not ceremony: the tests run with the race detector and
// the handler runs on the test server's own goroutine. A dry-run test
// asserts on len, because "made no request at all" is the whole promise,
// or on writes where a preview reads what it checks.
type calls struct {
	mu  sync.Mutex
	got []call
}

// wrap answers with h and remembers the request.
func (c *calls) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.got = append(c.got, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body)})
		c.mu.Unlock()
		h(w, r)
	}
}

func (c *calls) all() []call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

func (c *calls) len() int { return len(c.all()) }

// count is how many requests of one method reached Google.
func (c *calls) count(method string) int {
	n := 0
	for _, got := range c.all() {
		if got.Method == method {
			n++
		}
	}
	return n
}

// writes is how many requests that were not reads reached Google.
func (c *calls) writes() int { return c.len() - c.count(http.MethodGet) }

// last is the request the call under test ended on.
func (c *calls) last(t *testing.T) call {
	t.Helper()
	got := c.all()
	if len(got) == 0 {
		t.Fatal("no request reached Google")
	}
	return got[len(got)-1]
}

// mask is the updateMask the call sent, read out of the query rather
// than compared against the whole of it: every request also asks for
// compact JSON, and what a patch test is about is which fields it
// masked. An unmasked patch is what clears the cards and attachments
// this server cannot rebuild, so the mask is the assertion that matters.
func (c call) mask(t *testing.T) string {
	t.Helper()
	return c.param(t, "updateMask")
}

// param is one query parameter the call sent.
func (c call) param(t *testing.T, key string) string {
	t.Helper()
	q, err := url.ParseQuery(c.Query)
	if err != nil {
		t.Fatalf("query %q does not parse: %v", c.Query, err)
	}
	return q.Get(key)
}

// recorded builds a Service whose every request is remembered.
func recorded(t *testing.T, h http.HandlerFunc) (*Service, *calls) {
	t.Helper()
	c := &calls{}
	return newService(t, c.wrap(h)), c
}

func TestFindDirectMessageReturnsAnExistingOne(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/DM","spaceType":"DIRECT_MESSAGE"}`))
	got, err := s.FindDirectMessage(context.Background(), "janedoe@example.com")
	if err != nil {
		t.Fatalf("FindDirectMessage: %v", err)
	}
	if got != "spaces/DM" {
		t.Errorf("space = %q", got)
	}
	sent := rec.last(t)
	if !strings.Contains(sent.Path, "spaces:findDirectMessage") {
		t.Errorf("path = %q", sent.Path)
	}
	if !strings.Contains(sent.Query, "users%2Fjanedoe%40example.com") {
		t.Errorf("query = %q, want the person named as a resource name", sent.Query)
	}
	if rec.len() != 1 {
		t.Errorf("%d requests, want no create when one already exists", rec.len())
	}
}

// Creating on a miss is what makes the tool useful: there is no other
// way to name the space a caller wants to write in.
func TestFindDirectMessageCreatesOneWhenThereIsNone(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "findDirectMessage") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"status":"NOT_FOUND","message":"no dm"}}`)
			return
		}
		fmt.Fprint(w, `{"name":"spaces/NEW","spaceType":"DIRECT_MESSAGE"}`)
	}))
	got, err := s.FindDirectMessage(context.Background(), "janedoe@example.com")
	if err != nil {
		t.Fatalf("FindDirectMessage: %v", err)
	}
	if got != "spaces/NEW" {
		t.Errorf("space = %q", got)
	}
	setup := c.last(t)
	if !strings.Contains(setup.Path, "spaces:setup") {
		t.Fatalf("path = %q", setup.Path)
	}
	if !strings.Contains(setup.Body, `"spaceType":"DIRECT_MESSAGE"`) {
		t.Errorf("body = %s", setup.Body)
	}
	if strings.Contains(setup.Body, "displayName") {
		t.Errorf("body = %s, want no display name on a direct message", setup.Body)
	}
}

// The scope named must be the one the create needs. A person told to
// grant readonly would grant it and land here again.
func TestFindDirectMessageNamesTheCreateScope(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "findDirectMessage") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"status":"NOT_FOUND","message":"no dm"}}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	})
	_, err := s.FindDirectMessage(context.Background(), "janedoe@example.com")
	assertScope(t, err, scopes.SpacesCreate)
}

// Google's message for an address it cannot place says nothing about
// the directory, which is nearly always the reason.
func TestFindDirectMessageExplainsAnAddressGoogleWillNotTake(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "findDirectMessage") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"status":"NOT_FOUND","message":"no dm"}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"status":"INVALID_ARGUMENT","message":"invalid member"}}`)
	})
	_, err := s.FindDirectMessage(context.Background(), "janedoe@example.com")
	assertClass(t, err, ClassInvalid)
	if !strings.Contains(err.Error(), "directory") || !strings.Contains(err.Error(), "janedoe@example.com") {
		t.Errorf("error = %q, want the address and what to check", err)
	}
}

func TestFindDirectMessageRejectsABadAddress(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/DM"}`))
	for _, email := range []string{"", "  ", "janedoe", "jane doe@example.com", `jane"doe@example.com`, "janedoe@example"} {
		_, err := s.FindDirectMessage(context.Background(), email)
		assertClass(t, err, ClassInvalid)
	}
	if rec.len() != 0 {
		t.Errorf("a bad address reached Google %d times", rec.len())
	}
}

func TestCreateSpaceSendsWhatGoogleExpects(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/NEW","spaceType":"SPACE","displayName":"Team"}`))
	got, err := s.CreateSpace(context.Background(), CreateSpaceInput{
		DisplayName:  "Team",
		MemberEmails: []string{"janedoe@example.com", "johndoe@example.com"},
	})
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if got.Name != "spaces/NEW" || got.DisplayName != "Team" || got.MemberCount != 2 {
		t.Errorf("result = %+v", got)
	}
	body := rec.last(t).Body
	if !strings.Contains(body, `"spaceType":"SPACE"`) || !strings.Contains(body, `"displayName":"Team"`) {
		t.Errorf("body = %s", body)
	}
	if !strings.Contains(body, `"users/janedoe@example.com"`) {
		t.Errorf("body = %s", body)
	}
	if strings.Contains(body, "accessSettings") {
		t.Errorf("body = %s, want a private space to send no accessSettings", body)
	}
}

// A group chat has no name, so asking for one is a mistake worth
// naming rather than a field to drop quietly.
func TestCreateGroupChatRefusesADisplayName(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/NEW"}`))
	_, err := s.CreateGroupChat(context.Background(), CreateSpaceInput{
		DisplayName:  "Team",
		MemberEmails: []string{"janedoe@example.com", "johndoe@example.com"},
	})
	assertClass(t, err, ClassInvalid)
	if rec.len() != 0 {
		t.Error("the create reached Google anyway")
	}
}

func TestSpaceCreatesBoundTheirMemberLists(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/NEW"}`))
	twentyOne := make([]string, 21)
	for i := range twentyOne {
		twentyOne[i] = fmt.Sprintf("person%d@example.com", i)
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"a group chat of one", func() error {
			_, err := s.CreateGroupChat(context.Background(), CreateSpaceInput{MemberEmails: []string{"janedoe@example.com"}})
			return err
		}},
		{"a space with no name", func() error {
			_, err := s.CreateSpace(context.Background(), CreateSpaceInput{MemberEmails: []string{"janedoe@example.com"}})
			return err
		}},
		{"more members than the tool allows", func() error {
			_, err := s.CreateSpace(context.Background(), CreateSpaceInput{DisplayName: "Team", MemberEmails: twentyOne})
			return err
		}},
		{"an address that is not one", func() error {
			_, err := s.CreateSpace(context.Background(), CreateSpaceInput{DisplayName: "Team", MemberEmails: []string{"janedoe"}})
			return err
		}},
		{"the same person twice", func() error {
			_, err := s.CreateGroupChat(context.Background(), CreateSpaceInput{
				MemberEmails: []string{"janedoe@example.com", "JaneDoe@example.com"},
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertClass(t, tc.run(), ClassInvalid) })
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// Each argument limit takes its maximum and refuses one more. The
// numbers are Google's or this server's, written out here rather than
// read from the constants.
func TestEachLimitTakesItsMaximumAndRefusesOneMore(t *testing.T) {
	s := newService(t, ok(`{}`))
	ctx := writeCtx()
	members := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("person%d@example.com", i)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		run  func(n int) error
		max  int
	}{
		{"people query characters", func(n int) error {
			_, err := s.SearchPeople(ctx, SearchPeopleInput{Query: strings.Repeat("a", n)})
			return err
		}, 200},
		{"message search keyword characters", func(n int) error {
			_, err := s.SearchMessages(ctx, SearchMessagesInput{Space: "spaces/A", Query: strings.Repeat("a", n)})
			return err
		}, 500},
		{"emoji characters", func(n int) error {
			_, err := s.AddReaction(ctx, AddReactionInput{Message: "spaces/A/messages/1", Emoji: strings.Repeat("a", n)})
			return err
		}, 16},
		{"do-not-disturb minutes, a year", func(n int) error {
			_, err := s.SetAvailability(ctx, SetAvailabilityInput{State: "DO_NOT_DISTURB", Minutes: n, DryRun: true})
			return err
		}, 525600},
		{"space search display name characters", func(n int) error {
			_, err := s.SearchSpaces(ctx, SearchSpacesInput{DisplayName: strings.Repeat("a", n)})
			return err
		}, 200},
		{"members of a new space", func(n int) error {
			_, err := s.CreateSpace(ctx, CreateSpaceInput{DisplayName: "Team", MemberEmails: members(n), DryRun: true})
			return err
		}, 20},
		// Google's limits on a space count UTF-16 units: a two-byte
		// letter counts once and an emoji twice, as it did live.
		{"space description, two-byte letters", func(n int) error {
			d := strings.Repeat("é", n)
			_, err := s.UpdateSpace(ctx, UpdateSpaceInput{Space: "spaces/A", SpaceDisplayName: "Team", Description: &d, DryRun: true})
			return err
		}, 4096},
		{"space description, emoji", func(n int) error {
			d := strings.Repeat("😀", n)
			_, err := s.UpdateSpace(ctx, UpdateSpaceInput{Space: "spaces/A", SpaceDisplayName: "Team", Description: &d, DryRun: true})
			return err
		}, 2048},
		{"space guidelines, emoji", func(n int) error {
			g := strings.Repeat("😀", n)
			_, err := s.UpdateSpace(ctx, UpdateSpaceInput{Space: "spaces/A", SpaceDisplayName: "Team", Guidelines: &g, DryRun: true})
			return err
		}, 2500},
		{"space name, emoji", func(n int) error {
			_, err := s.CreateSpace(ctx, CreateSpaceInput{DisplayName: strings.Repeat("😀", n), DryRun: true})
			return err
		}, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var se *Error
			if err := tc.run(tc.max); errors.As(err, &se) && se.Class == ClassInvalid {
				t.Errorf("%d refused: %v", tc.max, err)
			}
			assertClass(t, tc.run(tc.max+1), ClassInvalid)
		})
	}
}

func TestSpaceCreateDryRunCreatesNothing(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/NEW"}`))
	got, err := s.CreateSpace(context.Background(), CreateSpaceInput{
		DisplayName: "Team", MemberEmails: []string{"janedoe@example.com"}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if got.Name != "" || !got.DryRun || got.MemberCount != 1 {
		t.Errorf("result = %+v", got)
	}
	space, _ := got.Rendered["space"].(map[string]any)
	if space["spaceType"] != "SPACE" || space["displayName"] != "Team" {
		t.Errorf("rendered = %v", got.Rendered)
	}
}

func TestUpdateSpaceMasksWhatItChanges(t *testing.T) {
	name, description := "Renamed", "What this room is for"
	for _, tc := range []struct {
		label       string
		in          UpdateSpaceInput
		wantMask    string
		wantInBody  string
		wantMissing string
	}{
		{"name only", UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", DisplayName: &name},
			"displayName", `"displayName":"Renamed"`, "spaceDetails"},
		{"description only", UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Description: &description},
			"spaceDetails", `"description":"What this room is for"`, "displayName"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, named(ok(`{"name":"spaces/A"}`)))
			got, err := s.UpdateSpace(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("UpdateSpace: %v", err)
			}
			sent := rec.last(t)
			if got := sent.mask(t); got != tc.wantMask {
				t.Errorf("mask = %q, want %q (query %q)", got, tc.wantMask, sent.Query)
			}
			if !strings.Contains(sent.Body, tc.wantInBody) {
				t.Errorf("body = %s", sent.Body)
			}
			if strings.Contains(sent.Body, tc.wantMissing) {
				t.Errorf("body = %s, want %q left out", sent.Body, tc.wantMissing)
			}
			if got.UpdateMask != tc.wantMask {
				t.Errorf("reported mask = %q", got.UpdateMask)
			}
		})
	}
}

// Clearing a description is a real edit, and an empty string is how a
// caller asks for it.
func TestUpdateSpaceCanClearADescription(t *testing.T) {
	empty := ""
	s, rec := recorded(t, named(ok(`{"name":"spaces/A"}`)))
	if _, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{
		SpaceDisplayName: "Team",
		Space:            "spaces/A", Description: &empty,
	}); err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	if !strings.Contains(rec.last(t).Body, `"description":""`) {
		t.Errorf("body = %s, want the empty description sent", rec.last(t).Body)
	}
}

func TestUpdateSpaceRejectsAnEmptyEdit(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	long := strings.Repeat("x", 4097)
	blank := "   "
	for _, tc := range []struct {
		name string
		in   UpdateSpaceInput
	}{
		{"nothing to change", UpdateSpaceInput{Space: "spaces/A"}},
		{"a description above the cap", UpdateSpaceInput{Space: "spaces/A", Description: &long}},
		{"a name of whitespace", UpdateSpaceInput{Space: "spaces/A", DisplayName: &blank}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.UpdateSpace(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// update_space makes a space private, masking the audience with no
// value, which is how Google removes it, and refuses to open one: that is
// set_space_audience's.
func TestUpdateSpaceMakesASpacePrivateAndNeverOpensOne(t *testing.T) {
	for _, audience := range []string{"private", " Private "} {
		s, rec := recorded(t, named(ok(`{"name":"spaces/A"}`)))
		got, err := s.UpdateSpace(writeCtx(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Audience: audience})
		if err != nil {
			t.Fatalf("UpdateSpace %q: %v", audience, err)
		}
		sent := rec.last(t)
		if m := sent.mask(t); m != "accessSettings.audience" || sent.Body != `{"accessSettings":{}}` {
			t.Errorf("%q: mask %q, body %s", audience, m, sent.Body)
		}
		if got.Audience == nil || *got.Audience != "private" {
			t.Errorf("%q: reported audience = %v", audience, got.Audience)
		}
	}
	s, rec := recorded(t, named(ok(`{"name":"spaces/A"}`)))
	for _, audience := range []string{"default", "audiences/AAAAaudience1"} {
		_, err := s.UpdateSpace(writeCtx(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Audience: audience})
		assertClass(t, err, ClassBlocked)
	}
	if rec.len() != 0 {
		t.Errorf("a refused opening reached Google %d times", rec.len())
	}
}

// teamStandup answers a read of a space with one called Team standup,
// and a write with an empty body.
func teamStandup(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `{"name":"spaces/A","spaceType":"SPACE","displayName":"Team standup"}`)
		return
	}
	fmt.Fprint(w, `{"name":"spaces/A"}`)
}

// set_space_audience opens a space, a bare id taken as an audience's,
// once the space reads as called what the call says; it does not make
// one private.
func TestSetSpaceAudienceOpensASpace(t *testing.T) {
	for _, tc := range []struct {
		space, audience, wantBody, want string
	}{
		{"spaces/A", "default", `{"accessSettings":{"audience":"audiences/default"}}`, "audiences/default"},
		{"AAAAspace1", "default", `{"accessSettings":{"audience":"audiences/default"}}`, "audiences/default"},
		{"spaces/A", " audiences/AAAAaudience1 ", `{"accessSettings":{"audience":"audiences/AAAAaudience1"}}`,
			"audiences/AAAAaudience1"},
	} {
		s, rec := recorded(t, teamStandup)
		got, err := s.SetSpaceAudience(writeCtx(), SetSpaceAudienceInput{Space: tc.space, Audience: tc.audience,
			DisplayName: "Team standup"})
		if err != nil {
			t.Fatalf("SetSpaceAudience %s %q: %v", tc.space, tc.audience, err)
		}
		sent := rec.last(t)
		if m := sent.mask(t); m != "accessSettings.audience" || sent.Body != tc.wantBody {
			t.Errorf("%q: mask %q, body %s, want %s", tc.audience, m, sent.Body, tc.wantBody)
		}
		if got.Audience == nil || *got.Audience != tc.want {
			t.Errorf("%q: reported audience = %v, want %q", tc.audience, got.Audience, tc.want)
		}
	}
}

// Opening a space widens who reads it for good, so a call that does not
// name the space as it reads changes nothing.
func TestSetSpaceAudienceRefusesAnotherSpace(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   SetSpaceAudienceInput
		want Class
	}{
		{"no name", SetSpaceAudienceInput{Space: "spaces/A", Audience: "default"}, ClassInvalid},
		{"another space's name", SetSpaceAudienceInput{Space: "spaces/A", Audience: "default", DisplayName: "Release planning"},
			ClassStale},
		{"another space's name in a dry run", SetSpaceAudienceInput{Space: "spaces/A", Audience: "default",
			DisplayName: "Release planning", DryRun: true}, ClassStale},
		{"private", SetSpaceAudienceInput{Space: "spaces/A", Audience: "private", DisplayName: "Team standup"}, ClassInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := recorded(t, teamStandup)
			_, err := s.SetSpaceAudience(writeCtx(), tc.in)
			assertClass(t, err, tc.want)
			if n := rec.writes(); n != 0 {
				t.Errorf("a refused call sent %d writes", n)
			}
		})
	}
}

// Google's mask covers all of spaceDetails, so a description sent alone
// would clear the guidelines. They are read and sent back as they were,
// a dry run included. The read that gives them is the one that holds
// space_display_name to the space, so neither edit reads twice.
func TestUpdateSpaceKeepsTheGuidelines(t *testing.T) {
	description, name := "What this room is for", "Renamed"
	for _, tc := range []struct {
		label    string
		in       UpdateSpaceInput
		wantBody string
		wantGets int
	}{
		{"a new description", UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Description: &description},
			`{"spaceDetails":{"description":"What this room is for","guidelines":"Be kind"}}`, 1},
		{"a rename", UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", DisplayName: &name}, `{"displayName":"Renamed"}`, 1},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, `{"name":"spaces/A","displayName":"Team","spaceDetails":{"description":"Old","guidelines":"Be kind"}}`)
					return
				}
				fmt.Fprint(w, `{"name":"spaces/A"}`)
			})
			if _, err := s.UpdateSpace(writeCtx(), tc.in); err != nil {
				t.Fatalf("UpdateSpace: %v", err)
			}
			if sent := rec.last(t); sent.Method != http.MethodPatch || sent.Body != tc.wantBody {
				t.Errorf("sent %s %s, want PATCH %s", sent.Method, sent.Body, tc.wantBody)
			}
			if gets := rec.count(http.MethodGet); gets != tc.wantGets {
				t.Errorf("%d reads, want %d", gets, tc.wantGets)
			}
		})
	}

	s, rec := recorded(t, ok(`{"name":"spaces/A","displayName":"Team","spaceDetails":{"guidelines":"Be kind"}}`))
	got, err := s.UpdateSpace(writeCtx(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Description: &description, DryRun: true})
	if err != nil {
		t.Fatalf("UpdateSpace dry run: %v", err)
	}
	details, _ := got.Rendered["spaceDetails"].(map[string]any)
	if details["guidelines"] != "Be kind" || rec.last(t).Method != http.MethodGet {
		t.Errorf("a dry run rendered %v after a %s; want the guidelines kept, after a read alone",
			got.Rendered, rec.last(t).Method)
	}
}

// Google refuses the audience beside any other mask, so the call is
// refused here, where the argument names are still known, rather than
// split into two patches that could half land.
func TestUpdateSpaceRefusesAnAudienceBesideOtherFields(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	name := "Renamed"
	for _, tc := range []struct {
		label string
		in    UpdateSpaceInput
	}{
		{"with a name", UpdateSpaceInput{Space: "spaces/A", DisplayName: &name, Audience: "default"}},
		{"with a description", UpdateSpaceInput{Space: "spaces/A", Description: &name, Audience: "private"}},
		{"misshapen", UpdateSpaceInput{Space: "spaces/A", Audience: "audiences/"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, err := s.UpdateSpace(writeCtx(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("refused edits reached Google %d times", rec.len())
	}
}

// A model fills unused fields with "". Beside a rename that is no
// audience at all: the rename goes through and access is not touched.
func TestUpdateSpaceTakesABlankAudienceAsLeftAlone(t *testing.T) {
	name := "Renamed"
	s, rec := recorded(t, named(ok(`{"name":"spaces/A"}`)))
	got, err := s.UpdateSpace(writeCtx(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", DisplayName: &name, Audience: " "})
	if err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	if m := rec.last(t).mask(t); m != "displayName" || got.Audience != nil {
		t.Errorf("mask = %q, audience = %v; want a rename alone", m, got.Audience)
	}

	_, err = s.UpdateSpace(writeCtx(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", Audience: ""})
	assertClass(t, err, ClassInvalid)
	// The rename read the space and wrote; the empty edit did neither.
	if rec.len() != 2 {
		t.Errorf("an empty edit reached Google")
	}
}

// A space is created private: an audience is set_space_audience's.
func TestCreateSpaceRefusesAnAudience(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/NEW","spaceType":"SPACE","displayName":"Team"}`))
	_, err := s.CreateSpace(writeCtx(), CreateSpaceInput{DisplayName: "Team", Audience: "default"})
	assertClass(t, err, ClassBlocked)
	_, err = s.CreateSpace(writeCtx(), CreateSpaceInput{DisplayName: "Team", Audience: "audiences/"})
	assertClass(t, err, ClassInvalid)
	if rec.len() != 0 {
		t.Errorf("a refused create reached Google %d times", rec.len())
	}

	// Blank and private both make a private space.
	for _, audience := range []string{"  ", "private"} {
		_, err := s.CreateSpace(writeCtx(), CreateSpaceInput{DisplayName: "Team", Audience: audience})
		if err != nil || strings.Contains(rec.last(t).Body, "accessSettings") {
			t.Errorf("audience %q: %v, body %s", audience, err, rec.last(t).Body)
		}
	}
}

func TestUpdateSpaceDryRunChangesNothing(t *testing.T) {
	name := "Renamed"
	s, rec := recorded(t, named(ok(`{}`)))
	got, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{
		SpaceDisplayName: "Team",
		Space:            "spaces/A", DisplayName: &name, DryRun: true,
	})
	if err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	// It reads the space, to hold space_display_name to it, and writes
	// nothing.
	if w := rec.writes(); w != 0 {
		t.Errorf("a dry run made %d writes", w)
	}
	if !got.DryRun || got.UpdateMask != "displayName" || got.Rendered["displayName"] != "Renamed" {
		t.Errorf("result = %+v", got)
	}
}

func TestSpaceWritesNameTheirScopes(t *testing.T) {
	s := newNamedService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	name := "Renamed"
	for _, tc := range []struct {
		label string
		run   func() error
		want  string
	}{
		{"create", func() error {
			_, err := s.CreateSpace(context.Background(), CreateSpaceInput{
				DisplayName: "Team", MemberEmails: []string{"janedoe@example.com"},
			})
			return err
		}, scopes.SpacesCreate},
		{"update", func() error {
			_, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{SpaceDisplayName: "Team", Space: "spaces/A", DisplayName: &name})
			return err
		}, scopes.Spaces},
	} {
		t.Run(tc.label, func(t *testing.T) { assertScope(t, tc.run(), tc.want) })
	}
}

// writeCtx is a context a write may be made under.
func writeCtx() context.Context { return context.Background() }

// bareSpace is the path of a read of one space, which is what the check
// every write that names a space makes before it writes.
var bareSpace = regexp.MustCompile(`/spaces/[^/:]+$`)

// named answers that read with a space called "Team", the name these
// tests pass as space_display_name, and hands every other request to h.
func named(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && bareSpace.MatchString(r.URL.Path) {
			fmt.Fprintf(w, `{"name":%q,"displayName":"Team","spaceType":"SPACE"}`,
				r.URL.Path[strings.LastIndex(r.URL.Path, "spaces/"):])
			return
		}
		h(w, r)
	}
}

// newNamedService is newService for a write that names a space.
func newNamedService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	return newService(t, named(handler))
}
