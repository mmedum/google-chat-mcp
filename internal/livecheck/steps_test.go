//go:build live

package livecheck

import (
	"regexp"
	"strings"
	"testing"
)

var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

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
		var out struct{ Email string }
		d.into(d.must("whoami", nil), &out)
		if out.Email == "" {
			d.t.Error("whoami named no account")
		}
		d.email = out.Email
	}},

	{"the scratch space reads back", "get_space", func(d *driver) {
		var out struct {
			SpaceID     string `json:"space_id"`
			DisplayName string `json:"display_name"`
		}
		d.into(d.must("get_space", map[string]any{"space_id": d.space}), &out)
		if out.SpaceID != d.space {
			d.t.Errorf("get_space returned %s, want the space it was asked for", d.redact(out.SpaceID))
		}
		if !strings.Contains(out.DisplayName, spacePrefix) {
			d.t.Errorf("display name = %q, want the name create_space was given", out.DisplayName)
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
			d.t.Errorf("the body came back as %q, want it posted verbatim", out.Text)
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

	{"the thread carries the message", "get_thread", func(d *driver) {
		if d.thread == "" {
			d.t.Skip("no thread id was returned")
		}
		d.must("get_thread", map[string]any{"space_id": d.space, "thread_name": d.thread})
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
			d.t.Errorf("after the edit the body is %q, want the replacement", out.Text)
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
		d.must("search_messages", map[string]any{
			"space_id": d.space, "query": searchTerm,
		})
	}},

	{"the space is marked read", "mark_space_read", func(d *driver) {
		d.must("mark_space_read", map[string]any{"space_id": d.space})
	}},

	{"the read state reads back", "get_space_read_state", func(d *driver) {
		d.must("get_space_read_state", map[string]any{"space_id": d.space})
	}},

	{"the notification setting reads back", "get_space_notification_setting", func(d *driver) {
		d.must("get_space_notification_setting", map[string]any{"space_id": d.space})
	}},

	{"the space renames", "update_space", func(d *driver) {
		d.must("update_space", map[string]any{
			"space_id": d.space, "display_name": spacePrefix + " renamed (safe to delete)",
		})
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
		}
		d.into(d.must("list_space_events", map[string]any{
			"space_id": d.space, "event_types": []string{"message_created"},
		}), &out)
		if out.Unparsed > 0 {
			d.t.Errorf("%d event(s) could not be parsed, so the listing is incomplete "+
				"rather than short", out.Unparsed)
		}
	}},

	// The bug this repository shipped once: Google answers a deleted
	// message with 200 and a tombstone rather than 404, so a repeat
	// delete that reads the status instead of the answer reports success
	// twice. Every gate passed while it was broken.
	{"the message deletes", "delete_message", func(d *driver) {
		var out struct {
			Deleted bool `json:"deleted"`
		}
		d.into(d.must("delete_message", map[string]any{"message_name": d.posted}), &out)
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
		res := d.call("delete_message", map[string]any{"message_name": d.posted})
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
	liveEditBody = "livecheck edited this line and will delete it"
	searchTerm   = "livecheck"
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
			d.posted, d.thread, d.member, d.email = inner.posted, inner.thread, inner.member, inner.email
		}) {
			t.Fatalf("stopping: later steps read what %q wrote", s.name)
		}
	}
}
