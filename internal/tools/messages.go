package tools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// MessageOutput is one message in a listing.
//
// sender_email and sender_display_name are null when the People API
// could not say who the sender is, which is normal for someone outside
// the caller's organisation. The message is still here: an enrichment
// failure never costs a row.
type MessageOutput struct {
	MessageID         string    `json:"message_id" jsonschema:"the message's resource name, spaces/{space}/messages/{message}"`
	SenderUserID      string    `json:"sender_user_id" jsonschema:"who sent it, users/{id}"`
	SenderEmail       *string   `json:"sender_email" jsonschema:"the sender's email address, or null when it could not be resolved"`
	SenderDisplayName *string   `json:"sender_display_name" jsonschema:"the sender's name, or null when Google gave none"`
	Text              string    `json:"text" jsonschema:"the message body as plain text; empty for a message that is only an attachment or a card"`
	Timestamp         time.Time `json:"timestamp" jsonschema:"when the message was created, RFC 3339 in UTC"`
	ThreadID          string    `json:"thread_id" jsonschema:"the thread's resource name; pass it to get_thread to read the rest"`
}

// MessageListOutput wraps a list of messages.
type MessageListOutput struct {
	Result []MessageOutput `json:"result" jsonschema:"the messages that were read"`
}

// GetMessagesInput selects a page of a space's history.
type GetMessagesInput struct {
	SpaceID string `json:"space_id" jsonschema:"the space to read, spaces/{id}, from list_spaces"`
	Since   string `json:"since,omitempty" jsonschema:"only messages created after this time; RFC 3339, such as 2026-01-01T00:00:00Z"`
	Limit   int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 20"`
}

// GetThreadInput selects one thread.
type GetThreadInput struct {
	SpaceID    string `json:"space_id" jsonschema:"the thread's parent space, spaces/{id}"`
	ThreadName string `json:"thread_name" jsonschema:"the thread, spaces/{space}/threads/{thread}, from a message's thread_id"`
	Limit      int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 100; default 50"`
}

// GetMessageInput names one message.
type GetMessageInput struct {
	MessageName string `json:"message_name" jsonschema:"the message, spaces/{space}/messages/{message}"`
}

// ReactionSummaryOutput is one emoji and how many people used it.
type ReactionSummaryOutput struct {
	Emoji string `json:"emoji" jsonschema:"the emoji character"`
	Count int    `json:"count" jsonschema:"how many people reacted with it"`
}

// MessageDetailOutput is one message with its reactions inline.
type MessageDetailOutput struct {
	MessageID         string                  `json:"message_id" jsonschema:"the message's resource name"`
	SpaceID           string                  `json:"space_id" jsonschema:"the space it was posted in"`
	ThreadID          string                  `json:"thread_id" jsonschema:"the thread it belongs to"`
	SenderUserID      string                  `json:"sender_user_id" jsonschema:"who sent it, users/{id}"`
	SenderEmail       *string                 `json:"sender_email" jsonschema:"the sender's email address, or null when it could not be resolved"`
	SenderDisplayName *string                 `json:"sender_display_name" jsonschema:"the sender's name, or null when Google gave none"`
	Text              string                  `json:"text" jsonschema:"the message body as plain text"`
	Timestamp         time.Time               `json:"timestamp" jsonschema:"when the message was created, RFC 3339 in UTC"`
	LastUpdateTime    *time.Time              `json:"last_update_time" jsonschema:"when it was last edited, or null when it never was"`
	Reactions         []ReactionSummaryOutput `json:"reactions" jsonschema:"one entry per distinct emoji on the message"`
	ReactionsPaged    bool                    `json:"reactions_paged" jsonschema:"true when the summaries were left out because there were too many; call list_reactions for the detail"`
	Attachments       []AttachmentOutput      `json:"attachments" jsonschema:"the files on the message; empty when it carries none"`
}

