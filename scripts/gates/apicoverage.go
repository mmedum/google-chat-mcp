package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// The API-coverage gate holds the gap between Google's APIs and this
// server to being a set of decisions rather than an accident.
//
// The Chat API is bigger than the tool surface, and it grows without
// notice — the same way it grows response fields, which is what the
// drift reporter watches. A method Google adds is invisible to every
// other gate here: the schema diff compares this server with its own
// last release, and the live driver's surface gate compares the driver
// with this server. Nothing else looks outward.
//
// Two files, and the split is which of them a person writes:
//
//   - testdata/api-methods.json is the snapshot: every method of every
//     API this server can reach, with its verb, path and the scopes
//     Google accepts for it, as published on the day it was fetched.
//     `gates api-diff` writes it. Nobody edits it by hand.
//   - testdata/api-coverage.tsv is the record: one verdict per method.
//     `used` names the gchat.Client method that implements it, `out`
//     gives the reason it is deliberately not called, and `unreachable`
//     names a scope this server does not ask for.
//
// `gates api-coverage` holds the two to each other and to the client,
// offline, in `make check`:
//
//  1. every method in the snapshot has a verdict, so a method Google
//     added fails the build until somebody judges it;
//  2. every row is a method the snapshot has, so a verdict cannot
//     outlive the method it was about;
//  3. every `used` row names a method that really exists on
//     gchat.Client, and no two rows name the same one, so deleting a
//     client method breaks the record rather than quietly making it a
//     lie;
//  4. every call on gchat.Client is claimed by exactly one row, so a new
//     call cannot be added without saying which API method it is;
//  5. every `out` row carries a reason, because "not used" without one
//     is not a decision;
//  6. the rows are sorted and unique, so the record diffs cleanly and a
//     second verdict on one method cannot hide under the first.
//
// The first version of this gate had the verb and path as hand-typed
// columns in the record and checked them only from `api-diff`, which
// needs the network and therefore never ran in CI. That put machine-
// owned data in a hand-edited file and left the completeness claim —
// the whole point of the gate — resting on a target somebody remembers.
// The snapshot is that data, generated, in the shape
// testdata/schemas-baseline.json already uses here.
//
//  7. every `used` row is bound to the method it names — the HTTP verb
//     and the custom-method suffix in the client's request literal must
//     be the ones Google publishes for that method;
//  8. every `unreachable` row is derived rather than argued: no scope
//     this server asks for authorises the method, and the reason names
//     one Google does accept.
//
// Rules 7 and 8 were written down as limits before they were built.
// Without 7, a `used` row was held only to naming an existing,
// unclaimed method, so swapping the reasons on
// `users.availability.markAsActive` and `markAsAway` left every gate
// green over a record naming the wrong method twice. Without 8,
// seventeen rows argued in prose that a method needs a scope this server
// does not ask for — a fact about two lists that both already exist, and
// one that stops being true the moment `scopes.All` grows. Adding
// `contacts` to it now fails thirteen rows instead of quietly falsifying
// thirteen sentences.
const (
	coverageFile = "testdata/api-coverage.tsv"
	snapshotFile = "testdata/api-methods.json"
)

// coverageColumns is the row shape: api, method, verdict, reason.
//
// google-drive-mcp's record has five columns and folds the API into the
// method name as a prefix, which spells the People API's own `people.get`
// as `people.people.get`. A column says the same thing without mangling
// a name that has to match a discovery document exactly.
const coverageColumns = 4

// methodKey addresses one method: an API and a name, because the two
// Google APIs here both have a `people` resource in reach.
type methodKey struct {
	api    string
	method string
}

func (k methodKey) String() string { return k.api + " " + k.method }

// coverageEntry is one row of the record.
type coverageEntry struct {
	key     methodKey
	verdict string
	reason  string
	line    int
}

// The verdicts a row may carry.
//
// `unreachable` is derived rather than argued: no scope this server asks
// for authorises the method, so leaving it out was never a choice. It is
// separate from `out` because the two rot differently — an `out` reason
// is a judgement that stays true until somebody revisits it, while an
// `unreachable` one stops being true the moment `scopes.All` grows, and
// then seventeen rows say something false with nothing objecting.
const (
	verdictUsed        = "used"
	verdictOut         = "out"
	verdictUnreachable = "unreachable"
)

