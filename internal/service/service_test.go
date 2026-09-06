package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/auth"
	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/directory"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
	"github.com/mmedum/google-chat-mcp/internal/scopes"
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
		HTTP:       srv.Client(),
		ChatBase:   srv.URL + "/v1",
		PeopleBase: srv.URL + "/people",
		OIDCBase:   srv.URL + "/oidc",
		Tokens:     staticToken("test"),
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
			ClassRateLimit,
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
		{"rate limited", 429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"slow down"}}`, ClassRateLimit},
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
// Google, so it has to be recognised on its own.
func TestClassifyRecognisesAMissingLogin(t *testing.T) {
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
// the handler runs on the test server's own goroutine. Every dry-run
// test in this package asserts on len, because "made no request at all"
// is the whole promise.
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

// last is the request the call under test ended on.
func (c *calls) last(t *testing.T) call {
	t.Helper()
	got := c.all()
	if len(got) == 0 {
		t.Fatal("no request reached Google")
	}
	return got[len(got)-1]
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
		{"name only", UpdateSpaceInput{Space: "spaces/A", DisplayName: &name},
			"displayName", `"displayName":"Renamed"`, "spaceDetails"},
		{"description only", UpdateSpaceInput{Space: "spaces/A", Description: &description},
			"spaceDetails", `"description":"What this room is for"`, "displayName"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, ok(`{"name":"spaces/A"}`))
			got, err := s.UpdateSpace(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("UpdateSpace: %v", err)
			}
			sent := rec.last(t)
			if sent.Query != "updateMask="+tc.wantMask {
				t.Errorf("query = %q, want the mask %q", sent.Query, tc.wantMask)
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
	s, rec := recorded(t, ok(`{"name":"spaces/A"}`))
	if _, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{
		Space: "spaces/A", Description: &empty,
	}); err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	if !strings.Contains(rec.last(t).Body, `"description":""`) {
		t.Errorf("body = %s, want the empty description sent", rec.last(t).Body)
	}
}

func TestUpdateSpaceRejectsAnEmptyEdit(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	long := strings.Repeat("x", maxSpaceDescription+1)
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

func TestUpdateSpaceDryRunChangesNothing(t *testing.T) {
	name := "Renamed"
	s, rec := recorded(t, ok(`{}`))
	got, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{
		Space: "spaces/A", DisplayName: &name, DryRun: true,
	})
	if err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if !got.DryRun || got.UpdateMask != "displayName" || got.Rendered["displayName"] != "Renamed" {
		t.Errorf("result = %+v", got)
	}
}

func TestSpaceWritesNameTheirScopes(t *testing.T) {
	s := newService(t, status(403,
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
			_, err := s.UpdateSpace(context.Background(), UpdateSpaceInput{Space: "spaces/A", DisplayName: &name})
			return err
		}, scopes.Spaces},
	} {
		t.Run(tc.label, func(t *testing.T) { assertScope(t, tc.run(), tc.want) })
	}
}
