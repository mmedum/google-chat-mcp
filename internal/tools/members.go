package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// MemberOutput is one row of a space's membership.
type MemberOutput struct {
	Kind string `json:"kind" jsonschema:"HUMAN for a person, GROUP for a Google Group whose members are in the space through it"`
	// The membership and the member are different resources, and the
	// three tools that change a membership need the first one.
	MembershipName string  `json:"membership_name" jsonschema:"the membership's own resource name, spaces/{space}/members/{member}. get_member, update_member_role and remove_member take THIS, not member_id"`
	MemberID       string  `json:"member_id" jsonschema:"who the membership is for: users/{id} for a person, groups/{id} for a group"`
	DisplayName    *string `json:"display_name" jsonschema:"what to call them, or null when Google gave no name"`
	Email          *string `json:"email" jsonschema:"the person's email address; null for a group and for anyone the People API could not resolve"`
	Role           string  `json:"role" jsonschema:"ROLE_MEMBER, ROLE_MANAGER, ROLE_ASSISTANT_MANAGER, or ROLE_UNSPECIFIED for a role this server does not recognise"`
	State          string  `json:"state" jsonschema:"JOINED for someone who is in the space, INVITED for someone who has been asked and has not accepted, NOT_A_MEMBER, or MEMBERSHIP_STATE_UNSPECIFIED"`
	Affiliation    string  `json:"affiliation,omitempty" jsonschema:"INTERNAL for someone in your organisation, EXTERNAL for a guest, MANAGED_EXTERNAL for a guest their own organisation manages. Empty when Google said nothing. A space with external members is one to think about before posting in"`
}

// memberRow shapes one membership for the model. get_member returns the
// same shape as a row of list_members, so the two are interchangeable.
func memberRow(m service.Member) MemberOutput {
	return MemberOutput{
		Kind:           string(m.Kind),
		MembershipName: m.Membership,
		MemberID:       m.Name,
		DisplayName:    nullable(m.DisplayName),
		Email:          nullable(m.Email),
		Role:           m.Role,
		State:          m.State,
		Affiliation:    m.Affiliation,
	}
}

// MemberListOutput wraps the rows.
type MemberListOutput struct {
	Result []MemberOutput `json:"result" jsonschema:"who is in the space"`
}

// ListMembersInput selects a page of a space's members.
type ListMembersInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space, spaces/{id}"`
	Limit   int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 200; default 50"`
}

func registerMembers(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "list_members",
		Description: "List the members of a Google Chat space: people, Google Groups, and anyone invited who has " +
			"not joined yet. Every row carries kind (HUMAN or GROUP) and state (JOINED, INVITED, NOT_A_MEMBER), so " +
			"check state before reporting someone as present. People come back with their email resolved through " +
			"the People API; a Google Group has neither an email nor a name of its own, only groups/{id}. Default " +
			"50 entries; pass limit (1-200) to widen.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListMembersInput) (*mcp.CallToolResult, MemberListOutput, error) {
		members, err := d.Service.ListMembers(ctx, service.ListMembersInput{Space: in.SpaceID, Limit: in.Limit})
		if err != nil {
			return nil, MemberListOutput{}, err
		}
		out := MemberListOutput{Result: make([]MemberOutput, 0, len(members))}
		for _, m := range members {
			out.Result = append(out.Result, memberRow(m))
		}
		return nil, out, nil
	})
}

// AddMemberInput invites one person into a space.
type AddMemberInput struct {
	SpaceID   string `json:"space_id" jsonschema:"the space to invite them into, spaces/{id}"`
	UserEmail string `json:"user_email,omitempty" jsonschema:"their email address; search_people turns a name into one. Pass this or group_name, not both"`
	GroupName string `json:"group_name,omitempty" jsonschema:"a Google Group to add, groups/{id}. The id comes from the Cloud Identity API, which this server cannot query, so bring it with you. Google allows a group only in a named space, never a group chat or a direct message"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"return the request body without inviting anyone"`
}

// GetMemberInput names one membership.
type GetMemberInput struct {
	MembershipName string `json:"membership_name" jsonschema:"the membership to read, spaces/{space}/members/{member}, from list_members"`
}

// UpdateMemberRoleInput names a membership and the role it should hold.
type UpdateMemberRoleInput struct {
	MembershipName string `json:"membership_name" jsonschema:"the membership to change, spaces/{space}/members/{member}, from list_members"`
	Role           string `json:"role" jsonschema:"MEMBER, MANAGER or ASSISTANT_MANAGER. A manager can change a space's settings and remove people, so promoting someone gives away control of the space"`
	DryRun         bool   `json:"dry_run,omitempty" jsonschema:"return the request body without changing anything"`
}

