package gchat

import (
	"context"
	"net/url"

	"github.com/mmedum/google-chat-mcp/v3/internal/scopes"
)

// DefaultCloudIdentityBase serves the Cloud Identity Groups API, which is
// where a Google Group's id comes from. Chat names a group in a space by
// that same id, groups/{id}, and offers no way to look one up.
const DefaultCloudIdentityBase = "https://cloudidentity.googleapis.com/v1"

// IdentityGroup is a Google Group as Cloud Identity describes it. Named
// apart from Group, which is Chat's reference to a group in a space.
type IdentityGroup struct {
	// Name is groups/{id}, the same name Chat uses.
	Name        string    `json:"name"`
	GroupKey    EntityKey `json:"groupKey"`
	DisplayName string    `json:"displayName,omitempty"`
}

// EntityKey identifies a group. For a Google Group, ID is its email
// address and Namespace is empty.
type EntityKey struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
}

// LookupGroupNameResponse is groups.lookup.
type LookupGroupNameResponse struct {
	Name string `json:"name"`
}

// LookupGroup turns a group's email address into its groups/{id} name.
//
// The address goes in the query, which errors and logs never carry, so a
// failed lookup does not put it in a log line.
func (c *Client) LookupGroup(ctx context.Context, email string) (string, error) {
	var out LookupGroupNameResponse
	err := c.do(ctx, request{
		api:    cloudIdentityAPI,
		method: "GET",
		path:   "groups",
		verb:   "lookup",
		query:  url.Values{"groupKey.id": {email}},
		scope:  scopes.GroupsReadonly,
	}, &out)
	return out.Name, err
}

// groupFields is what this server reads off a group. Google's fields
// system parameter trims the answer to it: a group also carries its
// parent, labels and timestamps, which nothing reads, and every field
// decoded but not modeled is reported as schema drift.
const groupFields = "name,groupKey,displayName"

// GetGroup reads one group by its groups/{id} name.
func (c *Client) GetGroup(ctx context.Context, name string) (*IdentityGroup, error) {
	var out IdentityGroup
	err := c.do(ctx, request{
		api:    cloudIdentityAPI,
		method: "GET",
		name:   name,
		query:  url.Values{"fields": {groupFields}},
		scope:  scopes.GroupsReadonly,
	}, &out)
	return &out, err
}
