package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/service"
)

// Output types are deliberate shadows of the wire types, not embeds of
// them. The duplication buys an explicit allow-list: a field Google adds
// upstream cannot reach the model without someone deciding it should.
//
// Their field names come from testdata/schemas-baseline.json and must not
// drift. A rename is a breaking change to every caller, and the schema
// diff fails on one.

// WhoamiOutput is the account behind the stored credentials.
type WhoamiOutput struct {
	UserSub     string `json:"user_sub" jsonschema:"the account's stable Google subject id"`
	Email       string `json:"email" jsonschema:"the account's email address"`
	DisplayName string `json:"display_name" jsonschema:"the account's display name"`
	PictureURL  string `json:"picture_url,omitempty" jsonschema:"URL of the account's profile picture"`
}

// SpaceSummaryOutput is one row of a space listing.
type SpaceSummaryOutput struct {
	SpaceID     string `json:"space_id" jsonschema:"the space's resource name, spaces/{id}; pass this to other tools"`
	Type        string `json:"type" jsonschema:"SPACE, DIRECT_MESSAGE, GROUP_CHAT, or SPACE_TYPE_UNSPECIFIED for a kind this server does not recognise"`
	DisplayName string `json:"display_name" jsonschema:"what to call the space; a direct message or group chat has no name of its own, so a label is supplied"`
}

// SpaceDetailOutput is one space in full.
type SpaceDetailOutput struct {
	SpaceID     string `json:"space_id" jsonschema:"the space's resource name, spaces/{id}"`
	Type        string `json:"type" jsonschema:"SPACE, DIRECT_MESSAGE, GROUP_CHAT, or SPACE_TYPE_UNSPECIFIED for a kind this server does not recognise"`
	DisplayName string `json:"display_name" jsonschema:"what to call the space; a direct message or group chat has no name of its own, so a label is supplied"`
	// The two booleans are pointers because Google omits them far more
	// often than it sends false, and reporting false would assert
	// something it never said.
	SingleUserBotDM     *bool      `json:"single_user_bot_dm" jsonschema:"true when the space is a direct message with a Chat app rather than a person; null when Google did not say"`
	ExternalUserAllowed *bool      `json:"external_user_allowed" jsonschema:"true when people outside the organisation may join; null when Google did not say"`
	CreateTime          *time.Time `json:"create_time" jsonschema:"when the space was created, RFC 3339 in UTC; null when Google did not say"`
}

// GetSpaceInput names one space.
type GetSpaceInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space, spaces/{id}, from list_spaces"`
}

// ListSpacesInput narrows a space listing.
type ListSpacesInput struct {
	SpaceType string `json:"space_type,omitempty" jsonschema:"narrow to SPACE, DIRECT_MESSAGE or GROUP_CHAT; omit for every kind"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 200; default 50"`
}

// ListSpacesOutput wraps the rows.
//
// The single `result` field is the released wire shape: MCP requires an
// object for structured content, so a tool returning a list has always
// been wrapped. Renaming it would break every caller.
type ListSpacesOutput struct {
	Result []SpaceSummaryOutput `json:"result" jsonschema:"the spaces the account belongs to"`
}

// SearchSpacesInput narrows a space search.
type SearchSpacesInput struct {
	DisplayName         string `json:"display_name,omitempty" jsonschema:"match spaces whose name begins with these words; matching is by word prefix, not substring, so 'eng' finds 'Engineering' but 'ing' does not. Omit to list every named space the other filters allow"`
	ExternalUserAllowed *bool  `json:"external_user_allowed,omitempty" jsonschema:"true for spaces that admit people outside the organisation, false for those that do not; omit for both"`
	UseAdminAccess      bool   `json:"use_admin_access,omitempty" jsonschema:"search every space in the Workspace rather than the ones you can see. Needs Workspace admin rights and the admin toolset, which is off unless GCM_TOOLSETS names it"`
	Limit               int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 50"`
	PageToken           string `json:"page_token,omitempty" jsonschema:"continue a previous search; Google returns a token only with admin access"`
}

