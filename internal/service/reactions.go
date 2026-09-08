package service

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
)

// Reaction listing limits, as the tool schema documents them.
const (
	defaultReactionLimit = 50
	maxReactionLimit     = 200
)

// Reaction is one person's reaction to a message.
type Reaction struct {
	Name   string
	Emoji  string
	UserID string
}

// ListReactionsInput selects a page of a message's reactions.
type ListReactionsInput struct {
	Message   string
	Limit     int
	PageToken string
}

// ListReactionsResult is a page of reactions.
type ListReactionsResult struct {
	Reactions     []Reaction
	NextPageToken string
}

// ListReactions returns who reacted to a message and with what.
func (s *Service) ListReactions(ctx context.Context, in ListReactionsInput) (*ListReactionsResult, error) {
	msg, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultReactionLimit, maxReactionLimit)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.ListReactions(ctx, gchat.ListReactionsOptions{
		Message:   msg,
		PageSize:  limit,
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}

	out := &ListReactionsResult{
		Reactions:     make([]Reaction, 0, len(resp.Reactions)),
		NextPageToken: resp.NextPageToken,
	}
	for _, r := range resp.Reactions {
		if r.Emoji == nil || r.Emoji.Unicode == "" || r.User == nil {
			// A custom emoji has no unicode character, so there is
			// nothing to show for it in a listing keyed on one.
			continue
		}
		out.Reactions = append(out.Reactions, Reaction{
			Name:   r.Name,
			Emoji:  r.Emoji.Unicode,
			UserID: r.User.Name,
		})
	}
	return out, nil
}

// maxEmoji is this server's bound. A unicode emoji is one character or a
// zero-width-joiner sequence of a few; sixteen is room for the longest
// of those and nothing else.
const maxEmoji = 16

// emojiPattern refuses the characters that would end the quoted string
// in a Chat filter.
//
// This is the check that keeps remove_reaction's lookup honest: it
// sends `emoji.unicode = "{value}"`, and a value carrying a quote or a
// backslash could widen that match to reactions the caller never named.
var emojiPattern = regexp.MustCompile(`^[^"\\\s]+$`)

// requireEmoji checks an emoji argument.
func requireEmoji(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", Invalidf("emoji is required")
	}
	if utf8.RuneCountInString(v) > maxEmoji {
		return "", Invalidf("emoji %q is too long; pass a single unicode emoji", value)
	}
	if !emojiPattern.MatchString(v) {
		return "", Invalidf("emoji %q must be a unicode emoji, with no quotes, backslashes or spaces", value)
	}
	return v, nil
}

// AddReactionInput is one reaction to add.
type AddReactionInput struct {
	Message string
	Emoji   string
}

// AddedReaction is the reaction that is now on the message.
type AddedReaction struct {
	Name   string
	Emoji  string
	UserID string
}

// AddReaction reacts to a message, and reports the existing reaction
// when the caller had already reacted that way.
//
// Google answers a repeat with ALREADY_EXISTS rather than treating it
// as a no-op, so the tool is idempotent only because this looks the
// existing one up. That lookup needs the caller's own id, which costs
// one extra request on a path that only runs when the reaction was
// already there.
func (s *Service) AddReaction(ctx context.Context, in AddReactionInput) (*AddedReaction, error) {
	msg, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	emoji, err := requireEmoji(in.Emoji)
	if err != nil {
		return nil, err
	}

	added, err := s.client.AddReaction(ctx, msg, gchat.BuildAddReaction(emoji))
	if err != nil {
		if gchat.IsAlreadyExists(err) {
			return s.existingReaction(ctx, msg, emoji)
		}
		return nil, Classify(err)
	}
	return addedReaction(*added, emoji), nil
}

// existingReaction finds the caller's own reaction after Google refused
// to add it twice.
//
// Chat's filter takes the numeric account id, so this needs to know who
// the caller is. That costs a request the first time and nothing
// afterwards, and any earlier whoami has already paid it.
func (s *Service) existingReaction(ctx context.Context, msg, emoji string) (*AddedReaction, error) {
	me, err := s.callerID(ctx)
	if err != nil {
		// The reaction is on the message either way, so this says what
		// is true and where to look rather than reporting the add as a
		// failure or naming a scope the caller did not ask about.
		return nil, Failf(ClassUpstream,
			"You have already reacted to %s with %s, but this server could not work out which reaction is yours: %v. "+
				"Read it with list_reactions.", msg, emoji, err)
	}
	listed, err := s.client.ListReactions(ctx, gchat.ListReactionsOptions{
		Message:  msg,
		PageSize: 1,
		Emoji:    emoji,
		User:     me,
	})
	if err != nil {
		return nil, Classify(err)
	}
	if len(listed.Reactions) == 0 {
		return nil, Failf(ClassUpstream,
			"Google reports a reaction of %s on %s that it will not list. Try list_reactions.", emoji, msg)
	}
	return addedReaction(listed.Reactions[0], emoji), nil
}

