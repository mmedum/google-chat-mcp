package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mmedum/google-chat-mcp/v4/internal/config"
	"github.com/mmedum/google-chat-mcp/v4/internal/gchat"
)

// groupAPI answers the lookup and the read the way Cloud Identity does,
// and counts the reads.
func groupAPI(reads *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "groups:lookup"):
			fmt.Fprint(w, `{"name":"groups/AAAAgroup1"}`)
		case strings.HasPrefix(r.URL.Path, "/cloudidentity/groups/"):
			reads.Add(1)
			fmt.Fprint(w, `{"name":"groups/AAAAgroup1","groupKey":{"id":"team@example.com"},"displayName":"Team"}`)
		default:
			http.NotFound(w, r)
		}
	}
}

func TestFindGroupNamesTheGroup(t *testing.T) {
	var reads atomic.Int32
	got, err := newService(t, groupAPI(&reads)).FindGroup(context.Background(), " team@example.com ")
	if err != nil {
		t.Fatalf("FindGroup: %v", err)
	}
	if got.Name != "groups/AAAAgroup1" || got.Email != "team@example.com" || got.DisplayName != "Team" {
		t.Errorf("group = %+v", got)
	}
}

// The id is the answer the caller came for. A read of the group that
// fails after the lookup worked costs its display name and nothing else.
func TestFindGroupKeepsTheIDWhenTheReadFails(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "groups:lookup") {
			fmt.Fprint(w, `{"name":"groups/AAAAgroup1"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"no"}}`)
	})
	got, err := s.FindGroup(context.Background(), "team@example.com")
	if err != nil {
		t.Fatalf("FindGroup: %v", err)
	}
	if got.Name != "groups/AAAAgroup1" || got.DisplayName != "" {
		t.Errorf("group = %+v", got)
	}
}

func TestFindGroupRefusals(t *testing.T) {
	for _, tc := range []struct {
		label  string
		email  string
		answer func(http.ResponseWriter)
		class  Class
		says   string
	}{
		{"not an address", "team", nil, ClassInvalid, "not an email address"},
		{"not found", "team@example.com", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`)
		}, ClassNotFound, "only its own members can see"},
		{"refused", "team@example.com", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`)
		}, ClassForbidden, "only its own members can see"},
		{"not a group name", "team@example.com", func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"name":"spaces/AAAAspace1"}`)
		}, ClassUnexpected, "not a group name"},
		{"API not enabled", "team@example.com", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Cloud Identity API has not been used",
			  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`)
		}, ClassForbidden, "is not enabled on the Google Cloud project"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var calls atomic.Int32
			s := newService(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				tc.answer(w)
			})
			_, err := s.FindGroup(context.Background(), tc.email)
			assertClass(t, err, tc.class)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %v, want it to say %q", err, tc.says)
			}
			if tc.answer == nil && calls.Load() != 0 {
				t.Error("a malformed address reached Google")
			}
		})
	}
}

// A group on two rows is read once, and the rows carry its address and
// name.
func TestListMembersNamesGroups(t *testing.T) {
	var reads atomic.Int32
	cloud := groupAPI(&reads)
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity") {
			cloud(w, r)
			return
		}
		fmt.Fprint(w, `{"memberships":[
		  {"name":"spaces/A/members/1","state":"JOINED","groupMember":{"name":"groups/AAAAgroup1"}},
		  {"name":"spaces/A/members/2","state":"INVITED","groupMember":{"name":"groups/AAAAgroup1"}}]}`)
	})
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	for _, m := range got.Members {
		if m.Kind != KindGroup || m.Email != "team@example.com" || m.DisplayName != "Team" {
			t.Errorf("row = %+v", m)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("the group was read %d times, want once", n)
	}
}

// A rejected token fails every read the same way too.
func TestListMembersStopsAskingOnARejectedToken(t *testing.T) {
	var reads atomic.Int32
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity") {
			reads.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"bad token"}}`)
			return
		}
		fmt.Fprint(w, `{"memberships":[
		  {"name":"spaces/A/members/1","groupMember":{"name":"groups/AAAAgroup1"}},
		  {"name":"spaces/A/members/2","groupMember":{"name":"groups/AAAAgroup2"}}]}`)
	})
	if _, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("asked %d times with a rejected token, want once", n)
	}
}

// Without the scope every read fails the same way, so the first refusal
// ends them, and the rows stay as Chat sent them.
func TestListMembersStopsAskingWithoutTheScope(t *testing.T) {
	var reads atomic.Int32
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity") {
			reads.Add(1)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED",
			  "message":"Request had insufficient authentication scopes."}}`)
			return
		}
		fmt.Fprint(w, `{"memberships":[
		  {"name":"spaces/A/members/1","groupMember":{"name":"groups/AAAAgroup1"}},
		  {"name":"spaces/A/members/2","groupMember":{"name":"groups/AAAAgroup2"}},
		  {"name":"spaces/A/members/3","groupMember":{"name":"groups/AAAAgroup3"}}]}`)
	})
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got.Members) != 3 || got.Members[0].Email != "" || got.Members[0].Name != "groups/AAAAgroup1" {
		t.Errorf("members = %+v", got.Members)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("asked %d times without the scope, want once", n)
	}
}

// Google's message for a disabled API names the Cloud project, which no
// log may carry. The degraded lookup logs its class and status instead.
func TestAFailedGroupReadLogsNoGoogleText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity") {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED",
			  "message":"Cloud Identity API has not been used in project AAAAproject1 before",
			  "details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`)
			return
		}
		fmt.Fprint(w, `{"memberships":[{"name":"spaces/A/members/1","groupMember":{"name":"groups/AAAAgroup1"}}]}`)
	}))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	client := gchat.New(gchat.Options{
		HTTP: srv.Client(), ChatBase: srv.URL + "/v1", PeopleBase: srv.URL + "/people",
		OIDCBase: srv.URL + "/oidc", CloudIdentityBase: srv.URL + "/cloudidentity", Tokens: staticToken("test"),
	})
	s := New(client, nil, config.Config{}, log)
	if _, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if !strings.Contains(logs.String(), "group_lookup_degraded") {
		t.Fatalf("no degraded line was logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "AAAAproject1") {
		t.Errorf("the log carries Google's message: %s", logs.String())
	}
}