// AttachmentOutput is one file on a message.
type AttachmentOutput struct {
	AttachmentName string  `json:"attachment_name" jsonschema:"the attachment's resource name; pass it to download_attachment when the message carries more than one"`
	FileName       string  `json:"file_name" jsonschema:"the original file name"`
	ContentType    string  `json:"content_type" jsonschema:"the MIME type Google reported"`
	Source         string  `json:"source" jsonschema:"UPLOADED_CONTENT for a file uploaded to Chat, DRIVE_FILE for one that lives in Google Drive"`
	DriveFileID    *string `json:"drive_file_id" jsonschema:"the Drive file id for a DRIVE_FILE attachment, or null"`
	Downloadable   bool    `json:"downloadable" jsonschema:"true when download_attachment can fetch the bytes. A Drive file is false: those bytes are Drive's, not Chat's"`
}

// SearchMessagesInput scans one space.
type SearchMessagesInput struct {
	SpaceID       string `json:"space_id,omitempty" jsonschema:"the space to search, spaces/{id}. Google's search takes every space you can see when this is omitted; a regex scan needs it"`
	Query         string `json:"query,omitempty" jsonschema:"keywords for Google's own search, matched as a phrase across whole words. Quotes, backslashes and brackets are not allowed; use regex for a literal or partial-word match"`
	Regex         string `json:"regex,omitempty" jsonschema:"a regular expression in RE2 syntax, scanned by this server over one space rather than by Google. The only way to match a pattern or part of a word, and the slowest: it reads pages of history. Cannot be combined with query or the filters below"`
	CreatedAfter  string `json:"created_after,omitempty" jsonschema:"only messages created at or after this time; RFC 3339, such as 2026-01-01T00:00:00Z. Strongly recommended for a regex scan: an unbounded scan of a busy space stops at the page cap and returns a partial result"`
	CreatedBefore string `json:"created_before,omitempty" jsonschema:"only messages created before this time; RFC 3339. Google's search only"`
	SenderEmail   string `json:"sender_email,omitempty" jsonschema:"only messages from this person. Google's search only"`
	MentionsMe    bool   `json:"mentions_me,omitempty" jsonschema:"only messages that @-mention you. Google's search only"`
	HasAttachment bool   `json:"has_attachment,omitempty" jsonschema:"only messages carrying an attachment. Google's search only"`
	HasLink       bool   `json:"has_link,omitempty" jsonschema:"only messages containing a hyperlink. Google's search only"`
	UnreadOnly    bool   `json:"unread_only,omitempty" jsonschema:"only messages you have not read. Google's search only, and it needs the read-state scope as well as the message one"`
	ByRelevance   bool   `json:"by_relevance,omitempty" jsonschema:"order by relevance instead of newest first. Google has this in Developer Preview and refuses it outside that programme"`
	Limit         int    `json:"limit,omitempty" jsonschema:"how many matches to return, 1 to 100; default 50"`
	MaxPages      int    `json:"max_pages,omitempty" jsonschema:"how many pages of history a regex scan may read, 1 to 50; default 10"`
	PageToken     string `json:"page_token,omitempty" jsonschema:"continue Google's search from a previous next_page_token"`
}

// SearchMatchOutput is one message that matched.
type SearchMatchOutput struct {
	MessageID    string    `json:"message_id" jsonschema:"the message's resource name"`
	ThreadID     string    `json:"thread_id" jsonschema:"the thread it belongs to"`
	SenderUserID string    `json:"sender_user_id" jsonschema:"who sent it, users/{id}"`
	Text         string    `json:"text" jsonschema:"the whole message body"`
	Timestamp    time.Time `json:"timestamp" jsonschema:"when the message was created, RFC 3339 in UTC"`
	Snippet      string    `json:"snippet" jsonschema:"up to about 160 characters of the body around the first match"`
}

// SearchMessagesOutput is what a scan found and how far it got.
type SearchMessagesOutput struct {
	Matches       []SearchMatchOutput `json:"matches" jsonschema:"the messages that matched, newest first"`
	Scanned       int                 `json:"scanned" jsonschema:"how many messages were read before filtering. Google filters upstream, so its search reports what it returned"`
	CapReached    bool                `json:"cap_reached" jsonschema:"true when there is more than was returned: a scan that hit max_pages or filled the limit, or a Google search with a further page. The answer is partial, so say so"`
	NextPageToken string              `json:"next_page_token,omitempty" jsonschema:"pass back as page_token to continue Google's search; a regex scan has none"`
	ServerSide    bool                `json:"server_side" jsonschema:"true when Google ran the search, false when this server scanned one space for a pattern"`
	Unparsed      int                 `json:"unparsed" jsonschema:"messages that were read but could not be searched. Non-zero means this result is INCOMPLETE, which is not the same as no matches; say so rather than reporting the space as empty"`
}

