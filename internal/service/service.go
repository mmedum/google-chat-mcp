package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/directory"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// Service turns tool intent into Chat API calls and applies the rules
// that outlive any one tool: what a delete means when the target is
// gone, how a partial failure degrades, which scope a call needs.
type Service struct {
	client *gchat.Client
	people *directory.Resolver
	cfg    config.Config
	log    *slog.Logger

	// callerSub is the signed-in account's subject, learned on first
	// use. It cannot change: one process holds one refresh token.
	callerMu  sync.RWMutex
	callerSub string
}

// New builds a Service. people may be nil, in which case nothing is
// enriched and every email comes back empty.
func New(client *gchat.Client, people *directory.Resolver, cfg config.Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{client: client, people: people, cfg: cfg, log: log}
}

// Preview returns a context under which no write may leave the process.
//
// internal/tools puts every dry run on one, so the promise that a
// preview changes nothing is kept by the client rather than by each
// tool remembering to return early.
func Preview(ctx context.Context) context.Context { return gchat.WithoutWrites(ctx) }

// DriftCount reports how many unknown response fields have been seen.
func (s *Service) DriftCount() int64 { return s.client.DriftCount() }

// DriftPaths lists those fields by path, for the doctor command.
func (s *Service) DriftPaths() []string { return s.client.DriftPaths() }

// Identity is who the stored token belongs to. The field names match
// the tool output, which callers depend on.
type Identity struct {
	// UserSub is Google's stable subject id for the account.
	UserSub     string
	Email       string
	DisplayName string
	PictureURL  string
}

// Whoami returns the account behind the stored credentials.
//
// It always asks Google. A caller using it as a credentials check needs
// a live answer, which a cached one would not be. What it learns is
// recorded, because the subject is also what add_reaction needs to find
// the caller's own reaction, and that value cannot change: this process
// holds one refresh token for its whole life.
func (s *Service) Whoami(ctx context.Context) (*Identity, error) {
	info, err := s.client.Userinfo(ctx)
	if err != nil {
		return nil, Classify(err)
	}
	s.rememberCaller(info.Sub)
	return &Identity{
		UserSub:     info.Sub,
		Email:       info.Email,
		DisplayName: info.Name,
		PictureURL:  info.Picture,
	}, nil
}

// numericID matches the account id Google uses in both users/{id} and
// the OpenID subject.
var numericID = regexp.MustCompile(`^[0-9]+$`)

// rememberCaller records the signed-in account's subject.
func (s *Service) rememberCaller(sub string) {
	if !numericID.MatchString(sub) {
		return
	}
	s.callerMu.Lock()
	s.callerSub = sub
	s.callerMu.Unlock()
}

// callerID is the signed-in account as a Chat user name, fetched once.
//
// The check on the shape is not ceremony: the value goes into a Chat
// filter expression, and everything else this server puts in one is
// checked before it gets there. Google's own endpoint is the source,
// so this says so structurally rather than trusting it.
func (s *Service) callerID(ctx context.Context) (string, error) {
	s.callerMu.RLock()
	sub := s.callerSub
	s.callerMu.RUnlock()
	if sub != "" {
		return "users/" + sub, nil
	}

	info, err := s.client.Userinfo(ctx)
	if err != nil {
		return "", Classify(err)
	}
	if !numericID.MatchString(info.Sub) {
		return "", Failf(ClassUpstream, "Google reported an account id this server does not recognise.")
	}
	s.rememberCaller(info.Sub)
	return "users/" + info.Sub, nil
}

// deleteIdempotent removes a resource and reports whether it was there.
//
// The rule the three deletes share is the subtle one, so it is written
// once: a target that is already gone is the state the caller asked
// for, and a missing scope never counts as gone — that would tell
// someone the delete succeeded when what they needed was a prompt to
// grant a scope.
//
// confirmGone is what a plain refusal costs. Google answers "you may
// not delete this" and "this is already deleted, and the space keeps no
// history" with the same 403, and reporting both as gone tells a caller
// a message they were refused is no longer there. So the refusal path
// goes and looks: only a not-found answer proves it. That read happens
// on a call that has already failed, and nowhere else.
//
// A nil confirmGone reports every refusal, which is what a section
// needs: its 403 is Google declining to remove a system section.
func deleteIdempotent(
	ctx context.Context,
	name string,
	del func(context.Context, string) error,
	confirmGone func(context.Context, string) bool,
) (bool, error) {
	err := del(ctx, name)
	switch {
	case err == nil:
		return true, nil
	case gchat.IsAlreadyGone(err):
		return false, nil
	case confirmGone == nil || !gchat.IsForbidden(err):
		return false, Classify(err)
	case confirmGone(ctx, name):
		return false, nil
	}
	// Still there, or not readable either. Both leave the refusal
	// standing, which is the honest answer: nothing was deleted.
	return false, Classify(err)
}

// confirmGone turns a read into the question deleteIdempotent asks
// after a refusal: is the target actually gone?
//
// A not-found answer proves it. So does a tombstone, which is the
// answer Google normally gives: a deleted message reads back as a 200
// carrying its own deleteTime, with the text and the sender stripped —
// found live on 2026-09-05, and the reason the first version of this
// check reported an ordinary repeat delete as a refusal.
//
// Anything else leaves the question open, and an open question means
// the caller hears the refusal rather than a report that the thing was
// already gone.
func confirmGone[T any](read func(context.Context, string) (T, error), deleted func(T) bool) func(context.Context, string) bool {
	return func(ctx context.Context, name string) bool {
		got, err := read(ctx, name)
		if err != nil {
			return gchat.IsNotFound(err)
		}
		return deleted(got)
	}
}

