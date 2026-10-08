package service

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmedum/google-chat-mcp/v5/internal/gchat"
)

// Search limits. The page cap is what stops an unbounded scan of a busy
// space from spending a caller's whole quota on one tool call.
const (
	defaultSearchLimit = 50
	maxSearchLimit     = 100
	maxSearchPages     = 50
	// defaultSearchPages stands in when no operator default was set,
	// which is every Service built without a loaded configuration.
	defaultSearchPages = 10
	snippetContext     = 80
)

// SearchMessagesInput is either of the two searches this tool does.
//
// Query goes to Google, which searches every space the caller can see
// and matches whole words. Regex is scanned here, over one space, and
// is the only way to match a pattern — Google's grammar has none.
type SearchMessagesInput struct {
	// Space scopes either search. Google's search takes every
	// accessible space when this is empty; the local scan requires it.
	Space string
	// Query is Google's search: bare keywords, quoted for a phrase.
	Query string
	// Regex is an RE2 pattern, scanned here.
	Regex string
	// CreatedAfter and CreatedBefore bound both searches, as RFC 3339
	// or a bare date. Without a lower bound a local scan of a large
	// space reaches the page cap and the result is partial.
	CreatedAfter  string
	CreatedBefore string
	// SenderEmail, MentionsMe, HasAttachment, HasLink and UnreadOnly
	// narrow Google's search. They are not available to the local scan,
	// which reads what list_messages returns.
	SenderEmail   string
	MentionsMe    bool
	HasAttachment bool
	HasLink       bool
	UnreadOnly    bool
	// SpaceType and SpaceName narrow Google's search to spaces of one
	// kind, or to spaces whose display names hold every one of the
	// words.
	SpaceType string
	SpaceName string
	// ByRelevance orders Google's search by relevance rather than by
	// time.
	ByRelevance bool
	Limit       int
	// MaxPages overrides the operator's default for the local scan.
	// Zero takes it.
	MaxPages int
	// PageToken continues Google's search.
	PageToken string
}

// SearchMatch is one message that matched.
type SearchMatch struct {
	Name         string
	ThreadName   string
	SenderUserID string
	// SenderEmail is the address Chat named for the sender, or one this
	// server has already seen, and empty otherwise. A search looks nobody
	// up in People.
	SenderEmail string
	Text        string
	CreateTime  time.Time
	// Snippet is the text around the first match, so a caller can see
	// why the message matched without reading all of it.
	Snippet string
	// Quote is what the message quotes or forwards, and is nil when it
	// quotes nothing. Its sender's address is unresolved here: a search
	// reports the ids it was given and resolves nobody.
	Quote *MessageQuote
	// Links is what the message's text links to; see MessageLink. A hit
	// whose body is a link reads as a bare word without it.
	Links []MessageLink
	MessageExtras
	// Read and SpaceMuted are the caller's read state for the hit and
	// mute setting for its space, from Google's search alone. Nil when
	// Google did not say, which it does not without the scope for each.
	Read       *bool
	SpaceMuted *bool

	// users is the sender and the people the message mentions, as Chat
	// named them, for withSenders.
	users []*gchat.User
}

// searchMatch shapes one hit, with the snippet centered on at.
//
// Both searches read the same messages and differ only in where the
// snippet starts, so they shape a hit here rather than each in its own
// loop: a field added to SearchMatch is filled once. A missing sender
// or thread arrives empty, the way a listing keeps the row.
func searchMatch(m gchat.Message, at int) SearchMatch {
	match := SearchMatch{
		Name:       m.Name,
		Text:       m.Text,
		CreateTime: parseTime(m.CreateTime),
		Snippet:    snippet(m.Text, at),
		Links:      messageLinks(m.Annotations),
		Quote:      messageQuote(m.QuotedMessage),

		MessageExtras: messageExtras(m),
	}
	if m.Thread != nil {
		match.ThreadName = m.Thread.Name
	}
	if m.Sender != nil {
		match.SenderUserID = m.Sender.Name
	}
	match.users = append([]*gchat.User{m.Sender}, mentioned(m.Annotations)...)
	return match
}

// withSenders names each match's sender from what Chat sent and what
// the cache already holds, and remembers what Chat named, the way a
// listing does. It asks People nothing: a search can match hundreds of
// messages, and a request per stranger is not what a search is for.
func (s *Service) withSenders(out *SearchMessagesResult, err error) (*SearchMessagesResult, error) {
	if err != nil {
		return nil, err
	}
	var users []*gchat.User
	for _, m := range out.Matches {
		users = append(users, m.users...)
	}
	known := s.knownPeople(users)
	for i := range out.Matches {
		out.Matches[i].SenderEmail = known[out.Matches[i].SenderUserID].Email
	}
	return out, nil
}

