package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// The query is built here, not by the caller, and Google's grammar
// decides most of it: spaceType is required, so a search finds named
// spaces and never a group chat.
func TestSearchSpacesBuildsGooglesQuery(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name string
		in   SearchSpacesInput
		want string
	}{
		{"the required clause alone", SearchSpacesInput{}, `spaceType = "SPACE"`},
		{
			"a display name is a prefix match",
			SearchSpacesInput{DisplayName: "Engineering"},
			`spaceType = "SPACE" AND displayName:"Engineering"`,
		},
		{
			"external access narrows further",
			SearchSpacesInput{DisplayName: "Eng", ExternalUserAllowed: &yes},
			`spaceType = "SPACE" AND displayName:"Eng" AND externalUserAllowed = true`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("query")
				fmt.Fprint(w, `{"results":[]}`)
			})
			if _, err := s.SearchSpaces(context.Background(), tc.in); err != nil {
				t.Fatalf("SearchSpaces: %v", err)
			}
			if got != tc.want {
				t.Errorf("query = %q, want %q", got, tc.want)
			}
		})
	}
}

// The grammar's escaping rules are not documented, so a value that
// could change what the query means is refused rather than guessed at.
func TestSearchSpacesRefusesAQuotedDisplayName(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, name := range []string{`Eng" OR displayName:"`, `back\slash`} {
		_, err := s.SearchSpaces(context.Background(), SearchSpacesInput{DisplayName: name})
		assertClass(t, err, ClassInvalid)
	}
}

// Google deprecated the field the rows used to arrive in and still
// sends it. Reading only the new one would report an empty search.
func TestSearchSpacesReadsEitherShape(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"results", `{"results":[{"space":{"name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":"Engineering"}}]}`},
		{"the deprecated spaces field", `{"spaces":[{"name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":"Engineering"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) })
			got, err := s.SearchSpaces(context.Background(), SearchSpacesInput{})
			if err != nil {
				t.Fatalf("SearchSpaces: %v", err)
			}
			if len(got.Spaces) != 1 || got.Spaces[0].DisplayName != "Engineering" {
				t.Errorf("spaces = %+v", got.Spaces)
			}
		})
	}
}

// Google matches a group chat by its whole human membership, so the
// caller is never named. The expanded view is what makes a row worth
// reading rather than a bare resource name.
func TestFindGroupChatsNamesOnlyTheOtherPeople(t *testing.T) {
	var users []string
	var view string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		users = r.URL.Query()["users"]
		view = r.URL.Query().Get("spaceView")
		fmt.Fprint(w, `{"spaces":[{"name":"spaces/AAAAspace1","spaceType":"GROUP_CHAT","displayName":"Trip"}]}`)
	})
	got, err := s.FindGroupChats(context.Background(), FindGroupChatsInput{
		Emails: []string{"janedoe@example.com", "johndoe@example.com"},
	})
	if err != nil {
		t.Fatalf("FindGroupChats: %v", err)
	}
	want := []string{"users/janedoe@example.com", "users/johndoe@example.com"}
	if !slices.Equal(users, want) {
		t.Errorf("users = %v, want %v", users, want)
	}
	if view != "SPACE_VIEW_EXPANDED" {
		t.Errorf("spaceView = %q, so a row would carry only a resource name", view)
	}
	if len(got.Spaces) != 1 || got.Spaces[0].Kind != KindGroupChat {
		t.Errorf("spaces = %+v", got.Spaces)
	}
}

// Google takes at most 49, and an address it cannot read is a mistake
// worth reporting before the call rather than after.
func TestFindGroupChatsChecksItsArguments(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	tooMany := make([]string, 50)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("person%d@example.com", i)
	}
	for _, tc := range []struct {
		name   string
		emails []string
	}{
		{"none", nil},
		{"fifty", tooMany},
		{"not an address", []string{"janedoe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.FindGroupChats(context.Background(), FindGroupChatsInput{Emails: tc.emails})
			assertClass(t, err, ClassInvalid)
		})
	}
}

// A named space of one's own is a real thing to want, and Google allows
// it. Requiring a second person meant the only way to try delete_space
// was to pull a colleague into a room made to be destroyed.
func TestASpaceCanBeMadeWithNobodyElseInIt(t *testing.T) {
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1","displayName":"Notes","spaceType":"SPACE"}`)
	})
	got, err := s.CreateSpace(context.Background(), CreateSpaceInput{DisplayName: "Notes"})
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if got.Name != "spaces/AAAAspace1" {
		t.Errorf("result = %+v", got)
	}
	if members, ok := body["memberships"].([]any); ok && len(members) != 0 {
		t.Errorf("memberships = %v, want none", members)
	}

	// A group chat still needs people: one of them is a direct message
	// and Google has a different call for that.
	_, err = s.CreateGroupChat(context.Background(), CreateSpaceInput{})
	assertClass(t, err, ClassInvalid)
}
