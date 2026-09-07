package tools

import (
	"strconv"
	"strings"
	"time"
)

// Every tool reply carries both halves: structuredContent, which the
// output schema describes, and the text rendered here. They say the same
// thing in different forms.
//
// Sending both is what the MCP specification asks for — "for backwards
// compatibility, a tool that returns structured content SHOULD also
// return the serialized JSON in a TextContent block", unchanged from
// 2025-06-18 through 2025-11-25. Making them different forms rather than
// the same bytes is SEP-1624, which asks that the two be "semantically
// equivalent (same information, different presentation)" and names
// stringified JSON in `content` as a source of redundant context.
//
// Neither half can be dropped, because clients disagree about which one
// they show a model. Claude Code forwards only structuredContent
// (anthropics/claude-code#55677, filed with a repro and closed as not
// planned); claude.ai and ChatGPT show the text. A server that puts its
// substance in one half goes blank on the clients that read the other.
//
// So: this file is the readable half, and it must carry every fact the
// JSON carries. A rendering that quietly stops matching its payload is
// the failure mode to watch for, and no gate can catch it — the tests in
// render_test.go are what hold it.
//
// The house style is one record per line, facts separated by " · ",
// anything Google did not send left out rather than printed as empty.

// renderer is implemented by every tool output. register constrains its
// output type to it, so a tool cannot be added without one.
type renderer interface {
	// Render returns the text half of this tool's reply.
	Render() string
}

// joinNonEmpty is the one rule both layouts share: anything Google did
// not send is left out rather than printed as a gap.
func joinNonEmpty(sep string, parts []string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// meta joins the facts on one line.
func meta(parts ...string) string { return joinNonEmpty(" · ", parts) }

// block stacks lines.
func block(lines ...string) string { return joinNonEmpty("\n", lines) }

// count is "1 space" or "3 spaces", and names the empty case rather
// than returning nothing at all.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// listing is a header line followed by one line per row.
func listing(header string, rows []string) string {
	if len(rows) == 0 {
		return header + "."
	}
	return header + ":\n" + strings.Join(rows, "\n")
}

// rows renders each element of a list.
func rows[T renderer](items []T) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Render()
	}
	return out
}

// deref is an optional string, empty when Google said nothing.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// labelled is "label value" when there is a value.
func labelled(label, v string) string {
	if v == "" {
		return ""
	}
	return label + " " + v
}

// flag renders a tri-state boolean, and nothing at all when Google did
// not say.
func flag(label string, v *bool) string {
	if v == nil {
		return ""
	}
	if *v {
		return label + ": yes"
	}
	return label + ": no"
}

// bytesOf is a size a person can read, in the units a file manager
// uses. The exact count is in the structured half beside it.
func bytesOf(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + []string{"KB", "MB", "GB", "TB"}[exp]
}

// utc is the one place the timestamp format is decided.
func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// stamp renders an optional timestamp, and nothing at all when Google
// did not send one.
func stamp(label string, t *time.Time) string {
	if t == nil {
		return ""
	}
	return label + " " + utc(*t)
}

// sortAt renders a section's position, and nothing when it has none.
func sortAt(order *int) string {
	if order == nil {
		return ""
	}
	return "sort " + strconv.Itoa(*order)
}

// gone is the answer the three idempotent deletes give. Each has to
// distinguish something this call removed from something that was
// already missing, because a caller retrying needs to know which
// happened — and three copies of that is three chances to word one of
// them as a success that did not occur.
func gone(name, verb string, done bool) string {
	if !done {
		return name + " was already gone; nothing was " + verb + "."
	}
	return strings.ToUpper(verb[:1]) + verb[1:] + " " + name + "."
}

// preview is what a dry run says.
//
// Two forms, because not every call has a body. A delete, a pin and a
// verb like markAsAway send none, and promising "the request body is in
// the structured result" for one of those points at a field that is
// null. The Phase 3 review caught that on a single tool; a live dry run
// of every write showed it was the general case.
func preview(would string) string {
	return "Dry run, nothing was sent. Would " + would + "."
}

// previewBody is preview for a call that does have a body to show. The
// body itself stays in the structured half; repeating it here would be
// the duplication this rendering exists to remove.
func previewBody(would string) string {
	return preview(would) + " The request body is in the structured result."
}

// more notes a paged answer, so the text half admits the
// same incompleteness the JSON does.
func more(token *string) string {
	if deref(token) == "" {
		return ""
	}
	return "More to come; pass the page token from the structured result."
}

