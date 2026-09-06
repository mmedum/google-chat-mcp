package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readResource asks for one URI and returns its single content.
func readResource(t *testing.T, cs *mcp.ClientSession, uri string, out any) error {
	t.Helper()
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return err
	}
	if len(res.Contents) != 1 {
		t.Fatalf("%s returned %d contents, want 1", uri, len(res.Contents))
	}
	if res.Contents[0].MIMEType != "application/json" {
		t.Errorf("mime type = %q", res.Contents[0].MIMEType)
	}
	if res.Contents[0].URI != uri {
		t.Errorf("uri = %q, want the one asked for", res.Contents[0].URI)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(res.Contents[0].Text), out); err != nil {
			t.Fatalf("decode %s: %v", uri, err)
		}
	}
	return nil
}

// The three templates are the surface a client attaches to a
// conversation, so their URIs are as much a contract as a tool name.
func TestResourceTemplatesAreRegistered(t *testing.T) {
	cs := session(t, body(`{}`))
	list, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	have := map[string]bool{}
	for _, tmpl := range list.ResourceTemplates {
		have[tmpl.URITemplate] = true
		if tmpl.Description == "" {
			t.Errorf("template %q has no description", tmpl.URITemplate)
		}
		if tmpl.MIMEType != "application/json" {
			t.Errorf("template %q serves %q", tmpl.URITemplate, tmpl.MIMEType)
		}
	}
	for _, want := range []string{
		"gchat://spaces/{space_id}",
		"gchat://spaces/{space_id}/messages/{message_id}",
		"gchat://spaces/{space_id}/threads/{thread_id}",
	} {
		if !have[want] {
			t.Errorf("template %q is not registered", want)
		}
	}
	if len(have) != 3 {
		t.Errorf("%d templates, want 3", len(have))
	}
}

// The ids in a resource URI are bare, because that is what a person
// copies out of a Chat URL. The Chat API wants full resource names.
func TestSpaceResourceTakesABareID(t *testing.T) {
	var path string
	cs := session(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"name":"spaces/A","spaceType":"SPACE","displayName":"Team"}`))
	})
	var out SpaceDetailOutput
	if err := readResource(t, cs, "gchat://spaces/A", &out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(path, "/spaces/A") {
		t.Errorf("path = %q", path)
	}
	if out.DisplayName != "Team" {
		t.Errorf("resource = %+v", out)
	}
}

// A resource carries the same content as the matching tool, which is
// what makes the two interchangeable.
func TestMessageResourceMatchesTheTool(t *testing.T) {
	const one = `{"name":"spaces/A/messages/1","sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z",
	  "thread":{"name":"spaces/A/threads/T1"},"text":"hello"}`
	cs := session(t, chatAndPeople(body(one), personHit))

	var viaTool MessageDetailOutput
	call(t, cs, "get_message", map[string]any{"message_name": "spaces/A/messages/1"}, &viaTool)

	var viaResource MessageDetailOutput
	if err := readResource(t, cs, "gchat://spaces/A/messages/1", &viaResource); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reflect.DeepEqual(viaTool, viaResource) {
		t.Errorf("resource = %+v, tool = %+v", viaResource, viaTool)
	}
}

func TestThreadResourceReadsTheThread(t *testing.T) {
	var query string
	cs := session(t, chatAndPeople(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(messagePage))
	}, personHit))

	var out MessageListOutput
	if err := readResource(t, cs, "gchat://spaces/A/threads/T1", &out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out.Result) != 1 || out.Result[0].MessageID != "spaces/A/messages/1" {
		t.Errorf("resource = %+v", out)
	}
	if !strings.Contains(query, "thread.name") {
		t.Errorf("query = %q, want the thread filter", query)
	}
	// A resource cannot page, so it asks for as much as the tool
	// allows rather than the tool's smaller default. Anything less
	// truncates a long thread while promising the whole of it.
	if !strings.Contains(query, "pageSize=100") {
		t.Errorf("query = %q, want the resource to ask for the maximum", query)
	}
}

// The thread resource says it carries the same content as get_thread,
// so it has to be the same shape a client would parse with the tool's
// schema, not a bare array.
func TestThreadResourceMatchesTheToolShape(t *testing.T) {
	cs := session(t, chatAndPeople(body(messagePage), personHit))

	var viaTool MessageListOutput
	call(t, cs, "get_thread", map[string]any{
		"space_id": "spaces/A", "thread_name": "spaces/A/threads/T1",
	}, &viaTool)

	var viaResource MessageListOutput
	if err := readResource(t, cs, "gchat://spaces/A/threads/T1", &viaResource); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reflect.DeepEqual(viaTool, viaResource) {
		t.Errorf("resource = %+v, tool = %+v", viaResource, viaTool)
	}
}

// A URI that does not name a resource is the spec's not-found, not an
// invalid argument: the template already matched.
func TestAMalformedURIIsNotFound(t *testing.T) {
	cs := session(t, body(`{}`))
	for _, uri := range []string{
		"gchat://spaces/",
		"gchat://spaces/A/messages/",
		"gchat://spaces/A/threads",
		"gchat://spaces/A/comments/1",
		"gdocs://spaces/A",
	} {
		if err := readResource(t, cs, uri, nil); err == nil {
			t.Errorf("%s was accepted", uri)
		}
	}
}

// A space that is not there is also a not-found, so a client can tell
// it apart from the server refusing.
func TestAMissingSpaceIsNotFound(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND","message":"no such space"}}`))
	})
	err := readResource(t, cs, "gchat://spaces/gone", nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("error = %v, want a resource-not-found", err)
	}
}

// Anything else keeps the class a model can act on.
func TestAnUpstreamFailureKeepsItsClass(t *testing.T) {
	cs := session(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	})
	err := readResource(t, cs, "gchat://spaces/A", nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "[scope]") {
		t.Errorf("error = %v, want the scope class", err)
	}
}
