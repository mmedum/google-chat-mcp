//go:build live

package livecheck

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// The admin steps need the account to be a Workspace administrator and
// the token to carry the administrator scopes: connect turns the admin
// toolset on, and login has to have asked for them
// (GCM_TOOLSETS=all,admin GCM_ENABLE_DESTRUCTIVE=true google-chat-mcp
// login).
//
// They run on the scratch space, which this account is in, and the
// delete on a second scratch space made for it.
func init() { steps = append(steps, adminSteps...) }

// adminSpace is the second scratch space, the one an administrator
// deletes.
var adminSpace struct{ id, name string }

var adminSteps = []step{
	{"an administrator's search finds the scratch space", "admin_search_spaces", func(d *driver) {
		var out struct {
			Result []struct {
				SpaceID string `json:"space_id"`
			} `json:"result"`
		}
		d.into(d.must("admin_search_spaces", map[string]any{
			"display_name": d.name, "created_after": time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
		}), &out)
		for _, r := range out.Result {
			if r.SpaceID == d.space {
				return
			}
		}
		d.t.Errorf("admin_search_spaces did not find the scratch space among %d rows; "+
			"if the space is minutes old, Google's index may not have it yet", len(out.Result))
	}},

	// The filters and orders exist only under admin access, and the
	// grammar's spelling of them is the reference's word against
	// Google's. A refusal here is the answer.
	{"an administrator's search takes every filter", "admin_search_spaces", func(d *driver) {
		tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
		for _, order := range []string{"most_members", "recently_active", "oldest"} {
			d.must("admin_search_spaces", map[string]any{
				"created_before": tomorrow, "last_active_after": "2026-01-01", "last_active_before": tomorrow,
				"history_state": "HISTORY_ON", "external_user_allowed": false, "order_by": order, "limit": 5,
			})
		}
	}},

	{"an administrator sets a name, a description and guidelines", "admin_update_space", func(d *driver) {
		name := d.name + " admin"
		var out struct {
			PreviousDisplayName *string `json:"previous_display_name"`
			PreviousGuidelines  *string `json:"previous_guidelines"`
		}
		d.into(d.must("admin_update_space", map[string]any{
			"space_id": d.space, "display_name": name,
			"description": liveAdminDescription, "guidelines": liveAdminGuidelines,
		}), &out)
		if out.PreviousDisplayName == nil || *out.PreviousDisplayName != d.name {
			d.t.Error("admin_update_space did not report the name it replaced")
		}
		if out.PreviousGuidelines == nil {
			d.t.Error("admin_update_space did not report the guidelines it replaced")
		}
		d.name = name
	}},

	// The claim the reference cannot settle: whether a read under admin
	// access carries the guidelines and the permission settings. If it
	// does not, a description edit would clear the guidelines.
	{"an administrator's read carries guidelines and permissions", "admin_get_space", func(d *driver) {
		var out struct {
			DisplayName string          `json:"display_name"`
			Description *string         `json:"description"`
			Guidelines  *string         `json:"guidelines"`
			Permissions *map[string]any `json:"permissions"`
		}
		d.into(d.must("admin_get_space", map[string]any{"space_id": d.space}), &out)
		if out.DisplayName != d.name {
			d.t.Error("admin_get_space does not read the name admin_update_space set")
		}
		if out.Description == nil || *out.Description != liveAdminDescription {
			d.t.Error("admin_get_space does not read the description admin_update_space set")
		}
		if out.Guidelines == nil || *out.Guidelines != liveAdminGuidelines {
			d.t.Error("admin_get_space does not read the guidelines: an administrator's description edit would clear them")
		}
		if out.Permissions == nil {
			d.t.Error("admin_get_space read no permission settings")
		}
	}},

	{"an administrator's description edit keeps the guidelines", "admin_update_space", func(d *driver) {
		d.must("admin_update_space", map[string]any{"space_id": d.space, "description": liveAdminDescription + " again"})
	}},

	{"the guidelines survive the edit", "admin_get_space", func(d *driver) {
		var out struct {
			Guidelines *string `json:"guidelines"`
		}
		d.into(d.must("admin_get_space", map[string]any{"space_id": d.space}), &out)
		if out.Guidelines == nil || *out.Guidelines != liveAdminGuidelines {
			d.t.Error("an administrator's description edit cleared the guidelines")
		}
	}},

	// Members can post in the scratch space, and there Google refuses a
	// change to who may reply, whoever asks. The server says so first.
	{"an administrator's reply setting is refused where members post", "admin_update_space_permissions", func(d *driver) {
		res := d.call("admin_update_space_permissions", map[string]any{
			"space_id": d.space, "confirm": true, "reply_messages": "managers",
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[invalid]") {
			d.t.Errorf("admin_update_space_permissions changed who may reply where members post, or refused it otherwise: %s", d.redact(res.Text))
		}
	}},

	// Every other setting at once, narrowed to managers: the scratch
	// space has nobody else in it, so nothing is taken from anyone.
	{"an administrator narrows every other permission", "admin_update_space_permissions", func(d *driver) {
		var out struct {
			Changes []struct{ Setting string } `json:"changes"`
		}
		d.into(d.must("admin_update_space_permissions", map[string]any{
			"space_id": d.space, "confirm": true,
			"manage_members_and_groups": "managers", "modify_space_details": "managers", "toggle_history": "managers",
			"use_at_mention_all": "managers", "manage_apps": "managers", "manage_webhooks": "managers",
		}), &out)
		if len(out.Changes) != 6 {
			d.t.Errorf("admin_update_space_permissions reported %d changes, want 6", len(out.Changes))
		}
	}},

	{"the permissions read back", "admin_get_space", func(d *driver) {
		var out struct {
			Permissions map[string]*string `json:"permissions"`
		}
		d.into(d.must("admin_get_space", map[string]any{"space_id": d.space}), &out)
		if len(out.Permissions) != 7 {
			d.t.Errorf("admin_get_space reported %d permissions, want 7", len(out.Permissions))
		}
		for setting, level := range out.Permissions {
			want := "managers"
			if setting == "reply_messages" {
				want = "members"
			}
			if level == nil || *level != want {
				d.t.Errorf("%s does not read back as %s", d.redact(setting), want)
			}
		}
	}},

	{"an administrator lists the members", "admin_list_members", func(d *driver) {
		var out struct {
			Result []struct {
				MembershipName string `json:"membership_name"`
			} `json:"result"`
		}
		d.into(d.must("admin_list_members", map[string]any{"space_id": d.space}), &out)
		if len(out.Result) == 0 {
			d.t.Error("admin_list_members found nobody in a space this account is in")
		}
	}},

	{"an administrator reads a membership", "admin_get_member", func(d *driver) {
		d.must("admin_get_member", map[string]any{"membership_name": d.member})
	}},

	// A manager made a manager again changes nothing, and is the one real
	// role write a single account can make.
	{"an administrator promotes a manager in place", "admin_promote_member", func(d *driver) {
		d.must("admin_promote_member", map[string]any{"membership_name": d.member, "role": "MANAGER", "confirm": true})
	}},

	{"an administrator's demotion previews", "admin_update_member_role", func(d *driver) {
		d.must("admin_update_member_role", map[string]any{"membership_name": d.member, "role": "MEMBER", "dry_run": true})
	}},

	// The account's own address is refused before anything is read in
	// the space: adding yourself would open the space to every tool.
	{"an administrator cannot add themselves", "admin_add_member", func(d *driver) {
		res := d.call("admin_add_member", map[string]any{"space_id": d.space, "user_email": d.email, "confirm": true})
		if !res.IsError || !strings.HasPrefix(res.Text, "[blocked]") {
			d.t.Errorf("admin_add_member added this account, or refused it otherwise: %s", d.redact(res.Text))
		}
	}},

	{"an administrator's delete under the wrong name is refused", "admin_delete_space", func(d *driver) {
		res := d.call("admin_delete_space", map[string]any{
			"space_id": d.space, "display_name": "a space this run did not make", "confirm": true,
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[stale]") {
			d.t.Fatalf("admin_delete_space ran under another space's name, or refused it otherwise: %s", d.redact(res.Text))
		}
	}},

	// The second scratch space, which an administrator deletes. The
	// account stays in it: a space left empty is gone within seconds
	// (live, 2026-10-31), so a run cannot leave one and then act on it.
	// What an administrator does in a space the caller is not in was
	// checked by hand that day, in a space with a second person in it.
	{"a second scratch space is made", "create_space", func(d *driver) {
		name := fmt.Sprintf("%s admin %d (safe to delete)", spacePrefix, time.Now().Unix())
		var made struct {
			SpaceID string `json:"space_id"`
		}
		d.into(d.must("create_space", map[string]any{"display_name": name}), &made)
		if made.SpaceID == "" {
			d.t.Fatal("create_space returned no space_id")
		}
		adminSpace.id, adminSpace.name = made.SpaceID, name
		d.record(made.SpaceID)
	}},

	// The delete, and its repeat. Google answers a deleted space with the
	// 403 it gives a space the caller may not touch, under admin access
	// too (live, 2026-10-31), so the repeat is refused [forbidden] rather
	// than reported as deleted: false.
	{"an administrator deletes a space", "admin_delete_space", func(d *driver) {
		args := map[string]any{"space_id": adminSpace.id, "display_name": adminSpace.name, "confirm": true}
		var first struct {
			Deleted bool `json:"deleted"`
		}
		d.into(d.must("admin_delete_space", args), &first)
		if !first.Deleted {
			d.t.Fatal("admin_delete_space did not delete the second scratch space")
		}
		adminSpace.id = ""
		if res := d.call("admin_delete_space", args); !res.IsError || !strings.HasPrefix(res.Text, "[forbidden]") {
			d.t.Errorf("a repeated admin_delete_space did not answer as Google answers a deleted space: %s", d.redact(res.Text))
		}
	}},
}

// dropAdminSpace deletes the second scratch space when a step stopped
// before admin_delete_space did, the way that step would. LIVE_KEEP
// keeps it, as it keeps the first.
func (d *driver) dropAdminSpace() {
	if adminSpace.id == "" {
		return
	}
	if os.Getenv("LIVE_KEEP") != "" {
		d.t.Logf("second space kept for inspection: %s", d.redact(adminSpace.id))
		return
	}
	res, err := d.try("admin_delete_space", map[string]any{
		"space_id": adminSpace.id, "display_name": adminSpace.name, "confirm": true,
	})
	if err != nil || res.IsError {
		d.t.Errorf("the second scratch space was left behind: %s", d.redact(adminSpace.id))
	}
}

// What the admin steps write. Ordinary words, as the other bodies are.
const (
	liveAdminDescription = "livecheck set this description and will delete the space"
	liveAdminGuidelines  = "livecheck set these guidelines and will delete the space"
)
