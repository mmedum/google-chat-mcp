package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

const membershipPage = `{"memberships":[
  {"name":"spaces/A/members/1","state":"JOINED","role":"ROLE_MANAGER","member":{"name":"users/1","displayName":"Jane Doe","type":"HUMAN"}},
  {"name":"spaces/A/members/2","state":"INVITED","role":"ROLE_MEMBER","groupMember":{"name":"groups/G1"}}
]}`

func TestListMembersSeparatesPeopleFromGroups(t *testing.T) {
	s := newService(t, route(ok(membershipPage), people("janedoe@example.com", "")))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d members, want 2", len(got))
	}
	if got[0].Kind != KindHuman || got[0].Email != "janedoe@example.com" || got[0].Role != "ROLE_MANAGER" {
		t.Errorf("person = %+v", got[0])
	}
	if got[1].Kind != KindGroup || got[1].Name != "groups/G1" {
		t.Errorf("group = %+v", got[1])
	}
	if got[1].Email != "" {
		t.Errorf("a group has no email, got %q", got[1].Email)
	}
	if got[1].State != "INVITED" {
		t.Errorf("state = %q", got[1].State)
	}
}

// Same rule as the message listing: a People failure costs the email
// and nothing else.
func TestListMembersSurvivesAPeopleFailure(t *testing.T) {
	s := newService(t, route(ok(membershipPage), status(http.StatusForbidden,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("a People failure must not fail the listing: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d members, want both", len(got))
	}
	if got[0].DisplayName != "Jane Doe" {
		t.Errorf("display name = %q, want the one Chat sent", got[0].DisplayName)
	}
}

// A role or state Google adds must not fail the row it arrived on.
func TestMembershipEnumsDegrade(t *testing.T) {
	// A role and a state Google does not have. ROLE_ASSISTANT_MANAGER
	// used to stand in for the unknown role here, which was the bug:
	// it is a real role, and it was degrading in production too.
	page := `{"memberships":[{"name":"spaces/A/members/1","state":"HIBERNATING","role":"ROLE_ARCHDUKE","member":{"name":"users/1"}}]}`
	s := newService(t, route(ok(page), nobody()))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the row must survive: %+v", got)
	}
	if got[0].Role != "ROLE_UNSPECIFIED" || got[0].State != "MEMBERSHIP_STATE_UNSPECIFIED" {
		t.Errorf("member = %+v, want the unknown values narrowed", got[0])
	}
}

// All three of Google's roles reach the caller. The third was missing
// from the allow-list, so an assistant manager's row said the server
// did not recognise their role.
func TestEveryRealRoleSurvives(t *testing.T) {
	for _, role := range []string{"ROLE_MEMBER", "ROLE_MANAGER", "ROLE_ASSISTANT_MANAGER"} {
		t.Run(role, func(t *testing.T) {
			page := `{"memberships":[{"name":"spaces/A/members/1","state":"JOINED","role":"` + role +
				`","member":{"name":"users/1"}}]}`
			s := newService(t, route(ok(page), nobody()))
			got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
			if err != nil {
				t.Fatalf("ListMembers: %v", err)
			}
			if len(got) != 1 || got[0].Role != role {
				t.Errorf("role = %+v, want %s", got, role)
			}
		})
	}
}

// A third kind of member is the one case where the row cannot be shaped
// at all. Dropping it silently would read as a smaller space.
func TestAMemberThatIsNeitherIsDropped(t *testing.T) {
	page := `{"memberships":[
	  {"name":"spaces/A/members/1","state":"JOINED","member":{"name":"users/1"}},
	  {"name":"spaces/A/members/2","state":"JOINED"}
	]}`
	s := newService(t, route(ok(page), nobody()))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("members = %+v, want only the readable row", got)
	}
}

// Google returns neither Google Groups nor invited people unless each
// is asked for by name. A space whose membership is mostly a group
// would otherwise answer "who is in this space" with almost nobody.
func TestListMembersAsksForGroupsAndInvitedPeople(t *testing.T) {
	var query url.Values
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		fmt.Fprint(w, `{"memberships":[]}`)
	}, nobody()))
	if _, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if query.Get("showGroups") != "true" {
		t.Errorf("showGroups = %q, want it asked for", query.Get("showGroups"))
	}
	if query.Get("showInvited") != "true" {
		t.Errorf("showInvited = %q, want it asked for", query.Get("showInvited"))
	}
}

