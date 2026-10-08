package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// groupMembersAPI answers Chat with one group row per name and Cloud
// Identity with each group's members, refusing the groups in hidden.
// The groups are read at once, so queries is kept under a lock.
func groupMembersAPI(groups []string, hidden map[string]bool, queries *[]string) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/memberships") && strings.HasPrefix(path, "/cloudidentity/"):
			mu.Lock()
			*queries = append(*queries, r.URL.RawQuery)
			mu.Unlock()
			if hidden[strings.TrimSuffix(strings.TrimPrefix(path, "/cloudidentity/"), "/memberships")] {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Error(2028): Permission denied for resource groups/AAAAgroup2"}}`)
				return
			}
			fmt.Fprint(w, `{"memberships":[
			  {"name":"groups/G/memberships/1","preferredMemberKey":{"id":"janedoe@example.com"},"roles":[{"name":"MEMBER"}],"type":"USER"},
			  {"name":"groups/G/memberships/2","preferredMemberKey":{"id":"johndoe@example.com"},
			   "roles":[{"name":"MEMBER"},{"name":"OWNER"}],"type":"USER"}],"nextPageToken":"more"}`)
		case strings.HasPrefix(path, "/cloudidentity/groups/"):
			fmt.Fprint(w, `{"name":"groups/G","groupKey":{"id":"team@example.com"},"displayName":"Team"}`)
		default:
			rows := make([]string, 0, len(groups))
			for i, g := range groups {
				rows = append(rows, fmt.Sprintf(`{"name":"spaces/A/members/%d","state":"JOINED","groupMember":{"name":%q}}`, i, g))
			}
			fmt.Fprintf(w, `{"memberships":[%s]}`, strings.Join(rows, ","))
		}
	}
}

// Asked to, a listing says who is in each group, one level down: the
// members' addresses, kinds and highest roles, and that there are more.
func TestListMembersExpandsGroups(t *testing.T) {
	var queries []string
	s := newService(t, groupMembersAPI([]string{"groups/AAAAgroup1"}, nil, &queries))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A", ExpandGroups: true})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	row := got.Members[0]
	want := []GroupMember{
		{Email: "janedoe@example.com", Kind: "USER", Role: "MEMBER"},
		{Email: "johndoe@example.com", Kind: "USER", Role: "OWNER"},
	}
	if fmt.Sprint(row.GroupMembers) != fmt.Sprint(want) || !row.GroupMembersMore || row.GroupMembersMissing != "" {
		t.Errorf("group row = %+v, want members %+v and more", row, want)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], "pageSize=200") || !strings.Contains(queries[0], "fields=") {
		t.Errorf("membership queries = %q, want one page of 200, trimmed to the fields read", queries)
	}
}

// Not asked, nothing is read: expanding costs a call per group.
func TestListMembersExpandsNothingUnasked(t *testing.T) {
	var queries []string
	s := newService(t, groupMembersAPI([]string{"groups/AAAAgroup1"}, nil, &queries))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(queries) != 0 || got.Members[0].GroupMembers != nil {
		t.Errorf("unasked, read %d membership pages and returned %+v", len(queries), got.Members[0].GroupMembers)
	}
}

// A group that hides its members costs that group's members and nothing
// else, and its row says why.
func TestListMembersSaysWhyAGroupWasNotExpanded(t *testing.T) {
	var queries []string
	hidden := map[string]bool{"groups/AAAAgroup2": true}
	s := newService(t, groupMembersAPI([]string{"groups/AAAAgroup1", "groups/AAAAgroup2"}, hidden, &queries))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A", ExpandGroups: true})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got.Members[0].GroupMembers) != 2 {
		t.Errorf("the readable group = %+v, want its two members", got.Members[0])
	}
	if hiddenRow := got.Members[1]; hiddenRow.GroupMembers != nil || !strings.HasPrefix(hiddenRow.GroupMembersMissing, "[forbidden]") {
		t.Errorf("the hidden group = %+v, want no members and a [forbidden] reason", hiddenRow)
	}
}

// One listing costs a known number of calls: past ten groups, a row says
// it was not read rather than reading it.
func TestListMembersExpandsTenGroupsAtMost(t *testing.T) {
	var queries []string
	groups := make([]string, 0, 11)
	for i := range 11 {
		groups = append(groups, fmt.Sprintf("groups/AAAAgroup%d", i))
	}
	s := newService(t, groupMembersAPI(groups, nil, &queries))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A", ExpandGroups: true})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(queries) != 10 {
		t.Errorf("read %d groups' members, want 10", len(queries))
	}
	if last := got.Members[10]; last.GroupMembers != nil || last.GroupMembersMissing != "not read: only the first 10 groups on a page are expanded" {
		t.Errorf("the eleventh group = %+v, want it left unread and said so", last)
	}
}

// Every member holds MEMBER, and an owner or a manager holds that too.
func TestHighestGroupRole(t *testing.T) {
	for _, tc := range []struct {
		roles []string
		want  string
	}{
		{[]string{"MEMBER"}, "MEMBER"},
		{[]string{"MEMBER", "MANAGER"}, "MANAGER"},
		{[]string{"MANAGER", "MEMBER"}, "MANAGER"},
		{[]string{"MEMBER", "OWNER", "MANAGER"}, "OWNER"},
		{[]string{"OWNER", "MANAGER"}, "OWNER"},
		{nil, ""},
	} {
		roles := make([]gchat.GroupMembershipRole, 0, len(tc.roles))
		for _, r := range tc.roles {
			roles = append(roles, gchat.GroupMembershipRole{Name: r})
		}
		if got := highestGroupRole(roles); got != tc.want {
			t.Errorf("highestGroupRole(%v) = %q, want %q", tc.roles, got, tc.want)
		}
	}
}

// A failure every group would share, met while the groups were named, is
// not met again for their members: one listing, one refusal.
func TestListMembersDoesNotRepeatAFailureTheLookupsMet(t *testing.T) {
	var memberReads atomic.Int32
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cloudidentity") {
			if strings.HasSuffix(r.URL.Path, "/memberships") {
				memberReads.Add(1)
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
			return
		}
		fmt.Fprint(w, `{"memberships":[
		  {"name":"spaces/A/members/1","groupMember":{"name":"groups/AAAAgroup1"}},
		  {"name":"spaces/A/members/2","groupMember":{"name":"groups/AAAAgroup2"}}]}`)
	})
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A", ExpandGroups: true})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if n := memberReads.Load(); n != 0 {
		t.Errorf("read members %d times after the lookups met a missing scope, want none", n)
	}
	for _, row := range got.Members {
		if !strings.HasPrefix(row.GroupMembersMissing, "[scope]") {
			t.Errorf("row %s says %q, want the missing scope", row.Name, row.GroupMembersMissing)
		}
	}
}
