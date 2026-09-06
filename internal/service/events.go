package service

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// Event listing limits. The page size is this server's, the window is
// Google's.
const (
	defaultSpaceEventLimit = 50
	maxSpaceEventLimit     = gchat.MaxSpaceEventsPageSize
)

// eventShorthands is the list a refusal offers, in the order
// gchat.EventTypes declares them.
func eventShorthands() []string {
	out := make([]string, 0, len(gchat.EventTypes))
	for _, t := range gchat.EventTypes {
		out = append(out, t.Short)
	}
	return out
}

// ListSpaceEventsInput narrows a space's history of changes.
type ListSpaceEventsInput struct {
	Space string
	// Types is what to list, as short names or Google's own strings.
	// Google requires at least one.
	Types []string
	// Since and Until bound the window, RFC 3339. Google keeps 28 days.
	Since string
	Until string
	Limit int
	Page  string
}

// SpaceEventRow is one change, with the resources it covers named.
type SpaceEventRow struct {
	Name      string
	EventType string
	EventTime time.Time
	// Kind is what changed: message, membership, reaction or space.
	Kind string
	// Batch says Google collapsed several changes of one type into this
	// event, which it does without being asked.
	Batch bool
	// Resources are the resource names the event covers. A batch event
	// names several; a deleted message names one that is now a
	// tombstone.
	Resources []string
}

// ListSpaceEventsResult is one page of events, oldest first.
type ListSpaceEventsResult struct {
	Events        []SpaceEventRow
	NextPageToken string
	// Unparsed counts rows Google sent that carried no resource name.
	// Non-zero means this page is incomplete, not that nothing happened.
	Unparsed int
}

// ListSpaceEvents reads what changed in a space.
func (s *Service) ListSpaceEvents(ctx context.Context, in ListSpaceEventsInput) (*ListSpaceEventsResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	types, err := eventTypes(in.Types)
	if err != nil {
		return nil, err
	}
	since, err := eventTime("since", in.Since)
	if err != nil {
		return nil, err
	}
	until, err := eventTime("until", in.Until)
	if err != nil {
		return nil, err
	}

	limit, err := clampLimit("limit", in.Limit, defaultSpaceEventLimit, maxSpaceEventLimit)
	if err != nil {
		return nil, err
	}

	got, err := s.client.ListSpaceEvents(ctx, gchat.ListSpaceEventsOptions{
		Space:      space,
		EventTypes: types,
		StartTime:  since,
		EndTime:    until,
		PageSize:   limit,
		PageToken:  in.Page,
	})
	if err != nil {
		return nil, Classify(err)
	}

	out := &ListSpaceEventsResult{
		Events:        make([]SpaceEventRow, 0, len(got.SpaceEvents)),
		NextPageToken: got.NextPageToken,
	}
	for i := range got.SpaceEvents {
		row, ok := eventRow(&got.SpaceEvents[i])
		if !ok {
			out.Unparsed++
			continue
		}
		out.Events = append(out.Events, row)
	}
	return out, nil
}

// GetSpaceEvent reads one event by name.
func (s *Service) GetSpaceEvent(ctx context.Context, name string) (*SpaceEventRow, error) {
	event, err := requireSpaceEvent(name)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetSpaceEvent(ctx, event)
	if err != nil {
		return nil, Classify(err)
	}
	row, ok := eventRow(got)
	if !ok {
		return nil, Failf(ClassUpstream, "Google returned an event with no resource name.")
	}
	return &row, nil
}

// eventTypes turns what a caller asked for into Google's own strings.
//
// Batch types are refused rather than passed through: Google returns
// them alongside the plain type without being asked, and naming one in
// a filter is an INVALID_ARGUMENT.
func eventTypes(want []string) ([]string, error) {
	if len(want) == 0 {
		return nil, Invalidf("event_types is required: name at least one of %s.",
			strings.Join(eventShorthands(), ", "))
	}
	out := make([]string, 0, len(want))
	for _, raw := range want {
		name := strings.ToLower(strings.TrimSpace(raw))
		full := ""
		for _, known := range gchat.EventTypes {
			if name == known.Short || name == strings.ToLower(known.Full) {
				full = known.Full
				break
			}
		}
		if full == "" {
			return nil, Invalidf("%q is not an event type this server sends. Use one of: %s. "+
				"Google's batch types are returned on their own and cannot be asked for.",
				raw, strings.Join(eventShorthands(), ", "))
		}
		if !slices.Contains(out, full) {
			out = append(out, full)
		}
	}
	return out, nil
}

// eventTime checks a bound and refuses one Google cannot answer.
//
// The parsing is parseArgTime's, so a bare date works here exactly as
// it does on search_messages; a tool that accepted "2026-01-01" and its
// neighbour that refused it would be one surface with two rules. What
// is added is the window: Google keeps 28 days of events, and an older
// start time comes back empty, which reads as "nothing happened"
// rather than "you asked past the end".
func eventTime(field, value string) (string, error) {
	t, err := parseArgTime(field, value)
	if err != nil || t.IsZero() {
		return "", err
	}
	if oldest := time.Now().AddDate(0, 0, -gchat.SpaceEventWindowInDays); t.Before(oldest) {
		return "", Invalidf("%s is further back than Google keeps space events, which is %d days. "+
			"The earliest it answers for is %s.", field, gchat.SpaceEventWindowInDays,
			oldest.UTC().Format(time.RFC3339))
	}
	return t.UTC().Format(time.RFC3339), nil
}

