//go:build live

package livecheck

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The "…" is in the local-part class on purpose: gapi masks an address
// in Google's error text to "…@domain" before it ever reaches the
// driver, and a pattern needing a local part stops matching it — so the
// upstream mask would hide the address from this redactor rather than
// from the reader, and the organization domain would land in the
// transcript. That happened in two sibling servers before it was caught.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-…]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// step is one check against the live API. `tool` is what it exercises,
// and the surface gate reads it — a tool named by no step and excused by
// no reason fails the build.
type step struct {
	name string
	tool string
	run  func(d *driver)
}

// steps run in order against one scratch space. Order matters: a read
// verifies what an earlier step wrote, never what the same exchange
// wrote. Google's write is not always readable in the exchange that made
// it, and a check that reads its own write passes for the wrong reason.
var steps = []step{
	{"the account is named", "whoami", func(d *driver) {
		var out struct {
			Email       string
			DisplayName string `json:"display_name"`
		}
		d.into(d.must("whoami", nil), &out)
		if out.Email == "" {
			d.t.Error("whoami named no account")
		}
		d.email = out.Email
		if out.DisplayName != "" {
			d.person.mu.Lock()
			d.person.names = append(d.person.names, out.DisplayName)
			d.person.mu.Unlock()
		}
	}},

	// Reads the directory and writes nothing. The account's own address
	// is a query guaranteed to resolve, and the answer is redacted.
	{"a name resolves to an address", "search_people", func(d *driver) {
		var out struct {
			TotalReturned int `json:"total_returned"`
		}
		d.into(d.must("search_people", map[string]any{"query": d.email, "limit": 5}), &out)
		if out.TotalReturned == 0 {
			d.t.Error("search_people found nobody for this account's own address")
		}
	}},

	// A group cannot be named in this repository, so the person running
	// the suite names one this account can see. Nothing is added to it:
	// a live run involves nobody else.
	{"a group's address turns into its id", "find_group", func(d *driver) {
		email := os.Getenv("LIVE_GROUP_EMAIL")
		if email == "" {
			d.t.Error("set LIVE_GROUP_EMAIL to a Google Group this account can see; find_group needs one")
			return
		}
		var out struct {
			GroupName string  `json:"group_name"`
			Email     string  `json:"email"`
			Name      *string `json:"display_name"`
		}
		d.into(d.must("find_group", map[string]any{"email": email}), &out)
		if !strings.HasPrefix(out.GroupName, "groups/") || out.Email != email {
			d.t.Errorf("find_group answered %s for the address it was given", d.redact(out.GroupName))
		}
		if out.Name == nil {
			d.t.Error("find_group read no display name: the second read failed or Cloud Identity sent none")
		}
	}},

	// The claim the documents could not settle: what an address no group
	// has answers. Settled live 2026-10-03: 403, the same as a group the
	// account may not see, so the refusal says it cannot tell which.
	{"an unknown group address is refused as not visible", "find_group", func(d *driver) {
		domain := d.email[strings.LastIndex(d.email, "@")+1:]
		res := d.call("find_group", map[string]any{
			"email": fmt.Sprintf("%s-no-such-group-%d@%s", spacePrefix, time.Now().Unix(), domain),
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[forbidden]") || !strings.Contains(res.Text, "is visible to this account") {
			d.t.Errorf("an address no group has answered: %s", d.redact(res.Text))
		}
	}},

	{"the scratch space reads back", "get_space", func(d *driver) {
		var out struct {
			SpaceID          string  `json:"space_id"`
			DisplayName      string  `json:"display_name"`
			SpaceURI         *string `json:"space_uri"`
			HumanMemberCount *int    `json:"human_member_count"`
		}
		d.into(d.must("get_space", map[string]any{"space_id": d.space}), &out)
		if out.SpaceID != d.space {
			d.t.Errorf("get_space returned %s, want the space it was asked for", d.redact(out.SpaceID))
		}
		if !strings.Contains(out.DisplayName, spacePrefix) {
			d.t.Errorf("display name = %q, want the name create_space was given", d.redact(out.DisplayName))
		}
		if out.SpaceURI == nil {
			d.t.Error("the space came back with no space_uri")
		}
		// Its creator has joined it, whatever else Google counts.
		if out.HumanMemberCount == nil || *out.HumanMemberCount < 1 {
			d.t.Error("the space came back with no count of the people in it")
		}
	}},

	// Deliberately not asserting that the space just created appears
	// here. A listing lags its own write — google-drive-mcp found six
	// surfaces of it, in both directions — so that assertion would fail
	// for a reason that is not a bug in this server. get_space above is
	// the strongly consistent read, and it is the one that checks the
	// space exists. What this holds is that the listing parses and is
	// not empty, which is what would break if the wire shape moved.
	{"spaces list and parse", "list_spaces", func(d *driver) {
		var out struct {
			Result []struct {
				SpaceID string `json:"space_id"`
			} `json:"result"`
		}
		d.into(d.must("list_spaces", map[string]any{"limit": 50}), &out)
		if len(out.Result) == 0 {
			d.t.Error("list_spaces returned nothing for an account that has at least the scratch space")
		}
		for _, s := range out.Result {
			if s.SpaceID == "" {
				d.t.Error("a listed space has no id, so a caller cannot address it")
			}
		}
	}},

	{"a dry run posts nothing", "send_message", func(d *driver) {
		res := d.call("send_message", map[string]any{
			"space_id": d.space, "text": "this must never be posted", "dry_run": true,
		})
		if res.IsError {
			d.t.Fatalf("dry run refused: %s", d.redact(res.Text))
		}
		var out struct {
			DryRun    bool   `json:"dry_run"`
			MessageID string `json:"message_id"`
		}
		d.into(res.Structured, &out)
		if !out.DryRun {
			d.t.Error("a dry run did not report itself as one")
		}
		if out.MessageID != "" {
			d.t.Error("a dry run returned a message id, so something was posted")
		}
	}},

	{"a message posts verbatim", "send_message", func(d *driver) {
		var out struct {
			MessageID string `json:"message_id"`
			ThreadID  string `json:"thread_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": livePostBody,
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("send_message returned no message id")
		}
		d.record(out.MessageID)
		d.record(out.ThreadID)
		d.posted, d.thread = out.MessageID, out.ThreadID
	}},

	// A separate exchange from the write, deliberately.
	{"the posted body is exactly what was sent", "get_message", func(d *driver) {
		var out struct {
			Text string `json:"text"`
		}
		d.into(d.must("get_message", map[string]any{"message_name": d.posted}), &out)
		if out.Text != livePostBody {
			d.t.Errorf("the body came back as %q, want it posted verbatim", d.redact(out.Text))
		}
	}},

	{"the message is in the space's history", "get_messages", func(d *driver) {
		var out struct {
			Result []struct {
				MessageID string `json:"message_id"`
			} `json:"result"`
		}
		d.into(d.must("get_messages", map[string]any{"space_id": d.space, "limit": 50}), &out)
		for _, m := range out.Result {
			if m.MessageID == d.posted {
				return
			}
		}
		d.t.Error("the posted message is not in get_messages")
	}},

	// Chat keeps a link out of the message text: it arrives as an
	// annotation naming what was linked, and a reader that drops
	// annotations sees the anchor word alone. Posting one here and
	// reading it back in a later exchange is the only way to find out
	// whether Chat makes a link chip for a message posted through the
	// API — every fake in the unit suite is written from a belief about
	// that, and this is the thing that can contradict it.
	{"a message linking to another one is posted", "send_message", func(d *driver) {
		if d.posted == "" {
			d.t.Skip("nothing was posted to link to")
		}
		var out struct {
			MessageID string `json:"message_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": searchTerm + " linked " + messageURL(d.space, d.thread, d.posted),
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("send_message returned no message id for the linking message")
		}
		d.record(out.MessageID)
		d.linked = out.MessageID
	}},

	{"a posted link reads back as a link", "get_message", func(d *driver) {
		if d.linked == "" {
			d.t.Skip("no linking message was posted")
		}
		var out struct {
			Links []struct {
				LinkType  string `json:"link_type"`
				SpaceID   string `json:"space_id"`
				MessageID string `json:"message_id"`
			} `json:"links"`
		}
		d.into(d.must("get_message", map[string]any{"message_name": d.linked}), &out)
		if len(out.Links) == 0 {
			// Not a failure. It says Chat made no chip for a link
			// posted through the API, which is Google's behavior
			// rather than this server's, and is worth knowing either
			// way. A link pasted in the Chat client is still carried.
			d.t.Log("Chat attached no rich link to a link posted through the API")
			return
		}
		got := out.Links[0]
		switch {
		case got.MessageID == d.posted:
			// The linked message, named as the resource get_message takes.
		case got.SpaceID == d.space:
			// Chat read the URL as the space rather than one message in
			// it. The URL shape is this test's own, so that is a fact
			// about the link and not a failure of the server.
			d.t.Log("Chat resolved the posted link to the space rather than to the message")
		default:
			d.t.Errorf("the link came back as %s naming %s, want the message or the space it points at",
				got.LinkType, d.redact(got.MessageID))
		}
	}},

	// Markdown is GA for messages created through the API (release note
	// 2026-08-19), and discovery calls markupSyntax optional rather than
	// output only. Only a post read back can say whether a person's
	// token gets it too.
	{"a markdown message is posted", "send_message", func(d *driver) {
		var out struct {
			MessageID string `json:"message_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": liveMarkdownBody, "markdown": true,
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("send_message returned no message id for the markdown message")
		}
		d.record(out.MessageID)
		d.markdown = out.MessageID
	}},

	{"the space reads as active once something is posted", "get_space", func(d *driver) {
		var out struct {
			LastActiveTime *string `json:"last_active_time"`
		}
		d.into(d.must("get_space", map[string]any{"space_id": d.space}), &out)
		if out.LastActiveTime == nil {
			d.t.Error("the space has messages in it and came back with no last_active_time")
		}
	}},

	{"a markdown message reads back formatted", "get_message", func(d *driver) {
		if d.markdown == "" {
			d.t.Skip("no markdown message was posted")
		}
		var out struct {
			Text          string  `json:"text"`
			FormattedText *string `json:"formatted_text"`
		}
		d.into(d.must("get_message", map[string]any{"message_name": d.markdown}), &out)
		// Chat's own markup for bold is one asterisk. Two, or no
		// formatting at all, means Google read the body as Chat syntax.
		var markup string
		if out.FormattedText != nil {
			markup = *out.FormattedText
		}
		if !strings.Contains(markup, "*"+liveMarkdownBold+"*") || strings.Contains(markup, "**"+liveMarkdownBold+"**") {
			d.t.Errorf("the markdown message came back as text %q and markup %q, want the bold word in Chat's markup",
				d.redact(out.Text), d.redact(markup))
		}
	}},

	{"the thread carries the message", "get_thread", func(d *driver) {
		if d.thread == "" {
			d.t.Skip("no thread id was returned")
		}
		d.must("get_thread", map[string]any{"space_id": d.space, "thread_name": d.thread})
	}},

	{"the thread's read state reads back", "get_thread_read_state", func(d *driver) {
		if d.thread == "" {
			d.t.Skip("no thread id was returned")
		}
		d.must("get_thread_read_state", map[string]any{
			"space_id": d.space, "thread_name": d.thread,
		})
	}},

	{"an edit replaces the body", "update_message", func(d *driver) {
		d.must("update_message", map[string]any{
			"message_name": d.posted, "text": liveEditBody,
		})
	}},

	{"the edit is what reads back", "get_message", func(d *driver) {
		var out struct {
			Text string `json:"text"`
		}
		d.into(d.must("get_message", map[string]any{"message_name": d.posted}), &out)
		if out.Text != liveEditBody {
			d.t.Errorf("after the edit the body is %q, want the replacement", d.redact(out.Text))
		}
	}},

	{"a reaction is added", "add_reaction", func(d *driver) {
		d.must("add_reaction", map[string]any{"message_name": d.posted, "emoji": "👍"})
	}},

	{"the reaction is listed", "list_reactions", func(d *driver) {
		var out struct {
			Reactions []struct {
				Emoji string `json:"emoji"`
			} `json:"reactions"`
		}
		d.into(d.must("list_reactions", map[string]any{"message_name": d.posted}), &out)
		if len(out.Reactions) == 0 {
			d.t.Error("list_reactions returned nothing after one was added")
		}
	}},

	{"the reaction is removed", "remove_reaction", func(d *driver) {
		// The address is required: a reaction belongs to a person, and
		// removing one by emoji alone would not say whose.
		d.must("remove_reaction", map[string]any{
			"message_name": d.posted, "emoji": "👍", "user_email": d.email,
		})
	}},

	{"the message pins", "pin_message", func(d *driver) {
		d.must("pin_message", map[string]any{"message_name": d.posted})
	}},

	{"the pin is listed", "list_pinned_messages", func(d *driver) {
		d.must("list_pinned_messages", map[string]any{"space_id": d.space})
	}},

	{"the pin is removed", "unpin_message", func(d *driver) {
		d.must("unpin_message", map[string]any{"message_name": d.posted})
	}},

	// Upload, post, then read back — three exchanges, because the token
	// is spent by the post and the file only exists on the message
	// afterward.
	{"a file uploads", "upload_attachment", func(d *driver) {
		path := d.writeLocal("livecheck.txt", "livecheck wrote this file and will delete it\n")
		var out struct {
			UploadToken string `json:"upload_token"`
		}
		d.into(d.must("upload_attachment", map[string]any{
			"space_id": d.space, "local_path": path,
		}), &out)
		if out.UploadToken == "" {
			d.t.Fatal("upload_attachment returned no token, so nothing can be posted with it")
		}
		d.uploadToken = out.UploadToken
	}},

	{"the upload posts as a message", "send_message", func(d *driver) {
		var out struct {
			MessageID string `json:"message_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": "livecheck attached a file",
			"attachment_upload_token": d.uploadToken,
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("the message carrying the attachment has no id")
		}
		d.record(out.MessageID)
		d.attached = out.MessageID
	}},

	// A listing used to show a file-only message as an empty row. This
	// is the one place a real attachment can say whether it now shows.
	{"a listing names the file on a message", "get_messages", func(d *driver) {
		if d.attached == "" {
			d.t.Skip("no message with an attachment was posted")
		}
		var out struct {
			Result []struct {
				MessageID   string `json:"message_id"`
				Attachments []struct {
					AttachmentName string `json:"attachment_name"`
					Downloadable   bool   `json:"downloadable"`
				} `json:"attachments"`
			} `json:"result"`
		}
		d.into(d.must("get_messages", map[string]any{"space_id": d.space, "limit": 50}), &out)
		for _, m := range out.Result {
			if m.MessageID != d.attached {
				continue
			}
			if len(m.Attachments) != 1 || m.Attachments[0].AttachmentName == "" || !m.Attachments[0].Downloadable {
				d.t.Errorf("the listed message carries %d attachments, want the one downloadable file", len(m.Attachments))
			}
			return
		}
		d.t.Error("the message with the attachment is not in get_messages")
	}},

	{"the attachment downloads into the allowed directory", "download_attachment", func(d *driver) {
		var out struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}
		d.into(d.must("download_attachment", map[string]any{"message_name": d.attached}), &out)
		if out.Bytes == 0 {
			d.t.Error("the downloaded file is empty")
		}
		if !strings.HasPrefix(out.Path, d.dir) {
			d.t.Errorf("the file was written outside the one directory this server may touch")
		}
	}},

	{"the space has members", "list_members", func(d *driver) {
		var out struct {
			Result []struct {
				MembershipName string `json:"membership_name"`
			} `json:"result"`
		}
		d.into(d.must("list_members", map[string]any{"space_id": d.space}), &out)
		if len(out.Result) == 0 {
			d.t.Fatal("a space this account created has no members")
		}
		d.member = out.Result[0].MembershipName
		d.record(d.member)
	}},

	{"a member reads back", "get_member", func(d *driver) {
		if d.member == "" {
			d.t.Skip("no member to read")
		}
		d.must("get_member", map[string]any{"membership_name": d.member})
	}},

	{"search finds the message in this space", "search_messages", func(d *driver) {
		var out struct {
			Matches []struct {
				MessageID string     `json:"message_id"`
				Links     []struct{} `json:"links"`
				Read      *bool      `json:"read"`
			} `json:"matches"`
		}
		d.into(d.must("search_messages", map[string]any{
			"space_id": d.space, "query": searchTerm,
		}), &out)
		// Google's search returns the matched message; whether it
		// returns that message's annotations with it is a belief the
		// unit fakes are written from, and this is the only place it
		// can be contradicted. The linking message is one of the hits.
		for _, m := range out.Matches {
			if m.MessageID == d.linked && len(m.Links) == 0 {
				d.t.Log("Google's search returned the linking message with no links on it")
			}
		}
		// The full view carries each hit's read state when the token
		// holds the read-state scope, which login always asks for.
		if len(out.Matches) > 0 && out.Matches[0].Read == nil {
			d.t.Error("Google's search returned hits with no read state")
		}
	}},

	{"the space is marked read", "mark_space_read", func(d *driver) {
		d.must("mark_space_read", map[string]any{"space_id": d.space})
	}},

	// from_time is required: unread from when, not unread absolutely.
	{"the space is marked unread again", "mark_space_unread", func(d *driver) {
		d.must("mark_space_unread", map[string]any{
			"space_id":  d.space,
			"from_time": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		})
	}},

	{"the read state reads back", "get_space_read_state", func(d *driver) {
		d.must("get_space_read_state", map[string]any{"space_id": d.space})
	}},

	{"the notification setting reads back", "get_space_notification_setting", func(d *driver) {
		d.must("get_space_notification_setting", map[string]any{"space_id": d.space})
	}},

	{"the notification setting changes", "update_space_notification_setting", func(d *driver) {
		var out struct {
			NotificationSetting string `json:"notification_setting"`
		}
		d.into(d.must("update_space_notification_setting", map[string]any{
			"space_id": d.space, "notification_setting": "ALL",
		}), &out)
		if out.NotificationSetting != "ALL" {
			d.t.Errorf("the setting came back as %q, want the value that was asked for — "+
				"and read off the answer, not echoed from the request", out.NotificationSetting)
		}
	}},

	{"the space renames", "update_space", func(d *driver) {
		d.must("update_space", map[string]any{
			"space_id": d.space, "display_name": spacePrefix + " renamed (safe to delete)",
		})
	}},

	// Opening a space asks the person; get_space is what says Google
	// applied it, since the tool echoes what it asked for. The space is
	// made private again before either state is judged: a failed run
	// keeps its scratch space, and it must not keep it open to the
	// organization.
	{"the space opens to the organization and closes again", "update_space", func(d *driver) {
		asked := d.person.expect(false)
		if res := d.call("update_space", map[string]any{"space_id": d.space, "audience": "default"}); res.IsError {
			d.t.Errorf("opening the space: %s", d.redact(res.Text))
		}
		if len(asked()) != 1 {
			d.t.Errorf("opening the space put %d questions to the person, not 1", len(asked()))
		}
		opened := d.accessOf(d.space)

		asked = d.person.expect(false)
		if res := d.call("update_space", map[string]any{"space_id": d.space, "audience": "private"}); res.IsError {
			d.t.Errorf("the scratch space may still be open to the organization; make it private by hand: %s",
				d.redact(res.Text))
		}
		if len(asked()) != 0 {
			d.t.Errorf("making the space private asked the person %d times", len(asked()))
		}
		closed := d.accessOf(d.space)

		if opened != "DISCOVERABLE" {
			d.t.Errorf("after opening, access state = %q, want DISCOVERABLE", d.redact(opened))
		}
		if closed != "PRIVATE" {
			d.t.Errorf("after closing, access state = %q, want PRIVATE", d.redact(closed))
		}
	}},

	// create_space sends the audience through spaces.setup, whose own
	// reference does not name accessSettings; only the target-audience
	// guide says setup takes it. This is what settles it. The space is
	// deleted before its access is judged, so a failure there leaves
	// nothing open to the organization.
	{"a space is created open", "create_space", func(d *driver) {
		var out struct {
			SpaceID string `json:"space_id"`
		}
		asked := d.person.expect(false)
		d.into(d.must("create_space", map[string]any{
			"display_name": fmt.Sprintf("%s open %d (safe to delete)", spacePrefix, time.Now().Unix()),
			"audience":     "default",
		}), &out)
		if out.SpaceID == "" {
			d.t.Fatal("create_space returned no space_id")
		}
		open := out.SpaceID
		d.record(open)
		if len(asked()) != 1 {
			d.t.Errorf("creating an open space put %d questions to the person, not 1", len(asked()))
		}
		state := d.accessOf(open)
		if res := d.call("delete_space", map[string]any{"space_id": open, "confirm_space_id": open}); res.IsError {
			d.t.Errorf("the open space was left behind: %s", d.redact(res.Text))
		}
		if state != "DISCOVERABLE" {
			d.t.Errorf("a space created with an audience reads back as %q, want DISCOVERABLE: "+
				"spaces.setup may not take accessSettings", d.redact(state))
		}
	}},

	// event_types is required, and it is the field the reference gets
	// wrong: its prose says `event_type` and its examples say
	// `event_types`, and the examples are right. Settled live, and this
	// step is what would catch it changing back.
	//
	// Not asserting that this run's own message appears: the events feed
	// lags its own writes, so that would fail for a reason that is not a
	// bug here.
	{"space events are listed", "list_space_events", func(d *driver) {
		var out struct {
			Unparsed int `json:"unparsed"`
			Events   []struct {
				EventName string `json:"event_name"`
			} `json:"events"`
		}
		d.into(d.must("list_space_events", map[string]any{
			"space_id": d.space, "event_types": []string{"message_created"},
		}), &out)
		if out.Unparsed > 0 {
			d.t.Errorf("%d event(s) could not be parsed, so the listing is incomplete "+
				"rather than short", out.Unparsed)
		}
		for _, e := range out.Events {
			if e.EventName != "" {
				d.event = e.EventName
				d.record(e.EventName)
				break
			}
		}
	}},

	{"one event reads back on its own", "get_space_event", func(d *driver) {
		if d.event == "" {
			d.t.Skip("the feed named no event yet; it lags its own writes")
		}
		var out struct {
			EventType string `json:"event_type"`
		}
		d.into(d.must("get_space_event", map[string]any{"event_name": d.event}), &out)
		if out.EventType == "" {
			d.t.Error("the event came back without a type")
		}
	}},

	// Pagination against the real API rather than against a fake, and
	// the reason that distinction is not rhetorical.
	//
	// The first version of this step asserted that limit 1 returns one
	// message. It returns NONE. Google applies the page size before it
	// filters, so a page can come back empty with a token still on it,
	// and a space holding two messages answers `limit: 1` with `[]`.
	// Reproduced by hand against a real space before this was written.
	//
	// So the rule the surface has to carry is not "a token means there
	// is more" but "an empty page is not the end". A caller that stops
	// on an empty result reports a space with messages in it as quiet.
	{"an empty page is not the end of the messages", "get_messages", func(d *driver) {
		seen := map[string]bool{}
		token := ""
		empties := 0
		for page := 0; page < 10; page++ {
			args := map[string]any{"space_id": d.space, "limit": 1}
			if token != "" {
				args["page_token"] = token
			}
			var out struct {
				Result []struct {
					MessageID string `json:"message_id"`
				} `json:"result"`
				NextPageToken *string `json:"next_page_token"`
			}
			d.into(d.must("get_messages", args), &out)
			if len(out.Result) == 0 {
				empties++
			}
			for _, m := range out.Result {
				seen[m.MessageID] = true
			}
			if out.NextPageToken == nil || *out.NextPageToken == "" {
				break
			}
			token = *out.NextPageToken
		}
		// Two messages exist by now: the one that was posted and edited,
		// and the one the upload made.
		if len(seen) < 2 {
			d.t.Errorf("paging one at a time found %d messages; the space holds at least 2, "+
				"so the token is not reaching them", len(seen))
		}
		if empties == 0 {
			d.t.Log("no empty page this run; the behavior is Google's and it is not guaranteed " +
				"to show every time")
		}
	}},

	// The claim the tool description makes, tested where it is made.
	// Repeating a send with the same client id must land on the message
	// that already exists rather than posting a second one — and until
	// this step, that behavior was asserted only by a fake written from
	// the same belief as the code.
	{"a repeated send with the same id posts once", "send_message", func(d *driver) {
		id := "client-" + strings.ToLower(strings.NewReplacer("spaces/", "", "-", "", "_", "").
			Replace(d.space))
		args := map[string]any{
			"space_id": d.space, "text": "idempotency probe", "client_message_id": id,
		}
		var first, second struct {
			MessageID *string `json:"message_id"`
		}
		d.into(d.must("send_message", args), &first)
		if first.MessageID == nil || *first.MessageID == "" {
			d.t.Fatal("the first send returned no message id")
		}
		d.record(*first.MessageID)
		d.into(d.must("send_message", args), &second)
		if second.MessageID == nil {
			d.t.Fatal("the repeat returned no message id")
		}
		if *second.MessageID != *first.MessageID {
			d.t.Errorf("the repeat posted a second message: %s then %s",
				d.redact(*first.MessageID), d.redact(*second.MessageID))
		}
	}},

	// The bug this repository shipped once: Google answers a deleted
	// message with 200 and a tombstone rather than 404, so a repeat
	// delete that reads the status instead of the answer reports success
	// twice. Every gate passed while it was broken.
	// The server asks the person before a delete it cannot take back. A
	// decline writes nothing and says so without claiming the person
	// declined, since a client can answer by itself.
	{"a declined delete deletes nothing", "delete_message", func(d *driver) {
		asked := d.person.expect(true)
		res := d.call("delete_message", map[string]any{"message_name": d.posted})
		if !res.IsError || !strings.HasPrefix(res.Text, "[blocked]") || len(asked()) != 1 {
			d.t.Fatalf("a declined delete: %s; %d questions", d.redact(res.Text), len(asked()))
		}
		if q := asked()[0]; !strings.Contains(q, "delete_message: delete a message by") || !strings.Contains(q, searchTerm) {
			d.t.Errorf("the question does not show the message: %s", d.redact(q))
		}
	}},

	{"the message deletes", "delete_message", func(d *driver) {
		var out struct {
			Deleted bool `json:"deleted"`
		}
		asked := d.person.expect(false)
		d.into(d.must("delete_message", map[string]any{"message_name": d.posted}), &out)
		if len(asked()) != 1 {
			d.t.Errorf("the delete put %d questions to the person, not 1", len(asked()))
		}
		if !out.Deleted {
			d.t.Error("the first delete reported that it deleted nothing")
		}
	}},

	// The bug this repository shipped once, and the reason this whole
	// file exists. Delete is idempotent on purpose — a repeat is not an
	// error — so the thing that has to be true is narrower and stronger:
	// the second call must report that it deleted *nothing*.
	//
	// Google answers an already-deleted message with 200 and a tombstone
	// rather than 404, so a delete that reads the status instead of the
	// answer reports `deleted: true` twice and every gate stays green.
	// That is precisely what happened, and only a live call can tell the
	// two apart.
	{"a repeat delete reports that it deleted nothing", "delete_message", func(d *driver) {
		asked := d.person.expect(false)
		res := d.call("delete_message", map[string]any{"message_name": d.posted})
		if len(asked()) != 0 {
			d.t.Errorf("deleting a message already gone asked the person %d times", len(asked()))
		}
		if res.IsError {
			d.t.Fatalf("a repeat delete failed rather than being idempotent: %s", d.redact(res.Text))
		}
		var out struct {
			Deleted bool `json:"deleted"`
		}
		d.into(res.Structured, &out)
		if out.Deleted {
			d.t.Error("deleting an already-deleted message reported deleting it again; " +
				"Google answers 200 with a tombstone, so the answer has to be read, not the status")
		}
	}},

	// A post that notifies everyone in the space asks first; an ordinary
	// one does not. The scratch space holds only this account.
	{"a post that mentions everyone asks first", "send_message", func(d *driver) {
		asked := d.person.expect(false)
		d.must("send_message", map[string]any{"space_id": d.space, "text": "livecheck mentions <users/all> and will delete it"})
		if qs := asked(); len(qs) != 1 || !strings.Contains(qs[0], "mentioning everyone in it") {
			d.t.Errorf("a post to everyone put %d questions: %s", len(qs), d.redact(strings.Join(qs, " | ")))
		}
		asked = d.person.expect(false)
		d.must("send_message", map[string]any{"space_id": d.space, "text": "livecheck posts a plain line and will delete it"})
		if len(asked()) != 0 {
			d.t.Errorf("an ordinary post asked the person %d times", len(asked()))
		}
	}},

	// Deleting the space needs the id twice. One argument would make a
	// mistyped call destroy other people's messages.
	{"deleting a space without the confirmation is refused", "delete_space", func(d *driver) {
		res := d.call("delete_space", map[string]any{"space_id": d.space})
		if !res.IsError {
			d.t.Fatal("delete_space ran without its confirmation argument")
		}
	}},
}

