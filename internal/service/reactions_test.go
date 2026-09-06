package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

func TestListReactionsReadsWhoReacted(t *testing.T) {
	s := newService(t, ok(`{"reactions":[
	  {"name":"spaces/A/messages/1/reactions/R1","emoji":{"unicode":"👍"},"user":{"name":"users/1"}},
	  {"name":"spaces/A/messages/1/reactions/R2","emoji":{"customEmoji":{"uid":"x"}},"user":{"name":"users/2"}}
	],"nextPageToken":"more"}`))
	got, err := s.ListReactions(context.Background(), ListReactionsInput{Message: "spaces/A/messages/1"})
	if err != nil {
		t.Fatalf("ListReactions: %v", err)
	}
	if len(got.Reactions) != 1 {
		t.Fatalf("reactions = %+v, want the custom emoji left out", got.Reactions)
	}
	if got.Reactions[0].Emoji != "👍" || got.Reactions[0].UserID != "users/1" {
		t.Errorf("reaction = %+v", got.Reactions[0])
	}
	if got.NextPageToken != "more" {
		t.Errorf("page token = %q", got.NextPageToken)
	}
}

func TestListReactionsPagesAndBounds(t *testing.T) {
	var query, path string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		query, path = r.URL.RawQuery, r.URL.Path
		fmt.Fprint(w, `{"reactions":[]}`)
	})
	if _, err := s.ListReactions(context.Background(), ListReactionsInput{
		Message: "spaces/A/messages/1", Limit: 7, PageToken: "next",
	}); err != nil {
		t.Fatalf("ListReactions: %v", err)
	}
	if !strings.HasSuffix(path, "/spaces/A/messages/1/reactions") {
		t.Errorf("path = %q", path)
	}
	for _, want := range []string{"pageSize=7", "pageToken=next"} {
		if !strings.Contains(query, want) {
			t.Errorf("query = %q, want %q", query, want)
		}
	}
	_, err := s.ListReactions(context.Background(), ListReactionsInput{Message: "spaces/A/messages/1", Limit: 5000})
	assertClass(t, err, ClassInvalid)
	for _, name := range []string{"", "spaces/A"} {
		_, err := s.ListReactions(context.Background(), ListReactionsInput{Message: name})
		assertClass(t, err, ClassInvalid)
	}
}