func registerMessages(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "get_messages",
		Description: "Read recent messages from a space. Returns up to limit messages (default 20, max 100), newest " +
			"first. Sender email is resolved through the People API and is null when that fails.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMessagesInput) (*mcp.CallToolResult, MessageListOutput, error) {
		rows, err := d.Service.GetMessages(ctx, service.GetMessagesInput{
			Space: in.SpaceID, Since: in.Since, Limit: in.Limit,
		})
		if err != nil {
			return nil, MessageListOutput{}, err
		}
		return nil, MessageListOutput{Result: messageRows(rows)}, nil
	})

	register(s, d, spec{
		Name: "get_thread",
		Description: "Read all messages in a single thread, oldest first. Give the parent space_id and the thread_name " +
			"(spaces/{space}/threads/{thread}), which every message carries as thread_id. Default limit 50, max 100.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetThreadInput) (*mcp.CallToolResult, MessageListOutput, error) {
		rows, err := d.Service.GetThread(ctx, service.GetThreadInput{
			Space: in.SpaceID, Thread: in.ThreadName, Limit: in.Limit,
		})
		if err != nil {
			return nil, MessageListOutput{}, err
		}
		return nil, MessageListOutput{Result: messageRows(rows)}, nil
	})

	register(s, d, spec{
		Name: "get_message",
		Description: "Fetch a single message by its resource name (spaces/{space}/messages/{message}). Reaction " +
			"summaries are inline; reactions_paged true means there were too many to inline and list_reactions has " +
			"the detail. Attachments are listed with the name download_attachment takes.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetMessageInput) (*mcp.CallToolResult, MessageDetailOutput, error) {
		got, err := d.Service.GetMessage(ctx, in.MessageName)
		if err != nil {
			return nil, MessageDetailOutput{}, err
		}
		return nil, messageDetail(got), nil
	})

	register(s, d, spec{
		Name: "search_messages",
		Description: "Search Google Chat message history. Two searches in one tool. Pass query and Google searches " +
			"every space you can see, matching whole words; narrow it with space_id, sender_email, mentions_me, " +
			"has_attachment, has_link, unread_only and a time window. Pass regex instead and this server scans one " +
			"space itself, which is the only way to match a pattern or part of a word — it needs space_id, reads " +
			"pages of history, and takes none of the filters. Prefer query. If cap_reached is true the answer is " +
			"partial; if unparsed is non-zero it is incomplete, and saying the space is empty would be wrong.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SearchMessagesInput) (*mcp.CallToolResult, SearchMessagesOutput, error) {
		got, err := d.Service.SearchMessages(ctx, service.SearchMessagesInput{
			Space:         in.SpaceID,
			Query:         in.Query,
			Regex:         in.Regex,
			CreatedAfter:  in.CreatedAfter,
			CreatedBefore: in.CreatedBefore,
			SenderEmail:   in.SenderEmail,
			MentionsMe:    in.MentionsMe,
			HasAttachment: in.HasAttachment,
			HasLink:       in.HasLink,
			UnreadOnly:    in.UnreadOnly,
			ByRelevance:   in.ByRelevance,
			Limit:         in.Limit,
			MaxPages:      in.MaxPages,
			PageToken:     in.PageToken,
		})
		if err != nil {
			return nil, SearchMessagesOutput{}, err
		}
		out := SearchMessagesOutput{
			Matches:       make([]SearchMatchOutput, 0, len(got.Matches)),
			Scanned:       got.Scanned,
			CapReached:    got.CapReached,
			NextPageToken: got.NextPageToken,
			ServerSide:    got.Server,
			Unparsed:      got.Unparsed,
		}
		for _, m := range got.Matches {
			out.Matches = append(out.Matches, SearchMatchOutput{
				MessageID:    m.Name,
				ThreadID:     m.ThreadName,
				SenderUserID: m.SenderUserID,
				Text:         m.Text,
				Timestamp:    m.CreateTime,
				Snippet:      m.Snippet,
			})
		}
		return nil, out, nil
	})
}

