package service

import (
	"context"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// Message limits. These are this server's, not Google's: the tool
// schemas name them and a caller may have written them down.
const (
	defaultMessageLimit = 20
	defaultThreadLimit  = 50
	maxMessageLimit     = 100
	// maxPageSize is what one request to Google asks for.
	maxPageSize = 100
)

// MessageRow is one message in a listing.
//
// SenderEmail is empty when the People API could not say who the sender
// is, which is normal for someone outside the caller's organisation. It
// is never a reason to leave the message out.
type MessageRow struct {
	Name              string
	SenderUserID      string
	SenderEmail       string
	SenderDisplayName string
	Text              string
	CreateTime        time.Time
	ThreadName        string
}

// GetMessagesInput selects a page of a space's history.
type GetMessagesInput struct {
	Space string
	// Since bounds the listing below, as RFC 3339 or a bare date.
	// Empty means no bound.
	Since string
	Limit int
	// PageToken continues a previous call.
	PageToken string
}

// MessagesResult is one page of messages.
//
// A result rather than a bare slice, because the page token was being
// built on the wire and dropped here: a caller got the first 50 of 300
// messages with nothing in the reply to say so, and a model reading it
// reported a prefix as the whole conversation.
type MessagesResult struct {
	Messages      []MessageRow
	NextPageToken string
	// Unparsed is how many rows Google sent that this server could not
	// model, and so dropped. The count reached the log and stopped
	// there, which told the operator and not the model — and the
	// server's own instructions tell the model that a listing reporting
	// unparsed rows is incomplete rather than short.
	Unparsed int
}

// GetMessages reads a space's most recent messages, newest first.
func (s *Service) GetMessages(ctx context.Context, in GetMessagesInput) (*MessagesResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultMessageLimit, maxMessageLimit)
	if err != nil {
		return nil, err
	}
	since, err := parseArgTime("since", in.Since)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.ListMessages(ctx, gchat.ListMessagesOptions{
		Space:     space,
		OrderBy:   "createTime desc",
		PageSize:  limit,
		Filter:    createdAfterFilter(since),
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}
	rows, unparsed := s.enrich(ctx, resp.Messages)
	return &MessagesResult{
		Messages:      rows,
		NextPageToken: resp.NextPageToken,
		Unparsed:      unparsed,
	}, nil
}

// GetThreadInput selects one thread's messages.
type GetThreadInput struct {
	Space  string
	Thread string
	// Limit caps the read. Zero takes the tool's default; MaxLimit
	// asks for as much as the tool allows, which is what the thread
	// resource asks for, having no way to pass a page token.
	Limit int
	// PageToken continues a previous call.
	PageToken string
}

// MaxLimit asks a listing for as much as it allows, without the caller
// having to know the number.
const MaxLimit = -1

// GetThread reads one thread, oldest first, which is reading order.
//
// The space has to be given as well as the thread: Google lists
// messages under a space, and answers 400 when the thread belongs to a
// different one.
func (s *Service) GetThread(ctx context.Context, in GetThreadInput) (*MessagesResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	thread, err := requireThread(space, in.Thread)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == MaxLimit {
		limit = maxMessageLimit
	}
	limit, err = clampLimit("limit", limit, defaultThreadLimit, maxMessageLimit)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.ListMessages(ctx, gchat.ListMessagesOptions{
		Space:     space,
		Filter:    `thread.name = "` + thread + `"`,
		OrderBy:   "createTime asc",
		PageSize:  limit,
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}
	rows, unparsed := s.enrich(ctx, resp.Messages)
	return &MessagesResult{
		Messages:      rows,
		NextPageToken: resp.NextPageToken,
		Unparsed:      unparsed,
	}, nil
}

// ReactionCount is one emoji and how many people used it.
type ReactionCount struct {
	Emoji string
	Count int
}

// inlineReactionCap is how many distinct emoji a message may carry
// before get_message stops inlining them. Google keeps the summaries
// small, so this is a guard rather than a common path.
const inlineReactionCap = 25

// MessageDetail is one message in full, as get_message returns it.
type MessageDetail struct {
	Name              string
	Space             string
	ThreadName        string
	SenderUserID      string
	SenderEmail       string
	SenderDisplayName string
	Text              string
	CreateTime        time.Time
	// LastUpdateTime is the zero value when the message was never
	// edited.
	LastUpdateTime time.Time
	Reactions      []ReactionCount
	// ReactionsPaged says the summaries were left out because there
	// were too many, and list_reactions has the detail.
	ReactionsPaged bool
	// Attachments is the files on the message. download_attachment
	// needs the name of one, so this is where a caller learns it.
	Attachments []AttachmentRow
}

// AttachmentRow is one file on a message.
type AttachmentRow struct {
	Name        string
	ContentName string
	ContentType string
	// Source is Google's own word: UPLOADED_CONTENT for a file in
	// Chat, DRIVE_FILE for one that lives in Drive.
	Source string
	// DriveFileID is set for a Drive file, which download_attachment
	// cannot fetch: those bytes are Drive's.
	DriveFileID string
	// Downloadable says download_attachment can fetch this one.
	Downloadable bool
}

// attachmentRows shapes a message's attachments for a caller.
func attachmentRows(all []gchat.Attachment) []AttachmentRow {
	out := make([]AttachmentRow, 0, len(all))
	for _, a := range all {
		row := AttachmentRow{
			Name:        a.Name,
			ContentName: a.ContentName,
			ContentType: a.ContentType,
			Source:      a.Source,
		}
		if a.DriveDataRef != nil {
			row.DriveFileID = a.DriveDataRef.DriveFileID
		}
		row.Downloadable = a.AttachmentDataRef != nil && a.AttachmentDataRef.ResourceName != ""
		out = append(out, row)
	}
	return out
}

// GetMessage reads one message, with its reaction summaries inline.
func (s *Service) GetMessage(ctx context.Context, name string) (*MessageDetail, error) {
	msg, err := requireMessage(name)
	if err != nil {
		return nil, err
	}
	space := spaceOfMessage(msg)

	got, err := s.client.GetMessage(ctx, msg)
	if err != nil {
		return nil, Classify(err)
	}

	// Through enrich, so the degrade rule has one implementation. A
	// message with no resource name is the one thing it drops, and
	// Google answering a get with one would be strange enough to
	// report rather than to paper over.
	rows, _ := s.enrich(ctx, []gchat.Message{*got})
	if len(rows) == 0 {
		return nil, Failf(ClassUpstream, "Google returned a message with no resource name")
	}
	row := rows[0]

	out := &MessageDetail{
		Name:              row.Name,
		Space:             space,
		ThreadName:        row.ThreadName,
		SenderUserID:      row.SenderUserID,
		SenderEmail:       row.SenderEmail,
		SenderDisplayName: row.SenderDisplayName,
		Text:              row.Text,
		CreateTime:        row.CreateTime,
		LastUpdateTime:    parseTime(got.LastUpdateTime),
	}
	out.Reactions, out.ReactionsPaged = summarizeReactions(got.EmojiReactions)
	out.Attachments = attachmentRows(got.Attachments)
	return out, nil
}

// summarizeReactions turns Google's per-emoji counts into the inline
// summary, or reports that there were too many to inline.
func summarizeReactions(raw []gchat.ReactionSummary) ([]ReactionCount, bool) {
	out := make([]ReactionCount, 0, len(raw))
	for _, r := range raw {
		if r.Emoji == nil || r.Emoji.Unicode == "" {
			// A custom emoji has no unicode character to show. The
			// count is still reachable through list_reactions.
			continue
		}
		out = append(out, ReactionCount{Emoji: r.Emoji.Unicode, Count: r.ReactionCount})
	}
	if len(out) > inlineReactionCap {
		return []ReactionCount{}, true
	}
	return out, false
}

// enrich turns wire messages into rows, resolving each unique sender
// once.
//
// A row survives as long as it can be addressed. If Google stops
// sending a sender, a thread or a readable timestamp — a rename is all
// it takes — that field arrives empty and the message is still there,
// because a short list reads exactly like a quiet conversation and
// these tools have no field in which to say otherwise. Only a message
// with no resource name is dropped: there is nothing to return for it
// and nothing to follow it up with.
//
// A failed People lookup is not drift and never costs a row either.
func (s *Service) enrich(ctx context.Context, msgs []gchat.Message) ([]MessageRow, int) {
	senders := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Sender != nil {
			senders = append(senders, m.Sender.Name)
		}
	}
	people := s.resolvePeople(ctx, senders)

	rows := make([]MessageRow, 0, len(msgs))
	var unparsed int
	for _, m := range msgs {
		if m.Name == "" {
			unparsed++
			continue
		}
		row := MessageRow{Name: m.Name, Text: m.Text}
		row.CreateTime = parseTime(m.CreateTime)
		if m.Thread != nil {
			row.ThreadName = m.Thread.Name
		}
		if m.Sender != nil {
			person := people[m.Sender.Name]
			row.SenderUserID = m.Sender.Name
			row.SenderEmail = person.Email
			row.SenderDisplayName = person.DisplayName
			if row.SenderDisplayName == "" {
				row.SenderDisplayName = m.Sender.DisplayName
			}
		}
		rows = append(rows, row)
	}
	s.warnUnparsed("messages_without_a_name", unparsed, len(msgs))
	return rows, unparsed
}

// maxMessageText is this server's bound, not Google's. The tool schemas
// name it, update_message shares it for consistency, and a caller may
// have written it down.
const maxMessageText = 4096

// SendMessageInput is one message to post.
type SendMessageInput struct {
	Space string
	Text  string
	// Thread replies to an existing thread. Empty starts a new one.
	Thread string
	// ReplyFallback starts a new thread when the one named is gone,
	// instead of failing. Only meaningful with Thread.
	ReplyFallback bool
	// UploadToken attaches a file uploaded beforehand, from
	// upload_attachment.
	UploadToken string
	// ClientMessageID makes a retry from outside this server safe.
	// Empty mints one per call, which covers this call's own retries
	// and nothing further.
	ClientMessageID string
	// DryRun renders the request body and posts nothing.
	DryRun bool
}

// SendMessageResult is what landed, or what a dry run would have sent.
type SendMessageResult struct {
	// Name and Thread are empty after a dry run: nothing was created,
	// so there is nothing to name.
	Name   string
	Space  string
	Thread string
	DryRun bool
	// Rendered is the request body, on a dry run only.
	Rendered map[string]any
}

// SendMessage posts a message to a space.
//
// The text goes out exactly as it arrived: no prefix, no suffix, no
// marker saying a machine wrote it. Whoever runs this server decides
// what their own messages say.
func (s *Service) SendMessage(ctx context.Context, in SendMessageInput) (*SendMessageResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	text, err := requireText("text", in.Text, maxMessageText)
	if err != nil {
		return nil, err
	}
	// Refused here rather than by Google, whose answer for a malformed
	// id is a 400 naming a query parameter the caller never wrote.
	if in.ClientMessageID != "" && !gchat.ValidMessageID(in.ClientMessageID) {
		return nil, Invalidf("client_message_id %q must start with \"client-\", be at most 63 characters, "+
			"and hold only lowercase letters, digits and hyphens", in.ClientMessageID)
	}
	var thread string
	if in.Thread != "" {
		// Checked on any non-empty value, blank included. Treating "  "
		// as "no thread" would post a new top-level message to the
		// space when the caller asked to reply in one.
		if thread, err = requireThread(space, in.Thread); err != nil {
			return nil, err
		}
	}

	body := gchat.BuildSendMessage(text, thread, strings.TrimSpace(in.UploadToken))
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		return &SendMessageResult{Space: space, DryRun: true, Rendered: rendered}, nil
	}

	msg, err := s.client.SendMessage(ctx, space, body, in.ReplyFallback, in.ClientMessageID)
	if err != nil {
		return nil, Classify(err)
	}
	out := &SendMessageResult{Name: msg.Name, Space: space}
	if msg.Thread != nil {
		out.Thread = msg.Thread.Name
	}
	return out, nil
}

