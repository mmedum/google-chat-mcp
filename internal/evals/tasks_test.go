//go:build evals

package evals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// check is one scored assertion about the end state or the trace.
type check struct {
	name   string
	ok     bool
	detail string
}

// task is a prompt, an optional setup, and what counts as done.
//
// Every end state below is one the Phase 4 live run already produced
// through these same tools against this same API, which is what stops a
// task from being impossible for a reason that has nothing to do with
// the model. Where that is not enough — a search index that may not have
// caught up — the task carries a `reachable` probe that says so instead
// of blaming the model.
type task struct {
	name   string
	prompt string // %s is the scratch space id, unless promptFn is set
	// promptFn builds the prompt when it needs something only the seeded
	// space can supply, a message name for one.
	promptFn func(s *server, space string) string
	setup    func(s *server, space string)
	check    func(s *server, space string, tr *trace) []check
	// reachable runs before scoring. When it says no, the task is
	// reported as unreachable rather than failed: the world would not
	// have allowed the end state whatever the model did.
	reachable func(s *server, space string) (bool, string)
	// cleanup undoes anything the task made outside its scratch space.
	cleanup func(s *server, space string)
	// env is extra configuration for the server the model talks to, not
	// for the one that seeds and scores.
	env map[string]string
}

// seeded is what a setup leaves behind for the prompt and the checks.
// Keyed by space, because subtests may run in any order.
var seeded = map[string]message{}

// uploadName is the name the attachment task gives its file, unique per
// run. The transfer directory outlives a run and a download never
// overwrites — it lands beside the old file under a suffixed name — so a
// fixed name lets a second run pass by inspecting the first run's file
// while the model's actual download goes unexamined.
var uploadName = map[string]string{}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// usedTool reports whether the model called a tool, and lists what it
// did call, which is the detail worth reading on a failure.
func usedTool(tr *trace, name string) (bool, string) {
	return len(tr.callsTo(name)) > 0, strings.Join(tr.toolNames(), " → ")
}

// exactly is the check that matters most for send_message: the body
// Google stored has to be the body that was asked for, byte for byte.
func exactly(want string, got []message) (bool, string) {
	for _, m := range got {
		if m.Text == want {
			return true, ""
		}
	}
	var texts []string
	for _, m := range got {
		texts = append(texts, fmt.Sprintf("%q", m.Text))
	}
	return false, "space holds " + strings.Join(texts, ", ")
}

const verbatim = "Deploy at 14:00 — *not* 15:00. Ask #ops before changing it."

