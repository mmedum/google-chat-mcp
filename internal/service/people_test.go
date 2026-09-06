package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// peoplePaths routes the two People search endpoints separately, which
// is what a hybrid lookup needs.
func peoplePaths(directory, contacts http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "searchContacts") {
			contacts(w, r)
			return
		}
		directory(w, r)
	}
}

const directoryHit = `{"people":[{"resourceName":"people/123","emailAddresses":[{"value":"janedoe@example.com"}],"names":[{"displayName":"Jane Doe"}]}]}`
const contactsHit = `{"results":[{"person":{"resourceName":"people/c456","emailAddresses":[{"value":"johndoe@example.com"}],"names":[{"displayName":"John Doe"}]}}]}`

func TestSearchPeopleAsksBothSources(t *testing.T) {
	s := newService(t, peoplePaths(ok(directoryHit), ok(contactsHit)))
	got, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"})
	if err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	if len(got.People) != 2 {
		t.Fatalf("people = %+v, want a hit from each source", got.People)
	}
	if len(got.Succeeded) != 2 || len(got.Attempted) != 2 {
		t.Errorf("sources = %+v / %+v", got.Attempted, got.Succeeded)
	}
	// A Workspace profile shares its numeric id with Chat's users/{id};
	// a personal contact id names nobody in Chat.
	if got.People[0].UserID != "users/123" {
		t.Errorf("directory hit = %+v, want a Chat user id", got.People[0])
	}
	if got.People[1].UserID != "" {
		t.Errorf("contact hit = %+v, want no Chat user id", got.People[1])
	}
}

// Directory hits back-fill the cache, so the next message listing
// resolves the same person without another lookup.
func TestSearchPeopleFillsTheEmailCache(t *testing.T) {
	s, cache := newServiceCached(t, peoplePaths(ok(directoryHit), ok(contactsHit)))
	if _, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"}); err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	cached := cache.Get([]string{"users/123"})
	if cached["users/123"].Email != "janedoe@example.com" {
		t.Errorf("cache = %+v, want the directory hit remembered", cached)
	}
	// The contact id does not round-trip to Chat, so caching it under
	// a Chat user id would poison later lookups.
	if got := cache.Get([]string{"users/c456", "users/456"}); len(got) != 0 {
		t.Errorf("cache = %+v, want no contact id stored", got)
	}
}

// One source dying must not hide the other's hits: a model that gets an
// error simply retries with a broader query.
func TestSearchPeopleDegradesWhenOneSourceFails(t *testing.T) {
	s := newService(t, peoplePaths(
		status(http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED","message":"Directory sharing is off"}}`),
		ok(contactsHit)))
	got, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"})
	if err != nil {
		t.Fatalf("one source failing must not fail the lookup: %v", err)
	}
	if len(got.People) != 1 {
		t.Fatalf("people = %+v, want the surviving source's hit", got.People)
	}
	if len(got.Succeeded) != 1 || got.Succeeded[0] != SourceContacts {
		t.Errorf("succeeded = %+v, want contacts only", got.Succeeded)
	}
	if len(got.Attempted) != 2 {
		t.Errorf("attempted = %+v, want both recorded", got.Attempted)
	}
}

// With nothing left to answer, the caller has to hear why. A set of
// scope refusals maps to the prompt they can act on.
func TestSearchPeopleFailsWhenEverySourceDoes(t *testing.T) {
	scopeDenied := status(http.StatusForbidden,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	s := newService(t, peoplePaths(scopeDenied, scopeDenied))
	_, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"})
	assertClass(t, err, ClassScope)
	if !strings.Contains(err.Error(), "directory.readonly") {
		t.Errorf("error = %v, want a scope named", err)
	}
}

func TestSearchPeopleReportsANonScopeFailure(t *testing.T) {
	broken := status(http.StatusInternalServerError, `{"error":{"status":"INTERNAL","message":"oops"}}`)
	s := newService(t, peoplePaths(broken, broken))
	_, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"})
	assertClass(t, err, ClassServer)
}

// The same person in both sources is one hit. The directory is asked
// first, so their Workspace profile wins over a personal contact entry.
func TestSearchPeopleDeduplicates(t *testing.T) {
	same := `{"results":[{"person":{"resourceName":"people/123","emailAddresses":[{"value":"janedoe@example.com"}]}}]}`
	s := newService(t, peoplePaths(ok(directoryHit), ok(same)))
	got, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "doe"})
	if err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	if len(got.People) != 1 {
		t.Fatalf("people = %+v, want one row for one person", got.People)
	}
	if got.People[0].Source != SourceDirectory {
		t.Errorf("source = %q, want the directory to win", got.People[0].Source)
	}
}

func TestSearchPeopleHonoursOneSource(t *testing.T) {
	s := newService(t, peoplePaths(ok(directoryHit), func(http.ResponseWriter, *http.Request) {
		t.Error("contacts should not be asked")
	}))
	got, err := s.SearchPeople(context.Background(), SearchPeopleInput{
		Query: "doe", Sources: []PeopleSource{SourceDirectory, SourceDirectory},
	})
	if err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	if len(got.Attempted) != 1 {
		t.Errorf("attempted = %+v, want the repeat collapsed", got.Attempted)
	}
}

func TestSearchPeopleSendsWhatGoogleRequires(t *testing.T) {
	var query, sources, mask, size string
	s := newService(t, peoplePaths(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query, mask, size = q.Get("query"), q.Get("readMask"), q.Get("pageSize")
		sources = strings.Join(q["sources"], ",")
		fmt.Fprint(w, `{"people":[]}`)
	}, ok(`{"results":[]}`)))
	if _, err := s.SearchPeople(context.Background(), SearchPeopleInput{
		Query: "doe", Limit: 5, Sources: []PeopleSource{SourceDirectory},
	}); err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	if query != "doe" || mask != "emailAddresses,names" || size != "5" {
		t.Errorf("query=%q mask=%q size=%q", query, mask, size)
	}
	// Omitting sources answers 400, so both directory populations are
	// always asked for.
	if !strings.Contains(sources, "DOMAIN_PROFILE") || !strings.Contains(sources, "DOMAIN_CONTACT") {
		t.Errorf("sources = %q", sources)
	}
}

func TestSearchPeopleRejectsBadInput(t *testing.T) {
	s := newService(t, ok(`{"people":[]}`))
	for _, tc := range []struct {
		name string
		in   SearchPeopleInput
	}{
		{"no query", SearchPeopleInput{}},
		{"query too long", SearchPeopleInput{Query: strings.Repeat("a", 201)}},
		{"limit above the maximum", SearchPeopleInput{Query: "doe", Limit: 500}},
		{"unknown source", SearchPeopleInput{Query: "doe", Sources: []PeopleSource{"LDAP"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SearchPeople(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
}

// Google's contacts endpoint caps a page at 30, so a larger limit has
// to be trimmed on the way out rather than refused.
func TestSearchPeopleTrimsToTheLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"people":[`)
	for i := range 5 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"resourceName":"people/%d","emailAddresses":[{"value":"p%d@example.com"}]}`, i, i)
	}
	b.WriteString("]}")
	s := newService(t, peoplePaths(ok(b.String()), ok(`{"results":[]}`)))
	got, err := s.SearchPeople(context.Background(), SearchPeopleInput{Query: "p", Limit: 2})
	if err != nil {
		t.Fatalf("SearchPeople: %v", err)
	}
	if len(got.People) != 2 {
		t.Errorf("people = %d, want the limit applied", len(got.People))
	}
}
