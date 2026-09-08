package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// row spells one entry with tabs, so a fixture reads as columns rather
// than as a string full of escapes.
func row(fields ...string) string { return strings.Join(fields, "\t") + "\n" }

// key is the shorthand a fixture uses for a method.
func key(api, method string) methodKey { return methodKey{api: api, method: method} }

// The gate over the real record, the real snapshot and the real client.
// Everything else here drives synthetic input, and synthetic input is
// exactly what stopped agreeing with the repository the day the client
// changed.
func TestTheRecordTheSnapshotAndTheClientAgree(t *testing.T) {
	t.Chdir(repoRoot(t))
	var stdout, stderr bytes.Buffer
	if code := apiCoverage(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("api-coverage failed:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "api coverage ok") {
		t.Errorf("said nothing about what it read: %q", stdout.String())
	}
}

func TestReadCoverageRefusesAMalformedRecord(t *testing.T) {
	tests := []struct {
		name string
		rows string
		want string
	}{
		{
			name: "too few columns",
			rows: row("chat", "spaces.list", "used"),
			want: "want 4 tab-separated columns, got 3",
		},
		{
			name: "a verdict that is neither",
			rows: row("chat", "spaces.list", "maybe", "Client.ListSpaces"),
			want: `has verdict "maybe"`,
		},
		{
			name: "no reason",
			rows: row("chat", "spaces.list", "out", "   "),
			want: "has an empty reason column",
		},
		{
			name: "the same method twice",
			rows: row("chat", "spaces.list", "used", "Client.ListSpaces") +
				row("chat", "spaces.list", "out", "second thoughts"),
			want: "is already on line 1",
		},
		{
			name: "nothing at all",
			rows: "# every line a comment\n\n",
			want: "holds no rows",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, problems := readCoverage(write(t, "api-coverage.tsv", tt.rows))
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tt.want) }) {
				t.Errorf("want a problem containing %q, got %v", tt.want, problems)
			}
		})
	}
}

// Sort order is checked apart from parsing, so a row in the wrong place
// does not hide the findings that matter.
func TestUnsortedFindsTheRowOutOfPlace(t *testing.T) {
	rows := row("chat", "spaces.list", "used", "Client.ListSpaces") +
		row("chat", "spaces.get", "used", "Client.GetSpace")
	entries, problems := readCoverage(write(t, "api-coverage.tsv", rows))
	if len(problems) > 0 {
		t.Fatalf("sort order is not a parse problem: %v", problems)
	}
	got := unsorted(entries)
	if len(got) != 1 || !strings.Contains(got[0], "sorts before") {
		t.Errorf("want one out-of-order problem, got %v", got)
	}
}

// The same method name under two APIs is two rows, not a duplicate.
// Chat and People both have a `people` resource in reach, which is why
// the api is part of a row's identity.
func TestReadCoverageKeepsTheAPIsApart(t *testing.T) {
	rows := row("chat", "spaces.get", "used", "Client.GetSpace") +
		row("people", "spaces.get", "out", "a different API's method of the same name")
	entries, problems := readCoverage(write(t, "api-coverage.tsv", rows))
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
}

func TestReadCoverageSaysSoWhenThereIsNoFile(t *testing.T) {
	_, problems := readCoverage(filepath.Join(t.TempDir(), "absent.tsv"))
	if len(problems) != 1 || !strings.Contains(problems[0], "cannot read") {
		t.Errorf("want one unreadable-file problem, got %v", problems)
	}
}