// UpdateMessageInput is a text edit of a message the caller sent.
type UpdateMessageInput struct {
	Message string
	Text    string
	DryRun  bool
}

// UpdateMessageResult is the edited message.
type UpdateMessageResult struct {
	Name     string
	Text     string
	DryRun   bool
	Rendered map[string]any
}

// UpdateMessage replaces the text of a message.
//
// Text only. Cards and attachments are editable under app
// authentication, which this server does not have, and the mask keeps
// the patch from touching them.
func (s *Service) UpdateMessage(ctx context.Context, in UpdateMessageInput) (*UpdateMessageResult, error) {
	name, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	text, err := requireText("text", in.Text, maxMessageText)
	if err != nil {
		return nil, err
	}

	body := gchat.BuildUpdateMessage(text)
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		return &UpdateMessageResult{Name: name, Text: text, DryRun: true, Rendered: rendered}, nil
	}

	msg, err := s.client.UpdateMessage(ctx, name, body)
	if err != nil {
		return nil, Classify(err)
	}
	out := &UpdateMessageResult{Name: msg.Name, Text: msg.Text}
	if out.Name == "" {
		out.Name = name
	}
	if out.Text == "" {
		// The patch landed. A response that carries no text means
		// Google renamed the field, not that the message is now empty,
		// and echoing what was asked for says more than "".
		out.Text = text
	}
	return out, nil
}