// messageDetail shapes one message for the model. The resource
// template returns the same shape, which is what makes the two
// interchangeable.
func messageDetail(got *service.MessageDetail) MessageDetailOutput {
	out := MessageDetailOutput{
		MessageID:         got.Name,
		SpaceID:           got.Space,
		ThreadID:          got.ThreadName,
		SenderUserID:      got.SenderUserID,
		SenderEmail:       nullable(got.SenderEmail),
		SenderDisplayName: nullable(got.SenderDisplayName),
		Text:              got.Text,
		Timestamp:         got.CreateTime,
		LastUpdateTime:    nullableTime(got.LastUpdateTime),
		Reactions:         make([]ReactionSummaryOutput, 0, len(got.Reactions)),
		ReactionsPaged:    got.ReactionsPaged,
		Attachments:       make([]AttachmentOutput, 0, len(got.Attachments)),
	}
	for _, r := range got.Reactions {
		out.Reactions = append(out.Reactions, ReactionSummaryOutput{Emoji: r.Emoji, Count: r.Count})
	}
	for _, a := range got.Attachments {
		out.Attachments = append(out.Attachments, AttachmentOutput{
			AttachmentName: a.Name,
			FileName:       a.ContentName,
			ContentType:    a.ContentType,
			Source:         a.Source,
			DriveFileID:    nullable(a.DriveFileID),
			Downloadable:   a.Downloadable,
		})
	}
	return out
}

// messageRows shapes a service listing for the model.
func messageRows(rows []service.MessageRow) []MessageOutput {
	out := make([]MessageOutput, 0, len(rows))
	for _, r := range rows {
		out = append(out, MessageOutput{
			MessageID:         r.Name,
			SenderUserID:      r.SenderUserID,
			SenderEmail:       nullable(r.SenderEmail),
			SenderDisplayName: nullable(r.SenderDisplayName),
			Text:              r.Text,
			Timestamp:         r.CreateTime,
			ThreadID:          r.ThreadName,
		})
	}
	return out
}

// SendMessageInput is one message to post.
type SendMessageInput struct {
	SpaceID       string `json:"space_id" jsonschema:"the space to post in, spaces/{id}, from list_spaces or find_direct_message"`
	Text          string `json:"text" jsonschema:"the message body, 1 to 4096 characters; posted exactly as given. To @mention someone, write <users/their@address> in the text: Google resolves the address itself when the server is signed in as a person, which this one is. <users/all> mentions and notifies EVERYONE in the space, so use it only when asked to"`
	ThreadName    string `json:"thread_name,omitempty" jsonschema:"reply in this thread, spaces/{space}/threads/{thread}; omit to start a new one"`
	ReplyFallback bool   `json:"reply_fallback,omitempty" jsonschema:"if the thread named is gone, start a new thread instead of failing. Only meaningful with thread_name; the default fails, so a reply never lands somewhere unexpected"`
	UploadToken   string `json:"attachment_upload_token,omitempty" jsonschema:"attach a file uploaded beforehand: the upload_token from upload_attachment, for the same space. One file per message, and the token is spent once it is posted"`
	DryRun        bool   `json:"dry_run,omitempty" jsonschema:"return the request body without posting; call again without it to post"`
}

// SendMessageOutput is what was posted, or what a dry run would post.
type SendMessageOutput struct {
	MessageID       *string          `json:"message_id" jsonschema:"the new message's resource name; null after a dry run, because nothing was posted"`
	SpaceID         string           `json:"space_id" jsonschema:"the space it went to"`
	ThreadID        *string          `json:"thread_id" jsonschema:"the thread it landed in; null after a dry run"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was posted"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact body that would have been posted; null on a real post"`
}