// clientDir holds the client whose request literals bind a row to a
// method.
const clientDir = "internal/gchat"

// clientType is the type whose calls the record has to account for.
const clientType = "Client"

// apiSource is where one API's method list comes from: a discovery
// document, or the reason there is none.
//
// One field set, never both. Two maps would allow an API to be in both,
// which is a state with no meaning and a check to go with it.
type apiSource struct {
	// discovery is the document api-diff fetches.
	discovery string
	// handListed is why this API has no document, and is set instead.
	handListed string
}

// apis is every API this server can reach.
var apis = map[string]apiSource{
	"chat":   {discovery: "https://chat.googleapis.com/$discovery/rest?version=v1"},
	"people": {discovery: "https://people.googleapis.com/$discovery/rest?version=v1"},
	"openid": {handListed: "one OpenID Connect endpoint, described by a provider " +
		"configuration rather than a discovery document"},
}

// apiMethod is one method as a discovery document describes it.
type apiMethod struct {
	Verb string `json:"verb"`
	Path string `json:"path"`
	// Scopes is every OAuth scope Google accepts for this method. It is
	// what makes the `unreachable` verdict derivable rather than prose.
	Scopes []string `json:"scopes,omitempty"`
}

// reachable reports whether any scope this server asks for authorises
// this method.
//
// A plain intersection, not scopes.Satisfied: the question is whether
// login could ever obtain the consent, and login asks for exactly
// scopes.All. An umbrella that Google does not list for a method is not
// a scope it will accept.
func (m apiMethod) reachable() bool {
	for _, s := range m.Scopes {
		if slices.Contains(scopes.All, s) {
			return true
		}
	}
	return false
}

// snapshot is testdata/api-methods.json: what the APIs published, and
// when. The date is printed rather than checked — a gate that fails
// because a file is old fails on a day nothing changed, and that is how
// a gate gets switched off.
type snapshot struct {
	Fetched string                          `json:"fetched"`
	APIs    map[string]map[string]apiMethod `json:"apis"`
}

// methods is the snapshot flattened to one set of keys.
func (s snapshot) methods() map[methodKey]apiMethod {
	out := make(map[methodKey]apiMethod)
	for api, byName := range s.APIs {
		for name, m := range byName {
			out[methodKey{api: api, method: name}] = m
		}
	}
	return out
}

// reportProblems prints findings the way every gate here does, and
// returns the exit code that goes with having any.
func reportProblems(w io.Writer, problems []string) int {
	for _, p := range problems {
		_, _ = fmt.Fprintln(w, "gates: "+p)
	}
	return 1
}

