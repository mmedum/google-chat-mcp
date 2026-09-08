package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

func searchPage(messages ...string) string {
	return `{"messages":[` + strings.Join(messages, ",") + `]}`
}

func message(id, text string) string {
	return fmt.Sprintf(`{"name":"spaces/A/messages/%s","sender":{"name":"users/1"},
	  "createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T1"},"text":%q}`, id, text)
}

// The local scan is the pattern one, and a pattern says for itself
// whether case matters.
func TestSearchMatchesCaseInsensitivelyWhenAsked(t *testing.T) {
	s := newService(t, ok(searchPage(
		message("1", "the Quarterly review is Friday"),
		message("2", "lunch"),
	)))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "(?i)quarterly"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got.Matches) != 1 || got.Matches[0].Name != "spaces/A/messages/1" {
		t.Fatalf("matches = %+v, want the case-insensitive hit", got.Matches)
	}
	if got.Scanned != 2 {
		t.Errorf("scanned = %d, want both messages counted", got.Scanned)
	}
	if got.CapReached {
		t.Error("a single page that ended is not the cap")
	}
	if got.Matches[0].Snippet != "the Quarterly review is Friday" {
		t.Errorf("snippet = %q, want the whole short message", got.Matches[0].Snippet)
	}
}

func TestSearchMatchesAPattern(t *testing.T) {
	s := newService(t, ok(searchPage(message("1", "deploy 2026-01-02 went out"), message("2", "no date here"))))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{
		Space: "spaces/A", Regex: `\d{4}-\d{2}-\d{2}`,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("matches = %+v", got.Matches)
	}
}

// The snippet is what the caller reads to see why a message matched, so
// it has to be centred on the match and cut at a character boundary.
func TestSnippetSurroundsTheMatch(t *testing.T) {
	body := strings.Repeat("é", 200) + "needle" + strings.Repeat("ü", 200)
	s := newService(t, ok(searchPage(message("1", body))))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "needle"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	snip := got.Matches[0].Snippet
	if !strings.Contains(snip, "needle") {
		t.Errorf("snippet = %q, want the match in it", snip)
	}
	if !strings.HasPrefix(snip, "…") || !strings.HasSuffix(snip, "…") {
		t.Errorf("snippet = %q, want an ellipsis on each cut side", snip)
	}
	if strings.Contains(snip, "�") {
		t.Error("the snippet cut a character in half")
	}
	if n := len([]rune(snip)); n > 2*snippetContext+len("needle")+2 {
		t.Errorf("snippet is %d characters, want about %d", n, 2*snippetContext)
	}
}

// The page cap is what keeps one tool call from spending a caller's
// whole quota. Reaching it has to be visible, or a partial answer reads
// as a complete one.
func TestSearchStopsAtThePageCap(t *testing.T) {
	var pages int
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		pages++
		fmt.Fprint(w, `{"messages":[`+message("1", "nothing to see")+`],"nextPageToken":"more"}`)
	})
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{
		Space: "spaces/A", Regex: "needle", MaxPages: 3,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if pages != 3 {
		t.Errorf("fetched %d pages, want the cap of 3", pages)
	}
	if !got.CapReached {
		t.Error("cap_reached should say the answer is partial")
	}
}

func TestSearchStopsAtTheMatchLimit(t *testing.T) {
	s := newService(t, ok(searchPage(
		message("1", "needle"), message("2", "needle"), message("3", "needle"),
	)))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{
		Space: "spaces/A", Regex: "needle", Limit: 2,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got.Matches) != 2 {
		t.Errorf("matches = %d, want the limit respected", len(got.Matches))
	}
}

// A row that cannot be read was never searched, so reporting no matches
// would be wrong. The count is what says the answer is incomplete.
func TestSearchCountsWhatItCouldNotRead(t *testing.T) {
	page := `{"messages":[
	  {"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T"},"text":"needle"},
	  {"text":"needle but with no resource name"}
	]}`
	s := newService(t, ok(page))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "needle"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if got.Unparsed != 1 {
		t.Errorf("unparsed = %d, want the unusable row counted", got.Unparsed)
	}
	if len(got.Matches) != 1 {
		t.Errorf("matches = %+v, want the usable row kept", got.Matches)
	}
	if got.Scanned != 2 {
		t.Errorf("scanned = %d, want both counted", got.Scanned)
	}
}