func TestCheckCallsHoldsTheRecordAndTheClientToEachOther(t *testing.T) {
	tests := []struct {
		name  string
		rows  string
		calls []string
		want  string
	}{
		{
			name:  "a used row that names no client method",
			rows:  row("chat", "spaces.list", "used", "the spaces listing"),
			calls: []string{"ListSpaces"},
			want:  "must name the client method as Client.Something",
		},
		{
			name:  "a used row that names a method that is gone",
			rows:  row("chat", "spaces.list", "used", "Client.ListEverySpace"),
			calls: []string{"ListSpaces"},
			want:  "which is not a call on the client",
		},
		{
			name: "one method claimed twice",
			rows: row("chat", "spaces.list", "used", "Client.ListSpaces") +
				row("chat", "spaces.search", "used", "Client.ListSpaces"),
			calls: []string{"ListSpaces"},
			want:  "is claimed by both",
		},
		{
			name:  "a call no row claims",
			rows:  row("chat", "spaces.list", "used", "Client.ListSpaces"),
			calls: []string{"ListSpaces", "PinMessage"},
			want:  "Client.PinMessage calls Google and no row",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, problems := readCoverage(write(t, "api-coverage.tsv", tt.rows))
			if len(problems) > 0 {
				t.Fatalf("the fixture itself is malformed: %v", problems)
			}
			calls := map[string]bool{}
			for _, c := range tt.calls {
				calls[c] = true
			}
			got := checkCalls(entries, calls)
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tt.want) }) {
				t.Errorf("want a problem containing %q, got %v", tt.want, got)
			}
		})
	}
}

// An `out` row is a decision about a method nothing calls, so it must
// not claim a client method or leave one unclaimed.
func TestCheckCallsIgnoresOutRows(t *testing.T) {
	rows := row("chat", "spaces.completeImport", "out", "import mode is for an app") +
		row("chat", "spaces.list", "used", "Client.ListSpaces")
	entries, problems := readCoverage(write(t, "api-coverage.tsv", rows))
	if len(problems) > 0 {
		t.Fatalf("the fixture itself is malformed: %v", problems)
	}
	if got := checkCalls(entries, map[string]bool{"ListSpaces": true}); len(got) > 0 {
		t.Errorf("want no problems, got %v", got)
	}
}

func TestCheckPublishedHoldsTheRecordAndTheSnapshotToEachOther(t *testing.T) {
	published := map[methodKey]apiMethod{
		key("chat", "spaces.list"): {Verb: "GET", Path: "v1/spaces"},
		key("chat", "spaces.get"):  {Verb: "GET", Path: "v1/{+name}"},
	}
	contains := func(t *testing.T, got []string, want string) {
		t.Helper()
		if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("want a problem containing %q, got %v", want, got)
		}
	}

	t.Run("a method Google added", func(t *testing.T) {
		rows := row("chat", "spaces.list", "used", "Client.ListSpaces")
		entries, _ := readCoverage(write(t, "api-coverage.tsv", rows))
		contains(t, checkPublished(entries, published),
			"has chat spaces.get and testdata/api-coverage.tsv has no verdict on it")
	})
	t.Run("a verdict on a method that is gone", func(t *testing.T) {
		rows := row("chat", "spaces.get", "used", "Client.GetSpace") +
			row("chat", "spaces.list", "used", "Client.ListSpaces") +
			row("chat", "spaces.retired", "out", "kept to prove a point")
		entries, _ := readCoverage(write(t, "api-coverage.tsv", rows))
		contains(t, checkPublished(entries, published),
			"chat spaces.retired is not in testdata/api-methods.json")
	})
	// The OpenID endpoint has no discovery document, so its row is
	// neither unpublished nor missing a verdict.
	t.Run("a hand-listed API", func(t *testing.T) {
		rows := row("chat", "spaces.get", "used", "Client.GetSpace") +
			row("chat", "spaces.list", "used", "Client.ListSpaces") +
			row("openid", "userinfo", "used", "Client.Userinfo")
		entries, _ := readCoverage(write(t, "api-coverage.tsv", rows))
		if got := checkPublished(entries, published); len(got) > 0 {
			t.Errorf("want no problems, got %v", got)
		}
	})
}