// messageDeleted reports whether a message Google handed back is a
// tombstone rather than a message.
//
// Both fields are checked because Google documents deleteTime as the
// marker and sends deletionMetadata alongside it; a response carrying
// either one is not a message anybody can read.
func messageDeleted(m *gchat.Message) bool {
	return m != nil && (m.DeleteTime != "" || m.DeletionMetadata != nil)
}

// DeleteMessageInput names a message to delete.
type DeleteMessageInput struct {
	Message string
	// Force also deletes the message's threaded replies. Without it
	// Google refuses to delete a message that has any.
	Force  bool
	DryRun bool
}

// DeleteMessageResult says whether anything was deleted.
type DeleteMessageResult struct {
	Name string
	// Deleted is false when the message was already gone, and after a
	// dry run.
	Deleted bool
	// Forced repeats what was asked for, because it is the difference
	// between deleting one message and deleting a conversation.
	Forced bool
	DryRun bool
}

// DeleteMessage removes a message, and succeeds when it was already
// gone.
//
// A refusal is checked rather than assumed. Google reports an
// already-deleted message as a 403 when the space keeps no history, and
// uses the same 403 for a message the caller may not delete — someone
// else's. Reading the message back tells the two apart, so a caller is
// never told a message is gone while it is still there.
func (s *Service) DeleteMessage(ctx context.Context, in DeleteMessageInput) (*DeleteMessageResult, error) {
	name, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return &DeleteMessageResult{Name: name, Forced: in.Force, DryRun: true}, nil
	}
	deleted, err := deleteIdempotent(ctx, name,
		func(ctx context.Context, name string) error {
			return s.client.DeleteMessage(ctx, name, in.Force)
		},
		confirmGone(s.client.GetMessage, messageDeleted))
	if err != nil {
		return nil, err
	}
	return &DeleteMessageResult{Name: name, Deleted: deleted, Forced: in.Force}, nil
}
