package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// dropAfterReading reads the request, then closes the connection, so
// the client cannot tell whether Google acted on it.
func dropAfterReading(t *testing.T, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}
}

// lastID holds the messageId the server saw last; the handler runs on
// the server's goroutines.
type lastID struct {
	mu sync.Mutex
	v  string
}

func (l *lastID) set(v string) { l.mu.Lock(); l.v = v; l.mu.Unlock() }
func (l *lastID) get() string  { l.mu.Lock(); defer l.mu.Unlock(); return l.v }

// A send Google did not confirm may have posted. The error names the id
// that was sent, minted or given, so a repeat with it cannot double post.
func TestAnUnconfirmedSendNamesItsMessageID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		given   string
		handler func(t *testing.T, sent *lastID) http.HandlerFunc
	}{
		{"server failure, id minted", "", func(_ *testing.T, sent *lastID) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				sent.set(r.URL.Query().Get("messageId"))
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"status":"INTERNAL"}}`)
			}
		}},
		{"connection dropped, id given", "client-retry-1", func(t *testing.T, sent *lastID) http.HandlerFunc {
			var calls atomic.Int32
			drop := dropAfterReading(t, &calls)
			return func(w http.ResponseWriter, r *http.Request) {
				sent.set(r.URL.Query().Get("messageId"))
				drop(w, r)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent lastID
			s := newService(t, tc.handler(t, &sent))
			_, err := s.SendMessage(context.Background(), SendMessageInput{
				Space: "spaces/AAAAspace1", Text: "hello", ClientMessageID: tc.given})
			sentID := sent.get()
			if sentID == "" || (tc.given != "" && sentID != tc.given) {
				t.Fatalf("messageId sent = %q, want %q or a minted one", sentID, tc.given)
			}
			var e *Error
			if !errors.As(err, &e) || !strings.Contains(e.Message, "It may have been posted") ||
				!strings.Contains(e.Message, fmt.Sprintf("client_message_id %q", sentID)) {
				t.Errorf("err = %v, want it to say the message may have been posted and name %q", err, sentID)
			}
		})
	}
}

// A refusal is definite: the send did not happen, so nothing is said
// about it having been posted.
func TestARefusedSendSaysNothingAboutPosting(t *testing.T) {
	s := newService(t, status(http.StatusBadRequest, `{"error":{"status":"INVALID_ARGUMENT","message":"bad text"}}`))
	_, err := s.SendMessage(context.Background(), SendMessageInput{Space: "spaces/AAAAspace1", Text: "hello"})
	if err == nil || strings.Contains(err.Error(), "may have been posted") {
		t.Errorf("err = %v, want a plain refusal", err)
	}
}

// A write with no idempotency key whose connection broke after it was
// sent may have landed, and is sent once.
func TestADroppedUnkeyedWriteSaysItMayHaveApplied(t *testing.T) {
	var calls atomic.Int32
	s := newService(t, dropAfterReading(t, &calls))
	_, err := s.CreateSpace(context.Background(), CreateSpaceInput{
		DisplayName: "Team", MemberEmails: []string{"robin@example.com"}})
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Message, "may have been applied") {
		t.Errorf("err = %v, want it to say the write may have been applied", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the create was sent %d times, want 1", got)
	}
}

// A repeated upload only leaves an unreferenced copy, so its failure
// does not tell the caller to read before trying again.
func TestAFailedUploadIsNotCalledMaybeApplied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the standup notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTransferService(t, dir, status(http.StatusInternalServerError, `{"error":{"status":"INTERNAL"}}`))
	_, err := s.UploadAttachment(context.Background(), UploadAttachmentInput{Space: "spaces/AAAAspace1", Path: "notes.txt"})
	if err == nil || strings.Contains(err.Error(), "may have been applied") {
		t.Errorf("err = %v, want a failure that does not say it may have been applied", err)
	}
}
