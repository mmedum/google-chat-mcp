package directory

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
)

// PeopleAPI is the part of the Chat client this package uses.
type PeopleAPI interface {
	GetPeople(ctx context.Context, users []string) (*gchat.BatchGetPeopleResponse, error)
}

// Resolver turns Chat user ids into people, through the cache first.
type Resolver struct {
	people PeopleAPI
	cache  *Cache
	log    *slog.Logger
}

// NewResolver builds a Resolver.
func NewResolver(people PeopleAPI, cache *Cache, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Resolver{people: people, cache: cache, log: log}
}

// Resolve looks up every id and never fails.
//
// Every id asked for is present in the result, so a caller can index
// without a fallback; an id that could not be resolved maps to a zero
// Person. Lookups run per unique id, not per row: a fifty-message
// thread between three people costs three requests.
func (r *Resolver) Resolve(ctx context.Context, ids []string) map[string]Person {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	out := make(map[string]Person, len(unique))
	if len(unique) == 0 {
		return out
	}

	var misses []string
	if r.cache != nil {
		cached := r.cache.Get(unique)
		for _, id := range unique {
			if p, ok := cached[id]; ok {
				out[id] = p
				continue
			}
			misses = append(misses, id)
		}
	} else {
		misses = unique
	}
	if len(misses) == 0 {
		return out
	}

	learned, answered := r.lookup(ctx, misses)
	for _, id := range misses {
		out[id] = learned[id]
	}
	// Only what Google actually answered for. An id in a chunk that
	// failed is empty here for the same reason a genuine miss is, and
	// caching that would turn one 429 into a whole cache lifetime of
	// null addresses — the degrade rule says a People failure costs the
	// email field on that call, not for the next 24 hours.
	if r.cache != nil && len(answered) > 0 {
		cacheable := make(map[string]Person, len(answered))
		for _, id := range answered {
			cacheable[id] = learned[id]
		}
		r.cache.Put(cacheable)
	}
	return out
}

// ResolveOne is Resolve for a single id.
func (r *Resolver) ResolveOne(ctx context.Context, id string) Person {
	return r.Resolve(ctx, []string{id})[id]
}

// lookup resolves the misses in as few requests as Google allows, and
// absorbs every failure.
//
// A whole batch failing is logged rather than returned, because no
// caller could do anything with it. What an operator needs is in the
// line: a 403 means the directory scope was never granted, a 429 means
// quota, a 5xx means Google. The payload never is — it carries names
// and addresses — and neither does a per-person status, which is the
// ordinary answer for someone outside the caller's directory.
//
// Every id asked for is in the result, resolved or not. answered is the
// subset from a chunk Google replied to, which is the only part the
// caller may cache: an unresolved id from a failed chunk looks exactly
// like a person who is not in the directory.
func (r *Resolver) lookup(ctx context.Context, ids []string) (map[string]Person, []string) {
	out := make(map[string]Person, len(ids))
	for _, id := range ids {
		out[id] = Person{}
	}
	answered := make([]string, 0, len(ids))
	for chunk := range slices.Chunk(ids, gchat.MaxPeopleBatch) {
		resp, err := r.people.GetPeople(ctx, chunk)
		if err != nil {
			r.log.Warn("person_lookup_degraded", "count", len(chunk), "error", err.Error())
			continue
		}
		answered = append(answered, chunk...)
		for _, entry := range resp.Responses {
			id := chatUserOf(entry.RequestedResourceName)
			if id == "" || entry.Person == nil {
				continue
			}
			out[id] = Person{
				Email:       entry.Person.PrimaryEmail(),
				DisplayName: entry.Person.PrimaryName(),
			}
		}
	}
	return out, answered
}

// chatUserOf turns the resource name a batch entry echoes back into the
// Chat user id the caller asked about.
func chatUserOf(resourceName string) string {
	id, ok := strings.CutPrefix(resourceName, "people/")
	if !ok || id == "" {
		return ""
	}
	return "users/" + id
}

// workspaceProfile matches the one People resource shape that shares a
// namespace with Chat's users/{id}: the numeric Workspace profile id.
// A people/c{hex} contact id belongs to the caller's own contact list
// and does not round-trip, so it must never be cached under a Chat id.
var workspaceProfile = regexp.MustCompile(`^people/([0-9]+)$`)

// ChatUserID translates a People resource name to the Chat user id for
// the same person, or returns "" when the two namespaces do not meet.
func ChatUserID(resourceName string) string {
	m := workspaceProfile.FindStringSubmatch(resourceName)
	if m == nil {
		return ""
	}
	return "users/" + m[1]
}

// Learn records people that were resolved somewhere else, such as a
// directory search, so a later message listing does not look them up
// again.
func (r *Resolver) Learn(people map[string]Person) {
	if r.cache == nil || len(people) == 0 {
		return
	}
	r.cache.Put(people)
}
