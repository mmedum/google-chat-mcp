package gchat

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// Space event types, as Google spells them. These ten are what a filter
// may name; each has a batch form that Google returns alongside it
// without being asked, which is why the batch names are not here.
const (
	EventMessageCreated    = "google.workspace.chat.message.v1.created"
	EventMessageUpdated    = "google.workspace.chat.message.v1.updated"
	EventMessageDeleted    = "google.workspace.chat.message.v1.deleted"
	EventMembershipCreated = "google.workspace.chat.membership.v1.created"
	EventMembershipUpdated = "google.workspace.chat.membership.v1.updated"
	EventMembershipDeleted = "google.workspace.chat.membership.v1.deleted"
	EventReactionCreated   = "google.workspace.chat.reaction.v1.created"
	EventReactionDeleted   = "google.workspace.chat.reaction.v1.deleted"
	EventSpaceUpdated      = "google.workspace.chat.space.v1.updated"
)

// Limits Google documents for spaces.spaceEvents.list, checked against
// the REST reference on 2026-09-05.
const (
	MaxSpaceEventsPageSize = 100
	// SpaceEventWindowInDays is how far back Google keeps events. A
	// start time older than this is refused rather than sent.
	SpaceEventWindowInDays = 28
)

// EventType is one type a caller may ask for: Google's own string, the
// short name this server accepts for it, and the scope Google wants for
// reading it.
//
// One table rather than three lists and a substring match. Google
// documents sixteen scopes on the events endpoint and asks for one
// "appropriate for reading the requested data", so the scope belongs
// beside the type it is for — and a type added without one then cannot
// compile. The short name is what the tool documents, because Google's
// strings are long and a mistyped one is a 400. The order here is the
// order a refusal offers them in, so a stable order needs no sorting.
type EventType struct {
	Short string
	Full  string
	Scope string
}

// EventTypes is every event type a caller may ask for.
//
// The reaction rows name the umbrella reactions scope rather than the
// readonly one, because the readonly one is not in the set login asks
// for and the umbrella covers it.
var EventTypes = []EventType{
	{"message_created", EventMessageCreated, scopes.MessagesReadonly},
	{"message_updated", EventMessageUpdated, scopes.MessagesReadonly},
	{"message_deleted", EventMessageDeleted, scopes.MessagesReadonly},
	{"membership_created", EventMembershipCreated, scopes.MembershipsReadonly},
	{"membership_updated", EventMembershipUpdated, scopes.MembershipsReadonly},
	{"membership_deleted", EventMembershipDeleted, scopes.MembershipsReadonly},
	{"reaction_created", EventReactionCreated, scopes.MessagesReactions},
	{"reaction_deleted", EventReactionDeleted, scopes.MessagesReactions},
	{"space_updated", EventSpaceUpdated, scopes.SpacesReadonly},
}

// ScopeForEventTypes is what Google wants for reading these events.
//
// A request spanning areas needs all of their scopes, and Google's
// refusal does not say which was declined, so every one of them is
// named.
func ScopeForEventTypes(types []string) (scope string, also []string) {
	var needed []string
	for _, t := range types {
		for _, known := range EventTypes {
			if known.Full == t && !slices.Contains(needed, known.Scope) {
				needed = append(needed, known.Scope)
			}
		}
	}
	if len(needed) == 0 {
		// No types is not a request Google accepts, and the service
		// refuses it first. Naming the space scope keeps the refusal
		// from pointing at nothing.
		return scopes.SpacesReadonly, nil
	}
	return needed[0], needed[1:]
}

// ListSpaceEventsOptions narrows spaces.spaceEvents.list.
type ListSpaceEventsOptions struct {
	// Space is "spaces/{id}".
	Space string
	// EventTypes is what to list, from EventTypes. Required by Google.
	EventTypes []string
	// StartTime and EndTime bound the window, RFC 3339. Empty means
	// Google's own default, which is the last 28 days up to now.
	StartTime string
	EndTime   string
	// PageSize is capped by Google at MaxSpaceEventsPageSize.
	PageSize  int
	PageToken string
}

// ListSpaceEvents returns one page of what happened in a space.
//
// The filter is built here from values the service has already
// checked, so no string a caller wrote reaches the expression. Chat's
// event filter has no free-text clause, which is what makes that
// possible.
func (c *Client) ListSpaceEvents(ctx context.Context, o ListSpaceEventsOptions) (*ListSpaceEventsResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	q.Set("filter", spaceEventFilter(o.EventTypes, o.StartTime, o.EndTime))
	scope, also := ScopeForEventTypes(o.EventTypes)
	var out ListSpaceEventsResponse
	err := c.do(ctx, request{
		method:     http.MethodGet,
		name:       o.Space,
		path:       "spaceEvents",
		query:      q,
		scope:      scope,
		alsoScopes: also,
	}, &out)
	return &out, err
}

// GetSpaceEvent returns one event. name is
// "spaces/{s}/spaceEvents/{e}".
//
// Which scope it needs depends on the event, and the event is what the
// call returns — so there is nothing to derive from. The space scope is
// named, and a caller refused for one of the others learns it from
// whichever list they came through.
func (c *Client) GetSpaceEvent(ctx context.Context, name string) (*SpaceEvent, error) {
	var out SpaceEvent
	err := c.do(ctx, request{
		method: http.MethodGet,
		name:   name,
		scope:  scopes.SpacesReadonly,
	}, &out)
	return &out, err
}

// spaceEventFilter renders Google's filter expression.
//
// The spelling is event_types, plural, which is what the reference's
// own examples use; the same page's prose calls the field event_type.
// Chat's search filter had a discrepancy of exactly this shape and only
// running it settled which spelling worked, so this one is written down
// as unsettled until the live run.
//
// Only OR joins event types, and only AND joins a time bound to them,
// which is why the types are parenthesised.
func spaceEventFilter(types []string, start, end string) string {
	clauses := make([]string, 0, len(types))
	for _, t := range types {
		clauses = append(clauses, `event_types:"`+t+`"`)
	}
	filter := strings.Join(clauses, " OR ")
	if len(clauses) > 1 {
		filter = "(" + filter + ")"
	}
	if start != "" {
		filter = `start_time="` + start + `" AND ` + filter
	}
	if end != "" {
		filter += ` AND end_time="` + end + `"`
	}
	return filter
}
