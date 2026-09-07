package gchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// recorder captures what a write actually sent, which is the whole
// point of these tests: the body and the query decide whether Google
// posts once, twice or refuses.
type recorder struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// recording answers every request with reply and records the last one.
func recording(t *testing.T, reply string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.Method, rec.Path, rec.Query, rec.Body = r.Method, r.URL.Path, r.URL.RawQuery, string(body)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestSendMessageCarriesAClientAssignedID(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/messages/BBB","thread":{"name":"spaces/AAA/threads/CCC"}}`)
	got, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if rec.Method != "POST" || rec.Path != "/v1/spaces/AAA/messages" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}
	if !strings.Contains(rec.Query, "messageId=client-") {
		t.Errorf("query = %q, want a client-assigned message id", rec.Query)
	}
	if strings.Contains(rec.Query, "messageReplyOption") {
		t.Errorf("query = %q, want no reply option on a new thread", rec.Query)
	}
	if rec.Body != `{"text":"hello"}` {
		t.Errorf("body = %s", rec.Body)
	}
	if got.Name != "spaces/AAA/messages/BBB" {
		t.Errorf("message = %+v", got)
	}
}

// A reply to a thread that has gone must fail rather than starting a
// new one: a message in the wrong place is worse than an error.
func TestSendMessageAsksGoogleToFailARemovedThread(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/messages/BBB"}`)
	_, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hi", "spaces/AAA/threads/CCC", ""), false, "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(rec.Query, "messageReplyOption=REPLY_MESSAGE_OR_FAIL") {
		t.Errorf("query = %q", rec.Query)
	}
	if !strings.Contains(rec.Body, `"thread":{"name":"spaces/AAA/threads/CCC"}`) {
		t.Errorf("body = %s", rec.Body)
	}
}

// The whole reason for the client-assigned id: a 5xx can arrive after
// Google created the message, and every attempt must carry the same id
// or the retry posts a second copy.
func TestSendMessageRetriesWithTheSameID(t *testing.T) {
	var calls atomic.Int32
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids = append(ids, r.URL.Query().Get("messageId"))
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"status":"INTERNAL","message":"oops"}}`)
			return
		}
		fmt.Fprint(w, `{"name":"spaces/AAA/messages/BBB"}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, ""); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("%d attempts, want a retry", len(ids))
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Errorf("message ids = %q, %q; a retry with a fresh id posts twice", ids[0], ids[1])
	}
}

