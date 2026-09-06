package gchat

import (
	"context"
	"net/url"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// Pins, read state and notification settings: the three things Chat
// keeps about a space that are not its messages or its members.
//
// The read state and the notification setting are per person. Google
// spells the caller "users/me" and canonicalises it in the response, so
// nothing here has to know the caller's own id.

// MeSpaces addresses the caller's own view of a space.
const MeSpaces = "users/me/spaces"

// MaxPinsPageSize is Google's cap on one page of pins.
const MaxPinsPageSize = 100

// ListPinsOptions narrows spaces.messagePins.list.
type ListPinsOptions struct {
	// Space is "spaces/{id}".
	Space     string
	PageSize  int
	PageToken string
}

// ListPins returns one page of a space's pinned messages.
func (c *Client) ListPins(ctx context.Context, o ListPinsOptions) (*ListMessagePinsResponse, error) {
	var out ListMessagePinsResponse
	err := c.do(ctx, request{
		method: "GET",
		name:   o.Space,
		path:   "messagePins",
		query:  pageQuery(o.PageSize, o.PageToken),
		scope:  scopes.PinsReadonly,
	}, &out)
	return &out, err
}

// PinMessage pins one message in its space. message is
// "spaces/{s}/messages/{m}".
func (c *Client) PinMessage(ctx context.Context, space, message string) (*MessagePin, error) {
	var out MessagePin
	err := c.do(ctx, request{
		method: "POST",
		name:   space,
		path:   "messagePins",
		body:   &MessagePin{Message: message},
		scope:  scopes.Pins,
	}, &out)
	return &out, err
}

// UnpinMessage removes a pin. name is "spaces/{s}/messagePins/{p}".
func (c *Client) UnpinMessage(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.Pins)
}

// GetSpaceReadState returns how far the caller has read in a space.
// space is "spaces/{id}".
func (c *Client) GetSpaceReadState(ctx context.Context, space string) (*SpaceReadState, error) {
	var out SpaceReadState
	err := c.do(ctx, request{
		method: "GET",
		name:   MeSpaces + "/" + spaceID(space),
		path:   "spaceReadState",
		scope:  scopes.ReadStateReadonly,
	}, &out)
	return &out, err
}

// UpdateSpaceReadState moves the caller's read mark in a space.
//
// lastReadTime is RFC 3339. Google's rule, quoted from the reference:
// a time before the latest message's create time leaves the space
// unread, and a later one marks it read, coerced back to the latest
// message's time. Only top-level messages count, not thread replies.
func (c *Client) UpdateSpaceReadState(ctx context.Context, space, lastReadTime string) (*SpaceReadState, error) {
	var out SpaceReadState
	err := c.do(ctx, request{
		method: "PATCH",
		name:   MeSpaces + "/" + spaceID(space),
		path:   "spaceReadState",
		query:  url.Values{"updateMask": {"lastReadTime"}},
		body:   &SpaceReadState{LastReadTime: lastReadTime},
		scope:  scopes.ReadState,
	}, &out)
	return &out, err
}

// GetThreadReadState returns how far the caller has read in a thread.
// thread is "spaces/{s}/threads/{t}".
func (c *Client) GetThreadReadState(ctx context.Context, thread string) (*ThreadReadState, error) {
	var out ThreadReadState
	err := c.do(ctx, request{
		method: "GET",
		name:   MeSpaces + "/" + threadPath(thread),
		path:   "threadReadState",
		scope:  scopes.ReadStateReadonly,
	}, &out)
	return &out, err
}

// GetSpaceNotificationSetting returns the caller's notification choice
// for a space. space is "spaces/{id}".
func (c *Client) GetSpaceNotificationSetting(ctx context.Context, space string) (*SpaceNotificationSetting, error) {
	var out SpaceNotificationSetting
	err := c.do(ctx, request{
		method: "GET",
		name:   MeSpaces + "/" + spaceID(space),
		path:   "spaceNotificationSetting",
		scope:  scopes.SpaceSettings,
	}, &out)
	return &out, err
}

// UpdateSpaceNotificationSetting changes the caller's notification
// choice for a space. Google takes one or both field paths, and sending
// a mask for a field left empty would clear it, so the mask is built
// from what was actually asked for.
func (c *Client) UpdateSpaceNotificationSetting(
	ctx context.Context, space string, body *SpaceNotificationSetting, mask string,
) (*SpaceNotificationSetting, error) {
	var out SpaceNotificationSetting
	err := c.do(ctx, request{
		method: "PATCH",
		name:   MeSpaces + "/" + spaceID(space),
		path:   "spaceNotificationSetting",
		query:  url.Values{"updateMask": {mask}},
		body:   body,
		scope:  scopes.SpaceSettings,
	}, &out)
	return &out, err
}

// trimPrefix drops a known prefix, leaving the value alone when it is
// not there — a caller may pass a bare id.
func trimPrefix(v, prefix string) string { return strings.TrimPrefix(v, prefix) }

// spaceID is the id half of "spaces/{id}", because the per-person
// resources nest a space under the caller rather than addressing it
// directly: users/me/spaces/{id}/spaceReadState.
func spaceID(space string) string {
	return trimPrefix(space, "spaces/")
}

// threadPath turns "spaces/{s}/threads/{t}" into the tail these
// resources want: "{s}/threads/{t}".
func threadPath(thread string) string {
	return trimPrefix(thread, "spaces/")
}