// apiCoverage checks the record against the snapshot and the client.
func apiCoverage(_ []string, stdout, stderr io.Writer) int {
	// A row that would not parse is the one failure that stops the rest:
	// the entries are then an unknown fraction of the record, and every
	// method in the missing part would be reported as unjudged. Every
	// other check runs together, so a contributor sees the whole list
	// once rather than a sort complaint, a fix, and then the real one.
	entries, problems := readCoverage(coverageFile)
	if len(problems) > 0 {
		return reportProblems(stderr, problems)
	}
	snap, err := readSnapshot(snapshotFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	published := snap.methods()
	// Zero of either and zero findings are the same output, and one of
	// them is a gate reading the wrong thing.
	if len(published) == 0 {
		_, _ = fmt.Fprintf(stderr, "gates: %s lists no methods: this gate is looking at nothing\n", snapshotFile)
		return 1
	}
	calls := clientCalls()
	if len(calls) == 0 {
		_, _ = fmt.Fprintf(stderr, "gates: no calls on gchat.%s: this gate is looking at nothing\n", clientType)
		return 1
	}

	problems = append(problems, unsorted(entries)...)
	problems = append(problems, checkAPIs(entries)...)
	problems = append(problems, checkPublished(entries, published)...)
	problems = append(problems, checkReachable(entries, published)...)
	problems = append(problems, checkCalls(entries, calls)...)
	shapes, err := clientRequests(clientDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	problems = append(problems, checkBinding(entries, published, shapes)...)
	if len(problems) > 0 {
		return reportProblems(stderr, problems)
	}

	count := map[string]int{}
	for _, e := range entries {
		count[e.verdict]++
	}
	_, _ = fmt.Fprintf(stdout, "api coverage ok (%d methods across %d APIs: %d used, %d deliberately out, "+
		"%d out of scope; snapshot fetched %s)\n", len(entries), len(apis),
		count[verdictUsed], count[verdictOut], count[verdictUnreachable], snap.Fetched)
	return 0
}

// readCoverage parses the record. The problems it reports are the ones
// that make a row unreadable; what the rows say is checked elsewhere.
func readCoverage(path string) ([]coverageEntry, []string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, []string{"cannot read " + path + ": " + err.Error()}
	}
	defer func() { _ = f.Close() }()

	var entries []coverageEntry
	var problems []string
	seen := map[methodKey]int{}
	fail := func(line int, format string, a ...any) {
		problems = append(problems, fmt.Sprintf("%s:%d: %s", path, line, fmt.Sprintf(format, a...)))
	}

	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		// Only the carriage return comes off, for a checkout on Windows.
		// Trimming trailing whitespace as well would take an empty last
		// column with it, and "want 4 columns, got 3" is the wrong thing
		// to say about a row whose reason is blank.
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != coverageColumns {
			fail(n, "want %d tab-separated columns, got %d", coverageColumns, len(fields))
			continue
		}
		e := coverageEntry{
			key:     methodKey{api: fields[0], method: fields[1]},
			verdict: fields[2],
			reason:  fields[3],
			line:    n,
		}
		if first, ok := seen[e.key]; ok {
			fail(n, "%s is already on line %d", e.key, first)
			continue
		}
		seen[e.key] = n
		switch e.verdict {
		case verdictUsed, verdictOut, verdictUnreachable:
		default:
			fail(n, "%s has verdict %q; want %s, %s or %s",
				e.key, e.verdict, verdictUsed, verdictOut, verdictUnreachable)
		}
		for _, empty := range emptyFields(e) {
			fail(n, "%s has an empty %s column", e.key, empty)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, "reading "+path+": "+err.Error())
	}
	if len(entries) == 0 && len(problems) == 0 {
		problems = append(problems, path+" holds no rows: this gate is looking at nothing")
	}
	return entries, problems
}

// emptyFields names the columns of a row that were left blank. A blank
// reason is the one that matters — "not used" without one is not a
// decision — but a blank anything is a row somebody half-wrote.
func emptyFields(e coverageEntry) []string {
	var out []string
	for _, f := range []struct{ name, value string }{
		{"api", e.key.api}, {"method", e.key.method}, {"reason", e.reason},
	} {
		if strings.TrimSpace(f.value) == "" {
			out = append(out, f.name)
		}
	}
	return out
}

// unsorted reports rows out of order. The record is read by people and
// diffed by machines, and both want a new method to land where it
// belongs rather than at the bottom.
func unsorted(entries []coverageEntry) []string {
	var problems []string
	for i := 1; i < len(entries); i++ {
		before, after := entries[i-1], entries[i]
		if before.key.api > after.key.api ||
			(before.key.api == after.key.api && before.key.method > after.key.method) {
			problems = append(problems, fmt.Sprintf("%s:%d: %s sorts before %s on line %d; "+
				"the record is sorted by API and method",
				coverageFile, after.line, after.key, before.key, before.line))
		}
	}
	return problems
}

// checkAPIs holds the api column and the list of APIs to each other.
//
// Both directions, as everywhere here: a row naming an API the list does
// not know is a method nothing can ever fetch, and an API in the list
// with no rows is a rule about nothing.
func checkAPIs(entries []coverageEntry) []string {
	var problems []string
	present := map[string]bool{}
	for _, e := range entries {
		present[e.key.api] = true
		if _, ok := apis[e.key.api]; !ok {
			problems = append(problems, fmt.Sprintf("%s:%d: API %q is not in apis, so nothing knows "+
				"where its methods come from", coverageFile, e.line, e.key.api))
		}
	}
	for _, api := range slices.Sorted(maps.Keys(apis)) {
		if !present[api] {
			problems = append(problems, fmt.Sprintf("apis names %q, which has no rows in %s; drop it",
				api, coverageFile))
		}
	}
	return problems
}

