package gchat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mmedum/google-chat-mcp/v9/internal/scopes"
)

// Under WithAdminAccess every client method either runs as an
// administrator or sends nothing at all. Google gives nine methods an
// administrator form (discovery revision 20261026), and those nine are
// named here rather than read back from the client: a tenth method
// gaining adminScope by mistake would send a call Google has no admin
// form for, and one losing it would answer as the caller, which for a
// space the caller is not in is "not found".
func TestAdminAccessRunsTheNineAdminMethodsAndRefusesTheRest(t *testing.T) {
	wantAdmin := []string{
		"AddMember", "DeleteSpace", "GetMembership", "GetSpace", "ListMembers",
		"RemoveMember", "SearchSpaces", "UpdateMemberRole", "UpdateSpace",
	}
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, func(o *Options) { o.MaxRetries = 0 })

	adminScopes := map[string]bool{}
	for _, s := range scopes.Requested(true, false, true) {
		if !slices.Contains(scopes.All, s) {
			adminScopes[s] = true
		}
	}
	var admin, refused, passedThrough []string
	callEveryMethod(t, c, WithAdminAccess(context.Background()), func(name string, err error) {
		mu.Lock()
		got := slices.Clone(seen)
		seen = nil
		mu.Unlock()

		if errors.Is(err, ErrNoAdminForm) {
			if len(got) != 0 {
				t.Errorf("%s was refused as having no admin form, yet sent %d requests", name, len(got))
			}
			refused = append(refused, name)
			return
		}
		if len(got) == 0 {
			t.Errorf("%s under admin access: err = %v and no request, want a request or ErrNoAdminForm", name, err)
			return
		}
		r := got[0]
		isChat := strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/upload/")
		flagged := r.URL.Query().Get("useAdminAccess") == "true"
		switch {
		case !isChat && flagged:
			t.Errorf("%s went to %s with useAdminAccess, which only Chat takes", name, r.URL.Path)
		case !isChat:
			passedThrough = append(passedThrough, name)
		case !flagged:
			t.Errorf("%s reached Chat under admin access without useAdminAccess", name)
		default:
			admin = append(admin, name)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !adminScopes[apiErr.Scope] {
				t.Errorf("%s names scope %v under admin access, want one of the administrator scopes", name, err)
			} else if r.Method == http.MethodGet && !strings.HasSuffix(apiErr.Scope, ".readonly") {
				t.Errorf("%s reads and names %s, which a read-only admin login does not ask for", name, apiErr.Scope)
			}
		}
	})
	slices.Sort(admin)
	if !slices.Equal(admin, wantAdmin) {
		t.Errorf("ran as an administrator: %v\nwant %v", admin, wantAdmin)
	}
	// Floors, so a client whose methods all stopped matching the filter
	// above cannot pass by checking nothing.
	if len(refused) < 30 {
		t.Errorf("only %d Chat calls were refused under admin access, want every one without an admin form", len(refused))
	}
	if !slices.Contains(passedThrough, "Userinfo") || !slices.Contains(passedThrough, "SearchDirectoryPeople") {
		t.Errorf("passed through as the caller: %v, want Userinfo and SearchDirectoryPeople among them", passedThrough)
	}
}

// Without admin access nothing changes: no call carries useAdminAccess,
// and a read names the user's scope.
func TestOrdinaryCallsNeverAskForAdminAccess(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if _, err := c.GetSpace(context.Background(), "spaces/AAAAspace1"); err != nil {
		t.Fatalf("GetSpace: %v", err)
	}
	if strings.Contains(query, "useAdminAccess") {
		t.Errorf("an ordinary read sent %q", query)
	}
}

// Google requires a member type filter on an administrator's membership
// listing, and the one that keeps Google Group memberships is
// member.type != "BOT".
func TestAdminMemberListingCarriesTheFilterGoogleRequires(t *testing.T) {
	for _, tc := range []struct {
		label  string
		admin  bool
		filter string
		want   string
	}{
		{"admin, no filter of its own", true, "", `member.type != "BOT"`},
		{"admin, with a role filter", true, `role = "ROLE_MANAGER"`, `role = "ROLE_MANAGER" AND member.type != "BOT"`},
		{"as the caller", false, "", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("filter")
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			ctx := context.Background()
			if tc.admin {
				ctx = WithAdminAccess(ctx)
			}
			if _, err := newTestClient(t, srv).ListMembers(ctx, ListMembersOptions{Space: "spaces/AAAAspace1", Filter: tc.filter}); err != nil {
				t.Fatalf("ListMembers: %v", err)
			}
			if got != tc.want {
				t.Errorf("filter = %q, want %q", got, tc.want)
			}
		})
	}
}

// An administrator's search has to say whose spaces it covers, and an
// ordinary one must not: Google refuses the first without the clause.
func TestAdminSearchNamesTheCustomer(t *testing.T) {
	for _, tc := range []struct {
		label string
		admin bool
		want  string
	}{
		{"as an administrator", true, `customer = "customers/my_customer" AND spaceType = "SPACE"`},
		{"as the caller", false, `spaceType = "SPACE"`},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("query")
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			ctx := context.Background()
			if tc.admin {
				ctx = WithAdminAccess(ctx)
			}
			if _, err := newTestClient(t, srv).SearchSpaces(ctx, SearchSpacesOptions{Query: `spaceType = "SPACE"`}); err != nil {
				t.Fatalf("SearchSpaces: %v", err)
			}
			if got != tc.want {
				t.Errorf("query = %q, want %q", got, tc.want)
			}
		})
	}
}
