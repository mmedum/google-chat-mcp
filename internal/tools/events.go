package tools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/service"
)

// ListSpaceEventsInput narrows a space's history of changes.
type ListSpaceEventsInput struct {
	SpaceID    string   `json:"space_id" jsonschema:"the space to read, spaces/{id}"`
	EventTypes []string `json:"event_types" jsonschema:"what to list, at least one: message_created, message_updated, message_deleted, membership_created, membership_updated, membership_deleted, reaction_created, reaction_deleted, space_updated. Google returns its own batch versions of these alongside them and refuses a batch name asked for directly"`
	Since      string   `json:"since,omitempty" jsonschema:"only events after this time, exclusive; RFC 3339. Google keeps 28 days and nothing older can be asked for"`
	Until      string   `json:"until,omitempty" jsonschema:"only events up to this time, inclusive; RFC 3339. Defaults to now"`
	Limit      int      `json:"limit,omitempty" jsonschema:"how many events to return, 1 to 100; default 50"`
	PageToken  string   `json:"page_token,omitempty" jsonschema:"continue from a previous next_page_token"`
}

// GetSpaceEventInput names one event.
type GetSpaceEventInput struct {
	EventName string `json:"event_name" jsonschema:"the event, spaces/{space}/spaceEvents/{event}, from list_space_events"`
}

// SpaceEventOutput is one change in a space.
type SpaceEventOutput struct {
	EventName string    `json:"event_name" jsonschema:"the event's resource name; pass it to get_space_event"`
	EventType string    `json:"event_type" jsonschema:"Google's own event type, such as google.workspace.chat.message.v1.created"`
	EventTime time.Time `json:"event_time" jsonschema:"when it happened, RFC 3339 in UTC"`
	Kind      string    `json:"kind" jsonschema:"what changed: message, membership, reaction or space"`
	Batch     bool      `json:"batch" jsonschema:"true when Google collapsed several changes of one type into this event. It does that on its own, so a batch arrives even though only the plain type was asked for"`
	Resources []string  `json:"resources" jsonschema:"the resource names the event covers, one per change. Read a message with get_message; a deleted one is a tombstone and only its name survives"`
}

// Render is one event.
func (o SpaceEventOutput) Render() string {
	what := o.Kind
	if o.Batch {
		what = count(len(o.Resources), o.Kind, o.Kind+"s")
	}
	return meta(utc(o.EventTime), o.EventType, what,
		strings.Join(o.Resources, " "), o.EventName)
}

// ListSpaceEventsOutput is one page of events.
type ListSpaceEventsOutput struct {
	Events        []SpaceEventOutput `json:"events" jsonschema:"what changed, oldest first"`
	NextPageToken string             `json:"next_page_token,omitempty" jsonschema:"pass back as page_token for the next page"`
	Unparsed      int                `json:"unparsed" jsonschema:"events Google sent that carried nothing this server could name. Non-zero means this page is INCOMPLETE, which is not the same as nothing having happened"`
}

// Render lists the events and admits what was dropped.
func (o ListSpaceEventsOutput) Render() string {
	return block(
		listing(count(len(o.Events), "event", "events"), rows(o.Events)),
		unparsedNote(o.Unparsed),
		more(nullable(o.NextPageToken)),
	)
}

func registerEvents(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "list_space_events",
		Description: "What changed in a space over the last 28 days: messages posted, edited or deleted, people " +
			"joining or leaving, reactions, and edits to the space itself. Name at least one event type. Events " +
			"come oldest first and each names the resources it covers, which you read with get_message, " +
			"get_member or get_space. Google keeps 28 days and no more, and it answers a busy space with batch " +
			"events covering several changes at once.",
		Kind:    Read,
		Toolset: config.ToolsetEvents,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListSpaceEventsInput) (*mcp.CallToolResult, ListSpaceEventsOutput, error) {
		got, err := d.Service.ListSpaceEvents(ctx, service.ListSpaceEventsInput{
			Space: in.SpaceID, Types: in.EventTypes, Since: in.Since, Until: in.Until,
			Limit: in.Limit, Page: in.PageToken,
		})
		if err != nil {
			return nil, ListSpaceEventsOutput{}, err
		}
		out := ListSpaceEventsOutput{
			Events:        make([]SpaceEventOutput, 0, len(got.Events)),
			NextPageToken: got.NextPageToken,
			Unparsed:      got.Unparsed,
		}
		for _, e := range got.Events {
			out.Events = append(out.Events, spaceEvent(e))
		}
		return nil, out, nil
	})

	register(s, d, spec{
		Name: "get_space_event",
		Description: "One event from a space by its resource name, which list_space_events reports. The payload " +
			"is the resource as it is now, not as it was: an event about a message that has since been edited " +
			"names the message, and get_message returns the current text.",
		Kind:    Read,
		Toolset: config.ToolsetEvents,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GetSpaceEventInput) (*mcp.CallToolResult, SpaceEventOutput, error) {
		got, err := d.Service.GetSpaceEvent(ctx, in.EventName)
		if err != nil {
			return nil, SpaceEventOutput{}, err
		}
		return nil, spaceEvent(*got), nil
	})
}

// spaceEvent shapes one event for the model.
func spaceEvent(e service.SpaceEventRow) SpaceEventOutput {
	return SpaceEventOutput{
		EventName: e.Name,
		EventType: e.EventType,
		EventTime: e.EventTime,
		Kind:      e.Kind,
		Batch:     e.Batch,
		Resources: e.Resources,
	}
}
