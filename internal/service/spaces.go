package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/v4/internal/gchat"
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
}

// spaceSummary shapes one space for a listing, so list_spaces,
// search_spaces and find_group_chats read the same.
func spaceSummary(sp gchat.Space) SpaceSummary {
	return SpaceSummary{
		Name:           sp.Name,
		DisplayName:    displayNameOf(sp),
		Kind:           kindOf(sp),
		LastActiveTime: parseTime(sp.LastActive),
	}
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
		if err := requireEnum("space_type", string(in.Kind),
			string(KindSpace), string(KindDirectMessage), string(KindGroupChat)); err != nil {
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
// search; the display-name cap is this server's, and it exists so the
// query cannot approach Google's 1000-character limit.
const (
	defaultSearchSpaceLimit = 50
	maxSearchSpaceLimit     = 100
	maxSearchDisplayName    = 200
)

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
	// UseAdminAccess searches the whole Workspace. The tool layer
	// refuses it unless the admin toolset is on, because it needs a
	// scope login asks for only then.
	UseAdminAccess bool
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
	limit, err := clampLimit("limit", in.Limit, defaultSearchSpaceLimit, maxSearchSpaceLimit)
	if err != nil {
		return nil, err
	}
	query, err := searchSpacesQuery(in)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.SearchSpaces(ctx, gchat.SearchSpacesOptions{
		Query:          query,
		PageSize:       limit,
		PageToken:      in.PageToken,
		UseAdminAccess: in.UseAdminAccess,
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

// searchSpacesQuery builds Google's search expression.
//
// spaceType is always there because Google requires it, and a display
// name is refused rather than escaped when it carries a quote or a
// backslash: the reference documents the grammar's fields and operators
// but not its escaping, and a value that changes the meaning of a query
// is not something to guess at.
func searchSpacesQuery(in SearchSpacesInput) (string, error) {
	clauses := []string{`spaceType = "SPACE"`}
	if name := strings.TrimSpace(in.DisplayName); name != "" {
		if len(name) > maxSearchDisplayName {
			return "", Invalidf("display_name is %d characters; the maximum is %d",
				len(name), maxSearchDisplayName)
		}
		if strings.ContainsAny(name, `"\`) {
			return "", Invalidf(`display_name may not contain a quote or a backslash`)
		}
		clauses = append(clauses, `displayName:"`+name+`"`)
	}
	if in.ExternalUserAllowed != nil {
		clauses = append(clauses, fmt.Sprintf("externalUserAllowed = %t", *in.ExternalUserAllowed))
	}
	return strings.Join(clauses, " AND "), nil
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
	Name        string
	Kind        SpaceKind
	DisplayName string
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

	// LastActiveTime is when the last message was posted, and the zero
	// value when Google did not say.
	LastActiveTime time.Time
	// URI opens the space in Chat.
	URI         string
	Description string
	Guidelines  string
	// HumanMembers and GroupMembers count who has joined, people and
	// Google Groups apart. Nil when Google sent no count.
	HumanMembers *int
	GroupMembers *int
	// HistoryState is Google's own word: HISTORY_ON or HISTORY_OFF.
	HistoryState string
	// MembersCanPost is false when only managers may post, as in an
	// announcement space. Nil when Google did not say.
	MembersCanPost *bool
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
		Name:                sp.Name,
		Kind:                kindOf(*sp),
		DisplayName:         displayNameOf(*sp),
		SingleUserBotDM:     sp.SingleUserBot,
		ExternalUserAllowed: sp.ExternalUser,
		CreateTime:          parseTime(sp.CreateTime),
		LastActiveTime:      parseTime(sp.LastActive),
		URI:                 sp.URI,
		HistoryState:        sp.HistoryState,
	}
	if sp.Access != nil {
		out.AccessState, out.Audience = sp.Access.AccessState, sp.Access.Audience
	}
	if sp.Details != nil {
		out.Description, out.Guidelines = sp.Details.Description, sp.Details.Guidelines
	}
	if sp.Membership != nil {
		humans, groups := sp.Membership.JoinedDirectHumanUserCount, sp.Membership.JoinedGroupCount
		out.HumanMembers, out.GroupMembers = &humans, &groups
	}
	if sp.Permissions != nil && sp.Permissions.PostMessages != nil {
		members := sp.Permissions.PostMessages.MembersAllowed
		out.MembersCanPost = &members
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
	// Audience is the target audience the space was opened to, empty
	// for a private one.
	Audience string
	DryRun   bool
	Rendered map[string]any
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
	return s.setupSpace(ctx, gchat.SpaceTypeGroupChat, "", "", emails, in.DryRun)
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
	audience := ""
	if strings.TrimSpace(in.Audience) != "" {
		if audience, err = requireAudienceOrPrivate(in.Audience); err != nil {
			return nil, err
		}
	}
	return s.setupSpace(ctx, gchat.SpaceTypeSpace, name, audience, emails, in.DryRun)
}

// setupSpace is the half the two creates share.
func (s *Service) setupSpace(ctx context.Context, kind, displayName, audience string, emails []string, dryRun bool) (*CreateSpaceResult, error) {
	body := gchat.BuildSetupSpace(kind, displayName, emails)
	if audience != "" {
		body.Space.Access = &gchat.AudienceRequest{Audience: audience}
	}
	out := &CreateSpaceResult{DisplayName: displayName, MemberCount: len(emails), Audience: audience, DryRun: dryRun}
	if dryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}
	if audience != "" {
		if err := ask(ctx, askCreateOpenSpace(displayName, audience)); err != nil {
			return nil, err
		}
	}

	created, err := s.client.SetupSpace(ctx, body)
	if err != nil {
		return nil, directoryHint("every address in member_emails names someone", Classify(err))
	}
	out.Name = created.Name
	return out, nil
}

// UpdateSpaceInput is an edit of a space's name, description or
// audience. A nil field is one the caller did not ask to change.
type UpdateSpaceInput struct {
	Space       string
	DisplayName *string
	Description *string
	// Audience opens the space to a target audience, or with Private
	// makes it private again; blank leaves it alone.
	Audience string
	DryRun   bool
}

// UpdateSpaceResult is what the patch asked for.
type UpdateSpaceResult struct {
	Space       string
	DisplayName *string
	Description *string
	// Audience is the target audience that was set, Private when the
	// space was made private, nil when the call left it alone.
	Audience *string
	// UpdateMask is what was masked, so a caller can see which fields
	// the patch touched without deriving it again.
	UpdateMask string
	DryRun     bool
	Rendered   map[string]any
}

// UpdateSpace renames a space, edits its description, or sets who can
// find and join it.
//
// Editing the description clears the space's guidelines, if it has any.
// Google's mask accepts only top-level paths, so there is no way to
// patch one field of spaceDetails and leave the other; the tool
// description says so.
//
// Opening a space to an audience asks the person first: whoever is in
// it can read the space from then on, and making it private again does
// not take back what they read. Making it private asks nothing.
func (s *Service) UpdateSpace(ctx context.Context, in UpdateSpaceInput) (*UpdateSpaceResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	out := &UpdateSpaceResult{Space: space, DryRun: in.DryRun}
	var body *gchat.UpdateSpaceRequest
	// opening is the audience a space is being opened to, which is the
	// one change here the person is asked about.
	opening := ""
	if strings.TrimSpace(in.Audience) != "" {
		if in.DisplayName != nil || in.Description != nil {
			return nil, Invalidf("change audience in a call of its own; Google refuses it beside a name or description")
		}
		audience, err := requireAudienceOrPrivate(in.Audience)
		if err != nil {
			return nil, err
		}
		opening = audience
		shown := cmp.Or(audience, Private)
		out.Audience = &shown
		body, out.UpdateMask = gchat.BuildUpdateSpaceAudience(audience)
	} else {
		if in.DisplayName == nil && in.Description == nil {
			return nil, Invalidf("pass display_name, description or audience; an empty patch is refused by Google")
		}
		out.DisplayName, out.Description = in.DisplayName, in.Description
		if out.DisplayName != nil {
			name, err := requireDisplayName(*out.DisplayName, maxSpaceDisplayName)
			if err != nil {
				return nil, err
			}
			out.DisplayName = &name
		}
		if out.Description != nil && *out.Description != "" {
			// An empty description is a deliberate clear, which requireText
			// rejects; anything else is bounded the same way as every other
			// text this server sends.
			if _, err := requireText("description", *out.Description, maxSpaceDescription); err != nil {
				return nil, err
			}
		}
		body, out.UpdateMask = gchat.BuildUpdateSpace(out.DisplayName, out.Description)
	}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	if opening != "" && asks(ctx) {
		sp, err := s.client.GetSpace(ctx, space)
		if err != nil {
			return nil, Classify(err)
		}
		if err := ask(ctx, askOpenSpace(space, sp.DisplayName, opening)); err != nil {
			return nil, err
		}
	}
	if _, err := s.client.UpdateSpace(ctx, space, body, out.UpdateMask); err != nil {
		return nil, Classify(err)
	}
	// The values echoed back are what the caller asked for. A 2xx says
	// Google applied them, and its response nests the description a
	// level down where the caller would have to go looking for it.
	return out, nil
}
