package service

import (
	"context"
	"strings"
	"sync"

	"github.com/mmedum/google-chat-mcp/internal/directory"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// People search limits, as the tool schema documents them.
const (
	defaultPeopleLimit = 10
	maxPeopleLimit     = 100
	maxPeopleQuery     = 200
)

// PeopleSource is where a hit came from.
type PeopleSource string

// People sources. Directory is the Workspace directory; Contacts is the
// caller's own contact list, which is what a consumer account has
// instead.
const (
	SourceDirectory PeopleSource = "DIRECTORY"
	SourceContacts  PeopleSource = "CONTACTS"
)

// PersonHit is one match.
//
// UserID is set only when the hit resolves to a Workspace profile,
// whose numeric id is shared with Chat's users/{id}. A contact id from
// the caller's own list does not round-trip to a Chat sender, so it is
// left empty rather than guessed at.
type PersonHit struct {
	UserID      string
	Email       string
	DisplayName string
	Source      PeopleSource
}

// SearchPeopleInput is a lookup across the directory and contacts.
type SearchPeopleInput struct {
	Query   string
	Limit   int
	Sources []PeopleSource
}

// SearchPeopleResult is what the lookup found and which sources
// answered.
//
// A source in Attempted but not Succeeded refused. That is reported
// rather than raised: the hits from the other source are worth more to
// a caller than an error, and a model reading an error will simply try
// a broader query.
type SearchPeopleResult struct {
	People    []PersonHit
	Attempted []PeopleSource
	Succeeded []PeopleSource
}

// SearchPeople turns a name fragment into an email address.
//
// Hits from the directory back-fill the email cache, so a later
// get_messages or list_members resolves the same person without another
// People request.
func (s *Service) SearchPeople(ctx context.Context, in SearchPeopleInput) (*SearchPeopleResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, Invalidf("query is required")
	}
	if len(query) > maxPeopleQuery {
		return nil, Invalidf("query is %d characters; the maximum is %d", len(query), maxPeopleQuery)
	}
	limit, err := clampLimit("limit", in.Limit, defaultPeopleLimit, maxPeopleLimit)
	if err != nil {
		return nil, err
	}
	sources, err := narrowSources(in.Sources)
	if err != nil {
		return nil, err
	}

	type outcome struct {
		people []gchat.Person
		err    error
	}
	results := make([]outcome, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].people, results[i].err = s.searchOneSource(ctx, source, query, limit)
		}()
	}
	wg.Wait()

	out := &SearchPeopleResult{People: []PersonHit{}, Attempted: sources}
	seen := map[string]bool{}
	learned := map[string]directory.Person{}
	var failures []error

	for i, source := range sources {
		if err := results[i].err; err != nil {
			failures = append(failures, err)
			// One source dying must not mask the other's hits, so this
			// is logged rather than returned. An operator diagnosing a
			// Workspace whose administrator turned directory sharing
			// off needs the upstream status, which is in the error. A
			// scope nobody granted is not their problem: it comes back
			// as a [scope] error when neither source answered.
			if !gchat.IsMissingScope(err) {
				s.log.Warn("people_source_failed", "source", string(source), "error", err.Error())
			}
			continue
		}
		out.Succeeded = append(out.Succeeded, source)
		for _, person := range results[i].people {
			if person.ResourceName == "" || seen[person.ResourceName] {
				// First source wins. The directory is asked first by
				// default, so a colleague's Workspace profile beats
				// the same person's entry in a personal contact list.
				continue
			}
			seen[person.ResourceName] = true
			hit := PersonHit{
				UserID:      directory.ChatUserID(person.ResourceName),
				Email:       person.PrimaryEmail(),
				DisplayName: person.PrimaryName(),
				Source:      source,
			}
			out.People = append(out.People, hit)
			if hit.UserID != "" && hit.Email != "" {
				learned[hit.UserID] = directory.Person{Email: hit.Email, DisplayName: hit.DisplayName}
			}
		}
	}

	if len(out.Succeeded) == 0 && len(failures) > 0 {
		return nil, Classify(failures[0])
	}

	if len(learned) > 0 && s.people != nil {
		s.people.Learn(learned)
	}
	if len(out.People) > limit {
		out.People = out.People[:limit]
	}
	return out, nil
}

// searchOneSource queries one upstream.
func (s *Service) searchOneSource(ctx context.Context, source PeopleSource, query string, limit int) ([]gchat.Person, error) {
	if source == SourceContacts {
		resp, err := s.client.SearchContacts(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		people := make([]gchat.Person, 0, len(resp.Results))
		for _, r := range resp.Results {
			if r.Person != nil {
				people = append(people, *r.Person)
			}
		}
		return people, nil
	}
	resp, err := s.client.SearchDirectoryPeople(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return resp.People, nil
}

// narrowSources validates the requested sources, defaulting to both.
//
// Both by default so one tool answers for a Workspace account and a
// consumer one: the directory is empty for the second, and contacts are
// close to empty for the first.
func narrowSources(in []PeopleSource) ([]PeopleSource, error) {
	if len(in) == 0 {
		return []PeopleSource{SourceDirectory, SourceContacts}, nil
	}
	out := make([]PeopleSource, 0, len(in))
	seen := map[PeopleSource]bool{}
	for _, s := range in {
		if err := requireEnum("sources", string(s), string(SourceDirectory), string(SourceContacts)); err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}