func TestSearchRejectsBadInput(t *testing.T) {
	s := newService(t, ok(`{"messages":[]}`))
	for _, tc := range []struct {
		name string
		in   SearchMessagesInput
	}{
		// A space with no other term is no longer nothing to match: the
		// space itself is a clause now, and asking Google for a space's
		// messages is a real search.
		{"an upstream filter with a local pattern", SearchMessagesInput{Space: "spaces/A", Regex: "a", HasLink: true}},
		{"both terms", SearchMessagesInput{Space: "spaces/A", Query: "a", Regex: "b"}},
		{"pattern that does not compile", SearchMessagesInput{Space: "spaces/A", Regex: "([a-z"}},
		{"no space for a local scan", SearchMessagesInput{Regex: "a"}},
		{"limit above the maximum", SearchMessagesInput{Space: "spaces/A", Regex: "a", Limit: 500}},
		{"more pages than allowed", SearchMessagesInput{Space: "spaces/A", Regex: "a", MaxPages: 500}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SearchMessages(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
}

// A pattern that does not compile must be caught before anything
// reaches Google: it is the caller's mistake, not an upstream failure.
func TestABadPatternNeverReachesGoogle(t *testing.T) {
	var reached bool
	s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		fmt.Fprint(w, `{"messages":[]}`)
	})
	if _, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "("}); err == nil {
		t.Fatal("want an error")
	}
	if reached {
		t.Error("a request went out despite an invalid pattern")
	}
}

func TestSearchBoundsTheScanByTime(t *testing.T) {
	var filter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		filter = r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"messages":[]}`)
	})
	if _, err := s.SearchMessages(context.Background(), SearchMessagesInput{
		Space: "spaces/A", Regex: "x", CreatedAfter: "2026-01-02",
	}); err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if filter != `createTime > "2026-01-02T00:00:00.000000Z"` {
		t.Errorf("filter = %q", filter)
	}
}

// A model sends what it has. The strict form is what Google wants and
// what the schema documents; a bare date should still get the day it
// asked for rather than a schema violation it cannot see the shape of.
func TestATimestampArgumentAcceptsWhatAModelSends(t *testing.T) {
	for _, in := range []string{"2026-01-02T03:04:05Z", "2026-01-02T03:04:05.123Z", "2026-01-02", "2026-01-02T03:04:05"} {
		got, err := parseArgTime("since", in)
		if err != nil {
			t.Errorf("ParseTimestamp(%q) = %v", in, err)
			continue
		}
		if got.IsZero() {
			t.Errorf("ParseTimestamp(%q) is the zero time", in)
		}
	}
	if got, err := parseArgTime("since", "  "); err != nil || !got.IsZero() {
		t.Errorf("an empty value should mean no bound, got %v %v", got, err)
	}
	if _, err := parseArgTime("since", "last tuesday"); err == nil {
		t.Error("want an error naming the format")
	} else {
		assertClass(t, err, ClassInvalid)
	}
}

// Case folding is not rune-count-preserving: "İ" folds to two runes. An
// offset measured on a folded copy and applied to the original slides
// the snippet off the match, which is the one thing it exists to show.
func TestSnippetStaysOnTheMatchAfterAFoldedCharacter(t *testing.T) {
	body := strings.Repeat("İ", 100) + "needle" + strings.Repeat("x", 100)
	s := newService(t, ok(searchPage(message("1", body))))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "(?i)NEEDLE"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("matches = %+v", got.Matches)
	}
	if !strings.Contains(got.Matches[0].Snippet, "needle") {
		t.Errorf("snippet = %q, want the match in it", got.Matches[0].Snippet)
	}
}

// A search hit keeps its place for the same reason a listing row does.
func TestSearchKeepsAMatchMissingAField(t *testing.T) {
	page := `{"messages":[
	  {"name":"spaces/A/messages/1","createTime":"2026-01-02T03:04:05Z","text":"needle, no sender and no thread"}
	]}`
	s := newService(t, ok(page))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Space: "spaces/A", Regex: "needle"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("matches = %+v, want the hit kept", got.Matches)
	}
	if got.Unparsed != 0 {
		t.Errorf("unparsed = %d, want it reserved for a message that cannot be addressed", got.Unparsed)
	}
}

// Google's search is a POST whose filter this server builds. Every
// argument becomes one clause, and the shape of that expression is the
// only place the grammar is written down.
func TestUpstreamSearchBuildsGooglesFilter(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   SearchMessagesInput
		want string
	}{
		{"keywords are one phrase", SearchMessagesInput{Query: "pending reports"}, `"pending reports"`},
		{
			"a time window is two clauses",
			SearchMessagesInput{Query: "deploy", CreatedAfter: "2026-01-02", CreatedBefore: "2026-02-01"},
			`"deploy" AND create_time >= "2026-01-02T00:00:00Z" AND create_time < "2026-02-01T00:00:00Z"`,
		},
		{
			"a sender is named by address",
			SearchMessagesInput{Query: "x", SenderEmail: "janedoe@example.com"},
			`"x" AND sender.name = "users/janedoe@example.com"`,
		},
		{
			"the functions have no field of their own",
			SearchMessagesInput{Query: "x", HasAttachment: true, HasLink: true, UnreadOnly: true},
			`"x" AND attachment:* AND has_link() AND is_unread()`,
		},
		{"a filter alone is a search", SearchMessagesInput{UnreadOnly: true}, `is_unread()`},
		{
			// The space is a clause, not the parent: Google refuses a
			// single space as the parent outright, and says to use
			// space.name in the filter instead.
			"a space is a clause",
			SearchMessagesInput{Space: "spaces/AAAAspace1", Query: "deploy"},
			`space.name = "spaces/AAAAspace1" AND "deploy"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var filter, method, path string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				filter, method, path = r.URL.Query().Get("filter"), r.Method, r.URL.Path
				fmt.Fprint(w, `{"results":[]}`)
			})
			if _, err := s.SearchMessages(context.Background(), tc.in); err != nil {
				t.Fatalf("SearchMessages: %v", err)
			}
			if filter != tc.want {
				t.Errorf("filter = %q, want %q", filter, tc.want)
			}
			if method != http.MethodPost {
				t.Errorf("method = %s; Google models search as a POST", method)
			}
			if path != "/v1/spaces/-/messages:search" {
				t.Errorf("path = %q, want every accessible space", path)
			}
		})
	}
}