// SearchSpacesOutput wraps the matches.
type SearchSpacesOutput struct {
	Result        []SpaceSummaryOutput `json:"result" jsonschema:"the named spaces that matched"`
	NextPageToken string               `json:"next_page_token,omitempty" jsonschema:"pass back as page_token for the next page; only ever set under admin access"`
	TotalSize     int                  `json:"total_size,omitempty" jsonschema:"how many spaces matched in total, an estimate above 10000; only ever set under admin access"`
}

// FindGroupChatsInput names the people the chat must hold.
type FindGroupChatsInput struct {
	MemberEmails []string `json:"member_emails" jsonschema:"the other people's email addresses, 1 to 49. You are always included, so pass only the others"`
	Limit        int      `json:"limit,omitempty" jsonschema:"how many to return, 1 to 30; default 10"`
	PageToken    string   `json:"page_token,omitempty" jsonschema:"continue a previous call"`
}

// FindGroupChatsOutput wraps the matches.
type FindGroupChatsOutput struct {
	Result        []SpaceSummaryOutput `json:"result" jsonschema:"the group chats holding exactly you and the people named"`
	NextPageToken string               `json:"next_page_token,omitempty" jsonschema:"pass back as page_token for the next page"`
}

func registerSpaces(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "get_space",
		Description: "Fetch one Google Chat space by its resource name. Use it to identify an unnamed direct " +
			"message or group chat, or to confirm a space's metadata before writing to it.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetSpaceInput) (*mcp.CallToolResult, SpaceDetailOutput, error) {
		got, err := d.Service.GetSpace(ctx, in.SpaceID)
		if err != nil {
			return nil, SpaceDetailOutput{}, err
		}
		return nil, spaceDetail(got), nil
	})

	register(s, d, spec{
		Name: "whoami",
		Description: "Return the authenticated Google user's identity (subject id, email, display name). Useful as " +
			"a first-call smoke test, and for resolving who \"me\" is before filtering messages by sender. Reads " +
			"from the OpenID Connect userinfo endpoint.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, WhoamiOutput, error) {
		id, err := d.Service.Whoami(ctx)
		if err != nil {
			return nil, WhoamiOutput{}, err
		}
		return nil, WhoamiOutput{
			UserSub:     id.UserSub,
			Email:       id.Email,
			DisplayName: id.DisplayName,
			PictureURL:  id.PictureURL,
		}, nil
	})

	register(s, d, spec{
		Name: "list_spaces",
		Description: "List Google Chat spaces (direct messages, group chats, named spaces) the authenticated user " +
			"belongs to. Defaults to 50 entries; pass limit (1-200) to widen and space_type " +
			"('SPACE' | 'DIRECT_MESSAGE' | 'GROUP_CHAT') to narrow. Use this to find a space's resource name " +
			"before reading its messages.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListSpacesInput) (*mcp.CallToolResult, ListSpacesOutput, error) {
		got, err := d.Service.ListSpaces(ctx, service.ListSpacesInput{
			Kind:  service.SpaceKind(in.SpaceType),
			Limit: in.Limit,
		})
		if err != nil {
			return nil, ListSpacesOutput{}, err
		}
		return nil, ListSpacesOutput{Result: spaceSummaries(got.Spaces)}, nil
	})

	register(s, d, spec{
		Name: "search_spaces",
		Description: "Search named Google Chat spaces by display name, including spaces you are not a member of. " +
			"Use it to find a space to join or read; use list_spaces for the ones you are already in, and " +
			"find_group_chats for a group chat, which this cannot return. Matching is by word prefix. Without " +
			"admin access Google returns a single page of up to 100 and no total.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchSpacesInput) (*mcp.CallToolResult, SearchSpacesOutput, error) {
		if in.UseAdminAccess && !d.Config.Enabled(config.ToolsetAdmin) {
			// The scope for it is requested at login only when the
			// toolset is on, so without it Google would refuse the call
			// and the refusal would read as a missing permission.
			return nil, SearchSpacesOutput{}, service.Invalidf(
				"use_admin_access needs the admin toolset. Set %sTOOLSETS=all,admin and run `google-chat-mcp login` "+
					"again to grant the administrator scope.", config.EnvPrefix)
		}
		got, err := d.Service.SearchSpaces(ctx, service.SearchSpacesInput{
			DisplayName:         in.DisplayName,
			ExternalUserAllowed: in.ExternalUserAllowed,
			UseAdminAccess:      in.UseAdminAccess,
			Limit:               in.Limit,
			PageToken:           in.PageToken,
		})
		if err != nil {
			return nil, SearchSpacesOutput{}, err
		}
		return nil, SearchSpacesOutput{
			Result:        spaceSummaries(got.Spaces),
			NextPageToken: got.NextPageToken,
			TotalSize:     got.TotalSize,
		}, nil
	})

	register(s, d, spec{
		Name: "find_group_chats",
		Description: "Find the group chats whose members are exactly you and the people you name. Use it before " +
			"create_group_chat to reuse an existing chat rather than starting a second one with the same people. " +
			"A chat with one extra member does not match. For a one-to-one conversation use find_direct_message.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindGroupChatsInput) (*mcp.CallToolResult, FindGroupChatsOutput, error) {
		got, err := d.Service.FindGroupChats(ctx, service.FindGroupChatsInput{
			Emails:    in.MemberEmails,
			Limit:     in.Limit,
			PageToken: in.PageToken,
		})
		if err != nil {
			return nil, FindGroupChatsOutput{}, err
		}
		return nil, FindGroupChatsOutput{
			Result:        spaceSummaries(got.Spaces),
			NextPageToken: got.NextPageToken,
		}, nil
	})
}