// A Google Group carries only its resource name — Google's Group has
// no display name — so the row has to be honest about that rather than
// inventing one.
func TestAGroupMemberIsReportedAsItArrives(t *testing.T) {
	page := `{"memberships":[
	  {"name":"spaces/A/members/1","state":"JOINED","role":"ROLE_MEMBER","groupMember":{"name":"groups/G1"}},
	  {"name":"spaces/A/members/2","state":"INVITED","role":"ROLE_MEMBER","member":{"name":"users/2","displayName":"John Doe"}}
	]}`
	s := newService(t, route(ok(page), nobody()))
	got, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("members = %+v, want the group and the invited person", got)
	}
	if got[0].Kind != KindGroup || got[0].Name != "groups/G1" {
		t.Errorf("group = %+v", got[0])
	}
	if got[0].DisplayName != "" || got[0].Email != "" {
		t.Errorf("group = %+v, want no name or email invented for it", got[0])
	}
	// State is how a caller tells someone who is here from someone who
	// was only asked.
	if got[1].State != "INVITED" {
		t.Errorf("invited member = %+v", got[1])
	}
}

func TestListMembersDefaultsAndBounds(t *testing.T) {
	var size, path string
	s := newService(t, route(func(w http.ResponseWriter, r *http.Request) {
		size, path = r.URL.Query().Get("pageSize"), r.URL.Path
		fmt.Fprint(w, `{"memberships":[]}`)
	}, nobody()))
	if _, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if size != "50" {
		t.Errorf("page size = %q, want the default 50", size)
	}
	if !strings.HasSuffix(path, "/spaces/A/members") {
		t.Errorf("path = %q", path)
	}
	_, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A", Limit: 5000})
	assertClass(t, err, ClassInvalid)
	_, err = s.ListMembers(context.Background(), ListMembersInput{})
	assertClass(t, err, ClassInvalid)
}