// SearchMessagesResult is what a search found and how much it left.
type SearchMessagesResult struct {
	Matches []SearchMatch
	// Scanned is how many messages were read before filtering. Google's
	// search filters upstream, so there it is the number returned.
	Scanned int
	// CapReached says there is more than was returned: a local scan
	// that stopped at the page cap or filled the limit, or a Google
	// search with a further page. It is the only signal a caller has
	// that an answer is partial, so it covers every way of being
	// partial rather than only the page cap.
	CapReached bool
	// NextPageToken continues Google's search. The local scan has none:
	// it pages Google's history itself and reports CapReached instead.
	NextPageToken string
	// Unparsed counts messages that were read but could not be
	// searched. Non-zero means this server's models are stale and the
	// result is incomplete, which is not the same as no matches.
	Unparsed int
	// Server says which search answered, so a caller can tell "Google
	// found nothing" from "the local scan reached its cap".
	Server bool
}

// SearchMessages runs whichever of the two searches the arguments ask
// for.
//
// A pattern is scanned here, because Google's grammar has no regular
// expressions; anything else goes to Google, which reads every space
// the caller can see rather than the ten pages a local scan affords.
func (s *Service) SearchMessages(ctx context.Context, in SearchMessagesInput) (*SearchMessagesResult, error) {
	if in.Regex == "" {
		return s.withSenders(s.searchUpstream(ctx, in))
	}
	if in.Query != "" {
		return nil, Invalidf("pass query for Google's search or regex for a local scan, not both")
	}
	for field, set := range map[string]bool{
		"sender_email":   in.SenderEmail != "",
		"mentions_me":    in.MentionsMe,
		"has_attachment": in.HasAttachment,
		"has_link":       in.HasLink,
		"unread_only":    in.UnreadOnly,
		"space_type":     in.SpaceType != "",
		"space_name":     in.SpaceName != "",
		"by_relevance":   in.ByRelevance,
		"page_token":     in.PageToken != "",
	} {
		if set {
			return nil, Invalidf("%s narrows Google's search and cannot be combined with regex, "+
				"which is scanned here over one space", field)
		}
	}
	return s.withSenders(s.scanSpace(ctx, in))
}

// scanSpace reads one space's history and matches a pattern here.
func (s *Service) scanSpace(ctx context.Context, in SearchMessagesInput) (*SearchMessagesResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	pattern, err := searchTerm(in)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return nil, err
	}
	operatorPages := s.cfg.SearchMaxPages
	if operatorPages <= 0 {
		operatorPages = defaultSearchPages
	}
	maxPages, err := clampLimit("max_pages", in.MaxPages, operatorPages, maxSearchPages)
	if err != nil {
		return nil, err
	}
	after, err := parseArgTime("created_after", in.CreatedAfter)
	if err != nil {
		return nil, err
	}
	before, err := parseArgTime("created_before", in.CreatedBefore)
	if err != nil {
		return nil, err
	}
	if err := requireWindow("created_after", after, "created_before", before); err != nil {
		return nil, err
	}

	opts := gchat.ListMessagesOptions{
		Space:    space,
		OrderBy:  "createTime desc",
		PageSize: maxPageSize,
		// The listing filter has > but not >=, and created_after means
		// at or after; a microsecond, the timestamps' precision, closes
		// the gap.
		Filter: createTimeFilter(inclusive(after), before),
	}

	out := &SearchMessagesResult{Matches: []SearchMatch{}}
	// Logged however the scan ends. A message that was fetched but not
	// searched is drift, and a caller reading "no matches" has no way
	// to tell that from a space with nothing in it.
	defer func() {
		if out.Unparsed > 0 {
			s.log.Warn("search_messages_unparsed", "count", out.Unparsed, "scanned", out.Scanned)
		}
	}()

	for range maxPages {
		resp, err := s.client.ListMessages(ctx, opts)
		if err != nil {
			return nil, Classify(err)
		}
		for _, m := range resp.Messages {
			out.Scanned++
			// Only a message with no resource name is unsearchable:
			// there is nothing to hand back for it. A missing sender,
			// thread or timestamp arrives empty on the match, the same
			// way a listing keeps the row.
			if m.Name == "" {
				out.Unparsed++
				continue
			}
			at, found := matchIndex(m.Text, pattern)
			if !found {
				continue
			}
			out.Matches = append(out.Matches, searchMatch(m, at))
			if len(out.Matches) >= limit {
				// Full, with history still unread. Saying otherwise
				// reports fifty of four hundred matches as all of them,
				// and cap_reached is the only signal there is.
				out.CapReached = true
				return out, nil
			}
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		opts.PageToken = resp.NextPageToken
	}
	// The loop ended with a page token still in hand.
	out.CapReached = true
	return out, nil
}