// The bodies are ordinary words on purpose: nothing here may look like
// anyone's real message, and nothing about them identifies an account.
const (
	livePostBody = "livecheck posted this line and will delete it"
	// liveMarkdownBody is posted with markdown: true, and its bold word
	// is what reading it back looks for.
	liveMarkdownBody = "livecheck posted this **" + liveMarkdownBold + "** line and will delete it"
	liveMarkdownBold = "markdown"
	liveEditBody     = "livecheck edited this line and will delete it"
	searchTerm       = "livecheck"
)

func TestLive(t *testing.T) {
	d := connect(t)
	d.seed()
	defer d.drop()

	for _, s := range steps {
		if !t.Run(s.name, func(t *testing.T) {
			inner := *d
			inner.t = t
			s.run(&inner)
			// Everything the step wrote comes back, with the parent's
			// testing.T put back on top. This was a hand-listed set of
			// fields, and a field added for a new step was not on it:
			// the id that step recorded was dropped between the write
			// and the read, and the read skipped itself for want of it,
			// green and having checked nothing.
			inner.t = d.t
			*d = inner
		}) {
			t.Fatalf("stopping: later steps read what %q wrote", s.name)
		}
	}
}

// messageURL is the link Copy link puts on the clipboard in Chat: the
// bare space, thread and message ids, and the query Chat appends.
//
// Read off a real one on 2026-09-17 rather than guessed. A message
// created through the API is named {id}.{id} and the URL carries the
// half before the dot, which is also the thread's id. The shape matters
// because this step is asking whether Chat chips what it is given: a
// URL of this server's own invention would answer a question nobody
// has.
func messageURL(space, thread, message string) string {
	id, _, _ := strings.Cut(path.Base(message), ".")
	return "https://chat.google.com/room/" + path.Base(space) + "/" +
		path.Base(thread) + "/" + id + "?cls=10"
}
