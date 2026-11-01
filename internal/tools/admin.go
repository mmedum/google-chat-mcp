package tools

import (
	"context"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v9/internal/service"
)

// The admin tools act on any space in the Workspace with the caller's
// administrator privileges, on Google's useAdminAccess. Each is its own
// tool rather than a flag on the ordinary one, because a client
// remembers an approval per tool, never per argument
// (docs/architecture.md, "Administrator access"). register reads the
// admin_ prefix: it puts every call on the administrator's context and
// the tool in the admin toolset.

// AdminSearchSpacesInput narrows a search of every space in the
// Workspace.
type AdminSearchSpacesInput struct {
	DisplayName         string `json:"display_name,omitempty" jsonschema:"match spaces with words in their name that begin with these words, each matched on its own and in any order: 'fun eve' finds 'Fun event' and 'The evening was fun'. Omit to page through every named space"`
	ExternalUserAllowed *bool  `json:"external_user_allowed,omitempty" jsonschema:"true for spaces that admit people outside the organization, false for those that do not; omit for both"`
	CreatedAfter        string `json:"created_after,omitempty" jsonschema:"only spaces created at or after this time; RFC 3339, such as 2026-01-01T00:00:00Z, or a date"`
	CreatedBefore       string `json:"created_before,omitempty" jsonschema:"only spaces created before this time; RFC 3339 or a date"`
	LastActiveAfter     string `json:"last_active_after,omitempty" jsonschema:"only spaces with a message at or after this time; RFC 3339 or a date"`
	LastActiveBefore    string `json:"last_active_before,omitempty" jsonschema:"only spaces with no message since this time; RFC 3339 or a date"`
	HistoryState        string `json:"history_state,omitempty" jsonschema:"HISTORY_ON or HISTORY_OFF; omit for both"`
	OrderBy             string `json:"order_by,omitempty" jsonschema:"most_members, fewest_members, recently_active, least_recently_active, newest or oldest; omit for Google's own order"`
	Limit               int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 200; default 50"`
	PageToken           string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

// AdminSpaceSummaryOutput is one row of an administrator's search.
type AdminSpaceSummaryOutput struct {
	SpaceID        string     `json:"space_id" jsonschema:"the space's resource name, spaces/{id}; pass this to the other admin tools"`
	DisplayName    string     `json:"display_name" jsonschema:"what the space is called"`
	Description    *string    `json:"description" jsonschema:"what the space is for, as its managers wrote it; null when it has none or Google did not send it"`
	LastActiveTime *time.Time `json:"last_active_time" jsonschema:"when the last message was posted, RFC 3339 in UTC; null when Google did not say"`
}

// AdminSearchSpacesOutput wraps the matches.
type AdminSearchSpacesOutput struct {
	Result        []AdminSpaceSummaryOutput `json:"result" jsonschema:"the named spaces that matched"`
	NextPageToken *string                   `json:"next_page_token" jsonschema:"pass back as page_token for the next page; null when this is the last one"`
	TotalSize     int                       `json:"total_size" jsonschema:"how many spaces matched in total, an estimate above 10000"`
}

// AdminUpdateSpaceInput is an administrator's edit of a space's name,
// description or guidelines.
type AdminUpdateSpaceInput struct {
	SpaceID          string  `json:"space_id" jsonschema:"the space to edit, spaces/{id}, from admin_search_spaces"`
	SpaceDisplayName string  `json:"space_display_name" jsonschema:"what the space is called now, as admin_search_spaces or admin_get_space shows it, so the prompt that approves this call names it. Checked against the space before anything is written; a call naming another space is refused [stale]"`
	DisplayName      *string `json:"display_name,omitempty" jsonschema:"a new name, 1 to 128 characters, an emoji counting as two; omit to leave it alone"`
	Description      *string `json:"description,omitempty" jsonschema:"a new description, up to 4,096 characters, an emoji counting as two; omit to leave it alone. Pass an empty string to clear it"`
	Guidelines       *string `json:"guidelines,omitempty" jsonschema:"new guidelines, the space's rules and etiquette, up to 5,000 characters, an emoji counting as two; omit to leave them alone. Pass an empty string to clear them"`
	DryRun           bool    `json:"dry_run,omitempty" jsonschema:"return the patch body and its mask without applying them"`
}

// AdminUpdateSpaceOutput is what the edit asked for, and what it
// replaced.
type AdminUpdateSpaceOutput struct {
	SpaceID             string           `json:"space_id" jsonschema:"the space that was edited"`
	DisplayName         *string          `json:"display_name" jsonschema:"the name that was set, or null when the call left it alone"`
	Description         *string          `json:"description" jsonschema:"the description that was set, or null when the call left it alone"`
	Guidelines          *string          `json:"guidelines" jsonschema:"the guidelines that were set, or null when the call left them alone"`
	PreviousDisplayName *string          `json:"previous_display_name" jsonschema:"what the name was before this call, read just before it; null when the call left it alone"`
	PreviousDescription *string          `json:"previous_description" jsonschema:"what the description was before this call, empty when there was none; null when the call left it alone. Pass it back as description to undo the change"`
	PreviousGuidelines  *string          `json:"previous_guidelines" jsonschema:"what the guidelines were before this call, empty when there were none; null when the call left them alone"`
	UpdateMask          *string          `json:"update_mask" jsonschema:"which top-level fields the patch targeted"`
	DryRun              bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload     *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact patch body; null on a real edit"`
}

// AdminUpdateSpacePermissionsInput changes who holds a space's
// permissions.
type AdminUpdateSpacePermissionsInput struct {
	SpaceID                string `json:"space_id" jsonschema:"the space to change, spaces/{id}, from admin_search_spaces"`
	SpaceDisplayName       string `json:"space_display_name" jsonschema:"what the space is called now, as admin_search_spaces or admin_get_space shows it, so the prompt that approves this call names it. Checked against the space before anything is written; a call naming another space is refused [stale]"`
	ManageMembersAndGroups string `json:"manage_members_and_groups,omitempty" jsonschema:"who may add and remove members and groups: managers, assistant_managers or members; omit to leave it alone"`
	ModifySpaceDetails     string `json:"modify_space_details,omitempty" jsonschema:"who may change the name, avatar, description and guidelines: managers, assistant_managers or members; omit to leave it alone"`
	ToggleHistory          string `json:"toggle_history,omitempty" jsonschema:"who may turn history on and off: managers, assistant_managers or members; omit to leave it alone"`
	UseAtMentionAll        string `json:"use_at_mention_all,omitempty" jsonschema:"who may mention everyone with @all: managers, assistant_managers or members; omit to leave it alone"`
	ManageApps             string `json:"manage_apps,omitempty" jsonschema:"who may add and remove Chat apps: managers, assistant_managers or members; omit to leave it alone"`
	ManageWebhooks         string `json:"manage_webhooks,omitempty" jsonschema:"who may manage incoming webhooks: managers, assistant_managers or members; omit to leave it alone"`
	ReplyMessages          string `json:"reply_messages,omitempty" jsonschema:"who may reply in threads: managers, assistant_managers or members; omit to leave it alone. It changes only in a space where members cannot post (members_can_post false), and is refused [invalid] elsewhere"`
	Confirm                bool   `json:"confirm,omitempty" jsonschema:"must be true: widening a permission hands members what a manager holds"`
	DryRun                 bool   `json:"dry_run,omitempty" jsonschema:"return the patch body, its mask and the settings as they stand without changing them"`
}

// PermissionChangeOutput is one permission the call changed.
type PermissionChangeOutput struct {
	Setting string  `json:"setting" jsonschema:"the setting, as the input names it"`
	From    *string `json:"from" jsonschema:"what it was before this call: managers, assistant_managers, members or nobody; null when Google did not say"`
	To      string  `json:"to" jsonschema:"what it was set to"`
}

// AdminUpdateSpacePermissionsOutput is what the patch asked for.
type AdminUpdateSpacePermissionsOutput struct {
	SpaceID         string                   `json:"space_id" jsonschema:"the space that was changed"`
	Changes         []PermissionChangeOutput `json:"changes" jsonschema:"each setting the call changed, with what it was before as the widest role that held it. Passing from back undoes a change, except from nobody, which cannot be set"`
	UpdateMask      string                   `json:"update_mask" jsonschema:"the permission settings the patch targeted"`
	DryRun          bool                     `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload         `json:"rendered_payload" jsonschema:"on a dry run, the exact patch body; null on a real change"`
}

// AdminAddMemberInput adds one person to any named space.
type AdminAddMemberInput struct {
	SpaceID          string `json:"space_id" jsonschema:"the space to add them to, spaces/{id}, from admin_search_spaces"`
	SpaceDisplayName string `json:"space_display_name" jsonschema:"what the space is called, as admin_search_spaces or admin_get_space shows it, so the prompt that approves this call names it. Checked against the space before anything is written; a call naming another space is refused [stale]"`
	UserEmail        string `json:"user_email" jsonschema:"their email address, someone in your organization; search_people turns a name into one. Your own account is refused, by any of its addresses"`
	Confirm          bool   `json:"confirm,omitempty" jsonschema:"must be true: whoever is added can read what is already in the space, and removing them does not take that back"`
	DryRun           bool   `json:"dry_run,omitempty" jsonschema:"return the request body without adding anyone"`
}

func registerAdmin(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "admin_search_spaces",
		Description: "Search every named space in the Workspace as an administrator, spaces you are not in " +
			"included. Match words in the name, or omit display_name to page through them all; filter by when a " +
			"space was created or last active and by history, and order by member count, activity or age. Each " +
			"row carries the space's description, so spaces without one stand out. Group chats and direct " +
			"messages cannot be searched this way. Keep paging while next_page_token is non-null. search_spaces " +
			"searches the spaces you are in.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminSearchSpacesInput) (*mcp.CallToolResult, AdminSearchSpacesOutput, error) {
		got, err := d.Service.SearchSpaces(ctx, service.SearchSpacesInput{
			DisplayName:         in.DisplayName,
			ExternalUserAllowed: in.ExternalUserAllowed,
			CreatedAfter:        in.CreatedAfter,
			CreatedBefore:       in.CreatedBefore,
			LastActiveAfter:     in.LastActiveAfter,
			LastActiveBefore:    in.LastActiveBefore,
			HistoryState:        in.HistoryState,
			OrderBy:             in.OrderBy,
			Limit:               in.Limit,
			PageToken:           in.PageToken,
		})
		if err != nil {
			return nil, AdminSearchSpacesOutput{}, err
		}
		out := AdminSearchSpacesOutput{
			Result:        make([]AdminSpaceSummaryOutput, 0, len(got.Spaces)),
			NextPageToken: nullable(got.NextPageToken),
			TotalSize:     got.TotalSize,
		}
		for _, sp := range got.Spaces {
			out.Result = append(out.Result, AdminSpaceSummaryOutput{
				SpaceID:        sp.Name,
				DisplayName:    sp.DisplayName,
				Description:    nullable(sp.Description),
				LastActiveTime: nullableTime(sp.LastActiveTime),
			})
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name: "admin_get_space",
		Description: "Read any space in the Workspace as an administrator, one you are not in included: its " +
			"name, description, guidelines, member counts and permission settings. Messages cannot be read with " +
			"administrator access. get_space reads a space you are in.",
		Kind: Read,
	}, getSpace(d))

	register(s, d, spec{
		Name: "admin_list_members",
		Description: "List the members of any space in the Workspace as an administrator, one you are not in " +
			"included. The rows are list_members' rows; Google lists no Chat apps under administrator access. " +
			"Default 50 entries; pass limit (1-200) to widen, and keep paging while next_page_token is non-null.",
		Kind: Read,
	}, listMembers(d))

	register(s, d, spec{
		Name: "admin_get_member",
		Description: "Read one membership of any space as an administrator: who or what it is, their role, and " +
			"whether they have joined. Check it before changing a role or removing someone: a membership can " +
			"name a Google Group, and removing that one removes everyone in the group from the space.",
		Kind: Read,
	}, getMember(d))

	register(s, d, spec{
		Name: "admin_update_space",
		Description: "Rename any named space in the Workspace, or change its description or guidelines, as an " +
			"administrator, a space you are not in included. Pass at least one. Only what you pass changes: " +
			"Google takes the description and the guidelines together, so the server reads the one you leave " +
			"alone and sends it back. The reply says what each changed field was before, so a change can be put " +
			"back. Members see that the organization's administrator made the change. Set dry_run to see the " +
			"patch body first. update_space edits a space you are in.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminUpdateSpaceInput) (*mcp.CallToolResult, AdminUpdateSpaceOutput, error) {
		got, err := d.Service.UpdateSpace(ctx, service.UpdateSpaceInput{
			Space: in.SpaceID, SpaceDisplayName: in.SpaceDisplayName,
			DisplayName: in.DisplayName, Description: in.Description, Guidelines: in.Guidelines,
			DryRun: in.DryRun,
		})
		if err != nil {
			return nil, AdminUpdateSpaceOutput{}, err
		}
		return nil, AdminUpdateSpaceOutput{
			SpaceID:             got.Space,
			DisplayName:         got.DisplayName,
			Description:         got.Description,
			Guidelines:          got.Guidelines,
			PreviousDisplayName: got.Previous.DisplayName,
			PreviousDescription: got.Previous.Description,
			PreviousGuidelines:  got.Previous.Guidelines,
			UpdateMask:          nullable(got.UpdateMask),
			DryRun:              got.DryRun,
			RenderedPayload:     rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "admin_update_space_permissions",
		Description: "Change who may manage members and groups, edit the space's details, turn history on or " +
			"off, mention everyone with @all, manage apps, manage webhooks and reply, in any named space, as an " +
			"administrator. Each setting takes managers, assistant_managers or members; a space's managers " +
			"always hold every permission. Pass only the settings to change, and confirm: true, since widening " +
			"one hands members what a manager holds. The reply says what each setting was before. admin_get_space " +
			"shows them all. Who may post and who may see the member list cannot be changed here.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminUpdateSpacePermissionsInput) (*mcp.CallToolResult, AdminUpdateSpacePermissionsOutput, error) {
		got, err := d.Service.UpdateSpacePermissions(ctx, service.UpdateSpacePermissionsInput{
			Space: in.SpaceID, SpaceDisplayName: in.SpaceDisplayName, DryRun: in.DryRun,
			Set: service.SpacePermissions{
				ManageMembersAndGroups: in.ManageMembersAndGroups,
				ModifySpaceDetails:     in.ModifySpaceDetails,
				ToggleHistory:          in.ToggleHistory,
				UseAtMentionAll:        in.UseAtMentionAll,
				ManageApps:             in.ManageApps,
				ManageWebhooks:         in.ManageWebhooks,
				ReplyMessages:          in.ReplyMessages,
			},
		})
		if err != nil {
			return nil, AdminUpdateSpacePermissionsOutput{}, err
		}
		out := AdminUpdateSpacePermissionsOutput{
			SpaceID:         got.Space,
			Changes:         make([]PermissionChangeOutput, 0, len(got.Changes)),
			UpdateMask:      got.UpdateMask,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}
		for _, c := range got.Changes {
			out.Changes = append(out.Changes, PermissionChangeOutput{Setting: c.Setting, From: nullable(c.From), To: c.To})
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name: "admin_update_member_role",
		Description: "Make a manager or an assistant manager of any space an ordinary member again, as an " +
			"administrator: role MEMBER. Making someone a manager is refused [blocked] here; " +
			"admin_promote_member does it. Set dry_run to see the request body without sending it.",
		Kind: Write,
	}, updateMemberRole(d))

	register(s, d, spec{
		Name: "admin_promote_member",
		Description: "Make someone a manager or an assistant manager of any space, as an administrator, such as " +
			"to give a space with no managers one. A manager can change the space's settings and remove people. " +
			"Pass confirm: true. Set dry_run to see the request body without sending it.",
		Kind: Destructive,
	}, promoteMember(d))

	register(s, d, spec{
		Name: "admin_remove_member",
		Description: "Remove someone, or a Google Group, from any space as an administrator. Someone removed has " +
			"to be invited again, and a removed group takes everyone in it out of the space; check with " +
			"admin_get_member first. Pass confirm: true. A membership that is already gone is reported as " +
			"removed: false, not as an error.",
		Kind: Destructive,
	}, removeMember(d))

	register(s, d, spec{
		Name: "admin_add_member",
		Description: "Add someone in your organization to any named space as an administrator. They can then " +
			"read what is already in the space. People only: a Google Group is refused, and so is your own " +
			"account by any of its addresses, since every tool here would then read the space. Google does not " +
			"let an administrator add someone from outside the organization. Pass confirm: true. Set dry_run to " +
			"check the call first.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AdminAddMemberInput) (*mcp.CallToolResult, AddMemberOutput, error) {
		got, err := d.Service.AdminAddMember(ctx, service.AddMemberInput{
			Space: in.SpaceID, SpaceDisplayName: in.SpaceDisplayName, Email: in.UserEmail, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, AddMemberOutput{}, err
		}
		return nil, addedMember(got), nil
	})

	register(s, d, spec{
		Name: "admin_delete_space",
		Description: "Delete any named space in the Workspace as an administrator, and with it every message " +
			"and membership in it, for everyone. Google always cascades and there is no undo. Pass the space's " +
			"display_name, as admin_get_space reports it, and confirm: true; the space is read first and the " +
			"delete refused [stale] when it is called something else. Direct messages and group chats are " +
			"refused. delete_space deletes a space you manage.",
		Kind:   Destructive,
		Marked: true,
	}, deleteSpace(d))
}

// Render is a page of an administrator's search. Every row is a named
// space, so the type the ordinary rows carry is left out.
func (o AdminSearchSpacesOutput) Render() string {
	header := count(len(o.Result), "space", "spaces")
	if o.TotalSize > len(o.Result) {
		header += " of about " + strconv.Itoa(o.TotalSize)
	}
	return block(listing(header, rows(o.Result)), more(o.NextPageToken))
}

// Render is one row of an administrator's search. A space with no
// description says so, since finding those is what the field is for.
func (o AdminSpaceSummaryOutput) Render() string {
	description := "no description"
	if o.Description != nil {
		description = "description: " + *o.Description
	}
	return meta(o.SpaceID, o.DisplayName, stamp("active", o.LastActiveTime), description)
}

// Render says what an administrator's edit changed, and from what.
func (o AdminUpdateSpaceOutput) Render() string {
	changed := block(
		change("name", o.PreviousDisplayName, o.DisplayName),
		change("description", o.PreviousDescription, o.Description),
		change("guidelines", o.PreviousGuidelines, o.Guidelines),
	)
	if o.DryRun {
		return block(previewBody("update "+o.SpaceID), changed)
	}
	return block(meta("Updated "+o.SpaceID, labeled("mask", deref(o.UpdateMask))), changed)
}

// change is one field an edit set: from what it was to what it is. Nil
// is a field the edit left alone, and renders as nothing.
func change(field string, from, to *string) string {
	if to == nil {
		return ""
	}
	return fromTo(field, from, *to, quotedOrEmpty)
}

// fromTo is "field: before -> after", with "unknown" for a before
// Google did not report.
func fromTo(field string, from *string, to string, show func(string) string) string {
	before := "unknown"
	if from != nil {
		before = show(*from)
	}
	return field + ": " + before + " -> " + show(to)
}

// quotedOrEmpty is a value in quotes, or "(empty)" when there is none,
// so a cleared field is not read as a missing one.
func quotedOrEmpty(v string) string {
	if v == "" {
		return "(empty)"
	}
	return `"` + v + `"`
}

// Render says what each permission was set to, and from what.
func (o AdminUpdateSpacePermissionsOutput) Render() string {
	settings := listing("Settings", rows(o.Changes))
	if o.DryRun {
		return block(previewBody("change the permissions of "+o.SpaceID), settings)
	}
	return block(meta("Updated "+o.SpaceID, labeled("mask", o.UpdateMask)), settings)
}

// Render is one permission's change.
func (o PermissionChangeOutput) Render() string {
	return fromTo(o.Setting, o.From, o.To, func(v string) string { return v })
}