// The recovery half: Google created the message on the attempt that
// looked like a failure, so the retry is refused and the message that
// landed is what the caller wanted.
func TestSendMessageReadsBackWhatAlreadyLanded(t *testing.T) {
	var posted, fetched string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posted = r.URL.Query().Get("messageId")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"status":"ALREADY_EXISTS","message":"message already exists"}}`)
			return
		}
		fetched = r.URL.Path
		fmt.Fprint(w, `{"name":"spaces/AAA/messages/BBB","text":"hello"}`)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, "")
	if err != nil {
		t.Fatalf("an already-existing message is this call's own work, not a failure: %v", err)
	}
	// The relationship is the invariant: whatever id was posted is the
	// one read back, because that is the message this call created.
	if want := "/v1/spaces/AAA/messages/" + posted; fetched != want {
		t.Errorf("fetched %q, want %q", fetched, want)
	}
	if got.Text != "hello" {
		t.Errorf("message = %+v", got)
	}
}

// A dropped connection on a write may mean the write landed, so a plain
// write is not retried. send_message is, because its client-assigned id
// makes the second attempt land as the same message or not at all.
func TestAnIdempotentWriteRetriesATransportFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("the test server cannot drop a connection")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		fmt.Fprint(w, `{"name":"spaces/AAA/messages/BBB"}`)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, ""); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d attempts, want the dropped connection retried", n)
	}
}

// A dry run must not be able to write, whatever the tool above forgot.
func TestAWriteIsRefusedWhenTheContextForbidsOne(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := WithoutWrites(context.Background())
	if err := c.DeleteMessage(ctx, "spaces/AAA/messages/BBB", false); !errors.Is(err, ErrWriteForbidden) {
		t.Errorf("error = %v, want the write refused", err)
	}
	if _, err := c.SendMessage(ctx, "spaces/AAA", BuildSendMessage("hello", "", ""), false, ""); !errors.Is(err, ErrWriteForbidden) {
		t.Errorf("error = %v, want the write refused", err)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("%d requests reached Google under a no-write context", n)
	}
	// Reads are untouched: move_space_to_section previews by looking up
	// where a space sits, which is a read.
	if _, err := c.ListSpaces(ctx, ListSpacesOptions{}); err != nil {
		t.Errorf("a read must still work: %v", err)
	}
}

// An empty 2xx body is a success with nothing in it. Reporting it as a
// parse failure tells the caller a write failed after it landed, and
// the retry that follows posts a second copy.
func TestAnEmptyBodyIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, "")
	if err != nil {
		t.Fatalf("an empty body must not fail a write that landed: %v", err)
	}
	if got.Name != "" {
		t.Errorf("message = %+v, want the zero value", got)
	}
}

func TestNewMessageIDMeetsGooglesRules(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := NewMessageID()
		switch {
		case !strings.HasPrefix(id, "client-"):
			t.Fatalf("id %q does not start with client-", id)
		case len(id) > 63:
			t.Fatalf("id %q is %d characters, above Google's 63", id, len(id))
		case seen[id]:
			t.Fatalf("id %q was generated twice", id)
		}
		for _, r := range id {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", r) {
				t.Fatalf("id %q holds %q, which Google refuses", id, r)
			}
		}
		seen[id] = true
	}
}

// An unmasked patch would clear the cards and attachments this server
// cannot rebuild.
func TestUpdateMessageMasksTextOnly(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/messages/BBB","text":"edited"}`)
	if _, err := newTestClient(t, srv).UpdateMessage(context.Background(),
		"spaces/AAA/messages/BBB", BuildUpdateMessage("edited")); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if rec.Method != "PATCH" || rec.Query != "updateMask=text" {
		t.Errorf("request = %s ?%s", rec.Method, rec.Query)
	}
	if rec.Body != `{"text":"edited"}` {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestDeleteMessageSendsADelete(t *testing.T) {
	srv, rec := recording(t, `{}`)
	if err := newTestClient(t, srv).DeleteMessage(context.Background(), "spaces/AAA/messages/BBB", false); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if rec.Method != "DELETE" || rec.Path != "/v1/spaces/AAA/messages/BBB" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}
}

// The builder renders what it is given and decides nothing. Whether a
// space type may carry a display name is an argument rule, and it lives
// in internal/service where a caller who breaks it is told so; dropping
// the field here would hide the mistake and create an unnamed space.
func TestBuildSetupSpaceRendersWhatItIsGiven(t *testing.T) {
	for _, kind := range []string{SpaceTypeSpace, SpaceTypeGroupChat, SpaceTypeDirectMessage} {
		t.Run(kind, func(t *testing.T) {
			body := BuildSetupSpace(kind, "Team", []string{"janedoe@example.com"})
			if body.Space.SpaceType != kind || body.Space.DisplayName != "Team" {
				t.Errorf("space = %+v", body.Space)
			}
			if len(body.Memberships) != 1 || body.Memberships[0].Member.Name != "users/janedoe@example.com" {
				t.Errorf("memberships = %+v", body.Memberships)
			}
			if body.Memberships[0].Member.Type != MemberTypeHuman {
				t.Errorf("member type = %q", body.Memberships[0].Member.Type)
			}
		})
	}
}