func TestCheckAPIsClosesTheAPIColumn(t *testing.T) {
	// Every API the record really covers, so the only complaint is the
	// one each case introduces.
	all := row("chat", "spaces.list", "used", "Client.ListSpaces") +
		row("openid", "userinfo", "used", "Client.Userinfo") +
		row("people", "people.get", "used", "Client.GetPerson")

	t.Run("the record as it is", func(t *testing.T) {
		entries, _ := readCoverage(write(t, "api-coverage.tsv", all))
		if got := checkAPIs(entries); len(got) > 0 {
			t.Errorf("want no problems, got %v", got)
		}
	})
	t.Run("an API the list does not know", func(t *testing.T) {
		entries, _ := readCoverage(write(t, "api-coverage.tsv",
			all+row("zzz", "widgets.list", "out", "not a thing")))
		want := `API "zzz" is not in apis`
		if got := checkAPIs(entries); !slices.ContainsFunc(got,
			func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("want a problem containing %q, got %v", want, got)
		}
	})
	t.Run("an API in the list with no rows", func(t *testing.T) {
		entries, _ := readCoverage(write(t, "api-coverage.tsv",
			row("chat", "spaces.list", "used", "Client.ListSpaces")))
		got := checkAPIs(entries)
		for _, want := range []string{`apis names "openid"`, `apis names "people"`} {
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, want) }) {
				t.Errorf("want a problem containing %q, got %v", want, got)
			}
		}
	})
}

func TestDiffSnapshotsComparesNamesAndShape(t *testing.T) {
	old := snapshot{Fetched: "2026-09-01", APIs: map[string]map[string]apiMethod{
		"chat": {
			"spaces.get":     {Verb: "GET", Path: "v1/{+name}"},
			"spaces.list":    {Verb: "GET", Path: "v1/spaces"},
			"spaces.retired": {Verb: "GET", Path: "v1/spaces:retired"},
		},
	}}
	fresh := snapshot{Fetched: "2026-09-07", APIs: map[string]map[string]apiMethod{
		"chat": {
			"spaces.get":  {Verb: "GET", Path: "v1/{+name}"},
			"spaces.list": {Verb: "POST", Path: "v1/spaces"},
			"spaces.new":  {Verb: "GET", Path: "v1/spaces:new"},
		},
	}}
	want := []string{
		"CHANGED: chat spaces.list is POST v1/spaces and was GET v1/spaces",
		"GONE: chat spaces.retired",
		"NEW: chat spaces.new (GET v1/spaces:new)",
	}
	if got := diffSnapshots(old, fresh); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A snapshot survives the round trip to disk, so what api-diff compares
// next time is what it wrote.
func TestSnapshotRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-methods.json")
	want := snapshot{Fetched: "2026-09-07", APIs: map[string]map[string]apiMethod{
		"chat": {"spaces.list": {Verb: "GET", Path: "v1/spaces"}},
	}}
	if err := writeSnapshot(path, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Errorf("want a trailing newline so a diff reads as lines, got %q", string(raw))
	}
	got, err := readSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if changes := diffSnapshots(want, got); len(changes) > 0 {
		t.Errorf("the round trip changed it: %v", changes)
	}
}