// checkPublished compares the record with the snapshot in both
// directions. A hand-listed API has no snapshot to compare against, so
// its rows are skipped rather than reported as methods Google dropped.
func checkPublished(entries []coverageEntry, published map[methodKey]apiMethod) []string {
	var problems []string
	judged := map[methodKey]bool{}
	for _, e := range entries {
		if apis[e.key.api].handListed != "" {
			continue
		}
		judged[e.key] = true
		if _, ok := published[e.key]; !ok {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is not in %s. The method is gone, or the "+
				"snapshot is behind — run `make api-diff`", coverageFile, e.line, e.key, snapshotFile))
		}
	}
	var unjudged []string
	for key := range published {
		if !judged[key] {
			unjudged = append(unjudged, key.String())
		}
	}
	slices.Sort(unjudged)
	for _, key := range unjudged {
		problems = append(problems, fmt.Sprintf("%s has %s and %s has no verdict on it. Add a row "+
			"saying which client method uses it, or why it is deliberately not used",
			snapshotFile, key, coverageFile))
	}
	return problems
}

// checkReachable holds the `unreachable` verdict to what Google
// publishes, in both directions.
//
// Seventeen rows used to argue in prose that a method needs a scope this
// server does not ask for. That is not a judgement — it is a fact about
// two lists that both already exist, and prose has no way to notice when
// one of them changes. Add `contacts` to scopes.All and every one of
// those sentences becomes false with nothing objecting; now they fail
// instead, which is the moment somebody should be deciding what to do
// with the methods that just came into reach.
//
// The reason on an `unreachable` row names the scope, and the gate holds
// that too: a scope Google does not list for the method is a reason that
// sounds checked and is not.
func checkReachable(entries []coverageEntry, published map[methodKey]apiMethod) []string {
	var problems []string
	for _, e := range entries {
		m, ok := published[e.key]
		if !ok {
			continue // a hand-listed API, or a row checkPublished already reported
		}
		switch {
		case e.verdict == verdictUnreachable && m.reachable():
			problems = append(problems, fmt.Sprintf("%s:%d: %s is marked %s, but this server asks "+
				"for %s, which Google accepts for it. Judge it instead: %s, or %s with the reason",
				coverageFile, e.line, e.key, verdictUnreachable, reachableScope(m), verdictUsed, verdictOut))
		case e.verdict != verdictUnreachable && !m.reachable():
			problems = append(problems, fmt.Sprintf("%s:%d: %s is marked %s, and no scope this server "+
				"asks for authorises it — Google accepts only %s. Mark it %s; the reason it was left "+
				"out is not a choice anybody made", coverageFile, e.line, e.key, e.verdict,
				strings.Join(m.Scopes, " "), verdictUnreachable))
		case e.verdict == verdictUnreachable && !slices.Contains(m.Scopes, e.reason):
			problems = append(problems, fmt.Sprintf("%s:%d: %s is %s, so its reason must name one scope "+
				"Google accepts for it — %s. Got %q", coverageFile, e.line, e.key, verdictUnreachable,
				strings.Join(m.Scopes, " "), e.reason))
		}
	}
	return problems
}

// reachableScope is a scope this server asks for that Google accepts for
// this method, for the message that says the verdict is wrong.
func reachableScope(m apiMethod) string {
	for _, s := range m.Scopes {
		if slices.Contains(scopes.All, s) {
			return s
		}
	}
	return ""
}

