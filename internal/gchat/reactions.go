package gchat

import (
	"context"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// The three calls here name the sensitive-tier reactions scope, not the
// restricted chat.messages umbrella that also works. A deployer who
// declined the umbrella should not be pushed back into that tier by the
// prompt that says what to grant.

// ListReactionsOptions narrows spaces.messages.reactions.list.
type ListReactionsOptions struct {
	// Message is "spaces/{s}/messages/{m}".
	Message string
	// PageSize is capped by Google at 200.
	PageSize int
	// PageToken continues a previous call.
	PageToken string
	// Emoji and User filter server-side. Google requires the emoji
	// clause before the user clause when both are set, and it is the
	// only way to find one person's reaction without listing them all.
	Emoji string
	User  string
}

// ListReactions returns one page of a message's reactions.
func (c *Client) ListReactions(ctx context.Context, o ListReactionsOptions) (*ListReactionsResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	var clauses []string
	if o.Emoji != "" {
		clauses = append(clauses, `emoji.unicode = "`+o.Emoji+`"`)
	}
	if o.User != "" {
		clauses = append(clauses, `user.name = "`+o.User+`"`)
	}
	if len(clauses) > 0 {
		q.Set("filter", strings.Join(clauses, " AND "))
	}
	var out ListReactionsResponse
	err := c.do(ctx, request{
		method: "GET",
		name:   o.Message,
		path:   "reactions",
		query:  q,
		scope:  scopes.MessagesReactions,
	}, &out)
	return &out, err
}

// ReactionRequest is the body of spaces.messages.reactions.create.
type ReactionRequest struct {
	Emoji *Emoji `json:"emoji"`
}

// BuildAddReaction renders the body for a unicode reaction.
func BuildAddReaction(emoji string) *ReactionRequest {
	return &ReactionRequest{Emoji: &Emoji{Unicode: emoji}}
}

// AddReaction adds one reaction to a message. message is
// "spaces/{s}/messages/{m}".
//
// Google answers ALREADY_EXISTS when the caller already reacted with
// that emoji, rather than treating the call as a no-op.
func (c *Client) AddReaction(ctx context.Context, message string, body *ReactionRequest) (*Reaction, error) {
	var out Reaction
	err := c.do(ctx, request{
		method: "POST",
		name:   message,
		path:   "reactions",
		body:   body,
		scope:  scopes.MessagesReactions,
	}, &out)
	return &out, err
}

// DeleteReaction removes one reaction by its resource name.
func (c *Client) DeleteReaction(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.MessagesReactions)
}
