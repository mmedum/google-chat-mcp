package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// twoMessages is a page from two senders, one of whom the People API
// will refuse to resolve.
const twoMessages = `{"messages":[
  {"name":"spaces/A/messages/1","sender":{"name":"users/1","displayName":"Jane Doe"},
   "createTime":"2026-01-02T03:04:05.000000Z","text":"first","thread":{"name":"spaces/A/threads/T1"}},
  {"name":"spaces/A/messages/2","sender":{"name":"users/2","displayName":"John Doe"},
   "createTime":"2026-01-02T03:05:05.000000Z","text":"second","thread":{"name":"spaces/A/threads/T2"}}
]}`

// route sends Chat and People calls to different handlers, which is
// what a message listing needs: it reads a page and then enriches it.
func route(chat, people http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/people") {
			people(w, r)
			return
		}
		chat(w, r)
	}
}

func ok(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}
}

// people answers a batch lookup with the same person for everyone it
// was asked about, echoing the resource names back the way Google does.
func people(email, displayName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := make([]string, 0, 4)
		for _, name := range r.URL.Query()["resourceNames"] {
			entries = append(entries, fmt.Sprintf(
				`{"requestedResourceName":%q,"person":{"emailAddresses":[{"value":%q}],"names":[{"displayName":%q}]}}`,
				name, email, displayName))
		}
		fmt.Fprintf(w, `{"responses":[%s]}`, strings.Join(entries, ","))
	}
}

// nobody answers a batch lookup with a status and no profile for
// everyone, which is what Google sends for someone outside the
// caller's directory.
func nobody() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := make([]string, 0, 4)
		for _, name := range r.URL.Query()["resourceNames"] {
			entries = append(entries, fmt.Sprintf(`{"requestedResourceName":%q,"status":{"code":5}}`, name))
		}
		fmt.Fprintf(w, `{"responses":[%s]}`, strings.Join(entries, ","))
	}
}

func TestGetMessagesResolvesSenders(t *testing.T) {
	s := newService(t, route(ok(twoMessages), people("janedoe@example.com", "Jane D.")))
	got, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].SenderEmail != "janedoe@example.com" {
		t.Errorf("email = %q", got[0].SenderEmail)
	}
	// The People name wins over the one on the message: it is the
	// person's own profile rather than what Chat cached.
	if got[0].SenderDisplayName != "Jane D." {
		t.Errorf("display name = %q", got[0].SenderDisplayName)
	}
	if got[0].CreateTime.UTC().Format(time.RFC3339) != "2026-01-02T03:04:05Z" {
		t.Errorf("timestamp = %s", got[0].CreateTime)
	}
	if got[0].ThreadName != "spaces/A/threads/T1" {
		t.Errorf("thread = %q", got[0].ThreadName)
	}
}

