package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/service"
)

// AvailabilityOutput is your own presence.
type AvailabilityOutput struct {
	State             string     `json:"state" jsonschema:"ACTIVE, IDLE, AWAY, DO_NOT_DISTURB, or STATE_UNSPECIFIED for a state this server does not recognise. IDLE is Google's to report, not yours to set"`
	StatusText        string     `json:"status_text,omitempty" jsonschema:"the custom status beside your name, if you have one"`
	StatusEmoji       string     `json:"status_emoji,omitempty" jsonschema:"the emoji beside that status"`
	StatusExpires     *time.Time `json:"status_expires" jsonschema:"when the custom status lapses, RFC 3339 in UTC; null when it does not"`
	DoNotDisturbUntil *time.Time `json:"do_not_disturb_until" jsonschema:"when do-not-disturb ends, RFC 3339 in UTC; null when you are not in it"`
}

// SetAvailabilityInput asks for a presence state.
type SetAvailabilityInput struct {
	State   string `json:"state" jsonschema:"ACTIVE, AWAY or DO_NOT_DISTURB. IDLE cannot be set: Google decides it"`
	Minutes int    `json:"minutes,omitempty" jsonschema:"how long do-not-disturb should last. Pass this or until, and only with DO_NOT_DISTURB, which Google requires an expiry for and caps at a year"`
	Until   string `json:"until,omitempty" jsonschema:"when do-not-disturb should end, RFC 3339. Pass this or minutes"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without changing anything"`
}

// SetAvailabilityOutput is your presence as it now stands.
type SetAvailabilityOutput struct {
	AvailabilityOutput
	DryRun bool `json:"dry_run" jsonschema:"true when nothing was changed"`
}

// SetCustomStatusInput is the text and emoji beside your name.
type SetCustomStatusInput struct {
	Text    string `json:"text,omitempty" jsonschema:"what the status says, up to 64 characters. Required unless clear is set"`
	Emoji   string `json:"emoji,omitempty" jsonschema:"one Unicode emoji beside the text. Google requires it alongside the text and refuses one of your organisation's custom emoji here. Required unless clear is set"`
	Minutes int    `json:"minutes,omitempty" jsonschema:"how long the status should last. Pass this or until; leave both out and it stays until you change it"`
	Until   string `json:"until,omitempty" jsonschema:"when the status should lapse, RFC 3339. Pass this or minutes"`
	Clear   bool   `json:"clear,omitempty" jsonschema:"remove the status you have instead of setting one. Takes no other argument"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"return the request body without changing anything"`
}

// SetCustomStatusOutput is your presence as it now stands.
type SetCustomStatusOutput struct {
	AvailabilityOutput
	Cleared         bool             `json:"cleared" jsonschema:"true when the status was removed rather than set"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null otherwise"`
}

// CustomEmojiOutput is one of your organisation's own emoji.
type CustomEmojiOutput struct {
	Name         string `json:"name" jsonschema:"the resource name, customEmojis/{emoji}; get_custom_emoji and delete_custom_emoji take this"`
	EmojiName    string `json:"emoji_name" jsonschema:"the :shortcode: form, which is what you type in a message"`
	TemporaryURI string `json:"temporary_uri,omitempty" jsonschema:"a link to the image that Google says is good for at least ten minutes. Not stored anywhere and not worth keeping"`
}

// ListCustomEmojisInput selects a page.
type ListCustomEmojisInput struct {
	MineOnly  bool   `json:"mine_only,omitempty" jsonschema:"only the emoji this account created"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 200; default 50"`
	PageToken string `json:"page_token,omitempty" jsonschema:"continue a previous call"`
}

// ListCustomEmojisOutput wraps the rows.
type ListCustomEmojisOutput struct {
	Result        []CustomEmojiOutput `json:"result" jsonschema:"the organisation's custom emoji"`
	NextPageToken string              `json:"next_page_token,omitempty" jsonschema:"pass back as page_token for the next page"`
}

// GetCustomEmojiInput names one.
type GetCustomEmojiInput struct {
	Name string `json:"name" jsonschema:"the emoji's resource name, customEmojis/{emoji}, from list_custom_emojis"`
}

// CreateCustomEmojiInput names the emoji and the image.
type CreateCustomEmojiInput struct {
	EmojiName string `json:"emoji_name" jsonschema:"the :shortcode: to type it by, colons included: lowercase letters, digits, hyphens and underscores, as in :party-parrot:"`
	ImagePath string `json:"image_path" jsonschema:"a .png, .jpg or .gif on this machine. Google wants it square, between 64 and 500 pixels, and under 256 KB — the file is read and sent inside the request, so the size limit is checked here first"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"check the name and the image without creating anything"`
}