// The prompt has to name the scope a caller can grant for this call,
// not the umbrella that also happens to work.
func TestListMembersNamesItsScope(t *testing.T) {
	s := newService(t, status(http.StatusForbidden,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.ListMembers(context.Background(), ListMembersInput{Space: "spaces/A"})
	assertClass(t, err, ClassScope)
	if !strings.Contains(err.Error(), "chat.memberships.readonly") {
		t.Errorf("error = %v, want the memberships scope named", err)
	}
}

func TestAddMemberInvitesByEmail(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/members/M"}`))
	got, err := s.AddMember(context.Background(), AddMemberInput{
		Space: "spaces/A", Email: "janedoe@example.com",
	})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	sent := rec.last(t)
	if sent.Method != "POST" || sent.Path != "/v1/spaces/A/members" {
		t.Errorf("request = %s %s", sent.Method, sent.Path)
	}
	if !strings.Contains(sent.Body, `"users/janedoe@example.com"`) {
		t.Errorf("body = %s", sent.Body)
	}
	if got.Name != "spaces/A/members/M" || got.Email != "janedoe@example.com" {
		t.Errorf("result = %+v", got)
	}
}

// The membership that already exists belongs to whoever invited them
// first. Returning it would say this call added them when it did not.
func TestAddMemberReportsSomeoneAlreadyInTheSpace(t *testing.T) {
	s := newService(t, status(409, `{"error":{"status":"ALREADY_EXISTS","message":"already a member"}}`))
	_, err := s.AddMember(context.Background(), AddMemberInput{Space: "spaces/A", Email: "janedoe@example.com"})
	assertClass(t, err, ClassInvalid)
	if !strings.Contains(err.Error(), "janedoe@example.com") {
		t.Errorf("error = %q, want it to name the person", err)
	}
}

func TestAddMemberDryRunInvitesNobody(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"spaces/A/members/M"}`))
	got, err := s.AddMember(context.Background(), AddMemberInput{
		Space: "spaces/A", Email: "janedoe@example.com", DryRun: true,
	})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if got.Name != "" || !got.DryRun {
		t.Errorf("result = %+v", got)
	}
	member, _ := got.Rendered["member"].(map[string]any)
	if member["name"] != "users/janedoe@example.com" || member["type"] != "HUMAN" {
		t.Errorf("rendered = %v", got.Rendered)
	}
}

func TestMemberWritesRejectBadInput(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"no space", func() error {
			_, err := s.AddMember(context.Background(), AddMemberInput{Email: "janedoe@example.com"})
			return err
		}},
		{"no address", func() error {
			_, err := s.AddMember(context.Background(), AddMemberInput{Space: "spaces/A"})
			return err
		}},
		{"a space instead of a membership", func() error {
			_, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A"})
			return err
		}},
		{"a message instead of a membership", func() error {
			_, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/messages/1"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertClass(t, tc.run(), ClassInvalid) })
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// Someone already out of the space is the state the caller asked for.
func TestRemoveMemberIsIdempotent(t *testing.T) {
	s := newService(t, status(404, `{"error":{"status":"NOT_FOUND","message":"gone"}}`))
	got, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/members/M"})
	if err != nil {
		t.Fatalf("a second removal is not a failure: %v", err)
	}
	if got.Removed {
		t.Error("removed = true, want false for a membership that was already gone")
	}
}

// The same 403 means two things here as it does for a message, so the
// refusal path reads the membership back rather than guessing.
func TestRemoveMemberChecksWhatARefusalMeant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		read    http.HandlerFunc
		wantErr Class
	}{
		{"still a member, so the refusal stands", ok(`{"name":"spaces/A/members/M","state":"JOINED"}`), ClassForbidden},
		{"really gone", status(404, `{"error":{"status":"NOT_FOUND","message":"gone"}}`), ""},
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
			got, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/members/M"})
			if tc.wantErr != "" {
				assertClass(t, err, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("RemoveMember: %v", err)
			}
			if got.Removed {
				t.Error("removed = true for a membership that was already gone")
			}
		})
	}
}

func TestRemoveMemberDoesNotSwallowAMissingScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/members/M"})
	assertClass(t, err, ClassScope)
}

func TestRemoveMemberRemoves(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/members/M"})
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if !got.Removed {
		t.Error("removed = false after a removal that worked")
	}
	if sent := rec.last(t); sent.Method != "DELETE" || sent.Path != "/v1/spaces/A/members/M" {
		t.Errorf("request = %s %s", sent.Method, sent.Path)
	}
}

func TestRemoveMemberDryRunRemovesNobody(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	got, err := s.RemoveMember(context.Background(), RemoveMemberInput{
		Membership: "spaces/A/members/M", DryRun: true,
	})
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if rec.len() != 0 {
		t.Errorf("a dry run made %d requests", rec.len())
	}
	if got.Removed || !got.DryRun {
		t.Errorf("result = %+v", got)
	}
}

func TestMemberWritesNameTheirScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	for _, run := range []func() error{
		func() error {
			_, err := s.AddMember(context.Background(), AddMemberInput{Space: "spaces/A", Email: "janedoe@example.com"})
			return err
		},
		func() error {
			_, err := s.RemoveMember(context.Background(), RemoveMemberInput{Membership: "spaces/A/members/M"})
			return err
		},
	} {
		assertScope(t, run(), scopes.Memberships)
	}
}

// The tools that hand Google a caller-supplied address all say what to
// check, because the address is nearly always what Google objected to.
func TestAddMemberExplainsAnAddressGoogleWillNotTake(t *testing.T) {
	s := newService(t, status(400, `{"error":{"status":"INVALID_ARGUMENT","message":"invalid member"}}`))
	_, err := s.AddMember(context.Background(), AddMemberInput{
		Space: "spaces/A", Email: "janedoe@example.com",
	})
	assertClass(t, err, ClassInvalid)
	if !strings.Contains(err.Error(), "directory") || !strings.Contains(err.Error(), "janedoe@example.com") {
		t.Errorf("error = %q, want the address and what to check", err)
	}
}

// Role is the only field Google lets a user-authenticated caller patch,
// so the mask is fixed and nothing else can change by accident.
func TestUpdateMemberRoleMasksRoleAlone(t *testing.T) {
	var mask, method string
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		mask, method = r.URL.Query().Get("updateMask"), r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/members/AAAAmember1","role":"ROLE_MANAGER"}`)
	})
	got, err := s.UpdateMemberRole(context.Background(), UpdateMemberRoleInput{
		Membership: "spaces/AAAAspace1/members/AAAAmember1", Role: "MANAGER",
	})
	if err != nil {
		t.Fatalf("UpdateMemberRole: %v", err)
	}
	if method != http.MethodPatch || mask != "role" {
		t.Errorf("%s with updateMask=%q, want PATCH masking role alone", method, mask)
	}
	if body["role"] != "ROLE_MANAGER" || len(body) != 1 {
		t.Errorf("body = %v, want the role by itself", body)
	}
	// What Google recorded, not what was asked for.
	if got.Role != "ROLE_MANAGER" {
		t.Errorf("role = %q", got.Role)
	}
}