func unparsedNote(n int) string {
	if n == 0 {
		return ""
	}
	return count(n, "row was", "rows were") + " not understood and left out; this listing is incomplete."
}

// person is how a sender or member is named: whoever Google identified,
// falling back to the id that always exists.
func person(id string, name, email *string) string {
	who := deref(name)
	if e := deref(email); e != "" {
		if who == "" {
			who = e
		} else {
			who += " <" + e + ">"
		}
	}
	if who == "" {
		return id
	}
	return who + " (" + id + ")"
}

// --- spaces ---

// Render says who the stored credentials belong to.
func (o WhoamiOutput) Render() string {
	return meta("Signed in as "+person(o.UserSub, &o.DisplayName, &o.Email), labelled("picture", o.PictureURL))
}

// Render is one row of a space listing.
func (o SpaceSummaryOutput) Render() string {
	return meta(o.SpaceID, o.Type, o.DisplayName)
}

// Render is one space in full.
func (o SpaceDetailOutput) Render() string {
	return block(
		meta(o.SpaceID, o.Type, o.DisplayName),
		meta(
			flag("direct message with an app", o.SingleUserBotDM),
			flag("external users allowed", o.ExternalUserAllowed),
			stamp("created", o.CreateTime),
		),
	)
}

// Render lists the spaces the account belongs to.
func (o ListSpacesOutput) Render() string {
	return block(listing(count(len(o.Result), "space", "spaces"), rows(o.Result)), more(o.NextPageToken))
}

// Render is a page of search matches. The two counts Google withholds
// from an ordinary caller are shown only when they arrived.
func (o SearchSpacesOutput) Render() string {
	header := count(len(o.Result), "space", "spaces")
	if o.TotalSize > len(o.Result) {
		header += " of " + strconv.Itoa(o.TotalSize)
	}
	out := listing(header, rows(o.Result))
	if o.NextPageToken != "" {
		out += "\nMore: page_token " + o.NextPageToken
	}
	return out
}

// Render is a page of group chats.
func (o FindGroupChatsOutput) Render() string {
	out := listing(count(len(o.Result), "group chat", "group chats"), rows(o.Result))
	if o.NextPageToken != "" {
		out += "\nMore: page_token " + o.NextPageToken
	}
	return out
}

// Render names the direct message with one person.
func (o FindDirectMessageOutput) Render() string {
	return "Direct message: " + o.SpaceID
}

// Render says what group chat was created.
func (o CreateGroupChatOutput) Render() string {
	if o.DryRun {
		return previewBody("create a group chat with " + count(o.MemberCount, "member", "members"))
	}
	return "Created group chat " + deref(o.SpaceID) + " with " + count(o.MemberCount, "member", "members") + "."
}

// Render says what space was created.
func (o CreateSpaceOutput) Render() string {
	who := count(o.MemberCount, "member", "members")
	if o.DryRun {
		return previewBody("create the space " + o.DisplayName + " with " + who)
	}
	return "Created " + meta(deref(o.SpaceID), o.DisplayName) + " with " + who + "."
}

// Render says what changed about a space.
func (o UpdateSpaceOutput) Render() string {
	changed := meta(labelled("name", deref(o.DisplayName)), labelled("description", deref(o.Description)))
	if o.DryRun {
		return previewBody(meta("update "+o.SpaceID, changed))
	}
	return meta("Updated "+o.SpaceID, changed, labelled("mask", deref(o.UpdateMask)))
}

// --- messages ---

// Render is one message: where and from whom on the first line, what it
// said on the rest.
func (o MessageOutput) Render() string {
	return block(
		meta(o.MessageID, utc(o.Timestamp),
			person(o.SenderUserID, o.SenderDisplayName, o.SenderEmail),
			labelled("thread", o.ThreadID)),
		o.Text,
	)
}

// Render lists a page of messages.
func (o MessageListOutput) Render() string {
	return block(listing(count(len(o.Result), "message", "messages"), rows(o.Result)),
		unparsedNote(o.Unparsed), more(o.NextPageToken))
}

// Render is one emoji and how many people used it.
func (o ReactionSummaryOutput) Render() string {
	return o.Emoji + " " + strconv.Itoa(o.Count)
}

