package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/v9/internal/gchat"
)

// adminCtx is the context every admin tool's call runs on.
func adminCtx() context.Context { return AsAdministrator(context.Background()) }

// classOf is the class of a service error, empty for anything else.
func classOf(err error) Class {
	var se *Error
	if errors.As(err, &se) {
		return se.Class
	}
	return ""
}

// Google refuses an administrator's search that does not say whose
// spaces it searches, so the customer clause goes on every one, and
// never on an ordinary search. The client adds it; this holds the whole
// query a search sends.
func TestAdminSearchSaysWhoseSpaces(t *testing.T) {
	for _, tc := range []struct {
		label     string
		ctx       context.Context
		in        SearchSpacesInput
		wantQuery string
		wantOrder string
		wantAdmin bool
	}{
		{"as the caller", context.Background(), SearchSpacesInput{},
			`spaceType = "SPACE"`, "", false},
		{"admin_search_spaces", adminCtx(), SearchSpacesInput{},
			`customer = "customers/my_customer" AND spaceType = "SPACE"`, "", true},
		{"every filter", adminCtx(), SearchSpacesInput{
			DisplayName: "Fun eve", CreatedAfter: "2026-01-01", CreatedBefore: "2026-02-01T00:00:00Z",
			LastActiveBefore: "2026-03-01", HistoryState: "HISTORY_OFF", OrderBy: "least_recently_active",
		}, `customer = "customers/my_customer" AND spaceType = "SPACE" AND displayName:"Fun eve" AND ` +
			`createTime > "2025-12-31T23:59:59.999999Z" AND createTime < "2026-02-01T00:00:00.000000Z" AND ` +
			`lastActiveTime < "2026-03-01T00:00:00.000000Z" AND spaceHistoryState = "HISTORY_OFF"`,
			"last_active_time ASC", true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, ok(`{"spaces":[{"name":"spaces/AAAAspace1","spaceType":"SPACE","displayName":"Team",
				"spaceDetails":{"description":"Where the team talks"}}]}`))
			got, err := s.SearchSpaces(tc.ctx, tc.in)
			if err != nil {
				t.Fatalf("SearchSpaces(%+v): %v", tc.in, err)
			}
			sent := rec.last(t)
			if q := sent.param(t, "query"); q != tc.wantQuery {
				t.Errorf("query = %s\nwant    %s", q, tc.wantQuery)
			}
			if o := sent.param(t, "orderBy"); o != tc.wantOrder {
				t.Errorf("orderBy = %q, want %q", o, tc.wantOrder)
			}
			if admin := sent.param(t, "useAdminAccess") == "true"; admin != tc.wantAdmin {
				t.Errorf("useAdminAccess sent = %v, want %v", admin, tc.wantAdmin)
			}
			if len(got.Spaces) != 1 || got.Spaces[0].Description != "Where the team talks" {
				t.Errorf("rows = %+v, want the one space with its description", got.Spaces)
			}
		})
	}
}

// A filter Google would refuse, and the page sizes on each side of the
// limit, are refused before anything is sent.
func TestAdminSearchRefusesWhatGoogleWould(t *testing.T) {
	for _, tc := range []struct {
		label string
		ctx   context.Context
		in    SearchSpacesInput
		ok    bool
	}{
		{"an unknown order", adminCtx(), SearchSpacesInput{OrderBy: "alphabetical"}, false},
		{"an unknown history state", adminCtx(), SearchSpacesInput{HistoryState: "HISTORY_MAYBE"}, false},
		{"a window that holds nothing", adminCtx(), SearchSpacesInput{CreatedAfter: "2026-02-01", CreatedBefore: "2026-02-01"}, false},
		{"a timestamp that is not one", adminCtx(), SearchSpacesInput{LastActiveAfter: "last tuesday"}, false},
		{"100 as the caller", context.Background(), SearchSpacesInput{Limit: 100}, true},
		{"101 as the caller", context.Background(), SearchSpacesInput{Limit: 101}, false},
		{"200 as an administrator", adminCtx(), SearchSpacesInput{Limit: 200}, true},
		{"201 as an administrator", adminCtx(), SearchSpacesInput{Limit: 201}, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, ok(`{}`))
			_, err := s.SearchSpaces(tc.ctx, tc.in)
			switch {
			case tc.ok && err != nil:
				t.Errorf("SearchSpaces(%+v): %v, want it sent", tc.in, err)
			case !tc.ok && classOf(err) != ClassInvalid:
				t.Errorf("SearchSpaces(%+v): err = %v, want [invalid]", tc.in, err)
			case !tc.ok && rec.len() != 0:
				t.Errorf("SearchSpaces(%+v) was refused and still sent %d requests", tc.in, rec.len())
			}
		})
	}
}

