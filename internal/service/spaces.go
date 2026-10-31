package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/v9/internal/config"
	"github.com/mmedum/google-chat-mcp/v9/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v9/internal/scopes"
)

// SpaceKind is a space's type, in Google's spelling. The tool surface
// uses these strings verbatim, so the vocabulary is the same from the
// wire to the model.
type SpaceKind string

// Space kinds. Unknown is Google's own name for "not one of the above",
// and it is what an unrecognized value degrades to.
const (
	KindSpace         SpaceKind = gchat.SpaceTypeSpace
	KindGroupChat     SpaceKind = gchat.SpaceTypeGroupChat
	KindDirectMessage SpaceKind = gchat.SpaceTypeDirectMessage
	KindUnknown       SpaceKind = "SPACE_TYPE_UNSPECIFIED"
)

// spaceKinds recognizes both the current spaceType field and the
// deprecated type field, which spells two of them differently.
var spaceKinds = map[string]SpaceKind{
	"SPACE":          KindSpace,
	"GROUP_CHAT":     KindGroupChat,
	"DIRECT_MESSAGE": KindDirectMessage,
	"ROOM":           KindSpace,
	"DM":             KindDirectMessage,
}

// kindOf reads a space's type, preferring the current field.
//
// An unknown value degrades rather than failing: Google adding a space
// type must not take down list_spaces, and the caller can still act on
// a space whose kind this server has not learned yet.
func kindOf(sp gchat.Space) SpaceKind {
	for _, raw := range []string{sp.SpaceType, sp.Type} {
		if k, ok := spaceKinds[raw]; ok {
			return k
		}
	}
	return KindUnknown
}

// displayNameOf is what to call a space.
//
// A direct message and most group chats carry no display name, so the
// caller would otherwise see an empty string where a person expects a
// label. The parentheses mark the label as this server's, not the
// space's: a named space really can be called "Direct message", and the
// two must not read the same. Section labels follow the same rule.
func displayNameOf(sp gchat.Space) string {
	if sp.DisplayName != "" {
		return sp.DisplayName
	}
	switch kindOf(sp) {
	case KindDirectMessage:
		return "(direct message)"
	case KindGroupChat:
		return "(group chat)"
	case KindSpace, KindUnknown:
	}
	return "(unnamed space)"
}

// SpaceSummary is one row of a space listing.
type SpaceSummary struct {
	Name        string
	DisplayName string
	Kind        SpaceKind
	// LastActiveTime is when the last message was posted, and the zero
	// value when Google did not say.
	LastActiveTime time.Time
	// Description is what the space is for, empty when it has none or
	// Google did not say. Only admin_search_spaces reports it.
	Description string
}

// spaceSummary shapes one space for a listing, so list_spaces,
// search_spaces and find_group_chats read the same.
func spaceSummary(sp gchat.Space) SpaceSummary {
	return SpaceSummary{
		Name:           sp.Name,
		DisplayName:    displayNameOf(sp),
		Kind:           kindOf(sp),
		LastActiveTime: parseTime(sp.LastActive),
		Description:    descriptionOf(sp),
	}
}

// descriptionOf is a space's description, empty when it has none.
func descriptionOf(sp gchat.Space) string {
	if sp.Details == nil {
		return ""
	}
	return sp.Details.Description
}

// ListSpacesInput selects which spaces to list.
type ListSpacesInput struct {
	Kind      SpaceKind
	Limit     int
	PageToken string
}

// ListSpacesResult is a page of spaces.
type ListSpacesResult struct {
	Spaces        []SpaceSummary
	NextPageToken string
}

// narrowableKinds is every kind a caller may narrow a listing or a
// search to.
var narrowableKinds = []string{string(KindSpace), string(KindDirectMessage), string(KindGroupChat)}

// Google's filter spelling for each kind we can narrow by.
var spaceFilters = map[SpaceKind]string{
	KindSpace:         `spaceType = "SPACE"`,
	KindGroupChat:     `spaceType = "GROUP_CHAT"`,
	KindDirectMessage: `spaceType = "DIRECT_MESSAGE"`,
}

// defaultSpaceLimit and maxSpaceLimit are this server's. Google
// allows up to 1000 per page; the tool surface has always capped at 200
// and changing that would change the schema.
const (
	defaultSpaceLimit = 50
	maxSpaceLimit     = 200
)