// CreateCustomEmojiOutput is what was made.
type CreateCustomEmojiOutput struct {
	CustomEmojiOutput
	Bytes  int  `json:"bytes" jsonschema:"the image's size, reported so a dry run tells you whether it fits"`
	DryRun bool `json:"dry_run" jsonschema:"true when nothing was created"`
}

// DeleteCustomEmojiInput names one to remove.
type DeleteCustomEmojiInput struct {
	Name   string `json:"name" jsonschema:"the emoji's resource name, customEmojis/{emoji}"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without deleting anything"`
}

// DeleteCustomEmojiOutput says whether it went.
type DeleteCustomEmojiOutput struct {
	Name    string `json:"name" jsonschema:"the emoji that was named"`
	Deleted bool   `json:"deleted" jsonschema:"true only when this call deleted it. False can mean it was already gone"`
	DryRun  bool   `json:"dry_run" jsonschema:"true when nothing was deleted"`
}

// DeleteSpaceInput names a space to destroy.
type DeleteSpaceInput struct {
	SpaceID        string `json:"space_id" jsonschema:"the space to delete, spaces/{id}"`
	ConfirmSpaceID string `json:"confirm_space_id" jsonschema:"the same value as space_id. Deleting a space destroys every message and membership in it for everyone, and cannot be undone, so this call asks twice"`
	DryRun         bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without deleting anything"`
}

// DeleteSpaceOutput says whether the space is gone.
type DeleteSpaceOutput struct {
	SpaceID string `json:"space_id" jsonschema:"the space that was named"`
	Deleted bool   `json:"deleted" jsonschema:"true only when this call deleted it. False can mean it was already gone"`
	DryRun  bool   `json:"dry_run" jsonschema:"true when nothing was deleted"`
}