// eventRow shapes one event, naming the resources it covers.
//
// What changed and whether it was a batch both come from which payload
// field Google set, never from the event type string. The string is
// Google's punctuation — the reference's prose has disagreed with its
// own examples twice on this API — while the payload field is what its
// schema guarantees. Reading ".batch" out of the type would report
// every batch row as a single change if that spelling ever moved.
//
// A row that names nothing is dropped and counted: it is an event type
// this server does not model, or an answer with nothing in it, and
// reporting an event that names nothing would be worse than saying the
// page is incomplete.
func eventRow(e *gchat.SpaceEvent) (SpaceEventRow, bool) {
	row := SpaceEventRow{
		Name:      e.Name,
		EventType: e.EventType,
		EventTime: parseTime(e.EventTime),
	}
	switch {
	case e.MessageCreated != nil, e.MessageUpdated != nil, e.MessageDeleted != nil:
		row.Kind, row.Resources = "message", messageEventNames(e)
	case e.MessageBatchCreated != nil, e.MessageBatchUpdated != nil, e.MessageBatchDeleted != nil:
		row.Kind, row.Batch, row.Resources = "message", true, messageEventNames(e)
	case e.MembershipCreated != nil, e.MembershipUpdated != nil, e.MembershipDeleted != nil:
		row.Kind, row.Resources = "membership", membershipEventNames(e)
	case e.MembershipBatchCreated != nil, e.MembershipBatchUpdated != nil, e.MembershipBatchDeleted != nil:
		row.Kind, row.Batch, row.Resources = "membership", true, membershipEventNames(e)
	case e.ReactionCreated != nil, e.ReactionDeleted != nil:
		row.Kind, row.Resources = "reaction", reactionEventNames(e)
	case e.ReactionBatchCreated != nil, e.ReactionBatchDeleted != nil:
		row.Kind, row.Batch, row.Resources = "reaction", true, reactionEventNames(e)
	case e.SpaceUpdated != nil:
		row.Kind, row.Resources = "space", spaceEventNames(e)
	case e.SpaceBatchUpdated != nil:
		row.Kind, row.Batch, row.Resources = "space", true, spaceEventNames(e)
	}
	return row, row.Name != "" && len(row.Resources) > 0
}

func messageEventNames(e *gchat.SpaceEvent) []string {
	var out []string
	add := func(d *gchat.MessageEventData) {
		if d != nil && d.Message != nil && d.Message.Name != "" {
			out = append(out, d.Message.Name)
		}
	}
	add(e.MessageCreated)
	add(e.MessageUpdated)
	add(e.MessageDeleted)
	for _, batch := range []*gchat.MessageBatchEventData{
		e.MessageBatchCreated, e.MessageBatchUpdated, e.MessageBatchDeleted,
	} {
		if batch == nil {
			continue
		}
		for i := range batch.Messages {
			add(&batch.Messages[i])
		}
	}
	return out
}

func membershipEventNames(e *gchat.SpaceEvent) []string {
	var out []string
	add := func(d *gchat.MembershipEventData) {
		if d != nil && d.Membership != nil && d.Membership.Name != "" {
			out = append(out, d.Membership.Name)
		}
	}
	add(e.MembershipCreated)
	add(e.MembershipUpdated)
	add(e.MembershipDeleted)
	for _, batch := range []*gchat.MembershipBatchEventData{
		e.MembershipBatchCreated, e.MembershipBatchUpdated, e.MembershipBatchDeleted,
	} {
		if batch == nil {
			continue
		}
		for i := range batch.Memberships {
			add(&batch.Memberships[i])
		}
	}
	return out
}

func reactionEventNames(e *gchat.SpaceEvent) []string {
	var out []string
	add := func(d *gchat.ReactionEventData) {
		if d != nil && d.Reaction != nil && d.Reaction.Name != "" {
			out = append(out, d.Reaction.Name)
		}
	}
	add(e.ReactionCreated)
	add(e.ReactionDeleted)
	for _, batch := range []*gchat.ReactionBatchEventData{e.ReactionBatchCreated, e.ReactionBatchDeleted} {
		if batch == nil {
			continue
		}
		for i := range batch.Reactions {
			add(&batch.Reactions[i])
		}
	}
	return out
}

func spaceEventNames(e *gchat.SpaceEvent) []string {
	var out []string
	add := func(d *gchat.SpaceEventData) {
		if d != nil && d.Space != nil && d.Space.Name != "" {
			out = append(out, d.Space.Name)
		}
	}
	add(e.SpaceUpdated)
	if e.SpaceBatchUpdated != nil {
		for i := range e.SpaceBatchUpdated.Spaces {
			add(&e.SpaceBatchUpdated.Spaces[i])
		}
	}
	return out
}