// The re-consent prompt names the sensitive-tier reactions scope, not
// the restricted umbrella: a deployer who declined that tier should not
// be pushed back into it by the message telling them what to grant.
func TestListReactionsNamesTheGranularScope(t *testing.T) {
	s := newService(t, status(http.StatusForbidden,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.ListReactions(context.Background(), ListReactionsInput{Message: "spaces/A/messages/1"})
	assertClass(t, err, ClassScope)
	if !strings.Contains(err.Error(), "chat.messages.reactions") {
		t.Errorf("error = %v, want the reactions scope named", err)
	}
}

func TestAddReactionReacts(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1/reactions/R","emoji":{"unicode":"👍"},"user":{"name":"users/1"}}`))
	got, err := s.AddReaction(context.Background(), AddReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍",
	})
	if err != nil {
		t.Fatalf("AddReaction: %v", err)
	}
	if got.Name != "spaces/A/messages/1/reactions/R" || got.Emoji != "👍" || got.UserID != "users/1" {
		t.Errorf("result = %+v", got)
	}
	if sent := rec.last(t); sent.Body != `{"emoji":{"unicode":"👍"}}` {
		t.Errorf("body = %s", sent.Body)
	}
}

// Google answers a repeat with ALREADY_EXISTS rather than treating it
// as a no-op, so the tool is idempotent only because the reaction that
// is already there is looked up and returned.
func TestAddReactionReturnsTheReactionAlreadyThere(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oidc/"):
			fmt.Fprint(w, `{"sub":"1","email":"janedoe@example.com"}`)
		case r.Method == "POST":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"status":"ALREADY_EXISTS","message":"reaction exists"}}`)
		default:
			fmt.Fprint(w, `{"reactions":[{"name":"spaces/A/messages/1/reactions/R","emoji":{"unicode":"👍"},"user":{"name":"users/1"}}]}`)
		}
	}))
	got, err := s.AddReaction(context.Background(), AddReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍",
	})
	if err != nil {
		t.Fatalf("reacting the same way twice is not a failure: %v", err)
	}
	if got.Name != "spaces/A/messages/1/reactions/R" {
		t.Errorf("result = %+v", got)
	}
	// The lookup has to be the caller's own reaction, not the first one
	// anybody left.
	listed := c.last(t)
	if !strings.Contains(listed.Query, "user.name") || !strings.Contains(listed.Query, "users%2F1") {
		t.Errorf("filter = %q, want it narrowed to the signed-in account", listed.Query)
	}
}

// The emoji is interpolated into a Chat filter, so a value that could
// end the quoted string must never reach one.
func TestReactionWritesRefuseAnEmojiThatCouldEscapeAFilter(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	for _, emoji := range []string{"", "  ", `👍" OR emoji.unicode = "👎`, `👍\`, "👍 👎", strings.Repeat("👍", maxEmoji+1)} {
		_, err := s.AddReaction(context.Background(), AddReactionInput{Message: "spaces/A/messages/1", Emoji: emoji})
		assertClass(t, err, ClassInvalid)
	}
	if rec.len() != 0 {
		t.Errorf("a crafted emoji reached Google %d times", rec.len())
	}
}

func TestRemoveReactionDeletesByName(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.RemoveReaction(context.Background(), RemoveReactionInput{
		Reaction: "spaces/A/messages/1/reactions/R",
	})
	if err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if !got.Removed || got.Name != "spaces/A/messages/1/reactions/R" {
		t.Errorf("result = %+v", got)
	}
	if sent := rec.last(t); sent.Method != "DELETE" || rec.len() != 1 {
		t.Errorf("request = %s, %d in all; a named reaction needs no lookup", sent.Method, rec.len())
	}
}

// Chat's own filter takes a numeric user id, not an address, so the
// match on who reacted has to happen here.
func TestRemoveReactionFindsThePersonWhoReacted(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(route(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "DELETE" {
				fmt.Fprint(w, `{}`)
				return
			}
			fmt.Fprint(w, `{"reactions":[
			  {"name":"spaces/A/messages/1/reactions/R1","emoji":{"unicode":"👍"},"user":{"name":"users/1"}},
			  {"name":"spaces/A/messages/1/reactions/R2","emoji":{"unicode":"👍"},"user":{"name":"users/2"}}
			]}`)
		},
		func(w http.ResponseWriter, r *http.Request) {
			entries := make([]string, 0, 2)
			for _, name := range r.URL.Query()["resourceNames"] {
				email := "johndoe@example.com"
				if strings.HasSuffix(name, "/2") {
					email = "janedoe@example.com"
				}
				entries = append(entries, fmt.Sprintf(
					`{"requestedResourceName":%q,"person":{"emailAddresses":[{"value":%q}]}}`, name, email))
			}
			fmt.Fprintf(w, `{"responses":[%s]}`, strings.Join(entries, ","))
		},
	)))
	got, err := s.RemoveReaction(context.Background(), RemoveReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍", Email: "JaneDoe@example.com",
	})
	if err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if !got.Removed || got.Name != "spaces/A/messages/1/reactions/R2" {
		t.Errorf("result = %+v, want the second person's reaction", got)
	}
	if !strings.HasSuffix(c.last(t).Path, "/reactions/R2") {
		t.Errorf("deleted %q", c.last(t).Path)
	}
}

func TestRemoveReactionReportsNothingToDelete(t *testing.T) {
	s := newService(t, ok(`{"reactions":[]}`))
	got, err := s.RemoveReaction(context.Background(), RemoveReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍", Email: "janedoe@example.com",
	})
	if err != nil {
		t.Fatalf("nothing to delete is not a failure: %v", err)
	}
	if got.Removed || got.Name != "" {
		t.Errorf("result = %+v", got)
	}
}

// Removed false is read as "already gone" and a caller stops there. It
// must not also mean "could not tell": somebody who reacted did not
// resolve, so the person named may well still be reacting.
func TestRemoveReactionWillNotGuessWhenAReactorDoesNotResolve(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(route(
		ok(`{"reactions":[{"name":"spaces/A/messages/1/reactions/R1","emoji":{"unicode":"👍"},"user":{"name":"users/1"}}]}`),
		nobody(),
	)))
	_, err := s.RemoveReaction(context.Background(), RemoveReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍", Email: "janedoe@example.com",
	})
	if err == nil {
		t.Fatal("an unresolved reactor must not be reported as already gone")
	}
	if !strings.Contains(err.Error(), "Nothing was deleted") {
		t.Errorf("error = %q, want it to say nothing was deleted", err)
	}
	for _, sent := range c.all() {
		if sent.Method == "DELETE" {
			t.Errorf("a reaction was deleted anyway: %s", sent.Path)
		}
	}
}

func TestRemoveReactionNeedsOneShapeOrTheOther(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	for _, tc := range []struct {
		name string
		in   RemoveReactionInput
	}{
		{"nothing at all", RemoveReactionInput{}},
		{"both shapes", RemoveReactionInput{
			Reaction: "spaces/A/messages/1/reactions/R",
			Message:  "spaces/A/messages/1", Emoji: "👍", Email: "janedoe@example.com",
		}},
		{"half a triple", RemoveReactionInput{Message: "spaces/A/messages/1", Emoji: "👍"}},
		{"a message that is not one", RemoveReactionInput{
			Message: "spaces/A", Emoji: "👍", Email: "janedoe@example.com",
		}},
		{"a reaction name that is not one", RemoveReactionInput{Reaction: "spaces/A/messages/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.RemoveReaction(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// The sensitive-tier reactions scope is the one named, not the
// restricted umbrella that also works.
func TestReactionWritesNameTheGranularScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	for _, run := range []func() error{
		func() error {
			_, err := s.AddReaction(context.Background(), AddReactionInput{Message: "spaces/A/messages/1", Emoji: "👍"})
			return err
		},
		func() error {
			_, err := s.RemoveReaction(context.Background(), RemoveReactionInput{Reaction: "spaces/A/messages/1/reactions/R"})
			return err
		},
	} {
		assertScope(t, run(), scopes.MessagesReactions)
	}
}

// The caller's own id is fetched once. A session that has already asked
// who it is signed in as pays nothing for the duplicate-reaction path.
func TestTheCallersIdentityIsFetchedOnce(t *testing.T) {
	var identityCalls int
	c := &calls{}
	s := newService(t, c.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oidc/"):
			identityCalls++
			fmt.Fprint(w, `{"sub":"1","email":"janedoe@example.com"}`)
		case r.Method == "POST":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"status":"ALREADY_EXISTS","message":"reaction exists"}}`)
		default:
			fmt.Fprint(w, `{"reactions":[{"name":"spaces/A/messages/1/reactions/R","emoji":{"unicode":"👍"},"user":{"name":"users/1"}}]}`)
		}
	}))
	for range 3 {
		if _, err := s.AddReaction(context.Background(), AddReactionInput{
			Message: "spaces/A/messages/1", Emoji: "👍",
		}); err != nil {
			t.Fatalf("AddReaction: %v", err)
		}
	}
	if identityCalls != 1 {
		t.Errorf("%d identity lookups, want one for the life of the process", identityCalls)
	}
}

// The reaction is on the message either way, so a failure to work out
// which one is the caller's says that, rather than naming a scope the
// caller never asked about.
func TestADuplicateReactionSaysWhereToLookWhenItCannotIdentifyTheCaller(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oidc/") {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"status":"INTERNAL","message":"oops"}}`)
			return
		}
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":{"status":"ALREADY_EXISTS","message":"reaction exists"}}`)
	})
	_, err := s.AddReaction(context.Background(), AddReactionInput{
		Message: "spaces/A/messages/1", Emoji: "👍",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "list_reactions") {
		t.Errorf("error = %q, want it to say where to look", err)
	}
}
