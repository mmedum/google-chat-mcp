//go:build live

package livecheck

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// excused names a tool the live driver does not exercise, and why.
//
// A reason, not a list of names: an exemption with no reason is a gap
// nobody has decided about, and the point of this gate is that the gap
// is visible on every run rather than discovered at a release. Anything
// here that says "not written yet" is work, not a decision.
var excused = map[string]string{
	// Reaching another person. The scratch space has one member — this
	// account — and a live run may not write into somebody else's
	// sidebar, direct messages or membership to prove a tool works.
	"add_member":         "would change another person's membership",
	"remove_member":      "would change another person's membership",
	"update_member_role": "would change another person's membership",
	"find_direct_message": "opens a direct message with another person, " +
		"which is visible to them the moment anything is posted",
	"create_group_chat": "creates a chat with other people in it",
	"find_group_chats":  "reads chats with other people in them",

	// Account-wide or organisation-wide state, outside the scratch
	// space. A live run must be undoable by deleting one space, and
	// none of these are.
	"set_availability":    "sets this account's presence, which nothing here can restore",
	"set_custom_status":   "sets this account's status, which nothing here can restore",
	"get_availability":    "reads presence, which only has an answer once set_availability has run",
	"create_custom_emoji": "custom emoji are visible to the whole organisation",
	"delete_custom_emoji": "would delete an organisation's emoji",
	"get_custom_emoji":    "needs an emoji this run may not create",
	"list_custom_emojis":  "lists the organisation's emoji, which a report may not carry",
	"create_section":      "sidebar sections are account-wide, not inside the scratch space",
	"delete_section":      "sidebar sections are account-wide, not inside the scratch space",
	"rename_section":      "sidebar sections are account-wide, not inside the scratch space",
	"list_sections":       "sidebar sections are account-wide, not inside the scratch space",
	"list_section_items":  "sidebar sections are account-wide, not inside the scratch space",
	"move_space_to_section": "moves a space in the account's own sidebar, " +
		"and the scratch space is deleted before the move could be undone",
	"position_section": "reorders the account's sidebar",

	// Only a Workspace administrator can call it, and turning its
	// toolset on widens what login asks for.
	"search_spaces": "administrator-only, and its toolset is off by default",

	// Work, not a decision. These are safe inside the scratch space and
	// are simply not written yet.
	"upload_attachment":                 "not written yet: safe inside the scratch space",
	"download_attachment":               "not written yet: safe inside the scratch space",
	"search_people":                     "not written yet: reads the directory and writes nothing",
	"mark_space_unread":                 "not written yet: safe inside the scratch space",
	"get_thread_read_state":             "not written yet: safe inside the scratch space",
	"get_space_event":                   "not written yet: needs an event name from list_space_events",
	"update_space_notification_setting": "not written yet: safe inside the scratch space",
}

// registeredTools asks the built binary what it registers. It needs no
// credentials — registration does not call Google — so this gate runs
// anywhere, including where the live driver cannot.
func registeredTools(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(binPath(t), "--dump-schemas").Output()
	if err != nil {
		t.Fatalf("--dump-schemas: %v", err)
	}
	var dump struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("decode the schema dump: %v", err)
	}
	names := make([]string, 0, len(dump.Tools))
	for _, tool := range dump.Tools {
		names = append(names, tool.Name)
	}
	if len(names) == 0 {
		t.Fatal("the schema dump named no tools: this gate is looking at nothing")
	}
	return names
}

// The gate. A tool that no step exercises and no reason excuses fails
// the build, so a tool added tomorrow cannot be silently unexercised —
// which is the failure this whole file exists to prevent, and the one
// google-docs-mcp named as the reason its own live driver is a release
// gate rather than a script somebody remembers to run.
func TestEveryToolIsExercisedOrExcused(t *testing.T) {
	exercised := map[string]bool{
		// Not in the step table: the harness itself calls these, to make
		// the scratch space and to take it away again.
		"create_space": true,
		"delete_space": true,
	}
	for _, s := range steps {
		if s.tool == "" {
			t.Errorf("step %q names no tool, so the gate cannot count it", s.name)
			continue
		}
		exercised[s.tool] = true
	}

	registered := registeredTools(t)
	var missing []string
	for _, name := range registered {
		if exercised[name] {
			continue
		}
		if _, ok := excused[name]; ok {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d tool(s) are registered but neither exercised nor excused:\n  %s\n"+
			"Add a step, or add a reason to `excused` saying why a live run must not call it.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// The other direction. A name in either list that no longer exists
	// is a rule about nothing, and it reads exactly like a rule that
	// works — the shape google-docs-mcp found in three of its own gates
	// on the same day this was written.
	known := map[string]bool{}
	for _, name := range registered {
		known[name] = true
	}
	for name := range excused {
		if !known[name] {
			t.Errorf("excused tool %q is not registered any more; drop the excuse", name)
		}
	}
	for name := range exercised {
		if !known[name] {
			t.Errorf("a step exercises %q, which is not a registered tool", name)
		}
	}

	// A floor on what was read, because "nothing missing" and "nothing
	// examined" print the same thing.
	t.Logf("%d tools registered: %d exercised, %d excused",
		len(registered), len(exercised), len(excused))
	if len(exercised)+len(excused) != len(registered) {
		t.Errorf("the two lists cover %d of %d tools; they must account for all of them",
			len(exercised)+len(excused), len(registered))
	}
}