// checkCalls compares the record with the client in both directions.
func checkCalls(entries []coverageEntry, calls map[string]bool) []string {
	var problems []string
	claimed := map[string]methodKey{}
	for _, e := range entries {
		if e.verdict != verdictUsed {
			continue
		}
		name, ok := strings.CutPrefix(e.reason, clientType+".")
		if !ok {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is used, so its reason must name the "+
				"client method as %s.Something; got %q", coverageFile, e.line, e.key, clientType, e.reason))
			continue
		}
		if !calls[name] {
			problems = append(problems, fmt.Sprintf("%s:%d: %s names %s.%s, which is not a call on the client",
				coverageFile, e.line, e.key, clientType, name))
			continue
		}
		if first, ok := claimed[name]; ok {
			problems = append(problems, fmt.Sprintf("%s:%d: %s.%s is claimed by both %s and %s",
				coverageFile, e.line, clientType, name, first, e.key))
			continue
		}
		claimed[name] = e.key
	}

	var unclaimed []string
	for name := range calls {
		if _, ok := claimed[name]; !ok {
			unclaimed = append(unclaimed, name)
		}
	}
	slices.Sort(unclaimed)
	for _, name := range unclaimed {
		problems = append(problems, fmt.Sprintf("%s.%s calls Google and no row in %s claims it. Add the "+
			"API method it implements", clientType, name, coverageFile))
	}
	return problems
}

// clientCalls is every method on gchat.Client that reaches Google.
//
// Read by rule rather than by name: a call takes a context first and
// returns an error last, and `DriftCount` and `DriftPaths` do neither.
// Naming the two exceptions here instead would be a second list to keep,
// and one that goes stale in the direction that matters — a method that
// stopped calling Google would keep its excuse. The rule cannot: a
// method that still looks like a call still needs a row.
//
// The rule is safe to lean on because `internal/gchat`'s own
// TestEveryCallNamesItsScope refuses any method that is neither a call
// by this same rule nor one of those two, so nothing can reach Google
// from a signature this does not recognise.
func clientCalls() map[string]bool {
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	errType := reflect.TypeOf((*error)(nil)).Elem()
	client := reflect.TypeOf((*gchat.Client)(nil))

	out := map[string]bool{}
	for i := range client.NumMethod() {
		m := client.Method(i)
		// In(0) is the receiver, because this is a method read off a
		// type rather than off a value.
		sig := m.Type
		if sig.NumIn() < 2 || sig.In(1) != ctxType ||
			sig.NumOut() == 0 || sig.Out(sig.NumOut()-1) != errType {
			continue
		}
		out[m.Name] = true
	}
	return out
}

// readSnapshot loads what the APIs published.
func readSnapshot(path string) (snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return snapshot{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// apiDiff refetches the discovery documents, rewrites the snapshot and
// reports what changed.
//
// It reaches the network, so it is a target somebody runs rather than a
// gate CI depends on: a gate that fails when Google is slow is one
// people learn to rerun until it passes. What CI gets instead is the
// file this writes. CONTRIBUTING's release checklist says to run it,
// because a manual target nobody runs is a gate that never fires.
func apiDiff(_ []string, stdout, stderr io.Writer) int {
	return apiDiffWith(apis, snapshotFile, stdout, stderr)
}

// apiDiffWith is apiDiff over a given set of APIs and a given file, so
// that the property that matters can be tested: on a network failure it
// writes nothing. A refresh target that half-writes is worse than one
// nobody runs, because the next `check` then holds the record to a
// snapshot that is neither the old truth nor the new one.
func apiDiffWith(sources map[string]apiSource, snapPath string, stdout, stderr io.Writer) int {
	old, err := readSnapshot(snapPath)
	if err != nil {
		// A first run has no snapshot to compare with, and that is not a
		// failure: everything is new.
		_, _ = fmt.Fprintf(stdout, "%v; fetching a first snapshot\n", err)
	}
	fresh, err := fetchSnapshot(sources)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	changes := diffSnapshots(old, fresh)
	if err := writeSnapshot(snapPath, fresh); err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}
	if len(changes) == 0 {
		_, _ = fmt.Fprintf(stdout, "api diff ok (%d methods, unchanged)\n", len(fresh.methods()))
		return 0
	}
	for _, c := range changes {
		_, _ = fmt.Fprintln(stdout, c)
	}
	_, _ = fmt.Fprintf(stdout, "\n%s rewritten. Give every new method a row in %s, then run "+
		"`make api-coverage`.\n", snapPath, coverageFile)
	return 1
}

// diffSnapshots reports how the APIs changed, verb and path included: a
// method that keeps its name and moves is a break the name alone would
// not show.
func diffSnapshots(old, fresh snapshot) []string {
	before, after := old.methods(), fresh.methods()
	var out []string
	for key, m := range after {
		was, ok := before[key]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("NEW: %s (%s %s)", key, m.Verb, m.Path))
		case was.Verb != m.Verb || was.Path != m.Path:
			out = append(out, fmt.Sprintf("CHANGED: %s is %s %s and was %s %s",
				key, m.Verb, m.Path, was.Verb, was.Path))
		case !slices.Equal(was.Scopes, m.Scopes):
			out = append(out, fmt.Sprintf("SCOPES: %s now takes %s and took %s",
				key, strings.Join(m.Scopes, " "), strings.Join(was.Scopes, " ")))
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			out = append(out, fmt.Sprintf("GONE: %s", key))
		}
	}
	slices.Sort(out)
	return out
}

