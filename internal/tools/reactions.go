package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// ReactionOutput is one person's reaction to a message.
type ReactionOutput struct {
	ReactionName string `json:"reaction_name" jsonschema:"the reaction's resource name; remove_reaction takes it"`
	Emoji        string `json:"emoji" jsonschema:"the emoji character"`
	UserID       string `json:"user_id" jsonschema:"who reacted, users/{id}"`
}

// ListReactionsOutput is a page of reactions.
type ListReactionsOutput struct {
	Reactions     []ReactionOutput `json:"reactions" jsonschema:"one entry per person per emoji"`
	NextPageToken *string          `json:"next_page_token" jsonschema:"pass this back as page_token to read the next page; null when this is the last one"`
}

// ListReactionsInput selects a page of a message's reactions.
type ListReactionsInput struct {
	MessageName string `json:"message_name" jsonschema:"the message, spaces/{space}/messages/{message}"`
	Limit       int    `json:"limit,omitempty" jsonschema:"how many to return, 1 to 200; default 50"`
	PageToken   string `json:"page_token,omitempty" jsonschema:"next_page_token from a previous call"`
}

func registerReactions(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "list_reactions",
		Description: "List the reactions on a single message: who reacted and with what. Default limit 50, max 200; " +
			"page with page_token and next_page_token. Custom emoji are left out, having no character to show.",
		Kind: Read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListReactionsInput) (*mcp.CallToolResult, ListReactionsOutput, error) {
		got, err := d.Service.ListReactions(ctx, service.ListReactionsInput{
			Message: in.MessageName, Limit: in.Limit, PageToken: in.PageToken,
		})
		if err != nil {
			return nil, ListReactionsOutput{}, err
		}
		out := ListReactionsOutput{
			Reactions:     make([]ReactionOutput, 0, len(got.Reactions)),
			NextPageToken: nullable(got.NextPageToken),
		}
		for _, r := range got.Reactions {
			out.Reactions = append(out.Reactions, ReactionOutput{
				ReactionName: r.Name, Emoji: r.Emoji, UserID: r.UserID,
			})
		}
		return nil, out, nil
	})
}

// AddReactionInput is one reaction to add.
type AddReactionInput struct {
	MessageName string `json:"message_name" jsonschema:"the message to react to, spaces/{space}/messages/{message}"`
	Emoji       string `json:"emoji" jsonschema:"a single unicode emoji, such as 👍; custom emoji are not supported here"`
}

// AddReactionOutput is the reaction that is now on the message.
type AddReactionOutput struct {
	ReactionName string `json:"reaction_name" jsonschema:"the reaction's resource name; remove_reaction takes it"`
	Emoji        string `json:"emoji" jsonschema:"the emoji character"`
	UserID       string `json:"user_id" jsonschema:"who reacted, users/{id}; the signed-in account"`
}

// RemoveReactionInput names a reaction, one of two ways.
type RemoveReactionInput struct {
	ReactionName string `json:"reaction_name,omitempty" jsonschema:"the reaction to delete, from list_reactions or add_reaction. Pass this on its own"`
	MessageName  string `json:"message_name,omitempty" jsonschema:"the message the reaction is on; pass it with emoji and user_email"`
	Emoji        string `json:"emoji,omitempty" jsonschema:"the emoji to remove; pass it with message_name and user_email"`
	UserEmail    string `json:"user_email,omitempty" jsonschema:"whose reaction to remove; pass it with message_name and emoji"`
}

// RemoveReactionOutput says what was removed.
type RemoveReactionOutput struct {
	ReactionName *string `json:"reaction_name" jsonschema:"the reaction that was deleted; null when the lookup found none"`
	Removed      bool    `json:"removed" jsonschema:"false when there was no such reaction to delete, which is not an error"`
}

func registerReactionWrites(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "add_reaction",
		Description: "React to a message with a single unicode emoji. Reacting the same way twice is not an error: " +
			"the reaction that is already there is returned instead.",
		Kind: WriteIdempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AddReactionInput) (*mcp.CallToolResult, AddReactionOutput, error) {
		got, err := d.Service.AddReaction(ctx, service.AddReactionInput{Message: in.MessageName, Emoji: in.Emoji})
		if err != nil {
			return nil, AddReactionOutput{}, err
		}
		return nil, AddReactionOutput{ReactionName: got.Name, Emoji: got.Emoji, UserID: got.UserID}, nil
	})

	register(s, d, spec{
		Name: "remove_reaction",
		Description: "Delete a reaction. Pass reaction_name on its own, or message_name with emoji and user_email to " +
			"look it up first. The lookup matches on the reactor's email address, so it fails rather than reporting " +
			"nothing to delete when someone who reacted cannot be resolved: a silent no was the one answer it must " +
			"not give. Removed false means there was no such reaction.",
		Kind: Destructive,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveReactionInput) (*mcp.CallToolResult, RemoveReactionOutput, error) {
		got, err := d.Service.RemoveReaction(ctx, service.RemoveReactionInput{
			Reaction: in.ReactionName, Message: in.MessageName, Emoji: in.Emoji, Email: in.UserEmail,
		})
		if err != nil {
			return nil, RemoveReactionOutput{}, err
		}
		return nil, RemoveReactionOutput{ReactionName: nullable(got.Name), Removed: got.Removed}, nil
	})
}
