package gchat

import (
	"slices"
	"testing"
)

type inner struct {
	Name string `json:"name"`
}

type sample struct {
	Name     string            `json:"name"`
	Renamed  string            `json:"wireName"`
	Skipped  string            `json:"-"`
	Nested   *inner            `json:"nested"`
	Items    []inner           `json:"items"`
	Labels   map[string]string `json:"labels"`
	Freeform any               `json:"freeform"`
	unseen   string            //nolint:unused // guards the unexported-field path
}

func collect(t *testing.T, body string, out any) []string {
	t.Helper()
	var got []string
	if err := decode([]byte(body), out, func(p string) { got = append(got, p) }); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestDecodeReportsUnknownFields(t *testing.T) {
	var s sample
	got := collect(t, `{"name":"a","newTop":1}`, &s)
	if !slices.Equal(got, []string{"sample.newTop"}) {
		t.Errorf("drift = %v, want sample.newTop", got)
	}
	if s.Name != "a" {
		t.Errorf("the value must still decode: %+v", s)
	}
}

// A new field on a nested object must name its path, not just the leaf.
func TestDecodeReportsNestedPaths(t *testing.T) {
	var s sample
	got := collect(t, `{"nested":{"name":"n","extra":true}}`, &s)
	if !slices.Equal(got, []string{"sample.nested.extra"}) {
		t.Errorf("drift = %v", got)
	}
}

// Google adds a field to every row of a resource at once, so a list
// response reports it once per row. The caller dedupes for logging and
// counts every occurrence for the metric.
func TestDecodeWalksEveryElementOfASlice(t *testing.T) {
	var s sample
	got := collect(t, `{"items":[{"name":"a","extra":1},{"name":"b","extra":2}]}`, &s)
	want := []string{"sample.items.extra", "sample.items.extra"}
	if !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v", got, want)
	}
}

// A map models an open key set: its keys are data, not schema.
func TestDecodeDoesNotReportMapKeys(t *testing.T) {
	var s sample
	if got := collect(t, `{"labels":{"anything":"goes"}}`, &s); len(got) != 0 {
		t.Errorf("map keys reported as drift: %v", got)
	}
}

// An any-typed field models nothing, so nothing under it is unknown.
func TestDecodeDoesNotWalkFreeformFields(t *testing.T) {
	var s sample
	if got := collect(t, `{"freeform":{"whatever":{"deep":1}}}`, &s); len(got) != 0 {
		t.Errorf("freeform contents reported as drift: %v", got)
	}
}

func TestDecodeUsesWireNamesNotFieldNames(t *testing.T) {
	var s sample
	if got := collect(t, `{"wireName":"x"}`, &s); len(got) != 0 {
		t.Errorf("a known field reported as drift: %v", got)
	}
	if got := collect(t, `{"Renamed":"x"}`, &s); !slices.Equal(got, []string{"sample.Renamed"}) {
		t.Errorf("the Go field name should not match the wire: %v", got)
	}
	if got := collect(t, `{"Skipped":"x"}`, &s); !slices.Equal(got, []string{"sample.Skipped"}) {
		t.Errorf(`a json:"-" field should not match the wire: %v`, got)
	}
}

type embedded struct {
	inner
	Extra string `json:"extra"`
}

func TestDecodeFollowsEmbeddedStructs(t *testing.T) {
	var e embedded
	if got := collect(t, `{"name":"a","extra":"b"}`, &e); len(got) != 0 {
		t.Errorf("embedded fields reported as drift: %v", got)
	}
	if got := collect(t, `{"other":1}`, &e); !slices.Equal(got, []string{"embedded.other"}) {
		t.Errorf("drift = %v", got)
	}
}

func TestDecodeIsFineWithoutAReporter(t *testing.T) {
	var s sample
	if err := decode([]byte(`{"name":"a","extra":1}`), &s, nil); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Name != "a" {
		t.Errorf("value = %+v", s)
	}
}

func TestDecodeReturnsRealJSONErrors(t *testing.T) {
	var s sample
	if err := decode([]byte(`{"name":`), &s, func(string) {}); err == nil {
		t.Fatal("malformed JSON must fail")
	}
}

// A field the struct declares as required stays required: drift
// detection is about additions, and a removal must still be loud.
func TestDecodeDoesNotHideAMissingField(t *testing.T) {
	var s sample
	got := collect(t, `{"newName":"a"}`, &s)
	if !slices.Equal(got, []string{"sample.newName"}) {
		t.Errorf("drift = %v", got)
	}
	if s.Name != "" {
		t.Error("a renamed field must leave the old one empty for the caller to notice")
	}
}
