package directory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// newResolver points a Resolver at a stub People API, so no test here
// reaches Google.
func newResolver(t *testing.T, cache *Cache, handler http.HandlerFunc) *Resolver {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := gchat.New(gchat.Options{
		HTTP:       srv.Client(),
		PeopleBase: srv.URL + "/people",
		Tokens:     staticToken("test"),
	})
	return NewResolver(client, cache, nil)
}

func TestResolveReadsThePrimaryAddress(t *testing.T) {
	r := newResolver(t, tempCache(t, time.Hour), func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "resourceName":"people/1","emailAddresses":[
		    {"value":"old@example.com"},
		    {"value":"janedoe@example.com","metadata":{"primary":true}}
		  ],"names":[{"displayName":"Jane Doe","metadata":{"primary":true}}]}}]}`)
	})
	got := r.ResolveOne(context.Background(), "users/1")
	if got.Email != "janedoe@example.com" {
		t.Errorf("email = %q, want the primary one", got.Email)
	}
	if got.DisplayName != "Jane Doe" {
		t.Errorf("name = %q", got.DisplayName)
	}
}

// With no address marked primary, the first one is better than none.
func TestResolveFallsBackToTheFirstAddress(t *testing.T) {
	r := newResolver(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "emailAddresses":[{"value":"janedoe@example.com"}],"names":[{"displayName":"Jane Doe"}]}}]}`)
	})
	if got := r.ResolveOne(context.Background(), "users/1"); got.Email != "janedoe@example.com" {
		t.Errorf("person = %+v", got)
	}
}

// The whole point of this package: a People failure costs the email and
// nothing else. Nothing here may return an error or panic.
func TestResolveAbsorbsEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"scope never granted", http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`},
		{"outside the directory", http.StatusNotFound, `{"error":{"status":"NOT_FOUND","message":"not found"}}`},
		{"quota", http.StatusTooManyRequests, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`},
		{"google is unwell", http.StatusInternalServerError, `{"error":{"status":"INTERNAL"}}`},
		{"not even JSON", http.StatusOK, `<html>`},
		// The ordinary answer for someone outside the caller's
		// directory: the batch succeeds, that one entry carries a
		// status and no profile.
		{"a person the caller cannot see", http.StatusOK, `{"responses":[{"requestedResourceName":"people/1","status":{"code":7}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newResolver(t, nil, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			got := r.Resolve(context.Background(), []string{"users/1"})
			if _, ok := got["users/1"]; !ok {
				t.Fatal("every id asked for must be in the result, so callers can index without a fallback")
			}
			if got["users/1"].Email != "" {
				t.Errorf("person = %+v, want an empty answer", got["users/1"])
			}
		})
	}
}

// A page of messages costs one request, not one per sender. Google's
// batch endpoint takes 200 ids, so even the widest listing this server
// allows fits in a single call.
func TestResolveAsksOnceForThePage(t *testing.T) {
	var calls atomic.Int64
	var asked []string
	r := newResolver(t, tempCache(t, time.Hour), func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		asked = req.URL.Query()["resourceNames"]
		fmt.Fprint(w, `{"responses":[
		  {"requestedResourceName":"people/1","person":{"emailAddresses":[{"value":"one@example.com"}]}},
		  {"requestedResourceName":"people/2","person":{"emailAddresses":[{"value":"two@example.com"}]}},
		  {"requestedResourceName":"people/3","person":{"emailAddresses":[{"value":"three@example.com"}]}}
		]}`)
	})
	ids := make([]string, 0, 150)
	for range 50 {
		ids = append(ids, "users/1", "users/2", "users/3")
	}
	got := r.Resolve(context.Background(), ids)
	if len(got) != 3 {
		t.Errorf("resolved %d people, want 3", len(got))
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d requests, want one for the whole page", n)
	}
	if len(asked) != 3 {
		t.Errorf("asked for %v, want one entry per unique person", asked)
	}
	if got["users/2"].Email != "two@example.com" {
		t.Errorf("people = %+v, want each answer filed under the id that was asked", got)
	}
}

// More people than one batch holds still costs one request per batch,
// not one per person.
func TestResolveChunksALargeBatch(t *testing.T) {
	var calls atomic.Int64
	r := newResolver(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"responses":[]}`)
	})
	ids := make([]string, 0, 450)
	for i := range 450 {
		ids = append(ids, fmt.Sprintf("users/%d", i))
	}
	got := r.Resolve(context.Background(), ids)
	if len(got) != 450 {
		t.Errorf("resolved %d entries, want one per id asked", len(got))
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("%d requests for 450 people, want 3 batches of 200", n)
	}
}

// The second call is what the cache is for.
func TestResolveUsesTheCache(t *testing.T) {
	var calls atomic.Int64
	cache := tempCache(t, time.Hour)
	r := newResolver(t, cache, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "emailAddresses":[{"value":"janedoe@example.com"}]}}]}`)
	})
	r.Resolve(context.Background(), []string{"users/1"})
	r.Resolve(context.Background(), []string{"users/1"})
	if n := calls.Load(); n != 1 {
		t.Errorf("%d requests, want the second answered from the cache", n)
	}
}

// Someone outside the directory would otherwise cost a round trip on
// every tool call for the whole conversation.
func TestAnUnresolvablePersonIsAskedAboutOnce(t *testing.T) {
	var calls atomic.Int64
	r := newResolver(t, tempCache(t, time.Hour), func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","status":{"code":5}}]}`)
	})
	r.Resolve(context.Background(), []string{"users/1"})
	r.Resolve(context.Background(), []string{"users/1"})
	if n := calls.Load(); n != 1 {
		t.Errorf("%d requests, want the miss remembered for this process", n)
	}
}

