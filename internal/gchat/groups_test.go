package gchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The lookup goes to Cloud Identity, not Chat, as a custom method with
// the address in groupKey.id.
func TestLookupGroupAsksCloudIdentity(t *testing.T) {
	srv, rec := recording(t, `{"name":"groups/AAAAgroup1"}`)
	got, err := newTestClient(t, srv).LookupGroup(context.Background(), "team@example.com")
	if err != nil {
		t.Fatalf("LookupGroup: %v", err)
	}
	if rec.Method != "GET" || rec.Path != "/cloudidentity/groups:lookup" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}
	if !strings.Contains(rec.Query, "groupKey.id=team%40example.com") {
		t.Errorf("query = %q", rec.Query)
	}
	if got != "groups/AAAAgroup1" {
		t.Errorf("name = %q", got)
	}
}

// A failed lookup's error is logged, and the address is the payload: it
// rides in the query, which an error never carries.
func TestAFailedLookupLeavesTheAddressOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`)
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).LookupGroup(context.Background(), "team@example.com")
	if err == nil || !IsNotFound(err) {
		t.Fatalf("err = %v, want not found", err)
	}
	if strings.Contains(err.Error(), "team") {
		t.Errorf("error %q carries the address", err)
	}
}

func TestGetGroupReadsTheGroupByName(t *testing.T) {
	srv, rec := recording(t, `{"name":"groups/AAAAgroup1","groupKey":{"id":"team@example.com"},"displayName":"Team"}`)
	got, err := newTestClient(t, srv).GetGroup(context.Background(), "groups/AAAAgroup1")
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if rec.Path != "/cloudidentity/groups/AAAAgroup1" {
		t.Errorf("path = %q", rec.Path)
	}
	// Only what is read is asked for: the rest of a group would be
	// reported as schema drift on every read.
	if !strings.Contains(rec.Query, "fields=name%2CgroupKey%2CdisplayName") {
		t.Errorf("query = %q, want the fields this server reads", rec.Query)
	}
	if got.GroupKey.ID != "team@example.com" || got.DisplayName != "Team" {
		t.Errorf("group = %+v", got)
	}
}
