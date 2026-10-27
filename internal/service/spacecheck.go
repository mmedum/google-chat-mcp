package service

import (
	"context"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/v8/internal/gchat"
)

// spaceWitnessField is the argument every write that names a space takes
// to say which space it is.
const spaceWitnessField = "space_display_name"

// maxWitnessPages bounds the member listing a group chat's check reads:
// ten pages of the largest page Google serves.
const maxWitnessPages = 10

// checkSpace refuses a write to space unless witness says which space it
// is, the way a person reading the call would name it: a named space by
// its name, a direct message by the other person's email, and a group
// chat by the other members' emails, comma-separated in any order.
//
// The client's prompt that approves a call shows the call's arguments,
// and a space's id tells the person reading it nothing. So the space's
// name rides in the arguments, and it is checked here before anything is
// written, dry runs included, so the prompt cannot name one space while
// the call writes to another (§18, 2026-10-27).
func (s *Service) checkSpace(ctx context.Context, space, witness string) error {
	if strings.TrimSpace(witness) == "" {
		return Invalidf("pass %s: what %s is called, so the prompt that approves this call names it. A named space "+
			"by its name, a direct message by the other person's email, a group chat by the other members' emails, "+
			"comma-separated", spaceWitnessField, space)
	}
	sp, err := s.client.GetSpace(ctx, space)
	if err != nil {
		return Classify(err)
	}
	switch {
	case sp.DisplayName != "":
		return checkName(spaceWitnessField, space, sp.DisplayName, witness)
	case kindOf(*sp) == KindDirectMessage:
		return s.checkDirectMessage(ctx, space, witness)
	default:
		return s.checkMembers(ctx, space, witness)
	}
}

// checkDirectMessage holds witness to the one other person in the direct
// message space: Google's own lookup of the direct message with that
// address has to answer with this space.
func (s *Service) checkDirectMessage(ctx context.Context, space, witness string) error {
	addr, err := requireEmail(spaceWitnessField, witness)
	if err != nil {
		return Invalidf("%s is a direct message: pass %s as the other person's email address", space, spaceWitnessField)
	}
	found, err := s.client.FindDirectMessage(ctx, "users/"+addr)
	switch {
	case gchat.IsNotFound(err):
	case err != nil:
		return Classify(err)
	case found.Name == space:
		return nil
	}
	return Failf(ClassStale, "%s is not the direct message with %s, so it is not the one this call meant; nothing "+
		"was changed. Pass %s as the other person's email address", space, addr, spaceWitnessField)
}

// checkMembers holds witness to the people in a group chat, or a space
// with no name, other than the caller: the same addresses, in any order.
func (s *Service) checkMembers(ctx context.Context, space, witness string) error {
	me, err := s.callerID(ctx)
	if err != nil {
		return err
	}
	var others []string
	mine := ""
	token := ""
	for range maxWitnessPages {
		page, err := s.ListMembers(ctx, ListMembersInput{Space: space, Limit: maxMemberLimit, PageToken: token})
		if err != nil {
			return err
		}
		for _, m := range page.Members {
			switch {
			case m.Kind != KindHuman:
			case m.Name == me:
				mine = strings.ToLower(m.Email)
			default:
				others = append(others, strings.ToLower(m.Email))
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	var given []string
	for _, part := range strings.Split(witness, ",") {
		if addr := strings.ToLower(strings.TrimSpace(part)); addr != "" && addr != mine {
			given = append(given, addr)
		}
	}
	slices.Sort(others)
	slices.Sort(given)
	if slices.Equal(slices.Compact(others), slices.Compact(given)) {
		return nil
	}
	return Failf(ClassStale, "%s is a conversation with %s, not %s, so it is not the one this call meant; nothing was "+
		"changed. Pass %s as the other members' email addresses, comma-separated", space,
		strings.Join(others, ", "), strings.Join(given, ", "), spaceWitnessField)
}