func TestResolveHandlesNothingToDo(t *testing.T) {
	r := newResolver(t, nil, func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made")
	})
	if got := r.Resolve(context.Background(), nil); len(got) != 0 {
		t.Errorf("Resolve(nil) = %+v", got)
	}
	if got := r.Resolve(context.Background(), []string{""}); len(got) != 0 {
		t.Errorf("Resolve(empty id) = %+v", got)
	}
}

// A Resolver with no cache is what a Service built without one has. It
// must work, just without memory.
func TestResolveWithoutACache(t *testing.T) {
	var calls atomic.Int64
	r := newResolver(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "emailAddresses":[{"value":"janedoe@example.com"}]}}]}`)
	})
	r.Resolve(context.Background(), []string{"users/1"})
	r.Resolve(context.Background(), []string{"users/1"})
	if n := calls.Load(); n != 2 {
		t.Errorf("%d requests, want no caching", n)
	}
	r.Learn(map[string]Person{"users/2": {Email: "johndoe@example.com"}})
}

// A directory search resolves people the message tools would otherwise
// look up one at a time.
func TestLearnFillsTheCache(t *testing.T) {
	var calls atomic.Int64
	cache := tempCache(t, time.Hour)
	r := newResolver(t, cache, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "emailAddresses":[{"value":"other@example.com"}]}}]}`)
	})
	r.Learn(map[string]Person{"users/1": {Email: "janedoe@example.com", DisplayName: "Jane Doe"}})

	got := r.Resolve(context.Background(), []string{"users/1"})
	if got["users/1"].Email != "janedoe@example.com" {
		t.Errorf("person = %+v, want what Learn recorded", got["users/1"])
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d requests, want none after Learn", n)
	}
	r.Learn(nil)
}

func TestResolveAsksThePeopleAPIForTheRightResource(t *testing.T) {
	var path, fields string
	var names []string
	r := newResolver(t, nil, func(w http.ResponseWriter, req *http.Request) {
		path, fields = req.URL.Path, req.URL.Query().Get("personFields")
		names = req.URL.Query()["resourceNames"]
		fmt.Fprint(w, `{"responses":[]}`)
	})
	r.ResolveOne(context.Background(), "users/12345")
	if !strings.HasSuffix(path, "people:batchGet") {
		t.Errorf("path = %q, want the batch endpoint", path)
	}
	if len(names) != 1 || names[0] != "people/12345" {
		t.Errorf("resourceNames = %v, want the users id translated to a people resource", names)
	}
	if fields != "emailAddresses,names" {
		t.Errorf("personFields = %q", fields)
	}
}

// A contact id belongs to the caller's own list and names nobody in
// Chat, so it must never be cached under a Chat user id.
func TestChatUserIDAcceptsOnlyAWorkspaceProfile(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"people/123456789", "users/123456789"},
		{"people/c123456789", ""},
		{"people/", ""},
		{"", ""},
		{"users/123", ""},
	} {
		if got := ChatUserID(tc.in); got != tc.want {
			t.Errorf("ChatUserID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A cancelled context must not turn into a dropped row either: the
// caller still gets an entry for every id it asked about.
func TestResolveOnACancelledContext(t *testing.T) {
	r := newResolver(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"responses":[]}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := r.Resolve(ctx, []string{"users/1"})
	if _, ok := got["users/1"]; !ok {
		t.Error("a cancelled lookup must still leave an entry")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("context should be cancelled")
	}
}

// A failed lookup must not be cached as "this person has no address".
// It is indistinguishable in the result from a genuine miss, so caching
// it turns one 429 into a whole cache lifetime of null addresses — and
// the degrade rule is that a People failure costs the email field on
// that call, not for the next 24 hours.
func TestATransientFailureIsNotCachedAsAMiss(t *testing.T) {
	var calls int
	cache := tempCache(t, time.Hour)
	r := newResolver(t, cache, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","person":{
		  "emailAddresses":[{"value":"janedoe@example.com"}],
		  "names":[{"displayName":"Jane Doe"}]}}]}`)
	})

	if got := r.Resolve(context.Background(), []string{"users/1"}); got["users/1"].Email != "" {
		t.Fatalf("a refused lookup should resolve to nothing: %+v", got["users/1"])
	}
	got := r.Resolve(context.Background(), []string{"users/1"})
	if got["users/1"].Email != "janedoe@example.com" {
		t.Errorf("the second call served a cached failure instead of asking again: %+v", got["users/1"])
	}
	if calls != 2 {
		t.Errorf("upstream was called %d times; the failure was cached", calls)
	}
}

// A genuine miss is still cached, or every listing re-asks about the
// same people who are not in the directory.
func TestAGenuineMissIsCached(t *testing.T) {
	var calls int
	cache := tempCache(t, time.Hour)
	r := newResolver(t, cache, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"responses":[{"requestedResourceName":"people/1","status":{"code":7}}]}`)
	})
	r.Resolve(context.Background(), []string{"users/1"})
	r.Resolve(context.Background(), []string{"users/1"})
	if calls != 1 {
		t.Errorf("upstream was called %d times; a known miss should be cached", calls)
	}
}