// Render is one message with everything known about it.
func (o MessageDetailOutput) Render() string {
	reactions := ""
	if len(o.Reactions) > 0 {
		reactions = "reactions: " + strings.Join(rows(o.Reactions), "  ")
		if o.ReactionsPaged {
			reactions += " (more not shown; call list_reactions)"
		}
	}
	attachments := ""
	if len(o.Attachments) > 0 {
		attachments = listing(count(len(o.Attachments), "attachment", "attachments"), rows(o.Attachments))
	}
	return block(
		meta(o.MessageID, utc(o.Timestamp),
			person(o.SenderUserID, o.SenderDisplayName, o.SenderEmail),
			labelled("space", o.SpaceID), labelled("thread", o.ThreadID),
			stamp("edited", o.LastUpdateTime)),
		o.Text,
		reactions,
		attachments,
	)
}

// Render is one file on a message. Whether it can be downloaded is said
// either way: "a Drive file" is why, and silence would read as an
// oversight rather than a fact.
func (o AttachmentOutput) Render() string {
	downloadable := "downloadable"
	if !o.Downloadable {
		downloadable = "not downloadable through Chat"
	}
	return meta(o.FileName, o.ContentType, o.Source, downloadable,
		labelled("drive file", deref(o.DriveFileID)), o.AttachmentName)
}

// Render is one search hit.
func (o SearchMatchOutput) Render() string {
	return block(
		meta(o.MessageID, utc(o.Timestamp), o.SenderUserID, labelled("thread", o.ThreadID)),
		o.Snippet,
	)
}

// Render reports the hits and how much of the space was searched, which
// is what says whether an empty answer means anything.
func (o SearchMessagesOutput) Render() string {
	header := count(len(o.Matches), "match", "matches")
	if o.ServerSide {
		header += ", searched by Google"
	} else {
		header += " in " + count(o.Scanned, "message", "messages") + " scanned here"
	}
	out := listing(header, rows(o.Matches))
	// Both ways of being partial say so, and each says what to do next.
	switch {
	case o.CapReached && o.NextPageToken != "":
		out = block(out, "More matches remain; pass page_token "+o.NextPageToken+" for the next page.")
	case o.CapReached:
		out = block(out, "There is more than this: the scan stopped at its page or match limit, "+
			"so earlier messages were not searched.")
	}
	return block(out, unparsedNote(o.Unparsed))
}

// Render says where a message landed.
func (o SendMessageOutput) Render() string {
	if o.DryRun {
		return previewBody("post to " + o.SpaceID)
	}
	return meta("Posted "+deref(o.MessageID), labelled("in thread", deref(o.ThreadID)))
}

// Render says what a message now reads.
func (o UpdateMessageOutput) Render() string {
	if o.DryRun {
		return previewBody("edit " + o.MessageName)
	}
	return block("Edited "+o.MessageName+".", o.Text)
}

// Render distinguishes a message this call deleted from one that was
// already gone, because a caller retrying needs to know which happened.
func (o DeleteMessageOutput) Render() string {
	// The replies belong to other people, so a delete that took them
	// says so in both halves rather than only in the JSON.
	withReplies := ""
	if o.Forced {
		withReplies = ", and its threaded replies"
	}
	if o.DryRun {
		return preview("delete " + o.MessageName + withReplies)
	}
	if !o.Deleted {
		return gone(o.MessageName, "deleted", false)
	}
	return "Deleted " + o.MessageName + withReplies + "."
}

// --- availability, custom emoji and deleting a space ---

// Render is your own presence.
func (o AvailabilityOutput) Render() string {
	status := o.StatusText
	if o.StatusEmoji != "" {
		status = joinNonEmpty(" ", []string{o.StatusEmoji, o.StatusText})
	}
	return meta(
		"You are "+o.State,
		status,
		stamp("status until", o.StatusExpires),
		stamp("do not disturb until", o.DoNotDisturbUntil),
	)
}

// Render says what a presence change did.
func (o SetAvailabilityOutput) Render() string {
	if o.DryRun {
		return preview("set you to " + o.State)
	}
	return o.AvailabilityOutput.Render() + ". Other people see this."
}

// Render says what a custom status change did.
func (o SetCustomStatusOutput) Render() string {
	what := "set your status"
	if o.Cleared {
		what = "clear your status"
	}
	if o.DryRun {
		return previewBody(what)
	}
	return o.AvailabilityOutput.Render() + ". Everyone who sees you in Chat sees this."
}

// Render is one custom emoji.
func (o CustomEmojiOutput) Render() string { return meta(o.EmojiName, o.Name) }

// Render lists the organisation's emoji.
func (o ListCustomEmojisOutput) Render() string {
	out := listing(count(len(o.Result), "custom emoji", "custom emoji"), rows(o.Result))
	if o.NextPageToken != "" {
		out += "\nMore: page_token " + o.NextPageToken
	}
	return out
}