// The tool surface speaks MEMBER, not ROLE_MEMBER, and a role Google
// does not have is the caller's mistake to hear about.
func TestUpdateMemberRoleChecksTheRole(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, role := range []string{"", "ROLE_MANAGER", "OWNER", "manager"} {
		_, err := s.UpdateMemberRole(context.Background(), UpdateMemberRoleInput{
			Membership: "spaces/AAAAspace1/members/AAAAmember1", Role: role,
		})
		assertClass(t, err, ClassInvalid)
	}
}

// Every write has a dry run, and a dry run reaches nothing.
func TestUpdateMemberRoleDryRun(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run must not reach Google")
	})
	got, err := s.UpdateMemberRole(context.Background(), UpdateMemberRoleInput{
		Membership: "spaces/AAAAspace1/members/AAAAmember1", Role: "MANAGER", DryRun: true,
	})
	if err != nil {
		t.Fatalf("UpdateMemberRole: %v", err)
	}
	if !got.DryRun || got.Rendered["role"] != "ROLE_MANAGER" {
		t.Errorf("dry run = %+v, want the body it would have sent", got)
	}
}

// A group is added by resource name and a person by address, and the
// two shapes are not mixed.
func TestAddMemberTakesAGroupOrAPerson(t *testing.T) {
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/members/AAAAmember1","role":"ROLE_MANAGER"}`)
	})

	got, err := s.AddMember(context.Background(), AddMemberInput{
		Space: "spaces/AAAAspace1", Group: "groups/AAAAgroup1",
	})
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	group, _ := body["groupMember"].(map[string]any)
	if group == nil || group["name"] != "groups/AAAAgroup1" || body["member"] != nil {
		t.Errorf("body = %v, want a group membership alone", body)
	}
	if _, sent := body["role"]; sent {
		// Google ignores a role on create — verified live — so nothing
		// here should imply otherwise by sending one.
		t.Errorf("a role was sent while adding: %v", body["role"])
	}
	// The role comes from the answer, which is where the truth is.
	if got.Role != "ROLE_MANAGER" || got.Group != "groups/AAAAgroup1" {
		t.Errorf("result = %+v", got)
	}

	_, err = s.AddMember(context.Background(), AddMemberInput{
		Space: "spaces/AAAAspace1", Email: "janedoe@example.com", Group: "groups/AAAAgroup1",
	})
	assertClass(t, err, ClassInvalid)
}

// get_member answers what a membership is, which is what a caller needs
// before removing it: a group membership takes everyone in the group.
func TestGetMemberNamesWhatItIs(t *testing.T) {
	s := newService(t, ok(`{"name":"spaces/AAAAspace1/members/AAAAmember1","state":"JOINED",
	  "role":"ROLE_ASSISTANT_MANAGER","groupMember":{"name":"groups/AAAAgroup1"}}`))
	got, err := s.GetMember(context.Background(), "spaces/AAAAspace1/members/AAAAmember1")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Kind != KindGroup || got.Role != "ROLE_ASSISTANT_MANAGER" || got.State != "JOINED" {
		t.Errorf("member = %+v", got)
	}
}