// writeSnapshot puts the snapshot on disk, indented and newline
// terminated, so a change to it reads as a diff rather than as one line.
func writeSnapshot(path string, s snapshot) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// fetchSnapshot reads every fetchable API's method list.
func fetchSnapshot(sources map[string]apiSource) (snapshot, error) {
	out := snapshot{Fetched: time.Now().UTC().Format(time.DateOnly), APIs: map[string]map[string]apiMethod{}}
	client := &http.Client{Timeout: 30 * time.Second}
	// Sorted, so the first failure is the same one every time.
	for _, api := range slices.Sorted(maps.Keys(sources)) {
		url := sources[api].discovery
		if url == "" {
			continue
		}
		methods, err := fetchMethods(client, url)
		if err != nil {
			return snapshot{}, err
		}
		if len(methods) == 0 {
			return snapshot{}, fmt.Errorf("%s published no methods", url)
		}
		out.APIs[api] = methods
	}
	return out, nil
}

// fetchMethods reads one discovery document.
func fetchMethods(client *http.Client, url string) (map[string]apiMethod, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	var doc resource
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	out := map[string]apiMethod{}
	collectMethods("", doc, out)
	return out, nil
}

// maxDiscoveryBytes caps what a discovery document may be. Chat's is
// under half a megabyte; this is room to grow and a bound on a reply
// that is not what it claims to be.
const maxDiscoveryBytes = 16 << 20

// resource is as much of a discovery document as this needs: the methods
// at one level and the resources under it. It refers to itself, which
// encoding/json handles, so the document is decoded once rather than
// once per level.
type resource struct {
	Methods map[string]struct {
		HTTPMethod string   `json:"httpMethod"`
		Path       string   `json:"path"`
		Scopes     []string `json:"scopes"`
	} `json:"methods"`
	Resources map[string]resource `json:"resources"`
}

// collectMethods walks a decoded document, which is how reactions come
// out as spaces.messages.reactions.create.
func collectMethods(prefix string, res resource, out map[string]apiMethod) {
	for name, m := range res.Methods {
		scopes := slices.Clone(m.Scopes)
		slices.Sort(scopes)
		out[prefix+name] = apiMethod{Verb: m.HTTPMethod, Path: m.Path, Scopes: scopes}
	}
	for name, sub := range res.Resources {
		collectMethods(prefix+name+".", sub, out)
	}
}

// requestShape is what one client method's request literal says about
// the call it makes: the HTTP verb, and the `:verb` Google appends to a
// path for a custom method.
type requestShape struct {
	httpVerb string
	apiVerb  string
	// delegate is the client method this one hands its request to, when
	// it builds none of its own. `deleteName` is the case: six deletes
	// share it, and reading only each method's own body would leave all
	// six unbound.
	delegate string
}