// Render says what was created, and how big the image was — which is
// the fact a dry run exists to report.
func (o CreateCustomEmojiOutput) Render() string {
	size := bytesOf(int64(o.Bytes))
	if o.DryRun {
		return preview("create " + o.EmojiName + " from an image of " + size)
	}
	return "Created " + o.EmojiName + " (" + o.Name + ") from " + size +
		". Everyone in the organisation can use it."
}

// Render says whether an emoji went.
func (o DeleteCustomEmojiOutput) Render() string {
	if o.DryRun {
		return preview("delete " + o.Name)
	}
	return gone(o.Name, "deleted", o.Deleted)
}

// Render says whether a space went, and what went with it.
func (o DeleteSpaceOutput) Render() string {
	if o.DryRun {
		return preview("delete " + o.SpaceID + " and every message and membership in it")
	}
	if !o.Deleted {
		return gone(o.SpaceID, "deleted", false)
	}
	return "Deleted " + o.SpaceID + ", with every message and membership in it. This cannot be undone."
}

// --- pins, read state and notification settings ---

// Render is one pinned message.
func (o PinOutput) Render() string { return meta(o.MessageID, o.PinName) }

// Render lists what a space has pinned.
func (o ListPinnedMessagesOutput) Render() string {
	out := listing(count(len(o.Result), "pinned message", "pinned messages"), rows(o.Result))
	if o.NextPageToken != "" {
		out += "\nMore: page_token " + o.NextPageToken
	}
	return out
}

// Render says what happened to a pin. A message that was not pinned is
// distinguished from one this call unpinned, the way the deletes are.
func (o PinMessageOutput) Render() string {
	switch {
	case o.DryRun && o.Unpinned || o.DryRun && o.PinName != "" && !o.Pinned:
		return preview("unpin " + o.MessageID)
	case o.DryRun:
		return preview("pin " + o.MessageID)
	case o.Pinned:
		return "Pinned " + o.MessageID + "."
	case o.Unpinned:
		return "Unpinned " + o.MessageID + "."
	default:
		return o.MessageID + " was not pinned; nothing was unpinned."
	}
}

// Render says how far you have read.
func (o ReadStateOutput) Render() string {
	if o.LastReadTime == nil {
		return "Nothing read here yet."
	}
	return "Read up to " + utc(*o.LastReadTime) + "."
}

// Render says where the read mark now stands, and whose it is.
//
// The dry run says what it would do rather than lowercasing what it did:
// "Would marked read up to" is what that shortcut produced.
func (o MarkReadOutput) Render() string {
	at := utc(o.LastReadTime)
	if o.DryRun {
		if o.Unread {
			return preview("rewind your read mark to " + at + ", so later messages are unread again")
		}
		return preview("mark this space read up to " + at)
	}
	if o.Unread {
		return "Rewound your read mark to " + at +
			", so later messages are unread again. This is your own read state; nobody else sees a change."
	}
	return "Marked read up to " + at + ". This is your own read state; nobody else sees a change."
}

// Render is your notification choice for a space. A setting the caller
// left alone is left out rather than printed as a label with nothing
// after it.
func (o NotificationSettingOutput) Render() string {
	return meta(
		labelled("notifications:", o.NotificationSetting),
		labelled("mute:", o.MuteSetting),
	)
}

// Render says what a notification change did.
func (o UpdateSpaceNotificationSettingOutput) Render() string {
	if o.DryRun {
		return previewBody("set " + o.NotificationSettingOutput.Render())
	}
	return o.NotificationSettingOutput.Render() + ". Yours alone; nobody else sees a change."
}

// --- members ---

// Render says what a role change did. The role is the only thing this
// call can alter, so it is the only thing reported.
func (o UpdateMemberRoleOutput) Render() string {
	if o.DryRun {
		return previewBody("set " + o.MembershipName + " to " + o.Role)
	}
	return o.MembershipName + " is now " + o.Role + "."
}

// Render is one member of a space.
func (o MemberOutput) Render() string {
	return meta(o.MembershipName, o.Kind, person(o.MemberID, o.DisplayName, o.Email),
		o.Role, o.State, o.Affiliation)
}

// Render lists a space's members.
func (o MemberListOutput) Render() string {
	return block(listing(count(len(o.Result), "member", "members"), rows(o.Result)),
		unparsedNote(o.Unparsed), more(o.NextPageToken))
}

// Render says who was invited.
func (o AddMemberOutput) Render() string {
	if o.DryRun {
		return previewBody("invite " + o.UserEmail + " to " + o.SpaceID)
	}
	return meta("Invited "+o.UserEmail+" to "+o.SpaceID, deref(o.MembershipName))
}

