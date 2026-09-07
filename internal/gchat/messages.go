package gchat

import (
	"context"
	"crypto/rand"
	"net/url"
	"strconv"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// ListMessagesOptions narrows spaces.messages.list.
type ListMessagesOptions struct {
	// Space is "spaces/{id}".
	Space string
	// Filter is Google's filter expression, such as
	// `createTime > "2026-01-01T00:00:00.000000Z"`.
	Filter string
	// OrderBy is "createTime desc" for newest first, "createTime asc"
	// for reading order. Empty takes Google's default, which is
	// ascending.
	OrderBy string
	// PageSize is capped by Google at 1000, and by every caller here at
	// 100.
	PageSize int
	// PageToken continues a previous call.
	PageToken string
}

// ListMessages returns one page of a space's messages.
func (c *Client) ListMessages(ctx context.Context, o ListMessagesOptions) (*ListMessagesResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	if o.OrderBy != "" {
		q.Set("orderBy", o.OrderBy)
	}
	var out ListMessagesResponse
	err := c.do(ctx, request{
		method: "GET",
		name:   o.Space,
		path:   "messages",
		query:  q,
		scope:  scopes.MessagesReadonly,
	}, &out)
	return &out, err
}

// GetMessage returns one message. name is "spaces/{s}/messages/{m}".
func (c *Client) GetMessage(ctx context.Context, name string) (*Message, error) {
	var out Message
	err := c.do(ctx, request{method: "GET", name: name, scope: scopes.MessagesReadonly}, &out)
	return &out, err
}

// Search limits Google documents for spaces.messages.search, checked
// against the REST reference on 2026-09-05.
const (
	MaxSearchMessagesPageSize = 100
	MaxSearchMessagesFilter   = 1000
)

// AllSpaces is the parent that searches every space the caller can see.
const AllSpaces = "spaces/-"

// SearchMessagesOptions narrows spaces.messages.search.
type SearchMessagesOptions struct {
	// Parent is one space, or AllSpaces for every space the caller can
	// reach.
	Parent string
	// Filter is Google's search expression: bare keywords, plus
	// create_time, sender.name, space.name, attachment, mentions,
	// has_link() and is_unread(). Required.
	//
	// create_time, not createTime. The REST reference's own examples
	// say createTime and Google answers "Invalid filter query" to it;
	// the guide's snake_case spelling is the one that works. Found by
	// running it, on 2026-09-05.
	Filter string
	// OrderBy is "createTime desc" or "relevance desc". Empty takes
	// Google's default, which is newest first.
	OrderBy string
	// PageSize is capped by Google at MaxSearchMessagesPageSize.
	PageSize int
	// PageToken continues a previous search.
	PageToken string
	// Unread says the filter uses is_unread(), which Google documents
	// as needing a read-state scope on top of the message one. It is
	// carried so a refusal can name both.
	Unread bool
}

// SearchMessages runs Google's own search over one space or all of
// them.
//
// A POST that changes nothing: Google models search this way because a
// filter does not fit in a URL. readOnly is what keeps it off the write
// limiter and out of the write path's rules.
func (c *Client) SearchMessages(ctx context.Context, o SearchMessagesOptions) (*SearchMessagesResponse, error) {
	parent := o.Parent
	if parent == "" {
		parent = AllSpaces
	}
	q := pageQuery(o.PageSize, o.PageToken)
	q.Set("filter", o.Filter)
	if o.OrderBy != "" {
		q.Set("orderBy", o.OrderBy)
	}
	r := request{
		method:   "POST",
		name:     parent,
		path:     "messages",
		verb:     "search",
		query:    q,
		readOnly: true,
		scope:    scopes.MessagesReadonly,
	}
	if o.Unread {
		r.alsoScopes = []string{scopes.ReadStateReadonly}
	}
	var out SearchMessagesResponse
	err := c.do(ctx, r, &out)
	return &out, err
}

// SendMessageRequest is the body of spaces.messages.create.
//
// It is its own type rather than a Message: what a user-authenticated
// caller may post is a fraction of what Chat returns, and sharing the
// response struct would offer fields the API refuses.
type SendMessageRequest struct {
	Text string `json:"text"`
	// Thread is set when the message replies to an existing thread.
	Thread *Thread `json:"thread,omitempty"`
	// Attachments carries a file uploaded beforehand. It is the one
	// field of Attachment that is not output only: everything else
	// about the file — its name, its type, where it can be downloaded
	// — Google fills in from the upload.
	Attachments []MessageAttachment `json:"attachment,omitempty"`
}

// MessageAttachment attaches an already-uploaded file to a message.
type MessageAttachment struct {
	AttachmentDataRef AttachmentDataRef `json:"attachmentDataRef"`
}

// BuildSendMessage renders the body of one message.
//
// The dry-run preview and the real post both go through here, so a
// preview cannot describe a body the post would not send.
func BuildSendMessage(text, threadName, uploadToken string) *SendMessageRequest {
	body := &SendMessageRequest{Text: text}
	if threadName != "" {
		body.Thread = &Thread{Name: threadName}
	}
	if uploadToken != "" {
		body.Attachments = []MessageAttachment{
			{AttachmentDataRef: AttachmentDataRef{AttachmentUploadToken: uploadToken}},
		}
	}
	return body
}

// NewMessageID returns a client-assigned message id.
//
// Google's rules: it starts with "client-", is at most 63 characters,
// and holds only lowercase letters, digits and hyphens.
//
// One id per logical send, never one per HTTP attempt. That is the
// whole point of it: a 5xx can arrive after Google created the message,
// and a retry carrying the same id is refused as ALREADY_EXISTS rather
// than posting a second copy.
func NewMessageID() string {
	return "client-" + strings.ToLower(rand.Text())
}

// ValidMessageID reports whether an id meets Google's rules for a
// client-assigned message id.
func ValidMessageID(id string) bool {
	if !strings.HasPrefix(id, "client-") || len(id) > 63 {
		return false
	}
	for _, r := range id {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '-' {
			return false
		}
	}
	return true
}

// SendMessage posts a message to space and returns what Google stored.
//
// clientID is the client-assigned message id. Empty mints a fresh one,
// which makes this call's own retries idempotent: a 5xx or a dropped
// connection can arrive after Google created the message, and without
// the id the retry posts a second copy. With it, Google refuses the
// retry as ALREADY_EXISTS and the message that landed is read back.
//
// A caller that passes its own id extends that guarantee past this
// call, to a caller who tries the whole send again. That is the only
// way a retry from outside is safe, and until it existed the tool
// description had to tell the model that calling again might double
// post — true, and useless, because nothing was offered to prevent it.
//
// A reply carries messageReplyOption. The default is the strict one, so
// a thread that has since gone fails rather than quietly starting a new
// one elsewhere in the space; replyFallback asks for the other
// behaviour, which is Google's REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD.
func (c *Client) SendMessage(ctx context.Context, space string, body *SendMessageRequest, replyFallback bool, clientID string) (*Message, error) {
	id := clientID
	if id == "" {
		id = NewMessageID()
	}
	q := url.Values{"messageId": {id}}
	if body != nil && body.Thread != nil {
		option := "REPLY_MESSAGE_OR_FAIL"
		if replyFallback {
			option = "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
		}
		q.Set("messageReplyOption", option)
	}

	var out Message
	err := c.do(ctx, request{
		method:     "POST",
		name:       space,
		path:       "messages",
		query:      q,
		body:       body,
		scope:      scopes.MessagesCreate,
		idempotent: true,
	}, &out)
	switch {
	case err == nil:
		return &out, nil
	case !IsAlreadyExists(err):
		return nil, err
	}
	c.log.Warn("send_message_recovered_after_retry", "space", space)
	return c.GetMessage(ctx, space+"/messages/"+id)
}

// UpdateMessageRequest is the body of spaces.messages.patch.
type UpdateMessageRequest struct {
	Text string `json:"text"`
}

// BuildUpdateMessage renders the body of a text edit.
func BuildUpdateMessage(text string) *UpdateMessageRequest {
	return &UpdateMessageRequest{Text: text}
}

// UpdateMessage replaces a message's text.
//
// It needs the restricted-tier chat.messages umbrella, as a delete does:
// Google lists nothing narrower for either.
//
// The mask is fixed at "text": cards and attachments are editable only
// under app authentication, which this server does not have, and an
// unmasked patch would clear them.
func (c *Client) UpdateMessage(ctx context.Context, name string, body *UpdateMessageRequest) (*Message, error) {
	var out Message
	err := c.do(ctx, request{
		method: "PATCH",
		name:   name,
		query:  url.Values{"updateMask": {"text"}},
		body:   body,
		scope:  scopes.Messages,
	}, &out)
	return &out, err
}

// DeleteMessage removes a message. name is "spaces/{s}/messages/{m}".
//
// force also deletes the message's threaded replies. Without it Google
// refuses to delete a message that has any, which is the safe default:
// the replies are other people's.
func (c *Client) DeleteMessage(ctx context.Context, name string, force bool) error {
	// Sent either way, rather than omitted when false. The default is
	// Google's to change, and this call is the difference between
	// deleting one message and deleting a conversation, so it says which
	// it means.
	return c.do(ctx, request{
		method: "DELETE",
		name:   name,
		query:  url.Values{"force": {strconv.FormatBool(force)}},
		scope:  scopes.Messages,
	}, nil)
}