// searchUpstream runs Google's own search.
//
// Everything the caller asked for becomes one filter expression, which
// is the only place Google's grammar is written down: bare keywords,
// AND between fields, and the two functions that have no field of their
// own. A filter this builds cannot be malformed by a caller's text,
// because a keyword carrying a quote is refused rather than escaped.
func (s *Service) searchUpstream(ctx context.Context, in SearchMessagesInput) (*SearchMessagesResult, error) {
	limit, err := clampLimit("limit", in.Limit, defaultSearchLimit, gchat.MaxSearchMessagesPageSize)
	if err != nil {
		return nil, err
	}
	filter, err := s.searchFilter(ctx, in)
	if err != nil {
		return nil, err
	}

	orderBy := "createTime desc"
	if in.ByRelevance {
		// Google has this in Developer Preview. A project outside the
		// program is answered with a 400, which reaches the caller as
		// an [invalid] error carrying Google's own words.
		orderBy = "relevance desc"
	}

	resp, err := s.client.SearchMessages(ctx, gchat.SearchMessagesOptions{
		// Always every space. Google refuses a single space as the
		// parent — "Invalid parent. Specify 'spaces/-'", found live —
		// and takes the space in the filter instead.
		Parent:    gchat.AllSpaces,
		Filter:    filter,
		OrderBy:   orderBy,
		PageSize:  limit,
		PageToken: in.PageToken,
		Unread:    in.UnreadOnly,
		BySpace:   in.SpaceType != "" || in.SpaceName != "",
	})
	if err != nil {
		return nil, Classify(err)
	}

	out := &SearchMessagesResult{
		Matches:       make([]SearchMatch, 0, len(resp.Results)),
		Scanned:       len(resp.Results),
		NextPageToken: resp.NextPageToken,
		CapReached:    resp.NextPageToken != "",
		Server:        true,
	}
	for _, row := range resp.Results {
		m := row.Message
		if m == nil || m.Name == "" {
			// Nothing to hand back for a row with no message: the
			// caller cannot read it, quote it or open it.
			out.Unparsed++
			continue
		}
		// Google matches whole words wherever they are, so there is no
		// single offset to center on. The first line stands in, which
		// is what a person scanning results reads anyway.
		match := searchMatch(*m, 0)
		match.Read = row.Read
		if slices.Contains(muteSettings, row.SpaceMuteSetting) {
			muted := row.SpaceMuteSetting == "MUTED"
			match.SpaceMuted = &muted
		}
		out.Matches = append(out.Matches, match)
	}
	fillUnread(out.Matches, in.UnreadOnly)
	return out, nil
}

// fillUnread turns a missing read state into false where it can only mean
// unread.
//
// Google's JSON leaves a false boolean out, so an unread hit arrives with
// no read field, as every hit does when the token lacks the read-state
// scope. Two things tell them apart. unread_only matched only unread
// messages. And Google reports read state for every hit or for none, so
// one hit marked read says the scope is there and the silent ones are
// unread. With neither, unread and unknown look the same, and the hit
// says null.
func fillUnread(matches []SearchMatch, unreadOnly bool) {
	known := unreadOnly
	for _, m := range matches {
		known = known || m.Read != nil
	}
	if !known {
		return
	}
	for i := range matches {
		if matches[i].Read == nil {
			matches[i].Read = new(bool)
		}
	}
}