// ListSpaces returns one page of the caller's spaces.
func (s *Service) ListSpaces(ctx context.Context, in ListSpacesInput) (*ListSpacesResult, error) {
	limit, err := clampLimit("limit", in.Limit, defaultSpaceLimit, maxSpaceLimit)
	if err != nil {
		return nil, err
	}

	opts := gchat.ListSpacesOptions{PageSize: limit, PageToken: in.PageToken}
	if in.Kind != "" {
		if err := requireEnum("space_type", string(in.Kind), narrowableKinds...); err != nil {
			return nil, err
		}
		opts.Filter = spaceFilters[in.Kind]
	}

	resp, err := s.client.ListSpaces(ctx, opts)
	if err != nil {
		return nil, Classify(err)
	}

	out := &ListSpacesResult{
		Spaces:        make([]SpaceSummary, 0, len(resp.Spaces)),
		NextPageToken: resp.NextPageToken,
	}
	for _, sp := range resp.Spaces {
		out.Spaces = append(out.Spaces, spaceSummary(sp))
	}
	return out, nil
}

// Search limits. The page cap is Google's own for a user-authenticated
// search. An administrator's page is capped at this server's 200, like
// list_spaces, where Google allows 1000: a page that size would crowd
// out everything else the model is reading. The display-name cap is
// this server's, and it exists so the query cannot approach Google's
// 1000-character limit.
const (
	defaultSearchSpaceLimit  = 50
	maxSearchSpaceLimit      = 100
	maxAdminSearchSpaceLimit = 200
	maxSearchDisplayName     = 200
)

// spaceOrders are the orders an administrator's search may ask for: the
// tool surface's name for each, and Google's spelling of it, in the
// reference's own form. Google takes these only under admin access; an
// ordinary search is in its own order.
var spaceOrders = []struct{ name, google string }{
	{"most_members", "membership_count.joined_direct_human_user_count DESC"},
	{"fewest_members", "membership_count.joined_direct_human_user_count ASC"},
	{"recently_active", "last_active_time DESC"},
	{"least_recently_active", "last_active_time ASC"},
	{"newest", "create_time DESC"},
	{"oldest", "create_time ASC"},
}

// spaceOrder is Google's spelling of an order_by, empty for none.
func spaceOrder(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	names := make([]string, len(spaceOrders))
	for i, o := range spaceOrders {
		if o.name == name {
			return o.google, nil
		}
		names[i] = o.name
	}
	return "", requireEnum("order_by", name, names...)
}

// historyStates are the two values a history_state filter takes, in
// Google's spelling.
var historyStates = []string{"HISTORY_ON", "HISTORY_OFF"}

// SearchSpacesInput narrows a space search.
type SearchSpacesInput struct {
	// DisplayName matches whole words by prefix, not substrings:
	// Google's HAS operator tokenizes the name. Empty searches every
	// named space the query otherwise allows.
	DisplayName string
	// ExternalUserAllowed narrows to spaces that do or do not admit
	// people outside the organization. Nil leaves both in.
	ExternalUserAllowed *bool
	Limit               int
	PageToken           string
	// The rest only an administrator's search takes. The bounds are RFC
	// 3339 or a bare date, as in search_messages; empty is no bound on
	// that side, an after bound is inclusive and a before bound is not.
	CreatedAfter, CreatedBefore       string
	LastActiveAfter, LastActiveBefore string
	// HistoryState is HISTORY_ON or HISTORY_OFF, empty for both.
	HistoryState string
	// OrderBy is one of the names in spaceOrders, empty for Google's own order.
	OrderBy string
}

// SearchSpacesResult is a page of matches.
type SearchSpacesResult struct {
	Spaces []SpaceSummary
	// NextPageToken and TotalSize come back only under admin access.
	// Google returns neither to an ordinary caller, so a search without
	// admin access is one page and says so.
	NextPageToken string
	TotalSize     int
}

