package gchat

import (
	"context"
	"net/url"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

// Availability, custom emoji and deleting a space.
//
// Availability is the caller's own: Google's own reference says the id
// may be a person id, an email address or "me", and this server always
// asks about itself.

// MeAvailability addresses the caller's presence.
const MeAvailability = "users/me/availability"

// The presence states Google reports.
const (
	StateActive       = "ACTIVE"
	StateIdle         = "IDLE"
	StateAway         = "AWAY"
	StateDoNotDisturb = "DO_NOT_DISTURB"
)

// GetAvailability returns the caller's own presence and custom status.
func (c *Client) GetAvailability(ctx context.Context) (*Availability, error) {
	var out Availability
	err := c.do(ctx, request{
		method: "GET",
		name:   MeAvailability,
		scope:  scopes.AvailabilityReadonly,
	}, &out)
	return &out, err
}

// MarkActive sets the caller active.
//
// This and the two below are three methods rather than one taking the
// verb, because a verb that arrives as a value is a path segment built
// somewhere else — which the package's own syntax-tree test refuses,
// and rightly: it is the same mistake as a built path, one field along.
func (c *Client) MarkActive(ctx context.Context) (*Availability, error) {
	return c.mark(ctx, request{method: "POST", name: MeAvailability, verb: "markAsActive"})
}

// MarkAway sets the caller away.
func (c *Client) MarkAway(ctx context.Context) (*Availability, error) {
	return c.mark(ctx, request{method: "POST", name: MeAvailability, verb: "markAsAway"})
}

// MarkDoNotDisturb silences the caller until the expiry in body, which
// Google requires and caps at a year out.
func (c *Client) MarkDoNotDisturb(ctx context.Context, body *DNDRequest) (*Availability, error) {
	return c.mark(ctx, request{
		method: "POST",
		name:   MeAvailability,
		verb:   "markAsDoNotDisturb",
		body:   body,
	})
}

// MaxCustomStatusText is Google's cap on the status text, from the
// reference.
const MaxCustomStatusText = 64

// SetCustomStatus writes the text and emoji beside the caller's name.
//
// It is the only field users.availability.patch will change — the
// state is set by the three marks above — so the mask is fixed. A nil
// status clears what is there: the mask names the field and the body
// leaves it out, which is how a field mask spells "remove this".
func (c *Client) SetCustomStatus(ctx context.Context, status *CustomStatus) (*Availability, error) {
	var out Availability
	err := c.do(ctx, request{
		method: "PATCH",
		name:   MeAvailability,
		query:  url.Values{"updateMask": []string{"customStatus"}},
		body:   Availability{CustomStatus: status},
		scope:  scopes.Availability,
	}, &out)
	return &out, err
}

// mark is the half the three share, scope included.
func (c *Client) mark(ctx context.Context, r request) (*Availability, error) {
	r.scope = scopes.Availability
	var out Availability
	err := c.do(ctx, r, &out)
	return &out, err
}

// MaxCustomEmojiBytes is Google's cap on an emoji image, from the
// reference: the payload is under 256 KB, and the image is square and
// between 64 and 500 pixels.
const MaxCustomEmojiBytes = 256 * 1024

// ListCustomEmojisOptions narrows customEmojis.list.
type ListCustomEmojisOptions struct {
	PageSize  int
	PageToken string
	// Filter is Google's expression, such as `creator("users/me")`.
	Filter string
}

// ListCustomEmojis returns one page of the organisation's own emoji.
func (c *Client) ListCustomEmojis(ctx context.Context, o ListCustomEmojisOptions) (*ListCustomEmojisResponse, error) {
	q := pageQuery(o.PageSize, o.PageToken)
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	var out ListCustomEmojisResponse
	err := c.do(ctx, request{
		method: "GET",
		path:   "customEmojis",
		query:  q,
		scope:  scopes.CustomEmojisReadonly,
	}, &out)
	return &out, err
}

// GetCustomEmoji returns one. name is "customEmojis/{id}".
func (c *Client) GetCustomEmoji(ctx context.Context, name string) (*CustomEmoji, error) {
	var out CustomEmoji
	err := c.do(ctx, request{
		method: "GET",
		name:   name,
		scope:  scopes.CustomEmojisReadonly,
	}, &out)
	return &out, err
}

// CreateCustomEmoji adds one to the organisation. The image travels
// base64-encoded inside the JSON body, which is why the size cap
// matters: there is no resumable upload here.
func (c *Client) CreateCustomEmoji(ctx context.Context, body *CustomEmoji) (*CustomEmoji, error) {
	var out CustomEmoji
	err := c.do(ctx, request{
		method: "POST",
		path:   "customEmojis",
		body:   body,
		scope:  scopes.CustomEmojis,
	}, &out)
	return &out, err
}

// DeleteCustomEmoji removes one. name is "customEmojis/{id}".
func (c *Client) DeleteCustomEmoji(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.CustomEmojis)
}

// DeleteSpace removes a space. name is "spaces/{id}".
//
// Google always cascades: the space's messages and memberships go with
// it. Its own words, and the reason this is the last thing in the tool
// surface a caller should reach for.
func (c *Client) DeleteSpace(ctx context.Context, name string) error {
	return c.deleteName(ctx, name, scopes.Delete)
}