// A search with nothing to match would return the caller's whole
// history, and Google requires a filter anyway.
func TestUpstreamSearchNeedsSomethingToMatch(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an empty search must not reach Google")
	})
	_, err := s.SearchMessages(context.Background(), SearchMessagesInput{})
	assertClass(t, err, ClassInvalid)
}

// The grammar's escaping is undocumented, so a keyword that could
// change what the expression means is refused rather than guessed at.
func TestUpstreamSearchRefusesGrammarInAKeyword(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, q := range []string{`say "hello"`, `a AND (b)`, `back\slash`} {
		_, err := s.SearchMessages(context.Background(), SearchMessagesInput{Query: q})
		assertClass(t, err, ClassInvalid)
	}
}

// A page token is the only honest way to say there is more, and
// cap_reached is what a caller reads to know the answer is partial.
func TestUpstreamSearchReportsAFurtherPage(t *testing.T) {
	s := newService(t, ok(`{"results":[{"message":{"name":"spaces/AAAAspace1/messages/AAAAmsg1",
	  "text":"deploy went out","createTime":"2026-01-02T03:04:05Z",
	  "sender":{"name":"users/1"},"thread":{"name":"spaces/AAAAspace1/threads/AAAAthread1"}}}],
	  "nextPageToken":"tok"}`))
	got, err := s.SearchMessages(context.Background(), SearchMessagesInput{Query: "deploy"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if !got.Server {
		t.Error("the result does not say Google answered it")
	}
	if got.NextPageToken != "tok" || !got.CapReached {
		t.Errorf("token = %q, cap = %v; a further page is more than was returned",
			got.NextPageToken, got.CapReached)
	}
	if len(got.Matches) != 1 || got.Matches[0].SenderUserID != "users/1" {
		t.Errorf("matches = %+v", got.Matches)
	}
}

// is_unread() reads the caller's read state as well as their messages,
// and Google's refusal does not say which scope was declined.
func TestAnUnreadSearchNamesBothScopes(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.SearchMessages(context.Background(), SearchMessagesInput{UnreadOnly: true})
	assertClass(t, err, ClassScope)
	for _, want := range []string{scopes.MessagesReadonly, scopes.ReadStateReadonly} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message %q does not name %s", err, want)
		}
	}
}