// SearchSpaces finds named spaces by display name.
//
// Without admin access it covers only spaces the caller has joined; what
// separates it from list_spaces is the name match. Google's grammar
// decides two things this cannot: a search covers named spaces only,
// because spaceType = "SPACE" is required, and matching is by word prefix
// rather than substring.
func (s *Service) SearchSpaces(ctx context.Context, in SearchSpacesInput) (*SearchSpacesResult, error) {
	admin := gchat.AdminAccess(ctx)
	maxLimit := maxSearchSpaceLimit
	if admin {
		maxLimit = maxAdminSearchSpaceLimit
	}
	limit, err := clampLimit("limit", in.Limit, defaultSearchSpaceLimit, maxLimit)
	if err != nil {
		return nil, err
	}
	query, order, err := searchSpacesQuery(in)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.SearchSpaces(ctx, gchat.SearchSpacesOptions{
		Query:     query,
		OrderBy:   order,
		PageSize:  limit,
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}

	found := resp.Found()
	out := &SearchSpacesResult{
		Spaces:        make([]SpaceSummary, 0, len(found)),
		NextPageToken: resp.NextPageToken,
		TotalSize:     resp.TotalSize,
	}
	for _, sp := range found {
		out.Spaces = append(out.Spaces, spaceSummary(sp))
	}
	return out, nil
}

// searchSpacesQuery builds Google's search expression and its order.
//
// spaceType is always there because Google requires it. Under admin
// access the client adds the customer the query has to name. A display
// name is refused rather than escaped when it carries a quote or a
// backslash: the reference documents the grammar's fields and operators
// but not its escaping, and a value that changes the meaning of a query
// is not something to guess at.
//
// The time, history and order filters exist only under admin access.
// Only admin_search_spaces takes them.
func searchSpacesQuery(in SearchSpacesInput) (query, order string, err error) {
	if order, err = spaceOrder(in.OrderBy); err != nil {
		return "", "", err
	}
	clauses := []string{`spaceType = "SPACE"`}
	if name := strings.TrimSpace(in.DisplayName); name != "" {
		if len(name) > maxSearchDisplayName {
			return "", "", Invalidf("display_name is %d characters; the maximum is %d",
				len(name), maxSearchDisplayName)
		}
		if strings.ContainsAny(name, `"\`) {
			return "", "", Invalidf(`display_name may not contain a quote or a backslash`)
		}
		clauses = append(clauses, `displayName:"`+name+`"`)
	}
	if in.ExternalUserAllowed != nil {
		clauses = append(clauses, fmt.Sprintf("externalUserAllowed = %t", *in.ExternalUserAllowed))
	}
	for _, b := range []struct{ field, afterArg, after, beforeArg, before string }{
		{"createTime", "created_after", in.CreatedAfter, "created_before", in.CreatedBefore},
		{"lastActiveTime", "last_active_after", in.LastActiveAfter, "last_active_before", in.LastActiveBefore},
	} {
		bound, err := timeBound(b.field, b.afterArg, b.after, b.beforeArg, b.before)
		if err != nil {
			return "", "", err
		}
		if bound != "" {
			clauses = append(clauses, bound)
		}
	}
	if in.HistoryState != "" {
		if err := requireEnum("history_state", in.HistoryState, historyStates...); err != nil {
			return "", "", err
		}
		clauses = append(clauses, `spaceHistoryState = "`+in.HistoryState+`"`)
	}
	return strings.Join(clauses, " AND "), order, nil
}

// timeBound bounds a timestamp field of a space search: at or after one
// argument, and before the other. An empty argument is no bound on that
// side.
func timeBound(field, afterArg, afterValue, beforeArg, beforeValue string) (string, error) {
	after, err := parseArgTime(afterArg, afterValue)
	if err != nil {
		return "", err
	}
	before, err := parseArgTime(beforeArg, beforeValue)
	if err != nil {
		return "", err
	}
	if err := requireWindow(afterArg, after, beforeArg, before); err != nil {
		return "", err
	}
	return timeFilter(field, inclusive(after), before), nil
}

// FindGroupChatsInput names the people a group chat must hold.
type FindGroupChatsInput struct {
	// Emails are the other people. The caller is never named: Google
	// matches chats whose human members are exactly the caller plus
	// these.
	Emails    []string
	Limit     int
	PageToken string
}

// FindGroupChatsResult is a page of matches.
type FindGroupChatsResult struct {
	Spaces        []SpaceSummary
	NextPageToken string
}

// FindGroupChats returns the group chats holding exactly the caller and
// the people named.
//
// Exactly, not at least: a chat with one extra person does not match.
// That is Google's rule and it is the useful one, since it answers "is
// there already a chat with these three" without listing every space.
func (s *Service) FindGroupChats(ctx context.Context, in FindGroupChatsInput) (*FindGroupChatsResult, error) {
	emails, err := requireEmails("member_emails", in.Emails, 1, gchat.MaxFindGroupChatsUsers)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultGroupChatLimit, gchat.MaxFindGroupChatsPageSize)
	if err != nil {
		return nil, err
	}

	users := make([]string, 0, len(emails))
	for _, email := range emails {
		// Google resolves "users/{email}" itself, the way add_member
		// does. No directory lookup happens here, so a person the
		// caller cannot see in the directory is still findable.
		users = append(users, "users/"+email)
	}

	resp, err := s.client.FindGroupChats(ctx, gchat.FindGroupChatsOptions{
		Users:     users,
		PageSize:  limit,
		PageToken: in.PageToken,
	})
	if err != nil {
		return nil, directoryHint("every address in member_emails names someone", Classify(err))
	}

	out := &FindGroupChatsResult{
		Spaces:        make([]SpaceSummary, 0, len(resp.Spaces)),
		NextPageToken: resp.NextPageToken,
	}
	for _, sp := range resp.Spaces {
		out.Spaces = append(out.Spaces, spaceSummary(sp))
	}
	return out, nil
}

// defaultGroupChatLimit is Google's own default for the call.
const defaultGroupChatLimit = 10

// SpaceDetails is one space in full, as get_space returns it.
type SpaceDetails struct {
	SpaceSummary
	// SingleUserBotDM and ExternalUserAllowed are nil when Google did
	// not say. They are absent far more often than they are false, so
	// flattening them to false would assert something Google did not.
	SingleUserBotDM     *bool
	ExternalUserAllowed *bool
	CreateTime          time.Time
	// AccessState and Audience are who can find and join a named space,
	// as Google says; empty when it does not, as for a direct message.
	AccessState string
	Audience    string

	// URI opens the space in Chat.
	URI string
	// Guidelines are the space's rules. Its description is the
	// SpaceSummary's, which a search row carries too.
	Guidelines string
	// HumanMembers and GroupMembers count who has joined, people and
	// Google Groups apart. Nil when Google sent no count.
	HumanMembers *int
	GroupMembers *int
	// HistoryState is Google's own word: HISTORY_ON or HISTORY_OFF.
	HistoryState string
	// MembersCanPost is false when only managers may post, as in an
	// announcement space. Nil when Google did not say.
	MembersCanPost *bool
	// Permissions is who holds each permission
	// admin_update_space_permissions changes. Nil when Google sent no
	// permission settings.
	Permissions *SpacePermissions
}

// GetSpace returns one space by resource name.
func (s *Service) GetSpace(ctx context.Context, name string) (*SpaceDetails, error) {
	name, err := requireSpace(name)
	if err != nil {
		return nil, err
	}
	sp, err := s.client.GetSpace(ctx, name)
	if err != nil {
		return nil, Classify(err)
	}
	out := &SpaceDetails{
		SpaceSummary:        spaceSummary(*sp),
		SingleUserBotDM:     sp.SingleUserBot,
		ExternalUserAllowed: sp.ExternalUser,
		CreateTime:          parseTime(sp.CreateTime),
		URI:                 sp.URI,
		HistoryState:        sp.HistoryState,
	}
	if sp.Access != nil {
		out.AccessState, out.Audience = sp.Access.AccessState, sp.Access.Audience
	}
	if sp.Details != nil {
		out.Guidelines = sp.Details.Guidelines
	}
	if sp.Membership != nil {
		humans, groups := sp.Membership.JoinedDirectHumanUserCount, sp.Membership.JoinedGroupCount
		out.HumanMembers, out.GroupMembers = &humans, &groups
	}
	if sp.Permissions != nil {
		if post := sp.Permissions.PostMessages; post != nil {
			members := post.MembersAllowed
			out.MembersCanPost = &members
		}
		out.Permissions = &SpacePermissions{}
		for _, p := range permissionSettings {
			*p.in(out.Permissions) = permissionLevel(p.read(sp.Permissions))
		}
	}
	return out, nil
}

// Space write limits. These are this server's: Google allows more
// members on a create, and the tool schemas cap it lower so an agent
// flow cannot make a room of fifty people by accident.
const (
	maxSpaceDisplayName = 128
	maxSpaceDescription = 150
	// A group chat needs people in it to be one. A named space does
	// not: a space of one's own is a legitimate thing to make, and
	// requiring a second person meant the only way to try delete_space
	// was to pull a colleague into a room made to be destroyed.
	// Google itself allows it.
	minGroupChatMembers = 2
	minSpaceMembers     = 0
	maxSetupMembers     = 20
)

// FindDirectMessage returns the direct-message space shared with one
// person, creating it when there is none yet.
//
// Creating on a miss is what makes the tool useful: a caller who wants
// to write to a person has no other way to name the space. Nothing is
// disturbed by it either, because an empty direct message is invisible
// to the other person until something is posted in it.
func (s *Service) FindDirectMessage(ctx context.Context, email string) (string, error) {
	addr, err := requireEmail("user_email", email)
	if err != nil {
		return "", err
	}

	found, err := s.client.FindDirectMessage(ctx, "users/"+addr)
	switch {
	case err == nil:
		return found.Name, nil
	case !gchat.IsNotFound(err):
		return "", Classify(err)
	}

	created, err := s.client.SetupSpace(ctx,
		gchat.BuildSetupSpace(gchat.SpaceTypeDirectMessage, "", []string{addr}))
	if err != nil {
		return "", directoryHint(addr+" is someone", Classify(err))
	}
	// Worth a line: this is the one read-shaped tool that can make
	// something, and the space is the caller's from now on.
	s.log.Info("direct_message_created", "space", created.Name)
	return created.Name, nil
}

// directoryHint adds what to check to a refusal about a person.
//
// Google's message for an address it cannot place says nothing about
// the directory, and that is nearly always the reason. Every tool that
// hands Google a caller-supplied address goes through here, so the most
// likely mistake gets the same answer whichever one was called.
//
// subject names what to check, and is a phrase rather than always an
// address: a create passes twenty of them and Google's message already
// says which one it objected to.
//
// Auth, scope and rate-limit refusals are left alone: their answer is
// already in them, and checking a spelling would not help.
func directoryHint(subject string, err error) error {
	var se *Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Class {
	case ClassAuth, ClassScope, ClassRateLimited:
		return err
	case ClassNotFound, ClassInvalid, ClassUpstream, ClassUnexpected:
	}
	return &Error{
		Class: se.Class,
		Message: fmt.Sprintf("%s Check that %s in your Workspace directory.",
			strings.TrimSuffix(se.Message, ".")+".", subject),
		err: err,
	}
}

// CreateSpaceInput is a new space and who starts in it.
type CreateSpaceInput struct {
	// DisplayName is required for a named space and refused for a
	// group chat, which Google gives no name.
	DisplayName string
	// MemberEmails never includes the caller: Google adds the
	// authenticated user itself.
	MemberEmails []string
	// Audience opens a named space to a target audience; blank or
	// Private leaves it private.
	Audience string
	DryRun   bool
}

// CreateSpaceResult is the space that was made, or what a dry run would
// have sent.
type CreateSpaceResult struct {
	// Name is empty after a dry run.
	Name        string
	DisplayName string
	// MemberCount is how many people were asked for, not counting the
	// caller Google adds.
	MemberCount int
	DryRun      bool
	Rendered    map[string]any
}

// CreateGroupChat creates an unnamed multi-person direct message.
//
// Two members is the floor because one is a direct message, which
// find_direct_message already covers and which Google files
// differently.
func (s *Service) CreateGroupChat(ctx context.Context, in CreateSpaceInput) (*CreateSpaceResult, error) {
	if strings.TrimSpace(in.DisplayName) != "" {
		return nil, Invalidf("a group chat has no display name; use create_space for a named space")
	}
	emails, err := requireEmails("member_emails", in.MemberEmails, minGroupChatMembers, maxSetupMembers)
	if err != nil {
		return nil, err
	}
	return s.setupSpace(ctx, gchat.SpaceTypeGroupChat, "", emails, in.DryRun)
}

// CreateSpace creates a named space.
//
// One member is the floor. Google would accept none, but a space
// holding only its creator is not something an agent flow asked for on
// purpose.
func (s *Service) CreateSpace(ctx context.Context, in CreateSpaceInput) (*CreateSpaceResult, error) {
	name, err := requireDisplayName(in.DisplayName, maxSpaceDisplayName)
	if err != nil {
		return nil, err
	}
	emails, err := requireEmails("member_emails", in.MemberEmails, minSpaceMembers, maxSetupMembers)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Audience) != "" {
		audience, err := requireAudienceOrPrivate(in.Audience)
		if err != nil {
			return nil, err
		}
		if audience != "" {
			return nil, audienceRefused()
		}
	}
	return s.setupSpace(ctx, gchat.SpaceTypeSpace, name, emails, in.DryRun)
}

// setupSpace is the half the two creates share.
func (s *Service) setupSpace(ctx context.Context, kind, displayName string, emails []string, dryRun bool) (*CreateSpaceResult, error) {
	body := gchat.BuildSetupSpace(kind, displayName, emails)
	out := &CreateSpaceResult{DisplayName: displayName, MemberCount: len(emails), DryRun: dryRun}
	if dryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}
	created, err := s.client.SetupSpace(ctx, body)
	if err != nil {
		return nil, directoryHint("every address in member_emails names someone", Classify(err))
	}
	out.Name = created.Name
	return out, nil
}

// UpdateSpaceInput is an edit of a space's name, description,
// guidelines or audience. A nil field is one the caller did not ask to
// change.
type UpdateSpaceInput struct {
	Space string
	// SpaceDisplayName says which space Space is, as a person would name
	// it; checkSpace holds it to the space before anything is written.
	SpaceDisplayName string
	DisplayName      *string
	Description      *string
	// Guidelines is admin_update_space's: update_space keeps them as
	// they are.
	Guidelines *string
	// Audience is Private to make the space private; blank leaves it
	// alone, and anything else is refused.
	Audience string
	DryRun   bool
}

// UpdateSpaceResult is what the patch asked for.
type UpdateSpaceResult struct {
	Space       string
	DisplayName *string
	Description *string
	Guidelines  *string
	// Audience is the target audience that was set, Private when the
	// space was made private, nil when the call left it alone.
	Audience *string
	// UpdateMask is what was masked, so a caller can see which fields
	// the patch touched without deriving it again.
	UpdateMask string
	// Previous is what the space had in each field the call changed,
	// read just before the patch, so a change can be put back. Nil for
	// a field the call left alone; an empty string for one that was
	// empty.
	Previous SpaceText
	DryRun   bool
	Rendered map[string]any
}

// SpaceText is a space's name, description and guidelines, each nil
// when it is not reported.
type SpaceText struct {
	DisplayName *string
	Description *string
	Guidelines  *string
}

// maxSpaceGuidelines is Google's own limit (Space reference, 2026-09-30:
// "Supports up to 5,000 characters").
const maxSpaceGuidelines = 5000

// UpdateSpace renames a space, edits its description or guidelines, or
// makes it private.
//
// A description and the guidelines are one object to Google. Its mask
// accepts only top-level paths, so a patch of either is a patch of all
// of spaceDetails, and whichever was left out would be cleared. The
// space is read first, a dry run included, and the one the call did not
// change is resent as it was. Someone editing it between that read and
// the patch loses their edit; the window is one request wide.
//
// It makes a space private, and never opens one: that widens who can
// read it, so it is set_space_audience's, behind the destructive flag.
// Under admin access Google takes no audience at all.
func (s *Service) UpdateSpace(ctx context.Context, in UpdateSpaceInput) (*UpdateSpaceResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	out := &UpdateSpaceResult{Space: space, DryRun: in.DryRun}
	if strings.TrimSpace(in.Audience) != "" {
		return s.makePrivate(ctx, in, out)
	}
	if in.DisplayName == nil && in.Description == nil && in.Guidelines == nil {
		return nil, Invalidf("pass a field to change; an empty patch is refused by Google")
	}
	out.DisplayName, out.Description, out.Guidelines = in.DisplayName, in.Description, in.Guidelines
	if out.DisplayName != nil {
		name, err := requireDisplayName(*out.DisplayName, maxSpaceDisplayName)
		if err != nil {
			return nil, err
		}
		out.DisplayName = &name
	}
	// An empty value is a deliberate clear, which requireText rejects;
	// anything else is bounded the same way as every other text this
	// server sends.
	if out.Description != nil && *out.Description != "" {
		if _, err := requireText("description", *out.Description, maxSpaceDescription); err != nil {
			return nil, err
		}
	}
	if out.Guidelines != nil && *out.Guidelines != "" {
		if _, err := requireText("guidelines", *out.Guidelines, maxSpaceGuidelines); err != nil {
			return nil, err
		}
	}
	sp, err := s.checkedSpace(ctx, space, in.SpaceDisplayName, spacesWriteScope(ctx))
	if err != nil {
		return nil, err
	}
	var current gchat.SpaceDetails
	if sp.Details != nil {
		current = *sp.Details
	}
	if out.DisplayName != nil {
		out.Previous.DisplayName = &sp.DisplayName
	}
	if out.Description != nil {
		out.Previous.Description = &current.Description
	}
	if out.Guidelines != nil {
		out.Previous.Guidelines = &current.Guidelines
	}
	var body *gchat.UpdateSpaceRequest
	body, out.UpdateMask = gchat.BuildUpdateSpace(out.DisplayName, out.Description, out.Guidelines, current)
	return s.patchSpace(ctx, body, out)
}

// makePrivate is update_space's audience: private, in a patch of its
// own, since Google refuses accessSettings.audience beside any other
// mask.
func (s *Service) makePrivate(ctx context.Context, in UpdateSpaceInput, out *UpdateSpaceResult) (*UpdateSpaceResult, error) {
	if in.DisplayName != nil || in.Description != nil || in.Guidelines != nil {
		return nil, Invalidf("change audience in a call of its own; Google refuses it beside a name or description")
	}
	audience, err := requireAudienceOrPrivate(in.Audience)
	if err != nil {
		return nil, err
	}
	if audience != "" {
		return nil, audienceRefused()
	}
	shown := Private
	out.Audience = &shown
	var body *gchat.UpdateSpaceRequest
	body, out.UpdateMask = gchat.BuildUpdateSpaceAudience(audience)
	if err := s.checkSpace(ctx, out.Space, in.SpaceDisplayName); err != nil {
		return nil, err
	}
	return s.patchSpace(ctx, body, out)
}

// spacesWriteScope is the scope a space patch needs: the user's
// umbrella, or under admin access the administrator's.
func spacesWriteScope(ctx context.Context) string {
	if gchat.AdminAccess(ctx) {
		return scopes.AdminSpaces
	}
	return scopes.Spaces
}

// patchSpace sends a space patch, or on a dry run shows it.
//
// The values echoed back are what the caller asked for. A 2xx says
// Google applied them, and its response nests the description a level
// down where the caller would have to go looking for it.
func (s *Service) patchSpace(ctx context.Context, body *gchat.UpdateSpaceRequest, out *UpdateSpaceResult) (*UpdateSpaceResult, error) {
	rendered, err := s.sendSpacePatch(ctx, out.Space, body, out.UpdateMask, out.DryRun)
	if err != nil {
		return nil, err
	}
	out.Rendered = rendered
	return out, nil
}

// sendSpacePatch sends a space patch, or on a dry run renders it and
// sends nothing.
func (s *Service) sendSpacePatch(ctx context.Context, space string, body *gchat.UpdateSpaceRequest, mask string, dryRun bool) (map[string]any, error) {
	if dryRun {
		return renderBody(body)
	}
	if _, err := s.client.UpdateSpace(ctx, space, body, mask); err != nil {
		return nil, Classify(err)
	}
	return nil, nil
}

// Permission levels, from narrowest: who besides a space's managers
// holds a permission. A space's managers always do.
const (
	PermissionManagers          = "managers"
	PermissionAssistantManagers = "assistant_managers"
	PermissionMembers           = "members"
	// PermissionNobody is what a read reports for a permission Google
	// says nobody holds. It cannot be set.
	PermissionNobody = "nobody"
)

// permissionLevels lists what a permission may be set to, narrowest
// first.
var permissionLevels = []string{PermissionManagers, PermissionAssistantManagers, PermissionMembers}

// SpacePermissions is who holds each permission a space's settings can
// change. An empty field is one the caller leaves alone, or one Google
// did not report.
type SpacePermissions struct {
	ManageMembersAndGroups string
	ModifySpaceDetails     string
	ToggleHistory          string
	UseAtMentionAll        string
	ManageApps             string
	ManageWebhooks         string
	ReplyMessages          string
}

// permissionSetting is one setting admin_update_space_permissions
// changes: its name on the tool surface, Google's field name, and where
// it sits in the two structs that carry it.
//
// Two of Google's settings are missing on purpose. Who may post is
// output only. Who may see the member list must be sent with
// accessSettings.access_permission_settings, which Google refuses under
// admin access (spaces.patch reference, 2026-09-21).
type permissionSetting struct {
	name string
	wire string
	in   func(*SpacePermissions) *string
	read func(*gchat.PermSettings) *gchat.PermSetting
}

var permissionSettings = []permissionSetting{
	{"manage_members_and_groups", "manageMembersAndGroups",
		func(p *SpacePermissions) *string { return &p.ManageMembersAndGroups },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ManageMembersAndGroups }},
	{"modify_space_details", "modifySpaceDetails",
		func(p *SpacePermissions) *string { return &p.ModifySpaceDetails },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ModifySpaceDetails }},
	{"toggle_history", "toggleHistory",
		func(p *SpacePermissions) *string { return &p.ToggleHistory },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ToggleHistory }},
	{"use_at_mention_all", "useAtMentionAll",
		func(p *SpacePermissions) *string { return &p.UseAtMentionAll },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.UseAtMentionAll }},
	{"manage_apps", "manageApps",
		func(p *SpacePermissions) *string { return &p.ManageApps },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ManageApps }},
	{"manage_webhooks", "manageWebhooks",
		func(p *SpacePermissions) *string { return &p.ManageWebhooks },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ManageWebhooks }},
	{"reply_messages", "replyMessages",
		func(p *SpacePermissions) *string { return &p.ReplyMessages },
		func(p *gchat.PermSettings) *gchat.PermSetting { return p.ReplyMessages }},
}

// permissionRequest is who a level gives a permission to. A space's
// managers hold every permission at every level.
func permissionRequest(level string) gchat.PermissionRequest {
	return gchat.PermissionRequest{
		ManagersAllowed:          true,
		AssistantManagersAllowed: level != PermissionManagers,
		MembersAllowed:           level == PermissionMembers,
	}
}

// permissionLevel is the level a read permission sits at, the widest
// role that holds it. Empty when Google did not report it.
func permissionLevel(p *gchat.PermSetting) string {
	switch {
	case p == nil:
		return ""
	case p.MembersAllowed:
		return PermissionMembers
	case p.AssistantManagersAllowed:
		return PermissionAssistantManagers
	case p.ManagersAllowed:
		return PermissionManagers
	}
	return PermissionNobody
}

// UpdateSpacePermissionsInput changes who holds a space's permissions.
type UpdateSpacePermissionsInput struct {
	Space            string
	SpaceDisplayName string
	Set              SpacePermissions
	DryRun           bool
}

// PermissionChange is one permission the call changed.
type PermissionChange struct {
	Setting string
	// From is what the space had, read just before the patch; empty
	// when Google did not say.
	From string
	To   string
}

// UpdateSpacePermissionsResult is what the patch asked for.
type UpdateSpacePermissionsResult struct {
	Space      string
	Changes    []PermissionChange
	UpdateMask string
	DryRun     bool
	Rendered   map[string]any
}

// UpdateSpacePermissions changes who besides a space's managers may
// manage its members, edit its details, turn history on or off, mention
// everyone, manage apps and webhooks, and reply.
//
// Widening one hands members what a manager holds, which is why its
// tool is destructive. The space is read first, a dry run included, so
// the reply says what each setting was and a change can be put back.
func (s *Service) UpdateSpacePermissions(ctx context.Context, in UpdateSpacePermissionsInput) (*UpdateSpacePermissionsResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	body := &gchat.UpdateSpaceRequest{Permissions: gchat.PermissionsRequest{}}
	var masks []string
	var changed []permissionSetting
	out := &UpdateSpacePermissionsResult{Space: space, DryRun: in.DryRun}
	for _, p := range permissionSettings {
		level := strings.TrimSpace(*p.in(&in.Set))
		if level == "" {
			continue
		}
		if err := requireEnum(p.name, level, permissionLevels...); err != nil {
			return nil, err
		}
		body.Permissions[p.wire] = permissionRequest(level)
		masks = append(masks, "permission_settings."+p.wire)
		changed = append(changed, p)
		out.Changes = append(out.Changes, PermissionChange{Setting: p.name, To: level})
	}
	if len(masks) == 0 {
		names := make([]string, 0, len(permissionSettings))
		for _, p := range permissionSettings {
			names = append(names, p.name)
		}
		return nil, Invalidf("pass at least one of %s; an empty patch is refused by Google", strings.Join(names, ", "))
	}
	sp, err := s.checkedSpace(ctx, space, in.SpaceDisplayName, spacesWriteScope(ctx))
	if err != nil {
		return nil, err
	}
	if sp.Permissions != nil {
		// Who may reply changes only where members cannot post, as in an
		// announcement space. Elsewhere Google refuses it for everyone,
		// the space's own managers included, and under admin access as
		// permission denied (live, 2026-10-31). A read that does not say
		// who may post refuses nothing: Google decides.
		post := sp.Permissions.PostMessages
		if post != nil && post.MembersAllowed && strings.TrimSpace(in.Set.ReplyMessages) != "" {
			return nil, Invalidf("reply_messages changes only in a space where members cannot post, and members can post in %s "+
				"(members_can_post), so Google refuses it. Nothing was changed; leave reply_messages out", space)
		}
		for i, p := range changed {
			out.Changes[i].From = permissionLevel(p.read(sp.Permissions))
		}
	}
	out.UpdateMask = strings.Join(masks, ",")
	if out.Rendered, err = s.sendSpacePatch(ctx, space, body, out.UpdateMask, in.DryRun); err != nil {
		return nil, err
	}
	return out, nil
}

// audienceRefused is the refusal of opening a space to an audience
// anywhere but set_space_audience.
func audienceRefused() error {
	return Failf(ClassBlocked, "opening a space to a target audience lets everyone in it find and read the space, and "+
		"making it private again does not take back what they read, so create_space and update_space do not do it; "+
		"nothing was sent. set_space_audience does, when %sENABLE_DESTRUCTIVE=true", config.EnvPrefix)
}

// SetSpaceAudienceInput opens a space to a target audience.
type SetSpaceAudienceInput struct {
	Space string
	// Audience is default for the organization's default target
	// audience, or audiences/{id}.
	Audience string
	// DisplayName is what the caller read the space to be called: who
	// can read the space widens for good, so the call says which space.
	DisplayName string
	DryRun      bool
}

// SetSpaceAudience opens a space to a target audience. Whoever is in it
// can find the space, read it and join without an invitation, and
// making the space private again does not take back what they read.
func (s *Service) SetSpaceAudience(ctx context.Context, in SetSpaceAudienceInput) (*UpdateSpaceResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	audience, err := requireAudienceOrPrivate(in.Audience)
	if err != nil {
		return nil, err
	}
	if audience == "" {
		return nil, Invalidf("set_space_audience opens a space; update_space with audience private makes one private")
	}
	sp, err := s.client.GetSpace(ctx, space)
	if err != nil {
		return nil, needsScope(err, scopes.Spaces)
	}
	if err := checkName("display_name", space, sp.DisplayName, in.DisplayName); err != nil {
		return nil, err
	}
	out := &UpdateSpaceResult{Space: space, Audience: &audience, DryRun: in.DryRun}
	var body *gchat.UpdateSpaceRequest
	body, out.UpdateMask = gchat.BuildUpdateSpaceAudience(audience)
	return s.patchSpace(ctx, body, out)
}