// spaceSummaries shapes a page of spaces for the model.
func spaceSummaries(in []service.SpaceSummary) []SpaceSummaryOutput {
	out := make([]SpaceSummaryOutput, 0, len(in))
	for _, sp := range in {
		out = append(out, SpaceSummaryOutput{
			SpaceID:     sp.Name,
			Type:        string(sp.Kind),
			DisplayName: sp.DisplayName,
		})
	}
	return out
}

// spaceDetail shapes a space for the model. The resource template
// returns the same shape, which is what makes it interchangeable with
// the tool.
func spaceDetail(got *service.SpaceDetails) SpaceDetailOutput {
	return SpaceDetailOutput{
		SpaceID:             got.Name,
		Type:                string(got.Kind),
		DisplayName:         got.DisplayName,
		SingleUserBotDM:     got.SingleUserBotDM,
		ExternalUserAllowed: got.ExternalUserAllowed,
		CreateTime:          nullableTime(got.CreateTime),
	}
}

// FindDirectMessageInput names the other person.
type FindDirectMessageInput struct {
	UserEmail string `json:"user_email" jsonschema:"the other person's email address; search_people turns a name into one"`
}

// FindDirectMessageOutput is the space to write in.
type FindDirectMessageOutput struct {
	SpaceID string `json:"space_id" jsonschema:"the direct message's resource name; pass it to send_message"`
}

// CreateGroupChatInput is a new unnamed group conversation.
type CreateGroupChatInput struct {
	MemberEmails []string `json:"member_emails" jsonschema:"2 to 20 email addresses, not counting you; Google adds the signed-in account itself"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"return the request body without creating anything"`
}

// CreateSpaceInput is a new named space.
type CreateSpaceInput struct {
	DisplayName  string   `json:"display_name" jsonschema:"what to call the space, 1 to 128 characters"`
	MemberEmails []string `json:"member_emails,omitempty" jsonschema:"up to 20 email addresses, not counting you; Google adds the signed-in account itself. Omit it for a space of your own, which you can invite people to later with add_member"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"return the request body without creating anything"`
}

// CreateGroupChatOutput is the group chat that was made.
//
// It carries no display name, unlike the named-space create: Google
// gives a group chat none, and a field that is always null teaches the
// model nothing.
type CreateGroupChatOutput struct {
	SpaceID         *string          `json:"space_id" jsonschema:"the new group chat's resource name; null after a dry run, because Google assigns it"`
	MemberCount     int              `json:"member_count" jsonschema:"how many people were asked for, not counting you"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was created"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real create"`
}

// CreateSpaceOutput is the named space that was made.
type CreateSpaceOutput struct {
	SpaceID         *string          `json:"space_id" jsonschema:"the new space's resource name; null after a dry run, because Google assigns it"`
	DisplayName     string           `json:"display_name" jsonschema:"what it is called"`
	MemberCount     int              `json:"member_count" jsonschema:"how many people were asked for, not counting you"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was created"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real create"`
}