func TestCollectMethodsWalksSubResources(t *testing.T) {
	const doc = `{
	  "resources": {
	    "spaces": {
	      "methods": {"list": {"httpMethod": "GET", "path": "v1/spaces"}},
	      "resources": {
	        "messages": {
	          "resources": {
	            "reactions": {
	              "methods": {"create": {"httpMethod": "POST", "path": "v1/{+parent}/reactions"}}
	            }
	          }
	        }
	      }
	    }
	  }
	}`
	var res resource
	if err := json.Unmarshal([]byte(doc), &res); err != nil {
		t.Fatal(err)
	}
	out := map[string]apiMethod{}
	collectMethods("", res, out)
	want := map[string]apiMethod{
		"spaces.list":                      {Verb: "GET", Path: "v1/spaces"},
		"spaces.messages.reactions.create": {Verb: "POST", Path: "v1/{+parent}/reactions"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v, want %v", out, want)
	}
}

// The gate decides what a call is by its signature rather than from a
// list of names. This is the other half of that rule: nothing on the
// client falls outside it except the two methods that report what the
// client has seen, which is what internal/gchat's own
// TestEveryCallNamesItsScope holds from its side.
func TestClientCallsIsEveryMethodButTheTwoThatReport(t *testing.T) {
	calls := clientCalls()
	if len(calls) == 0 {
		t.Fatal("no calls found: this test is looking at nothing")
	}
	notCalls := map[string]bool{"DriftCount": true, "DriftPaths": true}
	client := reflect.TypeOf((*gchat.Client)(nil))
	for i := range client.NumMethod() {
		name := client.Method(i).Name
		if calls[name] == notCalls[name] {
			t.Errorf("%s reads as %v by the signature rule and %v by name; it must be exactly one",
				name, calls[name], notCalls[name])
		}
	}
	if want := client.NumMethod() - len(notCalls); len(calls) != want {
		t.Errorf("found %d calls, want %d", len(calls), want)
	}
}

// A method that takes a context and returns an error is a call whatever
// it is named, and one that does neither is not.
func TestClientCallsReadsTheSignature(t *testing.T) {
	calls := clientCalls()
	if !calls["SendMessage"] {
		t.Errorf("SendMessage takes a %s and returns an error, so it is a call",
			reflect.TypeOf((*context.Context)(nil)).Elem())
	}
	if calls["DriftCount"] {
		t.Error("DriftCount takes no context and returns no error, so it is not a call")
	}
}

// The floors are the checks that catch this gate reading nothing, and a
// floor nothing exercises is the failure it exists to prevent. Raised by
// the google-sheets-mcp session, whose own gate has a test per failing
// path: writing them is what found that its wildcard could absorb a
// method the client really called.
//
// One floor is missing from this table on purpose. `no calls on
// gchat.Client` cannot be reached without faking reflection over a
// compiled type, so it is asserted the other way round, by
// TestClientCallsIsEveryMethodButTheTwoThatReport — if that set ever
// empties, that test fails first.
func TestAPICoverageRefusesInputItCannotLearnFrom(t *testing.T) {
	tests := []struct {
		name     string
		record   string
		snapshot string
		want     string
	}{
		{
			name:     "a snapshot with no methods in it",
			record:   row("chat", "spaces.list", "used", "Client.ListSpaces"),
			snapshot: `{"fetched":"2026-09-07","apis":{}}`,
			want:     "lists no methods: this gate is looking at nothing",
		},
		{
			name:     "a snapshot whose APIs are all empty",
			record:   row("chat", "spaces.list", "used", "Client.ListSpaces"),
			snapshot: `{"fetched":"2026-09-07","apis":{"chat":{},"people":{}}}`,
			want:     "lists no methods: this gate is looking at nothing",
		},
		{
			name:     "a record with no rows in it",
			record:   "# every line a comment\n",
			snapshot: `{"fetched":"2026-09-07","apis":{"chat":{"spaces.list":{"verb":"GET","path":"v1/spaces"}}}}`,
			want:     "holds no rows: this gate is looking at nothing",
		},
		{
			name:     "a snapshot that is not JSON",
			record:   row("chat", "spaces.list", "used", "Client.ListSpaces"),
			snapshot: "not json at all",
			want:     snapshotFile + ":",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.MkdirAll("testdata", 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverageFile, []byte(tt.record), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(snapshotFile, []byte(tt.snapshot), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := apiCoverage(nil, &stdout, &stderr); code == 0 {
				t.Fatalf("passed on input it cannot learn from:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("want %q, got %q", tt.want, stderr.String())
			}
		})
	}
}

func TestAPICoverageRefusesASnapshotItCannotRead(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("testdata", 0o750); err != nil {
		t.Fatal(err)
	}
	rows := row("chat", "spaces.list", "used", "Client.ListSpaces")
	if err := os.WriteFile(coverageFile, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := apiCoverage(nil, &stdout, &stderr); code == 0 {
		t.Fatalf("passed with no snapshot to read:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "cannot read "+snapshotFile) {
		t.Errorf("want the missing snapshot named, got %q", stderr.String())
	}
}

// The property the shared standard asks of any refetch target: when the
// network fails it fails loudly and leaves the committed file untouched.
// A half-written snapshot is worse than a stale one, because the next
// `check` then holds the record to something that is neither the old
// truth nor the new one.
//
// The closed port is bound and released rather than guessed, so the
// test cannot pass because something happened to answer on it.
func TestARefetchThatCannotReachTheAPIWritesNothing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + listener.Addr().String() + "/discovery"
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "api-methods.json")
	before := snapshot{Fetched: "2026-09-01", APIs: map[string]map[string]apiMethod{
		"chat": {"spaces.list": {Verb: "GET", Path: "v1/spaces"}},
	}}
	if err := writeSnapshot(path, before); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := apiDiffWith(map[string]apiSource{"chat": {discovery: dead}}, path, &stdout, &stderr)
	if code == 0 {
		t.Errorf("a refetch that reached nothing reported success:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "fetch "+dead) {
		t.Errorf("want the unreachable URL named, got %q", stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Errorf("the snapshot was rewritten by a failed refetch:\nbefore %s\nafter  %s", original, after)
	}
}

// A method Google publishes with scopes, for the reachability table.
func published(scopes ...string) apiMethod {
	return apiMethod{Verb: "POST", Path: "v1/things", Scopes: scopes}
}

const (
	// One scope this server asks for, and one it does not.
	scopeAsked  = "https://www.googleapis.com/auth/chat.spaces"
	scopeUnheld = "https://www.googleapis.com/auth/contacts"
)

func TestCheckReachableDerivesTheVerdictRatherThanTrustingIt(t *testing.T) {
	tests := []struct {
		name   string
		row    string
		method apiMethod
		want   string
	}{
		{
			name:   "arguing in prose for something that was never a choice",
			row:    row("chat", "spaces.list", "out", "we would rather not"),
			method: published(scopeUnheld),
			want:   "no scope this server asks for authorises it",
		},
		{
			name:   "calling something no scope authorises",
			row:    row("chat", "spaces.list", "used", "Client.ListSpaces"),
			method: published(scopeUnheld),
			want:   "no scope this server asks for authorises it",
		},
		{
			name:   "out of scope when it is in reach",
			row:    row("chat", "spaces.list", "unreachable", scopeAsked),
			method: published(scopeAsked),
			want:   "which Google accepts for it. Judge it instead",
		},
		{
			name:   "naming a scope Google does not accept for it",
			row:    row("chat", "spaces.list", "unreachable", "https://www.googleapis.com/auth/chat.bot"),
			method: published(scopeUnheld),
			want:   "must name one scope Google accepts for it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, problems := readCoverage(write(t, "api-coverage.tsv", tt.row))
			if len(problems) > 0 {
				t.Fatalf("the fixture itself is malformed: %v", problems)
			}
			got := checkReachable(entries, map[methodKey]apiMethod{key("chat", "spaces.list"): tt.method})
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tt.want) }) {
				t.Errorf("want a problem containing %q, got %v", tt.want, got)
			}
		})
	}
}

// The row that is right stays quiet, or the table above proves nothing.
func TestCheckReachableAcceptsAVerdictThatMatches(t *testing.T) {
	rows := row("chat", "spaces.get", "unreachable", scopeUnheld) +
		row("chat", "spaces.list", "used", "Client.ListSpaces")
	entries, _ := readCoverage(write(t, "api-coverage.tsv", rows))
	got := checkReachable(entries, map[methodKey]apiMethod{
		key("chat", "spaces.get"):  published(scopeUnheld),
		key("chat", "spaces.list"): published(scopeAsked),
	})
	if len(got) > 0 {
		t.Errorf("want no problems, got %v", got)
	}
}

// The failure this check was built for: a row naming the wrong member of
// a family, which every other rule here passes.
func TestCheckBindingCatchesARowNamingTheWrongMethod(t *testing.T) {
	shapes := map[string]requestShape{
		"MarkActive":  {httpVerb: "POST", apiVerb: "markAsActive"},
		"MarkAway":    {httpVerb: "POST", apiVerb: "markAsAway"},
		"GetSpace":    {httpVerb: "GET"},
		"DeleteSpace": {httpVerb: "DELETE"},
		"Delegating":  {delegate: "GetSpace"},
	}
	active := apiMethod{Verb: "POST", Path: "v1/{+name}:markAsActive"}
	get := apiMethod{Verb: "GET", Path: "v1/{+name}"}

	tests := []struct {
		name   string
		method string
		claims string
		api    apiMethod
		want   string
	}{
		{
			name:   "the wrong member of a family",
			method: "users.availability.markAsActive",
			claims: "Client.MarkAway",
			api:    active,
			want:   "is :markAsActive and Client.MarkAway builds :markAsAway",
		},
		{
			name:   "the wrong HTTP verb",
			method: "spaces.get",
			claims: "Client.DeleteSpace",
			api:    get,
			want:   "is GET and Client.DeleteSpace sends DELETE",
		},
		{
			name:   "a custom method bound to a plain path",
			method: "users.availability.markAsActive",
			claims: "Client.GetSpace",
			api:    active,
			want:   "builds a plain resource path",
		},
		{
			name:   "a method that builds no request this can read",
			method: "spaces.get",
			claims: "Client.Missing",
			api:    get,
			want:   "no request this gate can read is built there",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, problems := readCoverage(write(t, "api-coverage.tsv",
				row("chat", tt.method, "used", tt.claims)))
			if len(problems) > 0 {
				t.Fatalf("the fixture itself is malformed: %v", problems)
			}
			got := checkBinding(entries,
				map[methodKey]apiMethod{key("chat", tt.method): tt.api}, shapes)
			if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tt.want) }) {
				t.Errorf("want a problem containing %q, got %v", tt.want, got)
			}
		})
	}
}

