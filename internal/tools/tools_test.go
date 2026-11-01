package tools

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/v9/internal/service"
)

// The model reads the leading class, so it has to survive the trip from
// the service through the handler.
func TestFailKeepsTheServiceClass(t *testing.T) {
	got := fail(service.Failf(service.ClassNotFound, "no such space"))
	if got.Error() != "[not_found] no such space" {
		t.Errorf("fail = %q", got.Error())
	}
}

func TestFailUnwrapsAWrappedServiceError(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), service.Failf(service.ClassScope, "need a scope"))
	if got := fail(wrapped).Error(); !strings.HasPrefix(got, "[scope]") {
		t.Errorf("fail = %q, want the scope class to survive wrapping", got)
	}
}

// An error from somewhere this server did not anticipate still has to
// arrive in the same shape, or the model cannot tell them apart.
func TestFailTagsAnUnclassifiedError(t *testing.T) {
	got := fail(errors.New("something odd")).Error()
	if !strings.HasPrefix(got, "[unexpected] ") {
		t.Errorf("fail = %q", got)
	}
	if !strings.Contains(got, "something odd") {
		t.Errorf("fail = %q, want it to keep the original text", got)
	}
}

// Only a write that adds says destructiveHint false (docs/architecture.md,
// "One Kind decides three things"): the Adding kinds, and
// download_attachment, which never overwrites a file. The list is
// literal because that is a per-tool decision; a new write is a Write,
// and says true, until somebody decides it only adds.
func TestOnlyToolsThatAddSayTheyAreNotDestructive(t *testing.T) {
	list, err := fullSession(t, func(http.ResponseWriter, *http.Request) {}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got []string
	writes := 0
	for _, tool := range list.Tools {
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s carries no annotations", tool.Name)
			continue
		}
		if a.ReadOnlyHint {
			continue
		}
		writes++
		if a.DestructiveHint != nil && !*a.DestructiveHint {
			got = append(got, tool.Name)
		}
	}
	if writes < 40 {
		t.Errorf("read %d writes, want at least 40", writes)
	}
	slices.Sort(got)
	want := []string{
		"add_member", "add_reaction", "create_custom_emoji", "create_group_chat",
		"create_section", "create_space", "download_attachment", "find_direct_message",
		"pin_message", "send_message", "upload_attachment",
	}
	if !slices.Equal(got, want) {
		t.Errorf("writes that say destructiveHint false = %v, want %v", got, want)
	}
}

// Annotations are advice a client shows a person. They have to be
// internally consistent: nothing read-only destroys anything.
func TestAnnotationsAreConsistent(t *testing.T) {
	for _, tc := range []struct {
		kind        Kind
		name        string
		readOnly    bool
		destructive bool
		idempotent  bool
	}{
		{Read, "Read", true, false, false},
		{ReadWritesLocally, "ReadWritesLocally", false, false, false},
		{Adding, "Adding", false, false, false},
		{AddingIdempotent, "AddingIdempotent", false, false, true},
		{Write, "Write", false, true, false},
		{WriteIdempotent, "WriteIdempotent", false, true, true},
		{Destructive, "Destructive", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.kind.annotations()
			if a.ReadOnlyHint != tc.readOnly {
				t.Errorf("ReadOnlyHint = %v, want %v", a.ReadOnlyHint, tc.readOnly)
			}
			if tc.readOnly && a.DestructiveHint != nil && *a.DestructiveHint {
				t.Error("a read-only tool cannot be destructive")
			}
			if !tc.readOnly && (a.DestructiveHint == nil || *a.DestructiveHint != tc.destructive) {
				t.Errorf("DestructiveHint = %v, want %v", a.DestructiveHint, tc.destructive)
			}
			if a.IdempotentHint != tc.idempotent {
				t.Errorf("IdempotentHint = %v, want %v", a.IdempotentHint, tc.idempotent)
			}
			// Every tool here reaches Google.
			if a.OpenWorldHint == nil || !*a.OpenWorldHint {
				t.Error("must be open-world")
			}
		})
	}
}