// teamWith answers a read of spaces/AAAAspace1 with the named space
// "Team" carrying details, and anything else with an empty object.
func teamWith(details string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"name":"spaces/AAAAspace1","displayName":"Team","spaceType":"SPACE"%s}`, details)
			return
		}
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1"}`)
	}
}

// An edit says what it replaced, field by field, so the change can be
// put back; a field the edit left alone reports nothing.
func TestUpdateSpaceReportsWhatItReplaced(t *testing.T) {
	name, guidelines := "Renamed", "No spam"
	s, rec := recorded(t, teamWith(`,"spaceDetails":{"description":"Old","guidelines":"Be kind"}`))
	got, err := s.UpdateSpace(adminCtx(), UpdateSpaceInput{
		Space: "spaces/AAAAspace1", SpaceDisplayName: "Team", DisplayName: &name, Guidelines: &guidelines,
	})
	if err != nil {
		t.Fatalf("UpdateSpace: %v", err)
	}
	if want := `{"displayName":"Renamed","spaceDetails":{"description":"Old","guidelines":"No spam"}}`; rec.last(t).Body != want {
		t.Errorf("sent %s, want %s", rec.last(t).Body, want)
	}
	if mask := rec.last(t).mask(t); mask != "displayName,spaceDetails" {
		t.Errorf("mask = %q, want displayName,spaceDetails", mask)
	}
	old, kind := "Team", "Be kind"
	if want := (SpaceText{DisplayName: &old, Guidelines: &kind}); !reflect.DeepEqual(got.Previous, want) {
		t.Errorf("previous = %s, want %s", spaceText(got.Previous), spaceText(want))
	}
	if rec.count(http.MethodGet) != 1 {
		t.Errorf("%d reads, want 1: the read that checks the name gives what is resent", rec.count(http.MethodGet))
	}
}

// Google's own limit on guidelines, at the edge.
func TestGuidelinesAreBoundedByGooglesLimit(t *testing.T) {
	for _, tc := range []struct {
		n  int
		ok bool
	}{{5000, true}, {5001, false}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			text := strings.Repeat("x", tc.n)
			s, rec := recorded(t, teamWith(""))
			_, err := s.UpdateSpace(adminCtx(), UpdateSpaceInput{Space: "spaces/AAAAspace1", SpaceDisplayName: "Team", Guidelines: &text})
			switch {
			case tc.ok && err != nil:
				t.Errorf("%d characters: %v, want it sent", tc.n, err)
			case !tc.ok && (classOf(err) != ClassInvalid || rec.len() != 0):
				t.Errorf("%d characters: err = %v after %d requests, want [invalid] before any", tc.n, err, rec.len())
			}
		})
	}
}

// An administrator's write reaches named spaces only: the check of a
// direct message or a group chat names the people in it beside the
// caller, which means nothing for a conversation the caller is not in.
func TestAdminWritesActOnNamedSpacesOnly(t *testing.T) {
	description := "x"
	for _, tc := range []struct {
		label string
		write func(*Service) error
	}{
		{"update", func(s *Service) error {
			_, err := s.UpdateSpace(adminCtx(), UpdateSpaceInput{Space: "spaces/AAAAdm1", SpaceDisplayName: "janedoe@example.com", Description: &description})
			return err
		}},
		{"permissions", func(s *Service) error {
			_, err := s.UpdateSpacePermissions(adminCtx(), UpdateSpacePermissionsInput{
				Space: "spaces/AAAAdm1", SpaceDisplayName: "janedoe@example.com", Set: SpacePermissions{ManageApps: PermissionManagers},
			})
			return err
		}},
		{"add", func(s *Service) error {
			_, err := s.AdminAddMember(adminCtx(), AddMemberInput{Space: "spaces/AAAAdm1", SpaceDisplayName: "janedoe@example.com", Email: "johndoe@example.com"})
			return err
		}},
		{"delete", func(s *Service) error {
			_, err := s.DeleteSpace(adminCtx(), DeleteSpaceInput{Space: "spaces/AAAAdm1", DisplayName: "janedoe@example.com"})
			return err
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/oidc"):
					fmt.Fprint(w, `{"sub":"111111111111111111111","email":"janedoe@example.com"}`)
				case strings.HasPrefix(r.URL.Path, "/people"):
					fmt.Fprint(w, `{"people":[{"resourceName":"people/222222222222222222222","emailAddresses":[{"value":"johndoe@example.com"}]}]}`)
				default:
					fmt.Fprint(w, `{"name":"spaces/AAAAdm1","spaceType":"DIRECT_MESSAGE"}`)
				}
			})
			if err := tc.write(s); classOf(err) != ClassInvalid {
				t.Errorf("err = %v, want [invalid]", err)
			}
			if rec.writes() != 0 {
				t.Errorf("%d writes reached Google, want none", rec.writes())
			}
		})
	}
}