var tasks = []task{
	{
		name: "post-verbatim",
		prompt: "Post this message, exactly as written, to the Google Chat space %s:\n\n" +
			verbatim + "\n\nDo not reformat it, do not add anything to it.",
		check: func(s *server, space string, tr *trace) []check {
			ok, detail := exactly(verbatim, s.messages(space))
			sends := tr.callsTo("send_message")
			return []check{
				{"the space holds the text exactly as asked", ok, detail},
				{"posted once", len(sends) == 1, fmt.Sprintf("%d send_message calls", len(sends))},
				{"did not edit it afterwards", len(tr.callsTo("update_message")) == 0, ""},
			}
		},
	},
	{
		name: "reply-in-thread",
		setup: func(s *server, space string) {
			seeded[space] = s.post(space, "Can someone take the 14:00 deploy?", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s there is a message asking who can take the "+
				"14:00 deploy. Reply to it in its own thread with exactly: On it.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			parent := seeded[space]
			var reply message
			var found bool
			for _, m := range s.messages(space) {
				if m.Text == "On it." {
					reply, found = m, true
					break
				}
			}
			if !found {
				return []check{{"replied", false, "no message says 'On it.'"}}
			}
			return []check{
				{"replied", true, ""},
				{"in the same thread", reply.ThreadID == parent.ThreadID,
					fmt.Sprintf("reply thread %s, parent thread %s", reply.ThreadID, parent.ThreadID)},
				{"named the thread rather than starting a new one",
					len(tr.callsTo("send_message")) > 0 && threadNamed(tr), traceInputs(tr, "send_message")},
			}
		},
	},
	{
		name: "edit-message",
		setup: func(s *server, space string) {
			seeded[space] = s.post(space, "Deploy at 14:00.", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s, the message that says 'Deploy at 14:00.' is "+
				"wrong: the deploy moved to 16:00. Correct that message in place.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			m, found := s.message(seeded[space].MessageID)
			used, calls := usedTool(tr, "update_message")
			return []check{
				{"the original message still exists", found, "it was deleted and replaced"},
				{"it now says 16:00", found && strings.Contains(m.Text, "16:00"), m.Text},
				{"edited rather than reposted", used, calls},
				{"did not delete anything", len(tr.callsTo("delete_message")) == 0, calls},
			}
		},
	},
	{
		name: "delete-thread",
		setup: func(s *server, space string) {
			parent := s.post(space, "Old incident notes, to be removed.", "")
			s.post(space, "Adding a detail here.", parent.ThreadID)
			seeded[space] = parent
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s, delete the message about old incident notes "+
				"and every reply under it.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			// Not get_message: Google answers a deleted message with a
			// 200 tombstone rather than a 404, which Phase 2 found the
			// hard way. Asking the space is the question that has a
			// truthful answer.
			found := false
			for _, m := range s.messages(space) {
				if m.MessageID == seeded[space].MessageID {
					found = true
				}
			}
			forced := false
			for _, c := range tr.callsTo("delete_message") {
				if b, _ := c.Input["force"].(bool); b {
					forced = true
				}
			}
			return []check{
				{"the message is gone", !found, "it is still there"},
				{"used force, which is the only way Google allows it", forced, traceInputs(tr, "delete_message")},
			}
		},
	},
	{
		name: "react-then-clear",
		setup: func(s *server, space string) {
			seeded[space] = s.post(space, "Release notes are up for review.", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s, add a 👍 reaction to the message about release "+
				"notes, then take that reaction off again.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			left := s.reactions(seeded[space].MessageID)
			added, _ := usedTool(tr, "add_reaction")
			removed, calls := usedTool(tr, "remove_reaction")
			return []check{
				{"no reaction left on the message", len(left) == 0, fmt.Sprintf("%d remain", len(left))},
				{"added one", added, calls},
				{"removed it", removed, calls},
			}
		},
	},
	{
		name: "find-message",
		setup: func(s *server, space string) {
			s.post(space, "Standup notes for Monday.", "")
			seeded[space] = s.post(space, "The rollback window is 40 minutes, not 20.", "")
			s.post(space, "Lunch is at noon.", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s, find the message that says how long the "+
				"rollback window is, and tell me the number of minutes.", space)
		},
		// Google's message search is an index, and an index can lag a
		// message posted seconds ago. If it has not caught up, this task
		// says so rather than counting it against the model.
		reachable: func(s *server, space string) (bool, string) {
			for _, m := range s.messages(space) {
				if strings.Contains(m.Text, "rollback window") {
					return true, ""
				}
			}
			return false, "the seeded message is not readable in the space yet"
		},
		check: func(s *server, space string, tr *trace) []check {
			scoped := false
			for _, c := range tr.callsTo("search_messages") {
				if sp, _ := c.Input["space_id"].(string); sp == space {
					scoped = true
				}
			}
			return []check{
				{"answered 40 minutes", strings.Contains(tr.Final, "40"), clip(tr.Final, 300)},
				{"did not quote the wrong message", !strings.Contains(tr.Final, "20 minutes"), clip(tr.Final, 300)},
				{"searched or read the space, not the whole account",
					scoped || len(tr.callsTo("get_messages")) > 0, strings.Join(tr.toolNames(), " → ")},
			}
		},
	},
	{
		name: "pin-message",
		setup: func(s *server, space string) {
			s.post(space, "Coffee machine is fixed.", "")
			seeded[space] = s.post(space, "Release 4.2 ships on Thursday.", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In the Google Chat space %s, pin the message about the release so it "+
				"stays at the top.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			pins := s.pins(space)
			right := len(pins) == 1 && pins[0] == seeded[space].MessageID
			return []check{
				{"exactly the release message is pinned", right, fmt.Sprintf("%v", pins)},
			}
		},
	},
	{
		name: "section-move",
		setup: func(s *server, space string) {
			var out struct {
				SectionName string `json:"section_name"`
			}
			s.into(s.must("create_section", map[string]any{
				"display_name": spacePrefix + " section (safe to delete)"}), &out)
			seeded[space] = message{MessageID: out.SectionName}
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("In my Google Chat sidebar there is a section called %q. File the space %s "+
				"under it.", spacePrefix+" section (safe to delete)", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			section := seeded[space].MessageID
			in := false
			for _, sp := range s.sectionItems(section) {
				if sp == space {
					in = true
				}
			}
			return []check{{"the space is filed under the section", in, "section " + section}}
		},
		cleanup: func(s *server, space string) {
			s.must("delete_section", map[string]any{"section_name": seeded[space].MessageID})
		},
	},
	{
		name: "attachment-round-trip",
		// The upload is renamed on the way out, and that is not
		// decoration: a download never overwrites, and it lands under the
		// attachment's own name in the one directory transfer may touch.
		// Upload eval-source.txt and the download would collide with the
		// file it came from, and the task would fail for a reason that has
		// nothing to do with the model.
		setup: func(s *server, space string) {
			stamp := time.Now().UnixNano()
			path, sum := s.writeFile(fmt.Sprintf("eval-source-%d.txt", stamp),
				[]byte(fmt.Sprintf("rollback window: 40 minutes\nrun: %d\n", stamp)))
			seeded[space] = message{MessageID: path, Text: sum}
			uploadName[space] = fmt.Sprintf("review-notes-%d.txt", stamp)
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("Upload the file %s to the Google Chat space %s, giving it the name "+
				"%s, and post it with the caption: notes for review. Then download that "+
				"attachment back and tell me its SHA-256.",
				seeded[space].MessageID, space, uploadName[space])
		},
		check: func(s *server, space string, tr *trace) []check {
			want := seeded[space].Text
			// A listing does not carry attachments — get_messages has no
			// such field, only get_message does — so this has to read
			// each message back rather than scan the list. The first
			// version of this check asserted a field the read path never
			// reports, and could not have passed.
			withAttachment := false
			for _, listed := range s.messages(space) {
				if m, ok := s.message(listed.MessageID); ok && len(m.Attachments) > 0 {
					withAttachment = true
				}
			}
			// The checksum can show up in either half: the tool reports it,
			// and the model repeats it.
			downloaded := ""
			for _, r := range tr.Results {
				if strings.Contains(r.Text, want) {
					downloaded = want
				}
			}
			// The name is unique to this run, so a file under it can only
			// have come from this run's download.
			landed := ""
			if _, err := os.Stat(filepath.Join(s.dir, uploadName[space])); err == nil {
				landed = sha256Of(s.t, filepath.Join(s.dir, uploadName[space]))
			}
			return []check{
				{"the space holds a message carrying the file", withAttachment, ""},
				{"the file came back down", landed != "", "nothing under review-notes.txt"},
				{"byte for byte what went up", landed == want, "downloaded " + clip(landed, 16)},
				{"reported the checksum", strings.Contains(strings.ToLower(tr.Final), want[:16]),
					clip(tr.Final, 200)},
				{"the tool reported it too", downloaded == want, "not in any tool result"},
			}
		},
	},
	{
		name: "mark-unread",
		setup: func(s *server, space string) {
			s.post(space, "First: the agenda.", "")
			seeded[space] = s.post(space, "Second: the decision we need.", "")
			s.post(space, "Third: any other business.", "")
		},
		promptFn: func(s *server, space string) string {
			return fmt.Sprintf("Mark the Google Chat space %s as unread from the message that starts "+
				"'Second:', so I come back to it. Then tell me whether the space now counts as unread.", space)
		},
		check: func(s *server, space string, tr *trace) []check {
			used, calls := usedTool(tr, "mark_space_unread")
			state := s.readState(space)
			return []check{
				{"marked it unread", used, calls},
				{"the space reports a read state", state != "", "empty read state"},
				{"said so in the answer", strings.Contains(strings.ToLower(tr.Final), "unread"),
					clip(tr.Final, 200)},
			}
		},
	},
}

// threadNamed reports whether any send_message named a thread rather
// than starting a new one.
func threadNamed(tr *trace) bool {
	for _, c := range tr.callsTo("send_message") {
		if v, _ := c.Input["thread_name"].(string); v != "" {
			return true
		}
	}
	return false
}

// traceInputs renders what the model passed to a tool, for a failure
// detail that says why rather than only what.
func traceInputs(tr *trace, name string) string {
	var out []string
	for _, c := range tr.callsTo(name) {
		out = append(out, fmt.Sprintf("%v", c.Input))
	}
	if len(out) == 0 {
		return "no " + name + " calls"
	}
	return strings.Join(out, "; ")
}
