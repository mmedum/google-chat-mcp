package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Not being signed in is a state to report, not a reason to stop. The
// text output says so and returns early; the object must still be whole,
// or a caller cannot tell "unauthorised" from "failed to parse".
func TestTheNotSignedInStateIsAWholeObject(t *testing.T) {
	r := statusReport{
		SchemaVersion: statusSchemaVersion,
		Scopes:        statusScopes{Granted: orEmpty(nil), Missing: orEmpty(nil)},
		Credentials:   statusCredentials{Reason: orNil("no profile written yet")},
	}
	var buf bytes.Buffer
	if err := r.writeJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	creds, _ := back["credentials"].(map[string]any)
	for _, k := range []string{"signed_in", "resolved", "token_store", "reason", "client_secret_path"} {
		if _, ok := creds[k]; !ok {
			t.Errorf("credentials.%s is absent; a truncated object reads the same as a broken one", k)
		}
	}
	if creds["signed_in"] != false || creds["resolved"] != false {
		t.Errorf("signed_in=%v resolved=%v, want both false", creds["signed_in"], creds["resolved"])
	}
	for _, k := range []string{"settings", "scopes", "schema_version"} {
		if _, ok := back[k]; !ok {
			t.Errorf("%s is absent from the unauthorised object", k)
		}
	}
}

// The JSON encoder writes straight to the stream and does not pass
// through anything that redacts, so the collector is where the address
// has to be masked.
func TestTheAccountIsMaskedInTheObject(t *testing.T) {
	var buf bytes.Buffer
	if err := (statusReport{Account: orNil("…@example.com")}).writeJSON(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "…@example.com") {
		t.Errorf("the masked account did not survive encoding:\n%s", buf.String())
	}
	// Any \u escape at all: the mask is a literal ellipsis in the text
	// output, and a caller comparing the two needs the same bytes.
	if strings.Contains(buf.String(), "\\u") {
		t.Errorf("the object carries a unicode escape, so it will not compare equal:\n%s", buf.String())
	}
}

// A nil slice marshals as null, and a caller counting scopes has to
// guard for that before it can count.
func TestEmptyScopeListsStayLists(t *testing.T) {
	var buf bytes.Buffer
	r := statusReport{Scopes: statusScopes{Granted: orEmpty(nil), Missing: orEmpty(nil)}}
	if err := r.writeJSON(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"granted": []`, `"missing": []`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%s missing:\n%s", want, buf.String())
		}
	}
}

// The whole of stdout has to be one JSON value.
func TestTheObjectIsExactlyOneValue(t *testing.T) {
	var buf bytes.Buffer
	if err := (statusReport{SchemaVersion: statusSchemaVersion}).writeJSON(&buf); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&buf)
	var first any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if dec.More() {
		t.Error("stdout carries more than one JSON value")
	}
}

// The text collapses a complete toolset list to "all"; the object
// carries the names, because a script wants to know which ones.
func TestToolsetsAreNamesInTheObjectAndAllInTheText(t *testing.T) {
	full := toolsetNames()
	var buf bytes.Buffer
	r := statusReport{Settings: statusSettings{Toolsets: full}, Credentials: statusCredentials{SignedIn: true}}
	r.writeText(&buf)
	if !strings.Contains(buf.String(), "toolsets:       all") {
		t.Errorf("the text stopped saying all:\n%s", buf.String())
	}
	var out bytes.Buffer
	if err := r.writeJSON(&out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"toolsets": "all"`) {
		t.Error("the object collapsed the toolsets to a word; a caller cannot read which ones")
	}
	if !strings.Contains(out.String(), full[0]) {
		t.Errorf("the object lost the toolset names:\n%s", out.String())
	}
}