// The rule this whole layer exists for. A People API refusal used to
// empty the list, and a model reads an empty list as "the space is
// empty" rather than "lookups are broken".
func TestPeopleFailuresNeverEmptyAListing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		people http.HandlerFunc
	}{
		{"scope never granted", status(http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)},
		{"quota", status(http.StatusTooManyRequests, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`)},
		{"google is unwell", status(http.StatusInternalServerError, `{"error":{"status":"INTERNAL"}}`)},
		{"not in the directory", nobody()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, route(ok(twoMessages), tc.people))
			got, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A"})
			if err != nil {
				t.Fatalf("a People failure must not fail the read: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d messages, want both of them", len(got))
			}
			if got[0].SenderEmail != "" {
				t.Errorf("email = %q, want it left empty", got[0].SenderEmail)
			}
			// The name Chat already sent with the message survives, so
			// the caller still knows who said it.
			if got[0].SenderDisplayName != "Jane Doe" {
				t.Errorf("display name = %q, want the one from the message", got[0].SenderDisplayName)
			}
		})
	}
}

// A message keeps its place as long as it can be addressed. Google
// renaming a field must not shorten the list: a short list reads
// exactly like a quiet conversation, and these tools have no field in
// which to say otherwise.
func TestAFieldGoogleStopsSendingDoesNotCostARow(t *testing.T) {
	page := `{"messages":[
	  {"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T1"},"text":"whole"},
	  {"name":"spaces/A/messages/2","createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T1"},"text":"no sender"},
	  {"name":"spaces/A/messages/3","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","text":"no thread"},
	  {"name":"spaces/A/messages/4","sender":{"name":"users/1"},"createTime":"not a time","thread":{"name":"spaces/A/threads/T1"},"text":"no timestamp"}
	]}`
	s := newService(t, route(ok(page), nobody()))
	got, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d messages, want all four kept: %+v", len(got), got)
	}
	if got[1].SenderUserID != "" || got[2].ThreadName != "" || !got[3].CreateTime.IsZero() {
		t.Errorf("the missing field should arrive empty, not shift the row: %+v", got)
	}
	for i, row := range got {
		if row.Text == "" {
			t.Errorf("row %d lost its text: %+v", i, row)
		}
	}
}

// A message with no resource name is the one that cannot be kept: there
// is nothing to return for it and nothing to follow it up with.
func TestAMessageWithNoNameIsDropped(t *testing.T) {
	page := `{"messages":[
	  {"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T1"},"text":"kept"},
	  {"sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","thread":{"name":"spaces/A/threads/T1"},"text":"unaddressable"}
	]}`
	s := newService(t, route(ok(page), nobody()))
	got, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 1 || got[0].Text != "kept" {
		t.Errorf("messages = %+v, want only the addressable row", got)
	}
}

func TestGetMessagesSendsTheRightQuery(t *testing.T) {
	var query, path string
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		query, path = r.URL.RawQuery, r.URL.Path
		fmt.Fprint(w, `{"messages":[]}`)
	}, nobody()))
	if _, err := s.GetMessages(context.Background(), GetMessagesInput{
		Space: "spaces/A", Since: "2026-01-02T03:04:05Z", Limit: 7,
	}); err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if !strings.HasSuffix(path, "/spaces/A/messages") {
		t.Errorf("path = %q", path)
	}
	for _, want := range []string{"orderBy=createTime+desc", "pageSize=7", `createTime+%3E+%222026-01-02T03%3A04%3A05.000000Z%22`} {
		if !strings.Contains(query, want) {
			t.Errorf("query = %q, want it to contain %q", query, want)
		}
	}
}

func TestGetMessagesDefaultsAndBounds(t *testing.T) {
	var size string
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		size = r.URL.Query().Get("pageSize")
		fmt.Fprint(w, `{"messages":[]}`)
	}, nobody()))
	if _, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if size != "20" {
		t.Errorf("page size = %q, want the documented default 20", size)
	}
	_, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "spaces/A", Limit: 500})
	assertClass(t, err, ClassInvalid)
}

// A bare id is what a model tends to send back after seeing a resource
// name once, and it costs nothing to accept.
func TestSpaceArgumentAcceptsABareID(t *testing.T) {
	var path string
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"messages":[]}`)
	}, nobody()))
	if _, err := s.GetMessages(context.Background(), GetMessagesInput{Space: "AAAA"}); err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if !strings.HasSuffix(path, "/spaces/AAAA/messages") {
		t.Errorf("path = %q", path)
	}
}

func TestGetMessagesRejectsAMissingSpace(t *testing.T) {
	s := newService(t, ok(`{"messages":[]}`))
	_, err := s.GetMessages(context.Background(), GetMessagesInput{})
	assertClass(t, err, ClassInvalid)
}

func TestGetThreadReadsInOrder(t *testing.T) {
	var query string
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, twoMessages)
	}, nobody()))
	got, err := s.GetThread(context.Background(), GetThreadInput{
		Space: "spaces/A", Thread: "spaces/A/threads/T1",
	})
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages", len(got))
	}
	if !strings.Contains(query, "orderBy=createTime+asc") {
		t.Errorf("query = %q, want reading order", query)
	}
	if !strings.Contains(query, "thread.name") {
		t.Errorf("query = %q, want the thread filter", query)
	}
	if !strings.Contains(query, "pageSize=50") {
		t.Errorf("query = %q, want the documented default of 50", query)
	}
}