// And the rows that are right stay quiet.
func TestCheckBindingAcceptsARowThatMatches(t *testing.T) {
	shapes := map[string]requestShape{
		"MarkActive": {httpVerb: "POST", apiVerb: "markAsActive"},
		"GetSpace":   {httpVerb: "GET"},
	}
	rows := row("chat", "spaces.get", "used", "Client.GetSpace") +
		row("chat", "users.availability.markAsActive", "used", "Client.MarkActive")
	entries, _ := readCoverage(write(t, "api-coverage.tsv", rows))
	got := checkBinding(entries, map[methodKey]apiMethod{
		key("chat", "spaces.get"):                      {Verb: "GET", Path: "v1/{+name}"},
		key("chat", "users.availability.markAsActive"): {Verb: "POST", Path: "v1/{+name}:markAsActive"},
	}, shapes)
	if len(got) > 0 {
		t.Errorf("want no problems, got %v", got)
	}
}

// Delegation is followed, because six deletes share one helper and
// reading only each method's own body would leave all six unbound.
func TestResolveFollowsDelegation(t *testing.T) {
	shapes := map[string]requestShape{
		"DeleteReaction": {delegate: "deleteName"},
		"deleteName":     {httpVerb: "DELETE"},
		"Circular":       {delegate: "AlsoCircular"},
		"AlsoCircular":   {delegate: "Circular"},
	}
	if got := resolve(shapes, "DeleteReaction"); got.httpVerb != "DELETE" {
		t.Errorf("want DELETE through the helper, got %+v", got)
	}
	if got := resolve(shapes, "Circular"); got.httpVerb != "" {
		t.Errorf("a cycle must end, got %+v", got)
	}
	if got := resolve(shapes, "Absent"); got.httpVerb != "" {
		t.Errorf("an unknown method has no shape, got %+v", got)
	}
}

// The real client, read the way the gate reads it: every method a `used`
// row names has to yield a verb, or the binding check is passing on
// methods it cannot see.
func TestClientRequestsReadsTheRealClient(t *testing.T) {
	t.Chdir(repoRoot(t))
	shapes, err := clientRequests(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	for name := range clientCalls() {
		if resolve(shapes, name).httpVerb == "" {
			t.Errorf("%s.%s builds no request this gate can read", clientType, name)
		}
	}
}