// searchFilter renders Google's search expression.
func (s *Service) searchFilter(ctx context.Context, in SearchMessagesInput) (string, error) {
	var clauses []string

	// The space is a filter clause, not the parent. Google's own words
	// when the parent is one space: "Invalid parent. Specify 'spaces/-'
	// to search across all spaces the user has access to. To limit the
	// search to one or more spaces, use the 'space.name' or
	// 'space.display_name' in the 'filter'."
	if in.Space != "" {
		space, err := requireSpace(in.Space)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, `space.name = "`+space+`"`)
	}

	if q := strings.TrimSpace(in.Query); q != "" {
		if utf8.RuneCountInString(q) > gchat.MaxSearchMessagesFilter/2 {
			return "", Invalidf("query is too long; keep it under %d characters",
				gchat.MaxSearchMessagesFilter/2)
		}
		if strings.ContainsAny(q, `"\()`) {
			// The grammar's escaping is not documented, and a keyword
			// carrying a quote or a bracket changes what the whole
			// expression means. Refusing is the honest answer.
			return "", Invalidf(`query may not contain quotes, backslashes or brackets; ` +
				`pass the words alone, or use regex for a literal match`)
		}
		// Quoted, so a multi-word query is one phrase rather than a
		// term that would also have to appear separately.
		clauses = append(clauses, `"`+q+`"`)
	}

	after, err := parseArgTime("created_after", in.CreatedAfter)
	if err != nil {
		return "", err
	}
	if !after.IsZero() {
		clauses = append(clauses, `create_time >= "`+after.UTC().Format(time.RFC3339)+`"`)
	}
	before, err := parseArgTime("created_before", in.CreatedBefore)
	if err != nil {
		return "", err
	}
	if err := requireWindow("created_after", after, "created_before", before); err != nil {
		return "", err
	}
	if !before.IsZero() {
		clauses = append(clauses, `create_time < "`+before.UTC().Format(time.RFC3339)+`"`)
	}

	if in.SenderEmail != "" {
		email, err := requireEmail("sender_email", in.SenderEmail)
		if err != nil {
			return "", err
		}
		// Google resolves an address in a sender clause itself, the way
		// it does for a membership.
		clauses = append(clauses, `sender.name = "users/`+email+`"`)
	}
	if in.MentionsMe {
		me, err := s.callerID(ctx)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, `annotations.user_mentions.user.name:"`+me+`"`)
	}
	if in.HasAttachment {
		clauses = append(clauses, "attachment:*")
	}
	if in.HasLink {
		clauses = append(clauses, "has_link()")
	}
	if in.UnreadOnly {
		clauses = append(clauses, "is_unread()")
	}
	if in.SpaceType != "" {
		if err := requireEnum("space_type", in.SpaceType, narrowableKinds...); err != nil {
			return "", err
		}
		clauses = append(clauses, `space.space_type = "`+in.SpaceType+`"`)
	}
	for _, word := range strings.Fields(in.SpaceName) {
		// Each word is its own clause, so a space must hold all of
		// them. A word is letters and digits only: the grammar's
		// quoting for this field is not documented, and anything else
		// could change what the expression means.
		if !spaceNameWord.MatchString(word) {
			return "", Invalidf("space_name %q holds something other than letters and digits; "+
				"pass the words of the name alone", word)
		}
		clauses = append(clauses, "space.display_name:"+word)
	}

	if len(clauses) == 0 {
		return "", Invalidf("give the search something to match: query, a time bound, " +
			"sender_email, mentions_me, has_attachment, has_link, unread_only, space_type or space_name")
	}
	return strings.Join(clauses, " AND "), nil
}

// spaceNameWord is one word of a space_name filter.
var spaceNameWord = regexp.MustCompile(`^[\p{L}\p{N}]+$`)

// searchTerm validates that exactly one of the two search modes was
// asked for, and compiles it before anything reaches Google.
//
// An exact substring becomes a quoted case-insensitive pattern rather
// than a strings.Index over a folded copy. Folding is not
// rune-count-preserving — "İ" folds to two runes — so an offset
// measured on the folded text and applied to the original slides the
// snippet off the match. One code path, and the offset is always into
// the text the caller will read.
func searchTerm(in SearchMessagesInput) (*regexp.Regexp, error) {
	query, pattern := strings.TrimSpace(in.Query), strings.TrimSpace(in.Regex)
	switch {
	case query == "" && pattern == "":
		return nil, Invalidf("pass query for an exact substring or regex for a pattern")
	case query != "" && pattern != "":
		return nil, Invalidf("pass query or regex, not both")
	case query != "":
		return regexp.MustCompile("(?i)" + regexp.QuoteMeta(query)), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, Invalidf("regex %q does not compile: %v", in.Regex, err)
	}
	return re, nil
}

// matchIndex returns where the first match starts, counted in
// characters so the snippet cannot cut one in half.
func matchIndex(text string, pattern *regexp.Regexp) (int, bool) {
	loc := pattern.FindStringIndex(text)
	if loc == nil {
		return 0, false
	}
	return utf8.RuneCountInString(text[:loc[0]]), true
}

// snippet is the text around a match, with an ellipsis on each side
// that was cut. at is a character offset into text, from matchIndex.
func snippet(text string, at int) string {
	runes := []rune(text)
	start := max(at-snippetContext, 0)
	end := min(at+snippetContext, len(runes))
	var b strings.Builder
	if start > 0 {
		b.WriteRune('…')
	}
	b.WriteString(string(runes[start:end]))
	if end < len(runes) {
		b.WriteRune('…')
	}
	return b.String()
}