// A nullable output field says "Google said nothing" with null. An
// empty string would read as a value the caller could use — an address
// to write to, a name to show.
func TestNullableDistinguishesAbsentFromEmpty(t *testing.T) {
	if nullable("") != nil {
		t.Error("an empty string must become null")
	}
	if got := nullable("janedoe@example.com"); got == nil || *got != "janedoe@example.com" {
		t.Errorf("nullable = %v", got)
	}
	if nullableTime(time.Time{}) != nil {
		t.Error("the zero time must become null")
	}
	now := time.Now()
	if got := nullableTime(now); got == nil || !got.Equal(now) {
		t.Errorf("nullableTime = %v", got)
	}
}

// The released surface annotates its timestamps, and jsonschema-go does
// not do it on its own.
func TestOutputSchemaAnnotatesTimestamps(t *testing.T) {
	schema := outputSchema[MessageDetailOutput]()
	ts, ok := schema.Properties["timestamp"]
	if !ok {
		t.Fatal("no timestamp property")
	}
	if ts.Format != "date-time" {
		t.Errorf("format = %q, want date-time", ts.Format)
	}
}

// modulePath is this module's import path, the prefix that tells one of
// its own packages from a dependency.
const modulePath = "github.com/mmedum/google-chat-mcp/v9/"

// The model never widens its own reach (the standard's §3): no tool turns
// a toolset on, lifts a refusal or edits what the operator set. The
// server's settings are the environment, the profile internal/userconfig
// writes and the token internal/credentials stores, all set outside the
// conversation. No package a tool can reach imports either of the two,
// and none sets an environment variable. Read from the source rather
// than listed, so a new import is caught the day it is added.
func TestNoToolCanWriteTheServersSettings(t *testing.T) {
	reached := map[string]bool{}
	setsEnv := map[string]string{}
	var walk func(pkg string)
	walk = func(pkg string) {
		if reached[pkg] {
			return
		}
		reached[pkg] = true
		dir := filepath.Join("..", "..", filepath.FromSlash(pkg))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			for _, imp := range file.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if internal, ok := strings.CutPrefix(path, modulePath); ok {
					walk(internal)
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" &&
					slices.Contains([]string{"Setenv", "Unsetenv", "Clearenv"}, sel.Sel.Name) {
					setsEnv[pkg+"/"+name] = "os." + sel.Sel.Name
				}
				return true
			})
		}
	}
	walk("internal/tools")

	// A floor on what was read: the walk has to have reached the
	// service and the client, or it is looking at nothing.
	for _, pkg := range []string{"internal/service", "internal/gchat", "internal/config"} {
		if !reached[pkg] {
			t.Fatalf("the walk never reached %s; it read %v", pkg, reached)
		}
	}
	for _, pkg := range []string{"internal/userconfig", "internal/credentials"} {
		if reached[pkg] {
			t.Errorf("a tool can reach %s, which writes the server's own settings", pkg)
		}
	}
	for file, call := range setsEnv {
		t.Errorf("%s calls %s, and a tool can reach it", file, call)
	}
	// The configuration reaches a tool as a copy it cannot write back.
	field, ok := reflect.TypeFor[Deps]().FieldByName("Config")
	if !ok || field.Type.Kind() != reflect.Struct {
		t.Errorf("Deps carries the configuration as %v; a pointer would let a tool change it", field.Type)
	}
}

// The same rule from the outside: every registered tool is called, and
// what is registered afterward is what was registered before. A tool
// that turned on a toolset or registered a refused tool would show here.
func TestNoToolChangesWhatIsRegistered(t *testing.T) {
	cs := fullSession(t, everyEndpoint)
	before := toolNames(t, cs)
	for name, args := range everyRegisteredTool(t, cs) {
		call(t, cs, name, args, nil)
	}
	if after := toolNames(t, cs); !slices.Equal(before, after) {
		t.Errorf("calling every tool changed what is registered:\nbefore %v\nafter  %v", before, after)
	}
}

// toolNames is what a client is told the server registers, sorted.
func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}