func registerPresence(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "get_availability",
		Description: "Read your own Chat presence: active, idle, away or do not disturb, with any custom " +
			"status. Yours only — Google's endpoint answers for the signed-in account and there is no way to " +
			"ask about anyone else.",
		Kind:    Read,
		Toolset: config.ToolsetAvailability,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, AvailabilityOutput, error) {
		got, err := d.Service.GetAvailability(ctx)
		if err != nil {
			return nil, AvailabilityOutput{}, err
		}
		return nil, availability(got), nil
	})

	register(s, d, spec{
		Name: "set_availability",
		Description: "Set your own Chat presence to ACTIVE, AWAY or DO_NOT_DISTURB. Do not disturb needs an " +
			"expiry — minutes or until — which Google requires and caps at a year. Other people see this, so it " +
			"is worth asking before silencing someone's notifications on their behalf.",
		Kind:    Write,
		Toolset: config.ToolsetAvailability,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SetAvailabilityInput) (*mcp.CallToolResult, SetAvailabilityOutput, error) {
		got, err := d.Service.SetAvailability(ctx, service.SetAvailabilityInput{
			State: in.State, Minutes: in.Minutes, Until: in.Until, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, SetAvailabilityOutput{}, err
		}
		return nil, SetAvailabilityOutput{
			AvailabilityOutput: availability(got.Presence),
			DryRun:             got.DryRun,
		}, nil
	})

	register(s, d, spec{
		Name: "set_custom_status",
		Description: "Set the text and emoji beside your name in Chat, or clear it. Google wants both together " +
			"and refuses one without the other; a custom emoji is not accepted here, only a Unicode one. An " +
			"expiry is optional, unlike do-not-disturb's. Everyone who sees you in Chat sees this.",
		Kind:    Write,
		Toolset: config.ToolsetAvailability,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SetCustomStatusInput) (*mcp.CallToolResult, SetCustomStatusOutput, error) {
		got, err := d.Service.SetCustomStatus(ctx, service.SetCustomStatusInput{
			Text: in.Text, Emoji: in.Emoji, Minutes: in.Minutes, Until: in.Until,
			Clear: in.Clear, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, SetCustomStatusOutput{}, err
		}
		return nil, SetCustomStatusOutput{
			AvailabilityOutput: availability(got.Presence),
			Cleared:            got.Cleared,
			DryRun:             got.DryRun,
			RenderedPayload:    rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "list_custom_emojis",
		Description: "List your organisation's own emoji, the ones people type as :shortcodes:. Custom emoji " +
			"exist only on Google Workspace accounts and only when an administrator has turned them on.",
		Kind:    Read,
		Toolset: config.ToolsetEmoji,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListCustomEmojisInput) (*mcp.CallToolResult, ListCustomEmojisOutput, error) {
		got, err := d.Service.ListCustomEmojis(ctx, service.ListCustomEmojisInput{
			MineOnly: in.MineOnly, Limit: in.Limit, PageToken: in.PageToken,
		})
		if err != nil {
			return nil, ListCustomEmojisOutput{}, err
		}
		out := ListCustomEmojisOutput{
			Result:        make([]CustomEmojiOutput, 0, len(got.Emojis)),
			NextPageToken: got.NextPageToken,
		}
		for _, e := range got.Emojis {
			out.Result = append(out.Result, customEmoji(e))
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name:        "get_custom_emoji",
		Description: "Read one custom emoji by its resource name, which list_custom_emojis reports.",
		Kind:        Read,
		Toolset:     config.ToolsetEmoji,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetCustomEmojiInput) (*mcp.CallToolResult, CustomEmojiOutput, error) {
		got, err := d.Service.GetCustomEmoji(ctx, in.Name)
		if err != nil {
			return nil, CustomEmojiOutput{}, err
		}
		return nil, customEmoji(*got), nil
	})

	register(s, d, spec{
		Name: "create_custom_emoji",
		Description: "Add a custom emoji to your organisation from an image on this machine. Everyone in the " +
			"organisation can then use it, so the name is worth agreeing first. The image must be a square " +
			"PNG, JPEG or GIF, 64 to 500 pixels, under 256 KB.",
		Kind:    Write,
		Toolset: config.ToolsetEmoji,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateCustomEmojiInput) (*mcp.CallToolResult, CreateCustomEmojiOutput, error) {
		got, err := d.Service.CreateCustomEmoji(ctx, service.CreateCustomEmojiInput{
			EmojiName: in.EmojiName, ImagePath: in.ImagePath, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, CreateCustomEmojiOutput{}, err
		}
		return nil, CreateCustomEmojiOutput{
			CustomEmojiOutput: customEmoji(*got.CustomEmoji),
			Bytes:             got.Bytes,
			DryRun:            got.DryRun,
		}, nil
	})

	register(s, d, spec{
		Name: "delete_custom_emoji",
		Description: "Remove a custom emoji from your organisation. It goes for everyone, and messages that " +
			"already use it lose the image.",
		Kind:    Destructive,
		Toolset: config.ToolsetEmoji,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteCustomEmojiInput) (*mcp.CallToolResult, DeleteCustomEmojiOutput, error) {
		got, err := d.Service.DeleteCustomEmoji(ctx, service.DeleteCustomEmojiInput{
			Name: in.Name, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, DeleteCustomEmojiOutput{}, err
		}
		return nil, DeleteCustomEmojiOutput{Name: got.Name, Deleted: got.Deleted, DryRun: got.DryRun}, nil
	})

	register(s, d, spec{
		Name: "delete_space",
		Description: "Delete a space, and with it every message and membership in it, for everyone. Google " +
			"always cascades and there is no undo. Pass confirm_space_id with the same value as space_id. " +
			"This is the most destructive call in this server: read the space first, and ask the person " +
			"before using it.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteSpaceInput) (*mcp.CallToolResult, DeleteSpaceOutput, error) {
		got, err := d.Service.DeleteSpace(ctx, service.DeleteSpaceInput{
			Space: in.SpaceID, Confirm: in.ConfirmSpaceID, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, DeleteSpaceOutput{}, err
		}
		return nil, DeleteSpaceOutput{SpaceID: got.Space, Deleted: got.Deleted, DryRun: got.DryRun}, nil
	})
}

// availability shapes a presence answer for the model.
func availability(got *service.Presence) AvailabilityOutput {
	return AvailabilityOutput{
		State:             got.State,
		StatusText:        got.StatusText,
		StatusEmoji:       got.StatusEmoji,
		StatusExpires:     nullableTime(got.StatusExpires),
		DoNotDisturbUntil: nullableTime(got.DoNotDisturbUntil),
	}
}

// customEmoji shapes one emoji for the model.
func customEmoji(e service.CustomEmoji) CustomEmojiOutput {
	return CustomEmojiOutput{Name: e.Name, EmojiName: e.EmojiName, TemporaryURI: e.TemporaryURI}
}