// resolvePeople is Resolve with a nil resolver allowed, so a Service
// built without one still answers.
func (s *Service) resolvePeople(ctx context.Context, ids []string) map[string]directory.Person {
	if s.people == nil {
		return map[string]directory.Person{}
	}
	return s.people.Resolve(ctx, ids)
}

// narrowEnum keeps a value Google sent only when this server knows it,
// and falls back otherwise.
//
// The fallback is the point. A closed enum that rejects an unknown
// value fails the whole row it arrived on, and Google adds enum members
// without notice. The caller still gets the row, tagged as a kind this
// server has not learned.
func narrowEnum(value string, allowed []string, fallback string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return fallback
}

// requireEnum is narrowEnum's opposite, for a value the caller sent.
//
// Google's own vocabulary degrades, because a value this server has not
// learned still describes something real. An argument does not: a
// caller who asked for a kind that does not exist wants to hear so,
// with the choices named.
func requireEnum(field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return Invalidf("%s %q is not one of %s", field, value, strings.Join(allowed, ", "))
}

// warnUnparsed reports rows a listing could not use. Every listing has
// the same three lines to write, and the count is the only signal a
// short page is short for a reason.
func (s *Service) warnUnparsed(event string, dropped, total int) {
	if dropped > 0 {
		s.log.Warn(event, "dropped", dropped, "of", total)
	}
}

// parseTime reads one of Google's timestamps.
//
// An absent or unreadable value becomes the zero time rather than an
// error. Google renaming createTime must not shorten a listing, and a
// row of zero timestamps is a visible symptom where a missing row is
// not; every caller here has an id and a body to show regardless.
func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseArgTime reads a timestamp a caller supplied, accepting a bare
// date alongside RFC 3339.
//
// Being lenient here is deliberate. The strict form is what Google
// wants and what the schema documents, but a model that sends
// "2026-01-01" should get the day it asked for rather than a schema
// violation it cannot see the shape of.
//
// It lives here rather than in the tool handler so that a tool taking a
// time cannot forget to call it: the service takes the string.
func parseArgTime(field, v string) (time.Time, error) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, Invalidf("%s %q is not a timestamp; use RFC 3339, such as 2026-01-01T00:00:00Z", field, v)
}

// googleTime formats a timestamp the way Chat's filter grammar wants
// it. Google documents the microsecond form and rejects some others.
func googleTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// clampLimit applies a tool's documented default and maximum.
func clampLimit(field string, limit, fallback, maximum int) (int, error) {
	switch {
	case limit <= 0:
		return fallback, nil
	case limit > maximum:
		return 0, Invalidf("%s %d is above the maximum of %d", field, limit, maximum)
	}
	return limit, nil
}

// spaceOfMessage slices "spaces/{s}" out of a message resource name.
func spaceOfMessage(name string) string {
	if i := strings.Index(name, "/messages/"); i > 0 {
		return name[:i]
	}
	return ""
}

// sectionOfItem slices "users/{u}/sections/{s}" out of an item name.
func sectionOfItem(name string) string {
	if i := strings.Index(name, "/items/"); i > 0 {
		return name[:i]
	}
	return ""
}

// createdAfterFilter bounds a message listing below.
//
// The quoting matters and is not obvious: Chat's filter grammar wants
// the timestamp quoted here, while the space filter on a section item
// listing must not be. One place to get it right.
func createdAfterFilter(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return `createTime > "` + googleTime(t) + `"`
}

// requireText checks a text argument the caller wrote.
//
// The value is returned unchanged, never trimmed. send_message posts
// what it is given, and a server that quietly edited a message body
// would be doing the one thing this server promises not to.
//
// The length is counted in characters rather than bytes, because that
// is what the schemas bound and what a person writing a
// message counts.
func requireText(field, value string, maximum int) (string, error) {
	if value == "" {
		return "", Invalidf("%s is required", field)
	}
	if n := utf8.RuneCountInString(value); n > maximum {
		return "", Invalidf("%s is %d characters, above the maximum of %d", field, n, maximum)
	}
	return value, nil
}

// requireDisplayName checks a label, which unlike message text may be
// trimmed: leading space in a name is a typo, in a message it is the
// message.
func requireDisplayName(value string, maximum int) (string, error) {
	return requireText("display_name", strings.TrimSpace(value), maximum)
}

// requireEmails checks a list of addresses and rejects duplicates.
//
// A repeated address is refused rather than deduplicated: the count
// this server reports back is the number of people the caller asked
// for, and silently returning a smaller space than requested is the
// kind of quiet difference nobody notices until later.
func requireEmails(field string, values []string, minimum, maximum int) ([]string, error) {
	if len(values) < minimum {
		return nil, Invalidf("%s needs at least %d address(es), got %d", field, minimum, len(values))
	}
	if len(values) > maximum {
		return nil, Invalidf("%s takes at most %d addresses, got %d", field, maximum, len(values))
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		email, err := requireEmail(field, raw)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(email)
		if seen[key] {
			return nil, Invalidf("%s lists %s twice", field, email)
		}
		seen[key] = true
		out = append(out, email)
	}
	return out, nil
}

// renderBody is the dry-run preview of a request body.
//
// It encodes the same struct the real call sends, so a preview cannot
// describe a body the write would not post. Every rendered_payload in
// this server comes through here for that reason.
//
// The error is returned rather than swallowed or panicked on. No body
// this package sends today can fail to encode, but a preview that
// silently came back null would read as a real write, and a panic in a
// tool handler costs the whole stdio session. Bodies carrying file
// content arrive with the attachment tools.
func renderBody(body any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, Failf(ClassUnexpected, "could not render the request body: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, Failf(ClassUnexpected, "could not render the request body: %v", err)
	}
	return out, nil
}