// UpdateMessageInput is a text edit.
type UpdateMessageInput struct {
	MessageName string `json:"message_name" jsonschema:"the message to edit, spaces/{space}/messages/{message}; you must be its sender"`
	Text        string `json:"text" jsonschema:"the replacement body, 1 to 4096 characters"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"return the patch body without applying it"`
}

// UpdateMessageOutput is the edited message.
type UpdateMessageOutput struct {
	MessageName     string           `json:"message_name" jsonschema:"the message that was edited"`
	Text            string           `json:"text" jsonschema:"the body it now has"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was changed"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the exact patch body; null on a real edit"`
}

// DeleteMessageInput names a message to delete.
type DeleteMessageInput struct {
	MessageName string `json:"message_name" jsonschema:"the message to delete, spaces/{space}/messages/{message}"`
	Force       bool   `json:"force,omitempty" jsonschema:"also delete the threaded replies to this message, which are other people's. Without it Google refuses to delete a message that has replies, and refusing is the safe answer: ask the person before setting this"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"check the arguments without deleting anything"`
}

// DeleteMessageOutput says whether anything was deleted.
type DeleteMessageOutput struct {
	MessageName string `json:"message_name" jsonschema:"the message that was named"`
	Deleted     bool   `json:"deleted" jsonschema:"true only when this call deleted the message. False means nothing was deleted: a dry run, a message that was already gone, or Google refusing — those last two are not told apart, so do not report the message as deleted on false"`
	Forced      bool   `json:"forced" jsonschema:"true when the call also deleted the message's threaded replies"`
	DryRun      bool   `json:"dry_run" jsonschema:"true when nothing was deleted"`
}

func registerMessageWrites(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "send_message",
		Description: "Post a text message to a Chat space, a group chat or a direct message. The body is posted " +
			"exactly as given: nothing is added to it, and nothing in it is rewritten. To @mention someone, put " +
			"<users/their@address> in the text yourself. Pass thread_name to reply in an existing thread, which " +
			"every message carries as thread_id. Set dry_run to see the request body without posting. The post " +
			"carries a client-assigned id, so this server's own retries cannot leave two copies — but calling the " +
			"tool again after an error can, because that is a new post. Read the space first if you are unsure. " +
			"To send a file, upload it with upload_attachment first and pass the token it returns.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SendMessageInput) (*mcp.CallToolResult, SendMessageOutput, error) {
		got, err := d.Service.SendMessage(ctx, service.SendMessageInput{
			Space: in.SpaceID, Text: in.Text, Thread: in.ThreadName,
			ReplyFallback: in.ReplyFallback, UploadToken: in.UploadToken, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, SendMessageOutput{}, err
		}
		return nil, SendMessageOutput{
			MessageID:       nullable(got.Name),
			SpaceID:         got.Space,
			ThreadID:        nullable(got.Thread),
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "update_message",
		Description: "Replace the text of a message you sent. Text only: cards and attachments are left untouched, " +
			"and editing them needs an identity this server does not have. Set dry_run to see the patch body without " +
			"applying it. Needs the restricted-tier chat.messages scope.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateMessageInput) (*mcp.CallToolResult, UpdateMessageOutput, error) {
		got, err := d.Service.UpdateMessage(ctx, service.UpdateMessageInput{
			Message: in.MessageName, Text: in.Text, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, UpdateMessageOutput{}, err
		}
		return nil, UpdateMessageOutput{
			MessageName:     got.Name,
			Text:            got.Text,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})

	register(s, d, spec{
		Name: "delete_message",
		Description: "Delete a message by its resource name. A message that is already gone reports deleted false " +
			"rather than failing, so a repeat is safe. A message you may not delete is reported as an error, not as " +
			"a deletion that already happened. Needs the restricted-tier chat.messages scope.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteMessageInput) (*mcp.CallToolResult, DeleteMessageOutput, error) {
		got, err := d.Service.DeleteMessage(ctx, service.DeleteMessageInput{
			Message: in.MessageName, Force: in.Force, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, DeleteMessageOutput{}, err
		}
		return nil, DeleteMessageOutput{
			MessageName: got.Name,
			Deleted:     got.Deleted,
			Forced:      got.Forced,
			DryRun:      got.DryRun,
		}, nil
	})
}