// Each level gives a permission to the managers and to everyone up to
// the level; the patch carries only what was asked for, and the reply
// what each setting was.
func TestUpdateSpacePermissionsSendsOnlyWhatChanges(t *testing.T) {
	s, rec := recorded(t, teamWith(`,"permissionSettings":{
		"manageApps":{"managersAllowed":true,"assistantManagersAllowed":true},
		"useAtMentionAll":{"managersAllowed":true,"membersAllowed":true}}`))
	got, err := s.UpdateSpacePermissions(adminCtx(), UpdateSpacePermissionsInput{
		Space: "spaces/AAAAspace1", SpaceDisplayName: "Team",
		Set: SpacePermissions{ManageApps: "managers", UseAtMentionAll: " ", ReplyMessages: "members"},
	})
	if err != nil {
		t.Fatalf("UpdateSpacePermissions: %v", err)
	}
	wantBody := `{"permissionSettings":{` +
		`"manageApps":{"managersAllowed":true,"assistantManagersAllowed":false,"membersAllowed":false},` +
		`"replyMessages":{"managersAllowed":true,"assistantManagersAllowed":true,"membersAllowed":true}}}`
	if sent := rec.last(t); sent.Body != wantBody {
		t.Errorf("sent %s\nwant %s", sent.Body, wantBody)
	}
	if mask := rec.last(t).mask(t); mask != "permission_settings.manageApps,permission_settings.replyMessages" {
		t.Errorf("mask = %q", mask)
	}
	want := []PermissionChange{
		{Setting: "manage_apps", From: "assistant_managers", To: "managers"},
		{Setting: "reply_messages", From: "", To: "members"},
	}
	if !reflect.DeepEqual(got.Changes, want) {
		t.Errorf("changes = %+v\nwant      %+v", got.Changes, want)
	}
}

func TestUpdateSpacePermissionsRefusesWhatItCannotSend(t *testing.T) {
	for _, tc := range []struct {
		label string
		set   SpacePermissions
	}{
		{"nothing to change", SpacePermissions{}},
		{"only blanks", SpacePermissions{ManageApps: "  "}},
		{"a level that is not one", SpacePermissions{ManageApps: "everyone"}},
		{"nobody, which can only be read", SpacePermissions{ManageApps: PermissionNobody}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, teamWith(""))
			_, err := s.UpdateSpacePermissions(adminCtx(), UpdateSpacePermissionsInput{Space: "spaces/AAAAspace1", SpaceDisplayName: "Team", Set: tc.set})
			if classOf(err) != ClassInvalid || rec.len() != 0 {
				t.Errorf("err = %v after %d requests, want [invalid] before any", err, rec.len())
			}
		})
	}
	s, rec := recorded(t, teamWith(""))
	if _, err := s.UpdateSpacePermissions(adminCtx(), UpdateSpacePermissionsInput{
		Space: "spaces/AAAAspace1", SpaceDisplayName: "Team", Set: SpacePermissions{ManageApps: "members"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if rec.writes() != 0 {
		t.Errorf("a dry run sent %d writes", rec.writes())
	}
}

// Google lets who may reply change only where members cannot post, and
// answers it elsewhere with a permission denied that reads like a
// missing space; the server says why before anything is sent.
func TestReplySettingChangesOnlyWhereMembersCannotPost(t *testing.T) {
	for _, tc := range []struct {
		label      string
		post       string
		wantClass  Class
		wantWrites int
	}{
		{"members can post", `{"managersAllowed":true,"assistantManagersAllowed":true,"membersAllowed":true}`, ClassInvalid, 0},
		{"only managers can post", `{"managersAllowed":true,"assistantManagersAllowed":true}`, "", 1},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s, rec := recorded(t, teamWith(`,"permissionSettings":{"postMessages":`+tc.post+`}`))
			_, err := s.UpdateSpacePermissions(adminCtx(), UpdateSpacePermissionsInput{
				Space: "spaces/AAAAspace1", SpaceDisplayName: "Team",
				Set: SpacePermissions{ManageApps: PermissionManagers, ReplyMessages: PermissionManagers},
			})
			if classOf(err) != tc.wantClass {
				t.Errorf("err = %v, want class %q", err, tc.wantClass)
			}
			if rec.writes() != tc.wantWrites {
				t.Errorf("%d writes reached Google, want %d", rec.writes(), tc.wantWrites)
			}
		})
	}
}

