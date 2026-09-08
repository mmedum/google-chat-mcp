package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// PinOutput is one pinned message.
type PinOutput struct {
	PinName   string `json:"pin_name" jsonschema:"the pin's resource name, spaces/{space}/messagePins/{pin}"`
	MessageID string `json:"message_id" jsonschema:"the message it points at; get_message reads it"`
}

// ListPinnedMessagesInput selects a page of a space's pins.
type ListPinnedMessagesInput struct {
	SpaceID   string `json:"space_id" jsonschema:"the space, spaces/{id}"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 50"`
	PageToken string `json:"page_token,omitempty" jsonschema:"continue a previous call"`
}

// ListPinnedMessagesOutput wraps the rows.
type ListPinnedMessagesOutput struct {
	Result        []PinOutput `json:"result" jsonschema:"the pinned messages, which are the ones the space treats as important"`
	NextPageToken string      `json:"next_page_token,omitempty" jsonschema:"pass back as page_token for the next page"`
}

// PinMessageInput names a message to pin or unpin.
type PinMessageInput struct {
	MessageName string `json:"message_name" jsonschema:"the message, spaces/{space}/messages/{message}. The space is taken from it"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without changing anything"`
}

// PinMessageOutput says what happened to the pin.
type PinMessageOutput struct {
	PinName   string `json:"pin_name" jsonschema:"the pin's resource name; empty after a dry run"`
	MessageID string `json:"message_id" jsonschema:"the message that was named"`
	Pinned    bool   `json:"pinned" jsonschema:"true only when this call pinned the message"`
	Unpinned  bool   `json:"unpinned" jsonschema:"true only when this call removed a pin. False can mean the message was not pinned"`
	DryRun    bool   `json:"dry_run" jsonschema:"true when nothing was changed"`
}

// ReadStateOutput is how far you have read.
type ReadStateOutput struct {
	Name         string     `json:"name" jsonschema:"the read state's own resource name"`
	LastReadTime *time.Time `json:"last_read_time" jsonschema:"when you last read here, RFC 3339 in UTC; null when Google has recorded none, which is what a space you have never opened looks like"`
}

// GetSpaceReadStateInput names a space.
type GetSpaceReadStateInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space, spaces/{id}"`
}

// GetThreadReadStateInput names a thread.
type GetThreadReadStateInput struct {
	SpaceID    string `json:"space_id" jsonschema:"the thread's space, spaces/{id}"`
	ThreadName string `json:"thread_name" jsonschema:"the thread, spaces/{space}/threads/{thread}, from a message's thread_id"`
}

// MarkSpaceReadInput names a space to mark read.
type MarkSpaceReadInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space, spaces/{id}"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without changing anything"`
}

// MarkSpaceUnreadInput names a space and where to rewind to.
type MarkSpaceUnreadInput struct {
	SpaceID  string `json:"space_id" jsonschema:"the space, spaces/{id}"`
	FromTime string `json:"from_time" jsonschema:"rewind your read mark to this time, RFC 3339: every top-level message after it becomes unread. get_messages reports each message's timestamp, so pass the timestamp of the message you want to come back to"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without changing anything"`
}

// MarkReadOutput is where the read mark now stands.
type MarkReadOutput struct {
	Name         string    `json:"name" jsonschema:"the read state's resource name; empty after a dry run"`
	LastReadTime time.Time `json:"last_read_time" jsonschema:"the mark Google recorded, which is not always what was asked for: marking read is pulled back to the newest message's own time"`
	Unread       bool      `json:"unread" jsonschema:"true when this call rewound the mark rather than clearing it"`
	DryRun       bool      `json:"dry_run" jsonschema:"true when nothing was changed"`
}

// NotificationSettingOutput is your notification choice for a space.
type NotificationSettingOutput struct {
	Name                string `json:"name" jsonschema:"the setting's own resource name"`
	NotificationSetting string `json:"notification_setting" jsonschema:"ALL for every new thread and mention, MAIN_CONVERSATIONS for the main conversation only, FOR_YOU for mentions and followed threads, OFF for none, or NOTIFICATION_SETTING_UNSPECIFIED for a value this server does not recognise"`
	MuteSetting         string `json:"mute_setting" jsonschema:"MUTED silences the space whatever notification_setting says; UNMUTED respects it"`
}