// A space with no name sends no displayName at all, which is what a
// direct message and a group chat need.
func TestBuildSetupSpaceOmitsAnEmptyDisplayName(t *testing.T) {
	raw, err := json.Marshal(BuildSetupSpace(SpaceTypeDirectMessage, "", []string{"janedoe@example.com"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "displayName") {
		t.Errorf("body = %s, want no display name", raw)
	}
}

// Google's mask takes only top-level paths, so a description edit masks
// the whole spaceDetails object.
func TestBuildUpdateSpaceMasksWhatItSends(t *testing.T) {
	name, description := "Team", ""
	for _, tc := range []struct {
		label       string
		displayName *string
		description *string
		wantMask    string
		wantBody    string
	}{
		{"name only", &name, nil, "displayName", `{"displayName":"Team"}`},
		{"description only", nil, &description, "spaceDetails", `{"spaceDetails":{"description":""}}`},
		{"both", &name, &description, "displayName,spaceDetails", `{"displayName":"Team","spaceDetails":{"description":""}}`},
	} {
		t.Run(tc.label, func(t *testing.T) {
			body, mask := BuildUpdateSpace(tc.displayName, tc.description)
			if mask != tc.wantMask {
				t.Errorf("mask = %q, want %q", mask, tc.wantMask)
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != tc.wantBody {
				t.Errorf("body = %s, want %s", raw, tc.wantBody)
			}
		})
	}
}

// Clearing a description is a real edit. An omitempty here would send
// an empty object and change nothing.
func TestAnEmptyDescriptionIsStillSent(t *testing.T) {
	empty := ""
	body, _ := BuildUpdateSpace(nil, &empty)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"description":""`) {
		t.Errorf("body = %s, want the empty description sent", raw)
	}
}

func TestAddMemberPostsTheEmailAsAResourceName(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/members/MMM"}`)
	got, err := newTestClient(t, srv).AddMember(context.Background(), "spaces/AAA",
		BuildAddMember("janedoe@example.com", ""))
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if rec.Path != "/v1/spaces/AAA/members" {
		t.Errorf("path = %q", rec.Path)
	}
	if rec.Body != `{"member":{"name":"users/janedoe@example.com","type":"HUMAN"}}` {
		t.Errorf("body = %s", rec.Body)
	}
	if got.Name != "spaces/AAA/members/MMM" {
		t.Errorf("membership = %+v", got)
	}
}

func TestAddReactionPostsTheEmoji(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/messages/BBB/reactions/RRR","emoji":{"unicode":"👍"}}`)
	got, err := newTestClient(t, srv).AddReaction(context.Background(),
		"spaces/AAA/messages/BBB", BuildAddReaction("👍"))
	if err != nil {
		t.Fatalf("AddReaction: %v", err)
	}
	if rec.Path != "/v1/spaces/AAA/messages/BBB/reactions" {
		t.Errorf("path = %q", rec.Path)
	}
	if rec.Body != `{"emoji":{"unicode":"👍"}}` {
		t.Errorf("body = %s", rec.Body)
	}
	if got.Emoji == nil || got.Emoji.Unicode != "👍" {
		t.Errorf("reaction = %+v", got)
	}
}

func TestCreateSectionAddressesTheCallersOwnSidebar(t *testing.T) {
	srv, rec := recording(t, `{"name":"users/123/sections/SSS","displayName":"Clients"}`)
	if _, err := newTestClient(t, srv).CreateSection(context.Background(),
		BuildCreateSection("Clients")); err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	if rec.Path != "/v1/users/me/sections" {
		t.Errorf("path = %q, want the caller's own sidebar", rec.Path)
	}
	if rec.Body != `{"displayName":"Clients","type":"CUSTOM_SECTION"}` {
		t.Errorf("body = %s", rec.Body)
	}
}

// A rename masks displayName and sends no type: the create-only field
// would be refused on a patch.
func TestRenameSectionMasksTheNameAndSendsNoType(t *testing.T) {
	srv, rec := recording(t, `{"name":"users/123/sections/SSS","displayName":"Clients"}`)
	if _, err := newTestClient(t, srv).RenameSection(context.Background(),
		"users/123/sections/SSS", BuildRenameSection("Clients")); err != nil {
		t.Fatalf("RenameSection: %v", err)
	}
	if rec.Query != "updateMask=displayName" {
		t.Errorf("query = %q", rec.Query)
	}
	if rec.Body != `{"displayName":"Clients"}` {
		t.Errorf("body = %s", rec.Body)
	}
}

// The action verb is appended after the name is escaped. Escaping the
// whole path would percent-encode the colon and address a section
// called "SSS:position".
func TestTheActionVerbSurvivesEscaping(t *testing.T) {
	t.Run("position", func(t *testing.T) {
		srv, rec := recording(t, `{"section":{"name":"users/123/sections/SSS","sortOrder":2}}`)
		order := 2
		got, err := newTestClient(t, srv).PositionSection(context.Background(),
			"users/123/sections/SSS", &PositionSectionRequest{SortOrder: &order})
		if err != nil {
			t.Fatalf("PositionSection: %v", err)
		}
		if rec.Path != "/v1/users/123/sections/SSS:position" {
			t.Errorf("path = %q", rec.Path)
		}
		if rec.Body != `{"sortOrder":2}` {
			t.Errorf("body = %s", rec.Body)
		}
		if got.Section == nil || got.Section.SortOrder == nil || *got.Section.SortOrder != 2 {
			t.Errorf("response = %+v", got.Section)
		}
	})

	t.Run("move", func(t *testing.T) {
		srv, rec := recording(t, `{"sectionItem":{"name":"users/123/sections/TTT/items/spaces/AAA"}}`)
		got, err := newTestClient(t, srv).MoveSectionItem(context.Background(),
			"users/123/sections/SSS/items/III", BuildMoveSectionItem("users/123/sections/TTT"))
		if err != nil {
			t.Fatalf("MoveSectionItem: %v", err)
		}
		if rec.Path != "/v1/users/123/sections/SSS/items/III:move" {
			t.Errorf("path = %q", rec.Path)
		}
		if rec.Body != `{"targetSection":"users/123/sections/TTT"}` {
			t.Errorf("body = %s", rec.Body)
		}
		if got.SectionItem == nil {
			t.Fatal("the moved item should come back")
		}
	})

	// Both shapes a section item id really takes. A base64url id keeps
	// its padding and a name that spells the space out keeps its
	// separator: escaping either one differently addresses a resource
	// that is not there.
	t.Run("item id shapes", func(t *testing.T) {
		for _, item := range []string{
			"users/123/sections/SSS/items/spaces/AAA",
			"users/123/sections/SSS/items/c3BhY2VzL0FBQQ==",
		} {
			srv, rec := recording(t, `{"sectionItem":{"name":"users/123/sections/TTT/items/III"}}`)
			if _, err := newTestClient(t, srv).MoveSectionItem(context.Background(), item,
				BuildMoveSectionItem("users/123/sections/TTT")); err != nil {
				t.Fatalf("MoveSectionItem: %v", err)
			}
			if want := "/v1/" + item + ":move"; rec.Path != want {
				t.Errorf("path = %q, want %q", rec.Path, want)
			}
		}
	})
}

// Exactly one positioning field reaches Google: the two are a union
// upstream and both together is a 400.
func TestPositionSectionSendsOneFieldAtATime(t *testing.T) {
	srv, rec := recording(t, `{}`)
	if _, err := newTestClient(t, srv).PositionSection(context.Background(),
		"users/123/sections/SSS", &PositionSectionRequest{RelativePosition: "START"}); err != nil {
		t.Fatalf("PositionSection: %v", err)
	}
	if rec.Body != `{"relativePosition":"START"}` {
		t.Errorf("body = %s", rec.Body)
	}
}

// A transport failure on a create is never repeated: the request may
// have landed and the answer been lost, and repeating it would make a
// second space. A delete is repeated, because deleting twice lands where
// deleting once did — the old rule refused that too, so a delete whose
// response was dropped reported a failure for work that had been done.
func TestATransportFailureRepeatsOnlyWhatIsSafeToRepeat(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server cannot drop a connection")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	c := newTestClient(t, srv)

	// A create carries no key, so one attempt and no more.
	if _, err := c.SetupSpace(context.Background(),
		BuildSetupSpace(SpaceTypeSpace, "Team", []string{"janedoe@example.com"})); err == nil {
		t.Fatal("a dropped connection should be reported")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("a create was attempted %d times, want exactly one", n)
	}

	// A delete names a fixed target, so a lost answer is worth another go.
	calls.Store(0)
	if err := c.DeleteMessage(context.Background(), "spaces/AAAAspace1/messages/AAAAmsg1", false); err == nil {
		t.Fatal("a dropped connection should be reported")
	}
	if n := calls.Load(); n < 2 {
		t.Errorf("a delete was attempted %d times; a lost answer should be retried", n)
	}
}

// A write with no idempotency key may only be retried on an answer that
// means Google turned the request away. "Google answered" is not "Google
// did not do it": a 500 can follow a commit and a 504 comes from a
// gateway that never learned what the backend did, so retrying a create
// on either makes two spaces.
func TestANonIdempotentWriteIsNotRetriedOnAnAmbiguous5xx(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantCalls int
		why       string
	}{
		{http.StatusInternalServerError, 1, "a 500 can arrive after the write landed"},
		{http.StatusBadGateway, 1, "a 502 comes from a gateway that saw no outcome"},
		{http.StatusGatewayTimeout, 1, "a 504 is the classic post-commit timeout"},
		{http.StatusTooManyRequests, 4, "a 429 is a refusal to start"},
		{http.StatusServiceUnavailable, 4, "a 503 means Google is not serving this"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"status":"INTERNAL"}}`)
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			// spaces:setup carries no client-chosen id, so a repeat is a
			// second space.
			_, _ = c.SetupSpace(context.Background(),
				BuildSetupSpace(SpaceTypeSpace, "Team", []string{"janedoe@example.com"}))
			if calls != tc.wantCalls {
				t.Errorf("%d attempts for %d, want %d: %s", calls, tc.status, tc.wantCalls, tc.why)
			}
		})
	}
}

// send_message still gets the wider set, because its client-chosen id is
// what makes a repeat safe: Google refuses the second copy.
func TestAnIdempotentWriteStillRetriesOnA500(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"status":"INTERNAL"}}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, _ = c.SendMessage(context.Background(), "spaces/AAAAspace1", BuildSendMessage("hello", "", ""), false, "")
	if calls < 2 {
		t.Errorf("%d attempts; a write carrying a message id should still retry a 500", calls)
	}
}

// The dry-run guarantee must not rest on a field someone remembers to
// set. Every non-GET is a write by its method, so a request added later
// cannot slip past the guard, take the read limiter, or retry like a
// read — which is what a missed `write: true` used to do, and the first
// of those three would have written during a preview.
func TestEveryNonGETIsAWriteByItsMethod(t *testing.T) {
	for _, tc := range []struct {
		method string
		write  bool
		repeat bool
	}{
		{http.MethodGet, false, true},
		{http.MethodPost, true, false},
		{http.MethodPatch, true, true},
		{http.MethodPut, true, true},
		{http.MethodDelete, true, true},
	} {
		t.Run(tc.method, func(t *testing.T) {
			r := request{method: tc.method}
			if r.isWrite() != tc.write {
				t.Errorf("isWrite = %t, want %t", r.isWrite(), tc.write)
			}
			if r.safeToRepeat() != tc.repeat {
				t.Errorf("safeToRepeat = %t, want %t", r.safeToRepeat(), tc.repeat)
			}
		})
	}
	// A POST becomes repeatable only by declaring the key that makes it so.
	if !(request{method: http.MethodPost, idempotent: true}).safeToRepeat() {
		t.Error("a POST carrying a client-chosen id should be repeatable")
	}
}

// The guard that makes a dry run structural, checked through the method
// rather than through a flag.
func TestADryRunCannotWriteWhateverTheMethod(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		var reached bool
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
		c := newTestClient(t, srv)
		err := c.do(WithoutWrites(context.Background()), request{method: method, path: "spaces/AAAAspace1"}, nil)
		srv.Close()
		if !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s under a dry run: %v, want it refused", method, err)
		}
		if reached {
			t.Errorf("%s reached Google during a dry run", method)
		}
	}
}

// A caller's own id is what makes a retry from OUTSIDE this server safe.
// The minted one only covers retries inside a single call, which is why
// the tool used to have to tell the model that calling again might post
// twice.
func TestSendMessageUsesTheCallersIDWhenGiven(t *testing.T) {
	srv, rec := recording(t, `{"name":"spaces/AAA/messages/BBB"}`)
	_, err := newTestClient(t, srv).SendMessage(context.Background(), "spaces/AAA",
		BuildSendMessage("hello", "", ""), false, "client-abc123")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(rec.Query, "messageId=client-abc123") {
		t.Errorf("query = %q, want the id the caller supplied", rec.Query)
	}
}

func TestValidMessageIDFollowsGooglesRules(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{id: "client-abc123", want: true},
		{id: "client-a-b-c", want: true},
		{id: "abc123"},                                        // no client- prefix
		{id: "client-ABC"},                                    // upper case
		{id: "client-a_b"},                                    // underscore
		{id: "client-a b"},                                    // space
		{id: "client-" + strings.Repeat("a", 57)},             // 64 characters
		{id: "client-" + strings.Repeat("a", 56), want: true}, // 63, the limit
		{id: ""},
	} {
		if got := ValidMessageID(tc.id); got != tc.want {
			t.Errorf("ValidMessageID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