// clientRequests reads what each client method asks Google for.
//
// This is what binds a row to the method it names. Without it a `used`
// row is held only to naming some existing, unclaimed method — so
// swapping the reasons on `markAsActive` and `markAsAway` left every
// gate green over a record that named the wrong method twice. The
// binding is the HTTP verb and the custom-method suffix, which is as
// much as a request literal says without evaluating it.
func clientRequests(dir string) (map[string]requestShape, error) {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]requestShape{}
	for _, entry := range names {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			out[fn.Name.Name] = shapeOf(fn)
		}
	}
	return out, nil
}

// shapeOf reads one method's body.
func shapeOf(fn *ast.FuncDecl) requestShape {
	var shape requestShape
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if ident, ok := node.Type.(*ast.Ident); !ok || ident.Name != "request" {
				return true
			}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "method":
					shape.httpVerb = literalString(kv.Value)
				case "verb":
					shape.apiVerb = literalString(kv.Value)
				}
			}
		case *ast.CallExpr:
			// A call on the receiver, for the one level of delegation
			// this needs. Recorded whatever it is; only a name that
			// turns out to be another client method is followed.
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if _, ok := sel.X.(*ast.Ident); ok && shape.delegate == "" {
					shape.delegate = sel.Sel.Name
				}
			}
		}
		return true
	})
	return shape
}

// literalString reads a string constant, spelled directly or as one of
// net/http's method constants.
func literalString(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, err := strconv.Unquote(v.Value)
			if err == nil {
				return s
			}
		}
	case *ast.SelectorExpr:
		// http.MethodGet and friends.
		if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "http" {
			return strings.ToUpper(strings.TrimPrefix(v.Sel.Name, "Method"))
		}
	}
	return ""
}

// resolve follows delegation until it finds a method that builds a
// request, so the six deletes that share `deleteName` are bound to it.
func resolve(shapes map[string]requestShape, name string) requestShape {
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		shape, ok := shapes[name]
		if !ok {
			return requestShape{}
		}
		if shape.httpVerb != "" {
			return shape
		}
		name = shape.delegate
	}
	return requestShape{}
}

// checkBinding holds a `used` row to the method it names.
//
// Two things a request literal says, checked against what Google
// publishes: the HTTP verb, and whether the path ends in a custom method
// — `v1/{+name}:markAsActive`. The second is what catches a swap inside
// a family, which is the mistake worth catching, because a family is
// where the names are close enough to confuse.
//
// A method whose verb this cannot read is reported rather than skipped.
// Skipping is how a binding check quietly stops binding: the day
// somebody builds a request some other way, every row it covers would
// pass for the wrong reason.
func checkBinding(entries []coverageEntry, published map[methodKey]apiMethod,
	shapes map[string]requestShape) []string {
	var problems []string
	for _, e := range entries {
		if e.verdict != verdictUsed {
			continue
		}
		m, ok := published[e.key]
		if !ok {
			continue // hand-listed, or already reported by checkPublished
		}
		name, _ := strings.CutPrefix(e.reason, clientType+".")
		shape := resolve(shapes, name)
		if shape.httpVerb == "" {
			problems = append(problems, fmt.Sprintf("%s:%d: %s names %s.%s, and no request this gate "+
				"can read is built there. Either it calls Google some other way, or this check has "+
				"stopped reading the code", coverageFile, e.line, e.key, clientType, name))
			continue
		}
		if shape.httpVerb != m.Verb {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is %s and %s.%s sends %s",
				coverageFile, e.line, e.key, m.Verb, clientType, name, shape.httpVerb))
		}
		want := customMethod(m.Path)
		if want != shape.apiVerb {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is %s and %s.%s builds %s — the row "+
				"names the wrong method", coverageFile, e.line, e.key, describeVerb(want),
				clientType, name, describeVerb(shape.apiVerb)))
		}
	}
	return problems
}

// customMethod is the `:verb` a discovery path ends with, or empty.
func customMethod(path string) string {
	_, verb, found := strings.Cut(path, ":")
	if !found {
		return ""
	}
	return verb
}

// describeVerb spells a custom method for a failure message, including
// its absence.
func describeVerb(verb string) string {
	if verb == "" {
		return "a plain resource path"
	}
	return ":" + verb
}