// Render distinguishes a membership this call removed from one that was
// already gone.
func (o RemoveMemberOutput) Render() string {
	if o.DryRun {
		return preview("remove " + o.MembershipName)
	}
	return gone(o.MembershipName, "removed", o.Removed)
}

// --- reactions ---

// Render is one reaction.
func (o ReactionOutput) Render() string {
	return meta(o.ReactionName, o.Emoji, o.UserID)
}

// Render lists the reactions on a message.
func (o ListReactionsOutput) Render() string {
	return block(
		listing(count(len(o.Reactions), "reaction", "reactions"), rows(o.Reactions)),
		more(o.NextPageToken),
	)
}

// Render says what reaction was added.
func (o AddReactionOutput) Render() string {
	return meta("Reacted "+o.Emoji, o.ReactionName, o.UserID)
}

// Render distinguishes a reaction this call removed from one that was
// not there.
func (o RemoveReactionOutput) Render() string {
	if !o.Removed {
		return "No matching reaction; nothing was removed."
	}
	return "Removed " + deref(o.ReactionName) + "."
}

// --- sections ---

// Render is one sidebar section.
func (o SectionOutput) Render() string {
	return meta(o.SectionName, o.DisplayName, o.Type, sortAt(o.SortOrder))
}

// Render lists the caller's sidebar sections.
func (o ListSectionsOutput) Render() string {
	return block(
		listing(count(len(o.Sections), "section", "sections"), rows(o.Sections)),
		more(o.NextPageToken),
		unparsedNote(o.Unparsed),
	)
}

// Render is one space filed in a section.
func (o SectionItemOutput) Render() string {
	return meta(o.ItemName, labelled("space", deref(o.SpaceID)), labelled("in", o.SectionName))
}

// Render lists what a section holds.
func (o ListSectionItemsOutput) Render() string {
	return block(
		listing(count(len(o.Items), "item", "items"), rows(o.Items)),
		more(o.NextPageToken),
		unparsedNote(o.Unparsed),
	)
}

// Render says what section was created or renamed.
func (o SectionWriteOutput) Render() string {
	if o.DryRun {
		return previewBody("name a section " + o.DisplayName)
	}
	return meta("Section "+o.DisplayName, deref(o.SectionName))
}

// Render distinguishes a section this call deleted from one that was
// already gone.
func (o DeleteSectionOutput) Render() string {
	if o.DryRun {
		return preview("delete " + o.SectionName)
	}
	return gone(o.SectionName, "deleted", o.Deleted)
}

// Render says where a section now sits.
func (o PositionSectionOutput) Render() string {
	if o.DryRun {
		return previewBody(meta("move "+o.SectionName, sortAt(o.SortOrder)))
	}
	return meta("Moved "+o.SectionName, sortAt(o.SortOrder))
}

// Render says whether the space actually moved. It is a no-op when the
// space is already filed where it was asked to go, and saying so is the
// point of the tool.
func (o MoveSpaceToSectionOutput) Render() string {
	// The preview is keyed on the request body, not on dry_run. A dry
	// run of a space already in the target section carries no body — the
	// service leaves it out on purpose, because moved=false means both
	// "skipped" and "dry run" and the body is what tells them apart. So
	// checking dry_run first told the reader a body was waiting for them
	// in a result that has none, about a move that would not happen.
	if o.RenderedPayload != nil {
		return previewBody("move " + o.SpaceID + " to " + o.SectionName)
	}
	if !o.Moved {
		return o.SpaceID + " is already in " + o.SectionName + "; nothing was moved."
	}
	return meta("Moved "+o.SpaceID+" to "+o.SectionName, labelled("from", o.FromSection), labelled("item", o.ItemName))
}

// --- people ---

// Render is one person the directory or the caller's contacts matched.
func (o PersonHitOutput) Render() string {
	return meta(person(deref(o.UserID), o.DisplayName, o.Email), o.Source)
}

// Render reports the matches and which sources answered, because a
// source that failed is why a name may be missing.
func (o SearchPeopleOutput) Render() string {
	out := listing(count(o.TotalReturned, "person", "people"), rows(o.People))
	if len(o.SourcesSucceeded) < len(o.SourcesAttempted) {
		out = block(out, "Only "+strings.Join(o.SourcesSucceeded, ", ")+" answered, of "+
			strings.Join(o.SourcesAttempted, ", ")+"; this list may be short.")
	}
	return out
}
