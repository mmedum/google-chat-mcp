package gchat

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// personFields is what this server reads off a Person. Asking for less
// than the caller needs costs a second round trip; asking for more
// widens what a People response can carry into a log.
const personFields = "emailAddresses,names"

// GetPerson resolves a Chat user id to a People profile. user is
// "users/{id}"; the numeric id is the same in both namespaces.
//
// A person who is not in the caller's directory answers 404, which the
// caller treats as "no email", not as a failure.
func (c *Client) GetPerson(ctx context.Context, user string) (*Person, error) {
	resource := "people/" + strings.TrimPrefix(user, "users/")
	q := url.Values{"personFields": {personFields}}
	var out Person
	err := c.do(ctx, request{
		api:    peopleAPI,
		method: "GET",
		name:   resource,
		query:  q,
		scope:  scopes.DirectoryReadonly,
	}, &out)
	return &out, err
}

// MaxPeopleBatch is how many people one lookup may ask about. Google's
// cap, and the reason a page of messages costs one request rather than
// one per sender.
const MaxPeopleBatch = 200

// GetPeople resolves up to MaxPeopleBatch Chat user ids at once.
//
// The batch endpoint answers per person, so someone the caller cannot
// see comes back with a status and no profile rather than failing the
// others. That matches this server's rule exactly: a lookup that fails
// costs the email field and nothing else.
func (c *Client) GetPeople(ctx context.Context, users []string) (*BatchGetPeopleResponse, error) {
	if len(users) > MaxPeopleBatch {
		users = users[:MaxPeopleBatch]
	}
	q := url.Values{"personFields": {personFields}}
	for _, u := range users {
		q.Add("resourceNames", "people/"+strings.TrimPrefix(u, "users/"))
	}
	var out BatchGetPeopleResponse
	err := c.do(ctx, request{
		api:    peopleAPI,
		method: "GET",
		path:   "people",
		verb:   "batchGet",
		query:  q,
		scope:  scopes.DirectoryReadonly,
	}, &out)
	return &out, err
}

// maxDirectoryPageSize and maxContactsPageSize are Google's caps.
const (
	maxDirectoryPageSize = 500
	maxContactsPageSize  = 30
)

// SearchDirectoryPeople searches the caller's Workspace directory.
//
// The sources are required: without them Google answers 400. Both are
// sent so one call covers Workspace members and the external contacts
// an administrator shared with the domain.
func (c *Client) SearchDirectoryPeople(ctx context.Context, query string, limit int) (*SearchDirectoryPeopleResponse, error) {
	q := url.Values{
		"query":    {query},
		"readMask": {personFields},
		"sources": {
			"DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE",
			"DIRECTORY_SOURCE_TYPE_DOMAIN_CONTACT",
		},
	}
	q.Set("pageSize", strconv.Itoa(min(limit, maxDirectoryPageSize)))
	var out SearchDirectoryPeopleResponse
	err := c.do(ctx, request{
		api:    peopleAPI,
		method: "GET",
		path:   "people",
		verb:   "searchDirectoryPeople",
		query:  q,
		scope:  scopes.DirectoryReadonly,
	}, &out)
	return &out, err
}

// SearchContacts searches the caller's own contacts. It is what a
// consumer account has instead of a directory, and it also matches the
// contacts Google fills in from past conversations.
func (c *Client) SearchContacts(ctx context.Context, query string, limit int) (*SearchContactsResponse, error) {
	q := url.Values{"query": {query}, "readMask": {personFields}}
	q.Set("pageSize", strconv.Itoa(min(limit, maxContactsPageSize)))
	var out SearchContactsResponse
	err := c.do(ctx, request{
		api:    peopleAPI,
		method: "GET",
		path:   "people",
		verb:   "searchContacts",
		query:  q,
		scope:  scopes.ContactsReadonly,
	}, &out)
	return &out, err
}

// PrimaryEmail is a person's primary address, or the first one Google
// listed when none is marked primary.
func (p *Person) PrimaryEmail() string {
	if p == nil {
		return ""
	}
	for _, e := range p.EmailAddrs {
		if e.Metadata != nil && e.Metadata.Primary && e.Value != "" {
			return e.Value
		}
	}
	for _, e := range p.EmailAddrs {
		if e.Value != "" {
			return e.Value
		}
	}
	return ""
}

// PrimaryName is a person's primary display name, or the first one.
func (p *Person) PrimaryName() string {
	if p == nil {
		return ""
	}
	for _, n := range p.Names {
		if n.Metadata != nil && n.Metadata.Primary && n.DisplayName != "" {
			return n.DisplayName
		}
	}
	for _, n := range p.Names {
		if n.DisplayName != "" {
			return n.DisplayName
		}
	}
	return ""
}