// A read reports each permission at the widest role that holds it.
func TestGetSpaceReportsWhoHoldsEachPermission(t *testing.T) {
	s := newService(t, ok(`{"name":"spaces/AAAAspace1","displayName":"Team","spaceType":"SPACE","permissionSettings":{
		"manageMembersAndGroups":{"managersAllowed":true,"assistantManagersAllowed":true,"membersAllowed":true},
		"modifySpaceDetails":{"managersAllowed":true,"assistantManagersAllowed":true},
		"toggleHistory":{"managersAllowed":true},
		"manageWebhooks":{}}}`))
	got, err := s.GetSpace(adminCtx(), "spaces/AAAAspace1")
	if err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	want := &SpacePermissions{
		ManageMembersAndGroups: "members", ModifySpaceDetails: "assistant_managers", ToggleHistory: "managers",
		ManageWebhooks: "nobody",
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("permissions = %+v\nwant          %+v", got.Permissions, want)
	}
}

// callerAndDirectory answers the OpenID Connect profile as the caller, and a
// directory search with whoever is given.
func callerAndDirectory(person string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/oidc"):
			fmt.Fprint(w, `{"sub":"111111111111111111111","email":"janedoe@example.com"}`)
		case strings.Contains(r.URL.Path, "searchDirectoryPeople"):
			fmt.Fprint(w, person)
		case r.Method == http.MethodGet:
			fmt.Fprint(w, `{"name":"spaces/AAAAspace1","displayName":"Team","spaceType":"SPACE"}`)
		default:
			fmt.Fprint(w, `{"name":"spaces/AAAAspace1/members/AAAAmember1"}`)
		}
	}
}

// An administrator adding themselves would turn every tool here on a
// space they could not read before, so the caller is refused by every
// address the directory knows them by, and so is a Google Group. Google
// adds nobody from outside on an administrator's say, so an address the
// directory does not know is refused before anything is sent.
func TestAdminAddMemberNeverAddsTheCaller(t *testing.T) {
	someoneElse := `{"people":[{"resourceName":"people/222222222222222222222","emailAddresses":[{"value":"johndoe@example.com"}]}]}`
	alias := `{"people":[{"resourceName":"people/111111111111111111111","emailAddresses":[{"value":"jane@example.com"}]}]}`
	for _, tc := range []struct {
		label     string
		in        AddMemberInput
		directory string
		wantAdd   bool
	}{
		{"someone else", AddMemberInput{Email: "johndoe@example.com"}, someoneElse, true},
		{"the caller", AddMemberInput{Email: "JaneDoe@example.com"}, someoneElse, false},
		{"the caller's alias", AddMemberInput{Email: "jane@example.com"}, alias, false},
		{"someone the directory does not know", AddMemberInput{Email: "johndoe@example.com"}, `{}`, false},
		{"a group", AddMemberInput{Group: "groups/AAAAgroup1"}, someoneElse, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			tc.in.Space, tc.in.SpaceDisplayName = "spaces/AAAAspace1", "Team"
			s, rec := recorded(t, callerAndDirectory(tc.directory))
			_, err := s.AdminAddMember(adminCtx(), tc.in)
			added := rec.count(http.MethodPost) == 1
			switch {
			case tc.wantAdd && (err != nil || !added):
				t.Errorf("err = %v, added = %v, want the add sent", err, added)
			case !tc.wantAdd && (classOf(err) != ClassBlocked || rec.writes() != 0):
				t.Errorf("err = %v after %d writes, want [blocked] and none", err, rec.writes())
			}
		})
	}
	// A directory that will not answer cannot tell an alias from anyone
	// else, so it vouches for nobody.
	s, rec := recorded(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "searchDirectoryPeople") {
			http.Error(w, `{"error":{"code":400,"message":"no"}}`, http.StatusBadRequest)
			return
		}
		callerAndDirectory("")(w, r)
	})
	_, err := s.AdminAddMember(adminCtx(), AddMemberInput{Space: "spaces/AAAAspace1", SpaceDisplayName: "Team", Email: "johndoe@example.com"})
	if classOf(err) != ClassBlocked || rec.writes() != 0 {
		t.Errorf("unreadable directory: err = %v after %d writes, want [blocked] and none", err, rec.writes())
	}
}

