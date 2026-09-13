package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate itself, over this repository's own snapshot and record. It
// reads its two files by the paths `make check` uses, so it runs from
// the root the way the command does.
func TestAPIFieldsGate(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()
	var sb strings.Builder
	if code := apiFields(nil, io.Discard, &sb); code != 0 {
		t.Errorf("api fields gate:\n%s", sb.String())
	}
}

func fieldsFixture() fieldsSnapshot {
	return fieldsSnapshot{Fetched: "2026-09-12", APIs: []fieldsAPI{
		{API: "chat", Schemas: []fieldsSchema{
			{Name: "Message", Properties: []string{"name", "text"}},
			{Name: "Membership", Properties: []string{"name", "role"}},
			{Name: "Section", Properties: []string{"header", "widgets"}},
		}},
		{API: "people", Schemas: []fieldsSchema{
			{Name: "Membership", Properties: []string{"contactGroupMembership"}},
		}},
	}}
}

func fieldsModelled() map[string]map[string]bool {
	return map[string]map[string]bool{
		"Message":    {"name": true, "text": true},
		"Membership": {"name": true, "role": true},
		"Section":    {"sortOrder": true},
	}
}

func TestFieldsProblems(t *testing.T) {
	// The record this repository's shape needs: one name two APIs share,
	// and one struct that only shares a name. Built by a function rather
	// than shared, because appending to a slice of a shared array
	// overwrites the next case's rows — which is what the first draft of
	// this table did, and it reported the corruption as a gate failure.
	ownerRow := fieldsRow{API: "chat", Schema: "Membership", Property: "*", Verdict: "owner", Reason: "Chat's", line: 1}
	unrelatedRow := fieldsRow{API: "chat", Schema: "Section", Property: "*", Verdict: "unrelated", Reason: "a sidebar section", line: 2}
	both := func(extra ...fieldsRow) []fieldsRow {
		return append([]fieldsRow{ownerRow, unrelatedRow}, extra...)
	}
	cases := []struct {
		name string
		rows []fieldsRow
		want string
	}{
		{"a matched set", both(), ""},
		{
			name: "a name two APIs publish, with nothing saying which is modelled",
			rows: []fieldsRow{unrelatedRow},
			want: "add an owner row saying which API it models",
		},
		{
			name: "a struct that only shares a name, with nothing saying so",
			rows: []fieldsRow{ownerRow},
			want: "Section.sortOrder is modelled and chat Section does not publish it",
		},
		{
			name: "unrelated claimed for one property rather than the schema",
			rows: []fieldsRow{ownerRow, {API: "chat", Schema: "Section", Property: "header",
				Verdict: "unrelated", Reason: "no", line: 3}},
			want: "is a verdict about a whole schema, so its property must be *",
		},
		{
			name: "an owner row for a name only one API publishes",
			rows: []fieldsRow{unrelatedRow, {API: "chat", Schema: "Message", Property: "*",
				Verdict: "owner", Reason: "mine", line: 3}},
			want: "claims ownership of a schema name only chat publishes",
		},
		{
			name: "a row for a schema Google has withdrawn",
			rows: both(fieldsRow{API: "chat", Schema: "Telepathy", Property: "x",
				Verdict: "out", Reason: "invented", line: 3}),
			want: "is not a published schema any more",
		},
		{
			name: "a verdict with no reason",
			rows: both(fieldsRow{API: "chat", Schema: "Message", Property: "text",
				Verdict: "out", line: 3}),
			want: "is out with no reason given",
		},
		{
			name: "a verdict that is none of the five",
			rows: both(fieldsRow{API: "chat", Schema: "Message", Property: "text",
				Verdict: "probably", Reason: "who knows", line: 3}),
			want: "is none of out, extra, alias, local, owner or unrelated",
		},
		{
			name: "the same exception judged twice",
			rows: both(fieldsRow{API: "chat", Schema: "Membership", Property: "*",
				Verdict: "owner", Reason: "Chat's", line: 9}),
			want: "already has a verdict on line 1",
		},
		{
			name: "an alias naming a struct that does not exist",
			rows: both(fieldsRow{API: "people", Schema: "Membership", Property: "*",
				Verdict: "alias", Reason: "Fictional", line: 3}),
			want: `aliased to "Fictional", which is not a struct`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems, _, _ := fieldsProblems(fieldsFixture(), tc.rows, fieldsModelled())
			joined := strings.Join(problems, "\n")
			switch {
			case tc.want == "" && len(problems) > 0:
				t.Errorf("wanted no problem, got:\n%s", joined)
			case tc.want != "" && !strings.Contains(joined, tc.want):
				t.Errorf("wanted a problem containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}

// A published field nobody has judged is the failure this gate exists
// for, and the one that has to fail loudest.
func TestFieldsProblemsCatchesAFieldGoogleAdds(t *testing.T) {
	snap := fieldsFixture()
	snap.APIs[0].Schemas[0].Properties = append(snap.APIs[0].Schemas[0].Properties, "somethingNew")
	rows := []fieldsRow{
		{API: "chat", Schema: "Membership", Property: "*", Verdict: "owner", Reason: "Chat's", line: 1},
		{API: "chat", Schema: "Section", Property: "*", Verdict: "unrelated", Reason: "a sidebar section", line: 2},
	}
	rows = rows[:len(rows):len(rows)] // no room to append into a shared array
	problems, _, _ := fieldsProblems(snap, rows, fieldsModelled())
	if !strings.Contains(strings.Join(problems, "\n"), "chat Message.somethingNew is published") {
		t.Errorf("a new published field was not reported: %v", problems)
	}
	// And writing it off with a reason settles it.
	rows = append(rows, fieldsRow{API: "chat", Schema: "Message", Property: "somethingNew",
		Verdict: "out", Reason: "no tool reads it", line: 3})
	if problems, _, out := fieldsProblems(snap, rows, fieldsModelled()); len(problems) > 0 || out != 1 {
		t.Errorf("an out row should settle it: %v (out = %d)", problems, out)
	}
}

// The half of the gate that reads Go rather than JSON. Promotion is the
// part with a rule in it: a field promoted from an embedded struct is on
// the wire exactly as if it had been declared.
func TestFieldsWireStructsResolvesEmbedding(t *testing.T) {
	dir := t.TempDir()
	src := "package gchat\n\n" +
		"type Common struct {\n\tName string `json:\"name,omitempty\"`\n}\n\n" +
		"type Message struct {\n\tCommon\n\tText string `json:\"text,omitempty\"`\n" +
		"\tHidden string `json:\"-\"`\n\tNoTag string\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types_test.go"),
		[]byte("package gchat\n\ntype Fake struct{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := fieldsWireStructs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["Fake"]; ok {
		t.Error("a _test.go file was read")
	}
	tags := got["Message"]
	if !tags["name"] || !tags["text"] {
		t.Errorf("Message should carry the promoted name and its own text: %v", tags)
	}
	if len(tags) != 2 {
		t.Errorf("a skipped tag and an untagged field are not wire fields: %v", tags)
	}
}

// The third direction: a struct the snapshot shares no name with is
// either aliased or written off, never silently skipped. Before it, a
// gate built on a name match went blind exactly when it mattered — a
// rename took a type out of the comparison and the gate said ok, because
// a floor of 20 against a real 49 could not notice.
func TestUnmatchedStructsMustBeAccountedFor(t *testing.T) {
	published := map[string][]string{"chat:Space": {"name"}}
	names := map[string][]string{"Space": {"chat:Space"}}
	modelled := map[string]map[string]bool{
		"Space":       {"name": true},
		"SetupSpace":  {"displayName": true},
		"ListOptions": {}, // no JSON tag anywhere: not a wire type
	}

	rec, problems := readDecisions(published, names, nil, modelled)
	if len(problems) > 0 {
		t.Fatalf("no rows should be no problems: %v", problems)
	}
	got := unmatchedStructs(published, rec, modelled)
	if len(got) != 1 || !strings.Contains(got[0], "SetupSpace") {
		t.Errorf("want SetupSpace reported and ListOptions skipped, got %v", got)
	}

	rows := []fieldsRow{{API: "-", Schema: "SetupSpace", Property: "*", Verdict: "local", Reason: "a request body", line: 1}}
	rec, problems = readDecisions(published, names, rows, modelled)
	if len(problems) > 0 {
		t.Fatalf("a valid local row is not a problem: %v", problems)
	}
	if got := unmatchedStructs(published, rec, modelled); len(got) != 0 {
		t.Errorf("want nothing left unaccounted for, got %v", got)
	}

	// A local row naming a published schema is a mistake, and a rejected
	// row must not still count as a decision.
	bad := []fieldsRow{{API: "-", Schema: "Space", Property: "*", Verdict: "local", Reason: "no", line: 1}}
	rec, problems = readDecisions(published, names, bad, modelled)
	if len(problems) != 1 || !strings.Contains(problems[0], "not local") {
		t.Errorf("want the local row refused, got %v", problems)
	}
	if len(rec.rows) != 0 {
		t.Errorf("a rejected row must not survive into the decisions: %+v", rec.rows)
	}
}