// UpdateSpaceInput is an edit of a space's name or description.
type UpdateSpaceInput struct {
	SpaceID     string  `json:"space_id" jsonschema:"the space to edit, spaces/{id}"`
	DisplayName *string `json:"display_name,omitempty" jsonschema:"a new name, 1 to 128 characters; omit to leave it alone"`
	Description *string `json:"description,omitempty" jsonschema:"a new description, up to 150 characters; omit to leave it alone. Pass an empty string to clear it"`
	DryRun      bool    `json:"dry_run,omitempty" jsonschema:"return the patch body and its mask without applying them"`
}

// UpdateSpaceOutput is what the edit asked for.
type UpdateSpaceOutput struct {
	SpaceID         string           `json:"space_id" jsonschema:"the space that was edited"`
	DisplayName     *string          `json:"display_name" jsonschema:"the name that was set, or null when the call left it alone"`
	Description     *string          `json:"description" jsonschema:"the description that was set, or null when the call left it alone"`
	UpdateMask      *string          `json:"update_mask" jsonschema:"which top-level fields the patch targeted"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact patch body; null on a real edit"`
}

func registerSpaceWrites(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "find_direct_message",
		Description: "Find the direct message with one person, and start one when there is none yet. Returns the " +
			"space to pass to send_message. A newly started direct message is empty, so the other person sees nothing " +
			"until a message is posted in it.",
		Kind: WriteIdempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in FindDirectMessageInput) (*mcp.CallToolResult, FindDirectMessageOutput, error) {
		space, err := d.Service.FindDirectMessage(ctx, in.UserEmail)
		if err != nil {
			return nil, FindDirectMessageOutput{}, err
		}
		return nil, FindDirectMessageOutput{SpaceID: space}, nil
	})

	register(s, d, spec{
		Name: "create_group_chat",
		Description: "Start an unnamed group conversation with 2 to 20 people. Leave yourself out of member_emails: " +
			"Google adds the signed-in account. A group chat cannot have a name; use create_space when you need one. " +
			"Set dry_run to see the request body without creating anything.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateGroupChatInput) (*mcp.CallToolResult, CreateGroupChatOutput, error) {
		got, err := d.Service.CreateGroupChat(ctx, service.CreateSpaceInput{
			MemberEmails: in.MemberEmails, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, CreateGroupChatOutput{}, err
		}
		return nil, CreateGroupChatOutput{
			SpaceID:         nullable(got.Name),
			MemberCount:     got.MemberCount,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "create_space",
		Description: "Create a named space, with up to 20 initial members or none at all. Leave yourself out of " +
			"member_emails: Google adds the signed-in account. A space of your own is fine — invite people later " +
			"with add_member. Set dry_run to see the request body without creating anything.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateSpaceInput) (*mcp.CallToolResult, CreateSpaceOutput, error) {
		got, err := d.Service.CreateSpace(ctx, service.CreateSpaceInput{
			DisplayName: in.DisplayName, MemberEmails: in.MemberEmails, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, CreateSpaceOutput{}, err
		}
		return nil, CreateSpaceOutput{
			SpaceID:         nullable(got.Name),
			DisplayName:     got.DisplayName,
			MemberCount:     got.MemberCount,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "update_space",
		Description: "Rename a space or change its description. Pass at least one of the two; an empty edit is " +
			"refused. Editing the description clears the space's guidelines, because Google's mask covers both at " +
			"once, so edit a space that has guidelines in the Chat interface instead. Set dry_run to see the patch " +
			"body first. Needs the restricted-tier chat.spaces scope.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateSpaceInput) (*mcp.CallToolResult, UpdateSpaceOutput, error) {
		got, err := d.Service.UpdateSpace(ctx, service.UpdateSpaceInput{
			Space: in.SpaceID, DisplayName: in.DisplayName, Description: in.Description, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, UpdateSpaceOutput{}, err
		}
		return nil, UpdateSpaceOutput{
			SpaceID:         got.Space,
			DisplayName:     got.DisplayName,
			Description:     got.Description,
			UpdateMask:      nullable(got.UpdateMask),
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})
}