// UpdateMemberRoleOutput is the membership as it now stands.
type UpdateMemberRoleOutput struct {
	MembershipName  string           `json:"membership_name" jsonschema:"the membership that was named"`
	Role            string           `json:"role" jsonschema:"the role Google recorded, in its own spelling; after a dry run, the role that was asked for"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real change"`
}

// AddMemberOutput is the membership that was made.
type AddMemberOutput struct {
	MembershipName  *string          `json:"membership_name" jsonschema:"the new membership's resource name; remove_member takes it. Null after a dry run"`
	SpaceID         string           `json:"space_id" jsonschema:"the space they were invited into"`
	UserEmail       string           `json:"user_email" jsonschema:"who was invited; empty when a group was added"`
	GroupName       string           `json:"group_name,omitempty" jsonschema:"the group that was added, when it was a group"`
	Role            string           `json:"role,omitempty" jsonschema:"the role Google recorded, in its own spelling; empty when Google said nothing, which means an ordinary member"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nobody was invited"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real invite"`
}

// RemoveMemberInput names a membership to delete.
type RemoveMemberInput struct {
	MembershipName string `json:"membership_name" jsonschema:"the membership to remove, spaces/{space}/members/{member}, from list_members"`
	DryRun         bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without removing anyone"`
}

// RemoveMemberOutput says whether anything was removed.
type RemoveMemberOutput struct {
	MembershipName string `json:"membership_name" jsonschema:"the membership that was named"`
	Removed        bool   `json:"removed" jsonschema:"true when this call removed the membership; false when it was already gone, and after a dry run. Neither is an error. A membership you may not remove is reported as an error instead, so false never hides a refusal"`
	DryRun         bool   `json:"dry_run" jsonschema:"true when nobody was removed"`
}

func registerMemberWrites(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "add_member",
		Description: "Invite someone into a space by email address, or add a Google Group. Someone already in " +
			"the space is reported as an error rather than as a success, because the membership that exists " +
			"belongs to whoever invited them first. Everyone joins as an ordinary member: Google ignores a role " +
			"given here, silently, so use update_member_role afterwards to make someone a manager. Set dry_run " +
			"to see the request body without inviting anyone.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AddMemberInput) (*mcp.CallToolResult, AddMemberOutput, error) {
		got, err := d.Service.AddMember(ctx, service.AddMemberInput{
			Space: in.SpaceID, Email: in.UserEmail, Group: in.GroupName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, AddMemberOutput{}, err
		}
		return nil, AddMemberOutput{
			MembershipName:  nullable(got.Name),
			SpaceID:         got.Space,
			UserEmail:       got.Email,
			GroupName:       got.Group,
			Role:            got.Role,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "get_member",
		Description: "Read one membership: who or what it is, their role, and whether they have joined. " +
			"list_members answers who is in a space; this answers what a membership is, which is what to check " +
			"before changing a role or removing someone — a membership can name a Google Group, and removing that " +
			"one removes everyone in the group from the space.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMemberInput) (*mcp.CallToolResult, MemberOutput, error) {
		got, err := d.Service.GetMember(ctx, in.MembershipName)
		if err != nil {
			return nil, MemberOutput{}, err
		}
		return nil, memberRow(*got), nil
	})

	register(s, d, spec{
		Name: "update_member_role",
		Description: "Change what someone may do in a space: MEMBER, MANAGER or ASSISTANT_MANAGER. A manager can " +
			"change the space's settings and remove people, so promoting someone gives away control of the space " +
			"and demoting the last manager may leave nobody able to administer it. Role is the only thing this " +
			"changes. Set dry_run to see the request body without sending it.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateMemberRoleInput) (*mcp.CallToolResult, UpdateMemberRoleOutput, error) {
		got, err := d.Service.UpdateMemberRole(ctx, service.UpdateMemberRoleInput{
			Membership: in.MembershipName, Role: in.Role, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, UpdateMemberRoleOutput{}, err
		}
		return nil, UpdateMemberRoleOutput{
			MembershipName:  got.Name,
			Role:            got.Role,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "remove_member",
		Description: "Remove someone from a space by their membership resource name, which list_members gives you. " +
			"Check what the membership is first: a membership can name a Google Group, and removing that one " +
			"removes everyone in the group from the space. list_members reports the kind. " +
			"A membership that is already gone reports removed false rather than failing, so a repeat is safe; a " +
			"membership you may not remove is reported as an error instead. There " +
			"is no by-email shape: the People API stays silent about people the caller cannot see, so a miss would " +
			"read as already gone while the person is still in the space.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveMemberInput) (*mcp.CallToolResult, RemoveMemberOutput, error) {
		got, err := d.Service.RemoveMember(ctx, service.RemoveMemberInput{
			Membership: in.MembershipName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, RemoveMemberOutput{}, err
		}
		return nil, RemoveMemberOutput{
			MembershipName: got.Name,
			Removed:        got.Removed,
			DryRun:         got.DryRun,
		}, nil
	})
}