// Google answers 400 when the thread belongs to another space, and its
// message says nothing useful. Catching it here names the argument.
func TestGetThreadChecksTheThreadBelongsToTheSpace(t *testing.T) {
	s := newService(t, ok(`{"messages":[]}`))
	for _, tc := range []struct {
		name string
		in   GetThreadInput
	}{
		{"no thread", GetThreadInput{Space: "spaces/A"}},
		{"another space's thread", GetThreadInput{Space: "spaces/A", Thread: "spaces/B/threads/T1"}},
		{"not a thread name", GetThreadInput{Space: "spaces/A", Thread: "T1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.GetThread(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
}

const oneMessage = `{"name":"spaces/A/messages/1","sender":{"name":"users/1","displayName":"Jane Doe"},
  "createTime":"2026-01-02T03:04:05Z","lastUpdateTime":"2026-01-02T04:00:00Z","text":"hello",
  "thread":{"name":"spaces/A/threads/T1"},
  "emojiReactionSummaries":[{"emoji":{"unicode":"👍"},"reactionCount":2},{"emoji":{"customEmoji":{"uid":"x"}},"reactionCount":1}]}`

func TestGetMessageInlinesReactions(t *testing.T) {
	s := newService(t, route(ok(oneMessage), people("janedoe@example.com", "")))
	got, err := s.GetMessage(context.Background(), "spaces/A/messages/1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Space != "spaces/A" {
		t.Errorf("space = %q, want it sliced off the message name", got.Space)
	}
	if len(got.Reactions) != 1 || got.Reactions[0].Emoji != "👍" || got.Reactions[0].Count != 2 {
		t.Errorf("reactions = %+v, want the unicode one only", got.Reactions)
	}
	if got.ReactionsPaged {
		t.Error("two reactions is not too many to inline")
	}
	if got.LastUpdateTime.IsZero() {
		t.Error("an edited message should carry its edit time")
	}
	if got.SenderEmail != "janedoe@example.com" {
		t.Errorf("email = %q", got.SenderEmail)
	}
}

// A message with more distinct emoji than fit inline says so, rather
// than returning a truncated list the caller would read as complete.
func TestGetMessageReportsTooManyReactions(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z",
	  "thread":{"name":"spaces/A/threads/T1"},"emojiReactionSummaries":[`)
	for i := range 30 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"emoji":{"unicode":"%c"},"reactionCount":1}`, rune('a'+i))
	}
	b.WriteString("]}")

	s := newService(t, route(ok(b.String()), nobody()))
	got, err := s.GetMessage(context.Background(), "spaces/A/messages/1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !got.ReactionsPaged {
		t.Error("reactions_paged should be set when the summaries were left out")
	}
	if len(got.Reactions) != 0 {
		t.Errorf("reactions = %+v, want none inline", got.Reactions)
	}
}

func TestGetMessageRejectsAName(t *testing.T) {
	s := newService(t, ok(`{}`))
	for _, name := range []string{"", "spaces/A", "spaces/A/threads/T1"} {
		_, err := s.GetMessage(context.Background(), name)
		assertClass(t, err, ClassInvalid)
	}
}

// assertClass fails unless err is a service error of the given class.
// assertScope checks which scope a refusal told the caller to grant.
//
// It fails before reading the error, rather than inside the message: a
// regression that returns a plain error would otherwise dereference nil
// and take the whole package's test binary down with a panic instead of
// naming the tool that broke.
func assertScope(t *testing.T, err error, want string) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a service error naming %s", err, want)
	}
	if se.Scope != want {
		t.Errorf("scope = %q, want %q (%v)", se.Scope, want, err)
	}
}

func assertClass(t *testing.T, err error, want Class) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want a service error", err)
	}
	if se.Class != want {
		t.Fatalf("class = %q, want %q (%v)", se.Class, want, err)
	}
}

// The body is posted exactly as it arrived. A server that trimmed,
// prefixed or suffixed a message would be editing what somebody said.
func TestSendMessagePostsTheTextVerbatim(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1","thread":{"name":"spaces/A/threads/T"}}`))
	body := "  hello\n\nworld  "
	got, err := s.SendMessage(context.Background(), SendMessageInput{Space: "spaces/A", Text: body})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := rec.last(t)
	var payload struct {
		Text   string `json:"text"`
		Thread *struct {
			Name string `json:"name"`
		} `json:"thread"`
	}
	if err := json.Unmarshal([]byte(sent.Body), &payload); err != nil {
		t.Fatalf("decode what was sent: %v", err)
	}
	if payload.Text != body {
		t.Errorf("posted %q, want the text unchanged", payload.Text)
	}
	if payload.Thread != nil {
		t.Errorf("thread = %+v, want none on a new thread", payload.Thread)
	}
	if got.Name != "spaces/A/messages/1" || got.Thread != "spaces/A/threads/T" {
		t.Errorf("result = %+v", got)
	}
}

