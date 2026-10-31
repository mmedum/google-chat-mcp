//go:build live

package livecheck

import (
	"cmp"
	"fmt"
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
			Email string
		}
		d.into(d.must("whoami", nil), &out)
		if out.Email == "" {
			d.t.Error("whoami named no account")
		}
		d.email = out.Email
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

	// The group is LIVE_GROUP_EMAIL, which seed reads before it makes
	// anything. Nothing is added to it: a live run involves nobody else.
	{"a group's address turns into its id", "find_group", func(d *driver) {
		var out struct {
			GroupName string  `json:"group_name"`
			Email     string  `json:"email"`
			Name      *string `json:"display_name"`
		}
		d.into(d.must("find_group", map[string]any{"email": d.group}), &out)
		if !strings.HasPrefix(out.GroupName, "groups/") || out.Email != d.group {
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
		// Google fills in a new space's member count a moment after the
		// create: on 2026-10-31 a read straight after create_space came
		// back once without a count and once with 0, and the same space
		// read minutes later counted its creator. So the read is
		// repeated a few times, and a space that never counts its
		// creator still fails.
		for try := range 5 {
			if try > 0 {
				time.Sleep(2 * time.Second)
			}
			d.into(d.must("get_space", map[string]any{"space_id": d.space}), &out)
			if out.HumanMemberCount != nil && *out.HumanMemberCount >= 1 {
				break
			}
		}
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

	// A search lags a new space as a listing does, so this holds that it
	// runs and parses, not that it finds the scratch space.
	{"spaces search and parse", "search_spaces", func(d *driver) {
		var out struct {
			Result []struct {
				SpaceID string `json:"space_id"`
			} `json:"result"`
		}
		d.into(d.must("search_spaces", map[string]any{
			"display_name": spacePrefix, "external_user_allowed": false, "limit": 10,
		}), &out)
		for _, s := range out.Result {
			if s.SpaceID == "" {
				d.t.Error("a space search row has no id, so a caller cannot address it")
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

	// The upper bound is a clause Google could refuse; the scratch space
	// is minutes old, so a window that ends an hour ago holds none of
	// this run's messages.
	{"a window that ends before the space existed is empty", "get_messages", func(d *driver) {
		var out struct {
			Result []struct {
				MessageID string `json:"message_id"`
			} `json:"result"`
		}
		d.into(d.must("get_messages", map[string]any{
			"space_id": d.space, "before": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		}), &out)
		if len(out.Result) != 0 {
			d.t.Errorf("a window ending an hour ago returned %d messages from a space made minutes ago", len(out.Result))
		}
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

	// Quoting needs the quoted message's exact timestamp, which the
	// server reads first; a stale one is refused. Only a reply: Google
	// refused every forward under a person's sign-in, live 2026-10-08 —
	// a main-chat message into its own space, a thread reply into another
	// thread, and a message into another space.
	{"a reply is posted into the thread", "send_message", func(d *driver) {
		if d.thread == "" {
			d.t.Skip("no thread to reply in")
		}
		var out struct {
			MessageID string `json:"message_id"`
			ThreadID  string `json:"thread_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": searchTerm + " replied in the thread", "thread_name": d.thread,
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("send_message returned no message id for the reply")
		}
		d.record(out.MessageID)
		if out.ThreadID != d.thread {
			d.t.Errorf("the reply landed in %s, want the thread it was posted into", d.redact(out.ThreadID))
		}
		d.threadReply = out.MessageID
	}},

	{"a reply quotes the posted message", "send_message", func(d *driver) {
		if d.posted == "" {
			d.t.Skip("nothing was posted to quote")
		}
		var out struct {
			MessageID string `json:"message_id"`
		}
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": searchTerm + " quoted this line", "quote_message": d.posted,
		}), &out)
		if out.MessageID == "" {
			d.t.Fatal("send_message returned no message id for the quote")
		}
		d.record(out.MessageID)
		d.quoted = out.MessageID
	}},

	{"a quote reads back naming what it quotes", "get_message", func(d *driver) {
		if d.quoted == "" {
			d.t.Skip("no quote was posted")
		}
		var out struct {
			Quote *struct {
				MessageID string `json:"message_id"`
				QuoteType string `json:"quote_type"`
			} `json:"quote"`
		}
		d.into(d.must("get_message", map[string]any{"message_name": d.quoted}), &out)
		switch {
		case out.Quote == nil:
			d.t.Error("the quote came back quoting nothing")
		case out.Quote.MessageID != d.posted:
			d.t.Errorf("the quote came back quoting %s, want the posted message", d.redact(out.Quote.MessageID))
		case cmp.Or(out.Quote.QuoteType, "REPLY") != "REPLY":
			d.t.Errorf("the quote came back as quote type %q", d.redact(out.Quote.QuoteType))
		}
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

	{"the reaction is listed, naming who reacted", "list_reactions", func(d *driver) {
		var out struct {
			Reactions []struct {
				Emoji     string  `json:"emoji"`
				UserEmail *string `json:"user_email"`
			} `json:"reactions"`
		}
		d.into(d.must("list_reactions", map[string]any{"message_name": d.posted}), &out)
		if len(out.Reactions) == 0 {
			d.t.Error("list_reactions returned nothing after one was added")
			return
		}
		// Chat names a reaction's user by id alone; the server resolves
		// the address, as it does a sender's.
		if got := out.Reactions[0].UserEmail; got == nil || !strings.EqualFold(*got, d.email) {
			d.t.Error("the reaction does not name the signed-in account")
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

	// A manager controls the space, so update_member_role makes nobody
	// one. A dry run on this account's own membership: refused before
	// anything is read or sent.
	{"making a manager is refused", "update_member_role", func(d *driver) {
		if d.member == "" {
			d.t.Skip("no member to promote")
		}
		res := d.call("update_member_role", map[string]any{
			"membership_name": d.member, "role": "MANAGER", "dry_run": true,
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[blocked]") {
			d.t.Errorf("update_member_role made a manager, or refused it otherwise: %s", d.redact(res.Text))
		}
	}},

	// add_member takes only people in the organization's directory, and
	// no Google Group, into a space that says it admits anyone from
	// outside it. Dry runs with an address no directory holds and a
	// made-up group: nothing is sent either way. Whether the scratch
	// space admits outsiders is the organization's policy, read off the
	// space rather than assumed; where it says nothing, Google left a
	// false out, refuses outsiders itself, and no lookup is made.
	{"an invite from outside the organization is refused", "add_member", func(d *driver) {
		var space struct {
			ExternalUserAllowed *bool `json:"external_user_allowed"`
		}
		d.into(d.must("get_space", map[string]any{"space_id": d.space}), &space)
		res := d.call("add_member", map[string]any{
			"space_id": d.space, "user_email": "livecheck-outsider@example.com", "dry_run": true,
		})
		group := d.call("add_member", map[string]any{
			"space_id": d.space, "group_name": "groups/AAAAlivecheckgroup", "dry_run": true,
		})
		if space.ExternalUserAllowed == nil || !*space.ExternalUserAllowed {
			d.t.Log("the scratch space does not say it admits anyone from outside, so no lookup is made")
			if res.IsError || group.IsError {
				d.t.Errorf("a dry run into a space that admits nobody from outside: %s %s",
					d.redact(res.Text), d.redact(group.Text))
			}
			return
		}
		if !res.IsError || !strings.HasPrefix(res.Text, "[blocked]") {
			d.t.Errorf("an address outside the directory was not refused: %s", d.redact(res.Text))
		}
		if !group.IsError || !strings.HasPrefix(group.Text, "[blocked]") {
			d.t.Errorf("a group into a space that admits outsiders was not refused: %s", d.redact(group.Text))
		}
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
		name := spacePrefix + " renamed (safe to delete)"
		d.must("update_space", map[string]any{"space_id": d.space, "display_name": name})
		d.name = name
	}},

	// A description edit masks all of spaceDetails, so the server reads
	// the guidelines and sends them back. The scratch space has none to
	// keep; what this settles is that the read before the patch and the
	// patch with guidelines left out still land, and the description
	// reads back.
	{"the description changes", "update_space", func(d *driver) {
		d.must("update_space", map[string]any{"space_id": d.space, "description": "livecheck scratch space"})
		var out struct {
			Description *string `json:"description"`
		}
		d.into(d.must("get_space", map[string]any{"space_id": d.space}), &out)
		if out.Description == nil || *out.Description != "livecheck scratch space" {
			d.t.Error("the description did not read back as it was set")
		}
	}},

	// Opening a space widens who can read it, so update_space refuses it
	// and set_space_audience does it; get_space is what says Google
	// applied it, since the tool echoes what it asked for. The space is
	// made private again before either state is judged, and cleanup
	// closes it again and reads it back, whatever stops the run from
	// here: a failed run keeps its scratch space, and it must not keep
	// it open to the organization.
	{"the space opens to the organization and closes again", "set_space_audience", func(d *driver) {
		if res := d.call("update_space", map[string]any{"space_id": d.space, "audience": "default"}); !res.IsError ||
			!strings.HasPrefix(res.Text, "[blocked]") {
			d.t.Errorf("update_space opened the space, or refused it otherwise: %s", d.redact(res.Text))
		}
		if res := d.call("set_space_audience", map[string]any{"space_id": d.space, "audience": "default",
			"display_name": "a space this run did not make", "confirm": true}); !res.IsError ||
			!strings.HasPrefix(res.Text, "[stale]") {
			d.t.Errorf("opening a space under another name was not refused as stale: %s", d.redact(res.Text))
		}
		if res := d.call("set_space_audience", map[string]any{"space_id": d.space, "audience": "default",
			"display_name": d.name, "dry_run": true}); res.IsError {
			d.t.Errorf("a dry run of opening the space: %s", d.redact(res.Text))
		}
		if res := d.call("set_space_audience", map[string]any{"space_id": d.space, "audience": "default",
			"display_name": d.name, "confirm": true}); res.IsError {
			d.t.Errorf("opening the space: %s", d.redact(res.Text))
		}
		opened := d.accessOf(d.space)

		if res := d.call("update_space", map[string]any{"space_id": d.space, "audience": "private"}); res.IsError {
			d.t.Errorf("the scratch space may still be open to the organization; make it private by hand: %s",
				d.redact(res.Text))
		}
		closed := d.accessOf(d.space)

		if opened != "DISCOVERABLE" {
			d.t.Errorf("after opening, access state = %q, want DISCOVERABLE", d.redact(opened))
		}
		if closed != "PRIVATE" {
			d.t.Errorf("after closing, access state = %q, want PRIVATE", d.redact(closed))
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

	// A delete names the message by how its text begins, and the server
	// reads the message to compare: text it does not begin with is
	// [stale], and nothing goes.
	{"a delete naming other text is refused", "delete_message", func(d *driver) {
		res := d.call("delete_message", map[string]any{
			"message_name": d.posted, "text_start": "livecheck wrote something else here", "confirm": true,
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[stale]") {
			d.t.Errorf("a delete naming other text was not refused as stale: %s", d.redact(res.Text))
		}
	}},

	// The bug this repository shipped once: Google answers a deleted
	// message with 200 and a tombstone rather than 404, so a repeat
	// delete that reads the status instead of the answer reports success
	// twice. Every gate passed while it was broken.
	// The reply goes first: Google refuses to delete a message that has
	// threaded replies, and taking them is delete_thread's.
	{"the thread reply deletes", "delete_message", func(d *driver) {
		if d.threadReply == "" {
			d.t.Skip("no reply was posted into the thread")
		}
		var out struct {
			Deleted bool `json:"deleted"`
		}
		d.into(d.must("delete_message", map[string]any{
			"message_name": d.threadReply, "text_start": searchTerm + " replied in the thread", "confirm": true,
		}), &out)
		if !out.Deleted {
			d.t.Error("the reply's delete reported that it deleted nothing")
		}
	}},

	{"the message deletes", "delete_message", func(d *driver) {
		var out struct {
			Deleted bool `json:"deleted"`
		}
		d.into(d.must("delete_message", map[string]any{
			"message_name": d.posted, "text_start": liveEditBody, "confirm": true,
		}), &out)
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
		res := d.call("delete_message", map[string]any{
			"message_name": d.posted, "text_start": liveEditBody, "confirm": true,
		})
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

	// A post that notifies everyone in the space is refused; an ordinary
	// one is not. The scratch space holds only this account.
	{"a post naming another space is refused", "send_message", func(d *driver) {
		res := d.call("send_message", map[string]any{
			"space_id": d.space, "space_display_name": "not " + d.name, "text": "livecheck names the wrong space",
		})
		if !res.IsError || !strings.HasPrefix(res.Text, "[stale]") {
			d.t.Errorf("a post naming another space: %s", d.redact(res.Text))
		}
	}},

	{"a post that mentions everyone is refused", "send_message", func(d *driver) {
		res := d.call("send_message", map[string]any{"space_id": d.space, "text": "livecheck mentions <users/all> and will delete it"})
		if !res.IsError || !strings.HasPrefix(res.Text, "[blocked]") {
			d.t.Errorf("a post to everyone: %s", d.redact(res.Text))
		}
		d.must("send_message", map[string]any{"space_id": d.space, "text": "livecheck posts a plain line and will delete it"})
	}},

	// A message with replies goes only through delete_thread, which
	// takes them too. Google refusing delete_message here is what
	// delete_message's description promises; the thread then goes whole,
	// and neither message is left in the space.
	{"a thread deletes with its replies", "delete_thread", func(d *driver) {
		var root, reply struct {
			MessageID string `json:"message_id"`
			ThreadID  string `json:"thread_id"`
		}
		d.into(d.must("send_message", map[string]any{"space_id": d.space, "text": liveThreadBody}), &root)
		d.record(root.MessageID)
		d.record(root.ThreadID)
		d.into(d.must("send_message", map[string]any{
			"space_id": d.space, "text": searchTerm + " replied and will go with the thread", "thread_name": root.ThreadID,
		}), &reply)
		d.record(reply.MessageID)

		if res := d.call("delete_message", map[string]any{
			"message_name": root.MessageID, "text_start": liveThreadBody, "confirm": true,
		}); !res.IsError {
			d.t.Error("delete_message deleted a message that has replies; Google was meant to refuse it")
		}
		if res := d.call("delete_thread", map[string]any{
			"message_name": reply.MessageID, "text_start": searchTerm + " replied and will go", "confirm": true,
		}); !res.IsError || !strings.HasPrefix(res.Text, "[invalid]") {
			d.t.Errorf("delete_thread took a reply as a thread's first message: %s", d.redact(res.Text))
		}
		var out struct {
			Deleted bool `json:"deleted"`
			Forced  bool `json:"forced"`
		}
		d.into(d.must("delete_thread", map[string]any{
			"message_name": root.MessageID, "text_start": liveThreadBody, "confirm": true,
		}), &out)
		if !out.Deleted {
			d.t.Error("delete_thread reported that it deleted nothing")
		}
		if !out.Forced {
			d.t.Error("delete_thread did not report taking the replies")
		}

		var left struct {
			Result []struct {
				MessageID string `json:"message_id"`
			} `json:"result"`
		}
		d.into(d.must("get_messages", map[string]any{"space_id": d.space, "limit": 50}), &left)
		for _, m := range left.Result {
			if m.MessageID == root.MessageID || m.MessageID == reply.MessageID {
				d.t.Errorf("%s is still in the space after delete_thread", d.redact(m.MessageID))
			}
		}
	}},

	// Deleting the space names it and says confirm. Leaving either out
	// changes nothing, and a name that is not the space's is [stale].
	{"deleting a space without the confirmation is refused", "delete_space", func(d *driver) {
		if res := d.call("delete_space", map[string]any{"space_id": d.space, "display_name": d.name}); !res.IsError ||
			!strings.HasPrefix(res.Text, "[invalid]") {
			d.t.Fatalf("delete_space ran without confirm, or refused it otherwise: %s", d.redact(res.Text))
		}
		if res := d.call("delete_space", map[string]any{
			"space_id": d.space, "display_name": "a space this run did not make", "confirm": true,
		}); !res.IsError || !strings.HasPrefix(res.Text, "[stale]") {
			d.t.Fatalf("delete_space ran under another space's name, or refused it otherwise: %s", d.redact(res.Text))
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
	// liveThreadBody starts the thread delete_thread takes whole.
	liveThreadBody = "livecheck started this thread and will delete all of it"
	searchTerm     = "livecheck"
)

func TestLive(t *testing.T) {
	d := connect(t)
	// A cleanup rather than a defer: it runs when a step panics too.
	t.Cleanup(d.drop)
	d.seed()

	for _, s := range steps {
		if !t.Run(s.name, func(t *testing.T) { d.runStep(t, s) }) {
			t.Fatalf("stopping: later steps read what %q wrote", s.name)
		}
	}
}

// runStep runs one step on a copy of the driver that carries the step's
// own testing.T.
func (d *driver) runStep(t testing.TB, s step) {
	// On the driver rather than the copy, and before the step runs: a
	// step that stops part way leaves the space as open as it got, and
	// its copy is not kept.
	if s.tool == "set_space_audience" {
		d.opened = true
	}
	inner := *d
	inner.t = t
	s.run(&inner)
	// Everything the step wrote comes back, with the parent's testing.T
	// put back on top. This was a hand-listed set of fields, and a field
	// added for a new step was not on it: the id that step recorded was
	// dropped between the write and the read, and the read skipped
	// itself for want of it, green and having checked nothing.
	inner.t = d.t
	*d = inner
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