// A refused remove is checked by reading the membership back, and under
// admin access that read has to be an administrator's too: read as the
// caller, a space the caller is not in answers not found, and the
// refusal would be reported as a member already gone.
func TestAdminRemoveReadsBackAsTheAdministrator(t *testing.T) {
	s, rec := recorded(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"no"}}`, http.StatusForbidden)
		case r.URL.Query().Get("useAdminAccess") == "true":
			fmt.Fprint(w, `{"name":"spaces/AAAAspace1/members/AAAAmember1","state":"JOINED"}`)
		default:
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no"}}`, http.StatusNotFound)
		}
	})
	got, err := s.RemoveMember(adminCtx(), RemoveMemberInput{Membership: "spaces/AAAAspace1/members/AAAAmember1"})
	if err == nil {
		t.Fatalf("RemoveMember = %+v, want the refusal to stand: the member is still there", got)
	}
	for _, c := range rec.all() {
		if c.param(t, "useAdminAccess") != "true" {
			t.Errorf("%s %s went as the caller", c.Method, c.Path)
		}
	}
}

// A refusal points to the tool the caller is using.
func TestAdminRefusalsNameTheAdminTool(t *testing.T) {
	s, _ := recorded(t, ok(`{}`))
	_, err := s.UpdateMemberRole(adminCtx(), UpdateMemberRoleInput{Membership: "spaces/AAAAspace1/members/AAAAmember1", Role: "MANAGER"})
	if classOf(err) != ClassBlocked || !strings.Contains(err.Error(), "admin_promote_member does") {
		t.Errorf("err = %v, want [blocked] pointing to admin_promote_member", err)
	}
	_, err = s.UpdateMemberRole(context.Background(), UpdateMemberRoleInput{Membership: "spaces/AAAAspace1/members/AAAAmember1", Role: "MANAGER"})
	if strings.Contains(err.Error(), "admin_") {
		t.Errorf("an ordinary refusal names an admin tool: %v", err)
	}
}

// gchat.AdminAccess is how the client knows, and AsAdministrator is the
// only way the service sets it.
func TestAsAdministratorMarksTheContext(t *testing.T) {
	if !gchat.AdminAccess(adminCtx()) || gchat.AdminAccess(context.Background()) {
		t.Error("AsAdministrator does not mark the context, or an ordinary one is marked")
	}
}

// spaceText prints a SpaceText's values rather than its pointers.
func spaceText(v SpaceText) string {
	show := func(p *string) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprintf("%q", *p)
	}
	return fmt.Sprintf("{name %s, description %s, guidelines %s}", show(v.DisplayName), show(v.Description), show(v.Guidelines))
}

// A group chat can carry a name, and is still not a named space: the
// check goes by the space's type.
func TestAdminWritesRefuseANamedGroupChat(t *testing.T) {
	description := "x"
	s, rec := recorded(t, ok(`{"name":"spaces/AAAAspace2","spaceType":"GROUP_CHAT","displayName":"Weekend plans"}`))
	_, err := s.UpdateSpace(adminCtx(), UpdateSpaceInput{Space: "spaces/AAAAspace2", SpaceDisplayName: "Weekend plans", Description: &description})
	if classOf(err) != ClassInvalid {
		t.Errorf("update: err = %v, want [invalid]", err)
	}
	_, err = s.DeleteSpace(adminCtx(), DeleteSpaceInput{Space: "spaces/AAAAspace2", DisplayName: "Weekend plans"})
	if classOf(err) != ClassInvalid {
		t.Errorf("delete: err = %v, want [invalid]", err)
	}
	if rec.writes() != 0 {
		t.Errorf("%d writes reached Google, want none", rec.writes())
	}
}

// The account id is fetched once even when Google names no address for
// the account: only admin_add_member needs the address.
func TestCallerIDIsFetchedOnceWithoutAnAddress(t *testing.T) {
	s, rec := recorded(t, ok(`{"sub":"111111111111111111111"}`))
	for range 3 {
		if id, err := s.callerID(context.Background()); err != nil || id != "users/111111111111111111111" {
			t.Fatalf("callerID = %q, %v", id, err)
		}
	}
	if rec.len() != 1 {
		t.Errorf("%d userinfo requests for three lookups, want 1", rec.len())
	}
}