func TestSendMessageRepliesInAThread(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1","thread":{"name":"spaces/A/threads/T"}}`))
	if _, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/A", Text: "hi", Thread: "spaces/A/threads/T",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(rec.last(t).Body, `"thread":{"name":"spaces/A/threads/T"}`) {
		t.Errorf("body = %s", rec.last(t).Body)
	}
}

// A dry run is a preview, so nothing may reach Google, and the body it
// shows has to be the body a real post would send.
func TestSendMessageDryRunPostsNothing(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1"}`))
	got, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/A", Text: "hello", Thread: "spaces/A/threads/T", DryRun: true,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if !got.DryRun || got.Name != "" || got.Thread != "" {
		t.Errorf("result = %+v, want nothing created", got)
	}
	if got.Rendered["text"] != "hello" {
		t.Errorf("rendered = %v, want the body that would be posted", got.Rendered)
	}
	thread, _ := got.Rendered["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("rendered thread = %v", got.Rendered["thread"])
	}
}

func TestSendMessageRejectsBadInput(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1"}`))
	for _, tc := range []struct {
		name string
		in   SendMessageInput
	}{
		{"no space", SendMessageInput{Text: "hi"}},
		{"no text", SendMessageInput{Space: "spaces/A"}},
		{"text above the cap", SendMessageInput{Space: "spaces/A", Text: strings.Repeat("x", maxMessageText+1)}},
		{"thread in another space", SendMessageInput{Space: "spaces/A", Text: "hi", Thread: "spaces/B/threads/T"}},
		{"thread that is not a thread", SendMessageInput{Space: "spaces/A", Text: "hi", Thread: "spaces/A/messages/1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SendMessage(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// The cap counts characters, not bytes: it is what the schema
// documents and what a person writing a message counts.
func TestMessageLengthIsCountedInCharacters(t *testing.T) {
	s, _ := recorded(t, ok(`{"name":"spaces/A/messages/1"}`))
	// Four bytes each, so a byte count would reject this at a quarter
	// of the documented limit.
	if _, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/A", Text: strings.Repeat("😀", maxMessageText),
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestUpdateMessageEditsTheText(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1","text":"edited"}`))
	got, err := s.UpdateMessage(context.Background(), UpdateMessageInput{
		Message: "spaces/A/messages/1", Text: "edited",
	})
	if err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if sent := rec.last(t); sent.Method != "PATCH" || sent.Query != "updateMask=text" {
		t.Errorf("request = %s ?%s", sent.Method, sent.Query)
	}
	if got.Name != "spaces/A/messages/1" || got.Text != "edited" {
		t.Errorf("result = %+v", got)
	}
}

// The patch landed. A response missing the text means Google renamed
// the field, not that the message is now empty.
func TestUpdateMessageEchoesWhatItAskedForWhenGoogleSaysNothing(t *testing.T) {
	s, _ := recorded(t, ok(`{"name":"spaces/A/messages/1"}`))
	got, err := s.UpdateMessage(context.Background(), UpdateMessageInput{
		Message: "spaces/A/messages/1", Text: "edited",
	})
	if err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if got.Text != "edited" {
		t.Errorf("text = %q, want the edit that was applied", got.Text)
	}
}

func TestUpdateMessageDryRunChangesNothing(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.UpdateMessage(context.Background(), UpdateMessageInput{
		Message: "spaces/A/messages/1", Text: "edited", DryRun: true,
	})
	if err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if !got.DryRun || got.Rendered["text"] != "edited" {
		t.Errorf("result = %+v", got)
	}
}

func TestUpdateMessageRejectsBadInput(t *testing.T) {
	s, _ := recorded(t, ok(`{}`))
	for _, in := range []UpdateMessageInput{
		{Text: "hi"},
		{Message: "spaces/A", Text: "hi"},
		{Message: "spaces/A/messages/1"},
	} {
		_, err := s.UpdateMessage(context.Background(), in)
		assertClass(t, err, ClassInvalid)
	}
}

// A message that is already gone is the state the caller asked for, so
// a repeat is a success that changed nothing.
func TestDeleteMessageIsIdempotent(t *testing.T) {
	s := newService(t, status(404, `{"error":{"status":"NOT_FOUND","message":"gone"}}`))
	got, err := s.DeleteMessage(context.Background(), DeleteMessageInput{Message: "spaces/A/messages/1"})
	if err != nil {
		t.Fatalf("a second delete is not a failure: %v", err)
	}
	if got.Deleted {
		t.Error("deleted = true, want false for something that was already gone")
	}
}

// Google spells "already deleted, and this space keeps no history" and
// "you may not delete someone else's message" as the same 403, so the
// refusal path reads the message back. Telling a caller their delete
// found nothing, when the message is still sitting there, is the one
// answer this must not give.
func TestDeleteMessageChecksWhatARefusalMeant(t *testing.T) {
	for _, tc := range []struct {
		name        string
		read        http.HandlerFunc
		wantDeleted bool
		wantErr     Class
	}{
		{
			name: "the message is still there, so the refusal stands",
			read: ok(`{"name":"spaces/A/messages/1","text":"still here"}`),
			// A refusal that stands is a permission problem, not an
			// unclassified upstream failure: retrying will not fix it,
			// and someone who administers the space can.
			wantErr: ClassForbidden,
		},
		{
			name: "the message really is gone",
			read: status(404, `{"error":{"status":"NOT_FOUND","message":"gone"}}`),
		},
		{
			// What Google actually does, found by deleting a message
			// twice against a live account: the name and the timestamp
			// survive, the text and the sender do not.
			name: "a tombstone, which is how Google reports a deleted message",
			read: ok(`{"name":"spaces/A/messages/1","createTime":"2026-01-02T03:04:05Z",
			           "deleteTime":"2026-01-02T04:00:00Z","deletionMetadata":{"deletionType":"CREATOR"}}`),
		},
		{
			name: "not readable either, so nothing is claimed",
			read: status(403, `{"error":{"status":"PERMISSION_DENIED","message":"no access"}}`),
			// Unreadable leaves the question open, and an open question
			// means the caller hears the refusal rather than a report
			// that the message was already gone.
			wantErr: ClassForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"no access"}}`)
					return
				}
				tc.read(w, r)
			})
			got, err := s.DeleteMessage(context.Background(), DeleteMessageInput{Message: "spaces/A/messages/1"})
			if tc.wantErr != "" {
				assertClass(t, err, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("DeleteMessage: %v", err)
			}
			if got.Deleted != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v", got.Deleted, tc.wantDeleted)
			}
		})
	}
}

// The one 403 that must not read as "already gone": the caller was told
// the delete succeeded when what they needed was a prompt to grant a
// scope.
func TestDeleteMessageDoesNotSwallowAMissingScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.DeleteMessage(context.Background(), DeleteMessageInput{Message: "spaces/A/messages/1"})
	assertClass(t, err, ClassScope)
}

func TestDeleteMessageDeletes(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.DeleteMessage(context.Background(), DeleteMessageInput{Message: "spaces/A/messages/1"})
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if !got.Deleted {
		t.Error("deleted = false after a delete that worked")
	}
	if sent := rec.last(t); sent.Method != "DELETE" {
		t.Errorf("method = %s", sent.Method)
	}
}

func TestDeleteMessageDryRunDeletesNothing(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.DeleteMessage(context.Background(), DeleteMessageInput{
		Message: "spaces/A/messages/1", DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if got.Deleted || !got.DryRun {
		t.Errorf("result = %+v", got)
	}
}

// Editing and deleting need the restricted-tier umbrella, and the
// person has to be told which string to grant.
func TestMessageWritesNameTheirScopes(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{"send", func() error {
			_, err := s.SendMessage(context.Background(), SendMessageInput{Space: "spaces/A", Text: "hi"})
			return err
		}, scopes.MessagesCreate},
		{"update", func() error {
			_, err := s.UpdateMessage(context.Background(), UpdateMessageInput{Message: "spaces/A/messages/1", Text: "hi"})
			return err
		}, scopes.Messages},
		{"delete", func() error {
			_, err := s.DeleteMessage(context.Background(), DeleteMessageInput{Message: "spaces/A/messages/1"})
			return err
		}, scopes.Messages},
	} {
		t.Run(tc.name, func(t *testing.T) { assertScope(t, tc.run(), tc.want) })
	}
}

// A blank thread_name used to be read as "no thread", so a caller who
// asked to reply got a new top-level message in the space instead. Any
// non-empty value is checked.
func TestSendMessageRefusesABlankThreadRatherThanStartingANewOne(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/messages/1"}`))
	_, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/A", Text: "hi", Thread: "   ",
	})
	assertClass(t, err, ClassInvalid)
	if rec.len() != 0 {
		t.Error("the message was posted with no thread")
	}
}

// The repeat delete a caller actually makes. Google answers the second
// one with a 403, and the message reads back as a tombstone rather than
// a 404 — so treating "readable" as "still there" reported an ordinary
// idempotent delete as a refusal. Found in the Phase 2 smoke.
func TestDeletingTwiceIsNotAnError(t *testing.T) {
	var deletes int
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && deletes == 0:
			deletes++
			fmt.Fprint(w, `{}`)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Permission denied to perform the requested action on the specified resource, or the resource doesn't exist."}}`)
		default:
			fmt.Fprint(w, `{"name":"spaces/A/messages/1","createTime":"2026-01-02T03:04:05Z","deleteTime":"2026-01-02T04:00:00Z"}`)
		}
	})
	in := DeleteMessageInput{Message: "spaces/A/messages/1"}

	first, err := s.DeleteMessage(context.Background(), in)
	if err != nil || !first.Deleted {
		t.Fatalf("first delete = %+v, %v", first, err)
	}
	second, err := s.DeleteMessage(context.Background(), in)
	if err != nil {
		t.Fatalf("a repeat delete must not fail: %v", err)
	}
	if second.Deleted {
		t.Error("the second delete claims to have deleted something")
	}
}