// GetSpaceNotificationSettingInput names a space.
type GetSpaceNotificationSettingInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space, spaces/{id}"`
}

// UpdateSpaceNotificationSettingInput changes one or both settings.
type UpdateSpaceNotificationSettingInput struct {
	SpaceID             string `json:"space_id" jsonschema:"the space, spaces/{id}"`
	NotificationSetting string `json:"notification_setting,omitempty" jsonschema:"ALL, MAIN_CONVERSATIONS, FOR_YOU or OFF; omit to leave it alone"`
	MuteSetting         string `json:"mute_setting,omitempty" jsonschema:"MUTED or UNMUTED; omit to leave it alone. MUTED silences the space whatever the other setting says"`
	DryRun              bool   `json:"dry_run,omitempty" jsonschema:"return the request body without changing anything"`
}

// UpdateSpaceNotificationSettingOutput is the setting as it now stands.
type UpdateSpaceNotificationSettingOutput struct {
	NotificationSettingOutput
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been sent; null on a real change"`
}

func registerSpaceState(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "list_pinned_messages",
		Description: "List the messages pinned in a space. A pin is how a space marks something worth keeping " +
			"to hand, so this is a good first read for what a space is about.",
		Kind:    Read,
		Toolset: config.ToolsetPins,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListPinnedMessagesInput) (*mcp.CallToolResult, ListPinnedMessagesOutput, error) {
		got, err := d.Service.ListPins(ctx, service.ListPinsInput{
			Space: in.SpaceID, Limit: in.Limit, PageToken: in.PageToken,
		})
		if err != nil {
			return nil, ListPinnedMessagesOutput{}, err
		}
		out := ListPinnedMessagesOutput{
			Result:        make([]PinOutput, 0, len(got.Pins)),
			NextPageToken: got.NextPageToken,
		}
		for _, p := range got.Pins {
			out.Result = append(out.Result, PinOutput{PinName: p.Name, MessageID: p.Message})
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name: "pin_message",
		Description: "Pin a message in its space, so everyone in the space sees it in the pinned list. " +
			"The space is taken from the message name. Pinning is visible to everyone.",
		Kind:    Write,
		Toolset: config.ToolsetPins,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PinMessageInput) (*mcp.CallToolResult, PinMessageOutput, error) {
		got, err := d.Service.PinMessage(ctx, service.PinInput{Message: in.MessageName, DryRun: in.DryRun})
		if err != nil {
			return nil, PinMessageOutput{}, err
		}
		return nil, pinResult(got), nil
	})

	register(s, d, spec{
		Name: "unpin_message",
		Description: "Remove a message's pin. Takes the message, not the pin: Google gives a pin the message's " +
			"own id. A message that was not pinned reports unpinned false rather than failing.",
		Kind:    Destructive,
		Toolset: config.ToolsetPins,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in PinMessageInput) (*mcp.CallToolResult, PinMessageOutput, error) {
		got, err := d.Service.UnpinMessage(ctx, service.PinInput{Message: in.MessageName, DryRun: in.DryRun})
		if err != nil {
			return nil, PinMessageOutput{}, err
		}
		return nil, pinResult(got), nil
	})

	register(s, d, spec{
		Name: "get_space_read_state",
		Description: "Read how far you have read in a space. This is your own state, not the space's: two " +
			"people reading the same space get different answers. Only top-level messages count, not thread replies.",
		Kind:    Read,
		Toolset: config.ToolsetReadState,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetSpaceReadStateInput) (*mcp.CallToolResult, ReadStateOutput, error) {
		got, err := d.Service.GetSpaceReadState(ctx, in.SpaceID)
		if err != nil {
			return nil, ReadStateOutput{}, err
		}
		return nil, readState(got), nil
	})

	register(s, d, spec{
		Name:        "get_thread_read_state",
		Description: "Read how far you have read in one thread. Your own state, as with a space.",
		Kind:        Read,
		Toolset:     config.ToolsetReadState,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetThreadReadStateInput) (*mcp.CallToolResult, ReadStateOutput, error) {
		got, err := d.Service.GetThreadReadState(ctx, in.SpaceID, in.ThreadName)
		if err != nil {
			return nil, ReadStateOutput{}, err
		}
		return nil, readState(got), nil
	})

	register(s, d, spec{
		Name: "mark_space_read",
		Description: "Mark a space as read up to now, clearing its unread badge for you and nobody else. " +
			"Google pulls the mark back to the newest message's own time, so the answer reports what it recorded.",
		Kind:    Write,
		Toolset: config.ToolsetReadState,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in MarkSpaceReadInput) (*mcp.CallToolResult, MarkReadOutput, error) {
		got, err := d.Service.MarkSpaceRead(ctx, service.MarkReadInput{Space: in.SpaceID, DryRun: in.DryRun})
		if err != nil {
			return nil, MarkReadOutput{}, err
		}
		return nil, markRead(got), nil
	})

	register(s, d, spec{
		Name: "mark_space_unread",
		Description: "Rewind your read mark in a space, so every top-level message after from_time is unread " +
			"again. Pass the timestamp of the message you want to come back to; get_messages reports it. " +
			"Affects you and nobody else.",
		Kind:    Write,
		Toolset: config.ToolsetReadState,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in MarkSpaceUnreadInput) (*mcp.CallToolResult, MarkReadOutput, error) {
		got, err := d.Service.MarkSpaceUnread(ctx, service.MarkReadInput{
			Space: in.SpaceID, From: in.FromTime, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, MarkReadOutput{}, err
		}
		return nil, markRead(got), nil
	})

	register(s, d, spec{
		Name: "get_space_notification_setting",
		Description: "Read what Chat tells you about a space: which messages notify you, and whether the " +
			"space is muted. Your own setting, not the space's.",
		Kind:    Read,
		Toolset: config.ToolsetSettings,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetSpaceNotificationSettingInput) (*mcp.CallToolResult, NotificationSettingOutput, error) {
		got, err := d.Service.GetSpaceNotificationSetting(ctx, in.SpaceID)
		if err != nil {
			return nil, NotificationSettingOutput{}, err
		}
		return nil, notificationSetting(got), nil
	})

	register(s, d, spec{
		Name: "update_space_notification_setting",
		Description: "Change what Chat tells you about a space, or mute it. Pass either setting or both; " +
			"whatever you leave out is left alone. Affects you and nobody else.",
		Kind:    Write,
		Toolset: config.ToolsetSettings,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateSpaceNotificationSettingInput) (*mcp.CallToolResult, UpdateSpaceNotificationSettingOutput, error) {
		got, err := d.Service.UpdateSpaceNotificationSetting(ctx, service.UpdateNotificationInput{
			Space: in.SpaceID, Notify: in.NotificationSetting, Mute: in.MuteSetting, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, UpdateSpaceNotificationSettingOutput{}, err
		}
		return nil, UpdateSpaceNotificationSettingOutput{
			NotificationSettingOutput: notificationSetting(got.NotificationSetting),
			DryRun:                    got.DryRun,
			RenderedPayload:           rendered(got.Rendered),
		}, nil
	})
}

// pinResult shapes a pin change for the model.
func pinResult(got *service.PinResult) PinMessageOutput {
	return PinMessageOutput{
		PinName:   got.Name,
		MessageID: got.Message,
		Pinned:    got.Pinned,
		Unpinned:  got.Unpinned,
		DryRun:    got.DryRun,
	}
}

// readState shapes a read state for the model.
func readState(got *service.ReadState) ReadStateOutput {
	return ReadStateOutput{Name: got.Name, LastReadTime: nullableTime(got.LastReadTime)}
}

// markRead shapes a read-mark change for the model.
func markRead(got *service.MarkReadResult) MarkReadOutput {
	return MarkReadOutput{
		Name:         got.Name,
		LastReadTime: got.LastReadTime,
		Unread:       got.Unread,
		DryRun:       got.DryRun,
	}
}

// notificationSetting shapes a notification choice for the model.
func notificationSetting(got *service.NotificationSetting) NotificationSettingOutput {
	return NotificationSettingOutput{
		Name:                got.Name,
		NotificationSetting: got.Notify,
		MuteSetting:         got.Mute,
	}
}