// addedReaction shapes one reaction, whether it was just made or found
// already there.
func addedReaction(r gchat.Reaction, requested string) *AddedReaction {
	return &AddedReaction{
		Name:   r.Name,
		Emoji:  emojiOf(r.Emoji, requested),
		UserID: userOf(r.User),
	}
}

// RemoveReactionInput names a reaction, either directly or by who
// reacted and with what.
type RemoveReactionInput struct {
	// Reaction is the resource name, which needs no lookup.
	Reaction string
	// Message, Emoji and Email are the other shape: find the reaction,
	// then delete it.
	Message string
	Emoji   string
	Email   string
}

// RemoveReactionResult says what was removed.
type RemoveReactionResult struct {
	// Name is empty when the lookup found nothing to delete.
	Name    string
	Removed bool
}

// RemoveReaction deletes a reaction.
//
// The lookup shape matches on the reactor's email address, which means
// resolving every person who used that emoji. Chat's own filter takes a
// numeric user id, not an address, so the match cannot happen upstream.
func (s *Service) RemoveReaction(ctx context.Context, in RemoveReactionInput) (*RemoveReactionResult, error) {
	// Any non-empty value counts, blank included: a caller who sent a
	// field meant to use that shape, and a value this server cannot use
	// should be reported rather than quietly read as absent.
	byName := in.Reaction != ""
	byTriple := in.Message != "" || in.Emoji != "" || in.Email != ""
	switch {
	case byName && byTriple:
		return nil, Invalidf("pass reaction_name, or message_name with emoji and user_email, not both")
	case byName:
		name, err := requireReaction(in.Reaction)
		if err != nil {
			return nil, err
		}
		if err := s.client.DeleteReaction(ctx, name); err != nil {
			return nil, Classify(err)
		}
		return &RemoveReactionResult{Name: name, Removed: true}, nil
	case !byTriple:
		return nil, Invalidf("pass reaction_name, or message_name with emoji and user_email")
	}
	return s.removeReactionByPerson(ctx, in)
}

// removeReactionByPerson finds one person's reaction and deletes it.
func (s *Service) removeReactionByPerson(ctx context.Context, in RemoveReactionInput) (*RemoveReactionResult, error) {
	msg, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	emoji, err := requireEmoji(in.Emoji)
	if err != nil {
		return nil, err
	}
	email, err := requireEmail("user_email", in.Email)
	if err != nil {
		return nil, err
	}

	listed, err := s.client.ListReactions(ctx, gchat.ListReactionsOptions{
		Message:  msg,
		PageSize: maxReactionLimit,
		Emoji:    emoji,
	})
	if err != nil {
		return nil, Classify(err)
	}
	if len(listed.Reactions) == 0 {
		return &RemoveReactionResult{}, nil
	}

	users := make([]string, 0, len(listed.Reactions))
	for _, r := range listed.Reactions {
		users = append(users, userOf(r.User))
	}
	people := s.resolvePeople(ctx, users)

	var unresolved bool
	for _, r := range listed.Reactions {
		got := people[userOf(r.User)].Email
		if got == "" {
			unresolved = true
			continue
		}
		if strings.EqualFold(got, email) {
			if err := s.client.DeleteReaction(ctx, r.Name); err != nil {
				return nil, Classify(err)
			}
			return &RemoveReactionResult{Name: r.Name, Removed: true}, nil
		}
	}
	if unresolved {
		// "Removed false" is read as "already gone", and a caller stops
		// there. It must not also mean "could not tell": somebody who
		// reacted did not resolve, so the person named may well still
		// be reacting.
		return nil, Failf(ClassUpstream,
			"Could not tell whether %s reacted with %s: at least one reactor's address did not resolve. "+
				"Nothing was deleted. Read the reaction with list_reactions and delete it by reaction_name.",
			email, emoji)
	}
	return &RemoveReactionResult{}, nil
}

// emojiOf is the character Google reports, or the one the caller asked
// for.
//
// The fallback matters on the recovery path: the reaction is already
// there by the time this runs, so a response this server cannot read
// must not fail the call, and the requested glyph is a true answer.
func emojiOf(e *gchat.Emoji, fallback string) string {
	if e == nil || e.Unicode == "" {
		return fallback
	}
	return e.Unicode
}

// userOf is a reaction's owner, or "" when Google sent none.
func userOf(u *gchat.User) string {
	if u == nil {
		return ""
	}
	return u.Name
}