// The replies to a message are other people's, so Google refuses to
// delete a message that has any unless the caller says otherwise. The
// flag has to reach Google, and the answer has to say it was used.
func TestDeleteMessageCarriesForce(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			var got string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("force")
				fmt.Fprint(w, `{}`)
			})
			out, err := s.DeleteMessage(context.Background(), DeleteMessageInput{
				Message: "spaces/AAAAspace1/messages/AAAAmsg1", Force: force,
			})
			if err != nil {
				t.Fatalf("DeleteMessage: %v", err)
			}
			want := "false"
			if force {
				want = "true"
			}
			if got != want {
				t.Errorf("force = %q, want %q", got, want)
			}
			if out.Forced != force {
				t.Errorf("forced = %v, want %v", out.Forced, force)
			}
		})
	}
}

// A dry run says whether it would have taken the replies too, because
// that is the difference between one message and a conversation.
func TestADryRunDeleteRepeatsForce(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run must not reach Google")
	})
	out, err := s.DeleteMessage(context.Background(), DeleteMessageInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1", Force: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if !out.DryRun || out.Deleted || !out.Forced {
		t.Errorf("dry run = %+v", out)
	}
}

// A reply to a thread that has gone fails by default, so a message
// never lands somewhere the caller did not mean. Asking for the
// fallback is what changes that.
func TestAReplyChoosesItsFallback(t *testing.T) {
	for _, tc := range []struct {
		fallback bool
		want     string
	}{
		{false, "REPLY_MESSAGE_OR_FAIL"},
		{true, "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var got string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("messageReplyOption")
				fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}`)
			})
			if _, err := s.SendMessage(context.Background(), SendMessageInput{
				Space:         "spaces/AAAAspace1",
				Text:          "hello",
				Thread:        "spaces/AAAAspace1/threads/AAAAthread1",
				ReplyFallback: tc.fallback,
			}); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			if got != tc.want {
				t.Errorf("messageReplyOption = %q, want %q", got, tc.want)
			}
		})
	}
}

// A mention is text, and Google resolves the address itself when the
// caller is a person. So the body goes out with the mention exactly as
// it arrived — no lookup, no rewriting, nothing appended.
func TestAMentionIsPostedVerbatim(t *testing.T) {
	const text = "<users/janedoe@example.com> can you look at this? cc <users/all>"
	var sent string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		sent = body.Text
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1"}`)
	})
	if _, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/AAAAspace1", Text: text,
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent != text {
		t.Errorf("sent %q, want the body verbatim: %q", sent, text)
	}
}
