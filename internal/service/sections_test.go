package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mmedum/google-chat-mcp/internal/scopes"
)

const sectionPage = `{"sections":[
  {"name":"users/me/sections/S1","type":"CUSTOM_SECTION","displayName":"Projects","sortOrder":2},
  {"name":"users/me/sections/S2","type":"DEFAULT_DIRECT_MESSAGES"},
  {"name":"users/me/sections/S3","type":"DEFAULT_SPACES"},
  {"name":"users/me/sections/S4","type":"DEFAULT_APPS"}
],"nextPageToken":"more"}`

// Google names only custom sections. The parenthesised label is what
// keeps a custom section called "Spaces" apart from the system one.
func TestListSectionsLabelsTheSystemOnes(t *testing.T) {
	s := newService(t, ok(sectionPage))
	got, err := s.ListSections(context.Background(), ListSectionsInput{})
	if err != nil {
		t.Fatalf("ListSections: %v", err)
	}
	want := []string{"Projects", "(direct messages)", "(spaces)", "(apps)"}
	if len(got.Sections) != len(want) {
		t.Fatalf("got %d sections, want %d", len(got.Sections), len(want))
	}
	for i, w := range want {
		if got.Sections[i].DisplayName != w {
			t.Errorf("section %d = %q, want %q", i, got.Sections[i].DisplayName, w)
		}
	}
	if got.Sections[0].SortOrder == nil || *got.Sections[0].SortOrder != 2 {
		t.Errorf("sort order = %v", got.Sections[0].SortOrder)
	}
	if got.Sections[1].SortOrder != nil {
		t.Errorf("a section Google did not rank should have no rank, got %v", *got.Sections[1].SortOrder)
	}
	if got.NextPageToken != "more" {
		t.Errorf("page token = %q", got.NextPageToken)
	}
}

func TestListSectionsDegradesAnUnknownType(t *testing.T) {
	s := newService(t, ok(`{"sections":[{"name":"users/me/sections/S9","type":"DEFAULT_STARRED"}]}`))
	got, err := s.ListSections(context.Background(), ListSectionsInput{})
	if err != nil {
		t.Fatalf("ListSections: %v", err)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("the row must survive: %+v", got.Sections)
	}
	if got.Sections[0].Type != "SECTION_TYPE_UNSPECIFIED" {
		t.Errorf("type = %q", got.Sections[0].Type)
	}
	if got.Sections[0].DisplayName != "(unnamed section)" {
		t.Errorf("display name = %q, want something to call it", got.Sections[0].DisplayName)
	}
}

// A short listing reads as a short sidebar unless the count says
// otherwise.
func TestListSectionsCountsWhatItSkipped(t *testing.T) {
	s := newService(t, ok(`{"sections":[{"name":"users/me/sections/S1","type":"CUSTOM_SECTION","displayName":"Kept"},{"type":"CUSTOM_SECTION"}]}`))
	got, err := s.ListSections(context.Background(), ListSectionsInput{})
	if err != nil {
		t.Fatalf("ListSections: %v", err)
	}
	if len(got.Sections) != 1 || got.Unparsed != 1 {
		t.Errorf("result = %+v, want one kept and one counted", got)
	}
}

func TestListSectionsAddressesTheCaller(t *testing.T) {
	var path, query string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		fmt.Fprint(w, `{"sections":[]}`)
	})
	if _, err := s.ListSections(context.Background(), ListSectionsInput{Limit: 3, PageToken: "next"}); err != nil {
		t.Fatalf("ListSections: %v", err)
	}
	if !strings.HasSuffix(path, "/users/me/sections") {
		t.Errorf("path = %q, want the caller addressed as me", path)
	}
	if !strings.Contains(query, "pageSize=3") || !strings.Contains(query, "pageToken=next") {
		t.Errorf("query = %q", query)
	}
	_, err := s.ListSections(context.Background(), ListSectionsInput{Limit: 5000})
	assertClass(t, err, ClassInvalid)
}

func TestListSectionItemsReadsOneSection(t *testing.T) {
	var path, filter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		path, filter = r.URL.Path, r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"sectionItems":[{"name":"users/me/sections/S1/items/spaces/A","space":"spaces/A"}]}`)
	})
	got, err := s.ListSectionItems(context.Background(), ListSectionItemsInput{Section: "users/me/sections/S1"})
	if err != nil {
		t.Fatalf("ListSectionItems: %v", err)
	}
	if !strings.HasSuffix(path, "/users/me/sections/S1/items") {
		t.Errorf("path = %q", path)
	}
	if filter != "" {
		t.Errorf("filter = %q, want none without a space", filter)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].SectionName != "users/me/sections/S1" {
		t.Errorf("section = %q, want it sliced off the item name", got.Items[0].SectionName)
	}
	if got.Items[0].Space != "spaces/A" {
		t.Errorf("space = %q", got.Items[0].Space)
	}
}

// Finding where one space sits uses Google's wildcard parent, and the
// filter is unquoted: the quoted form answers 400.
func TestListSectionItemsFindsOneSpace(t *testing.T) {
	var path, filter string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		path, filter = r.URL.Path, r.URL.Query().Get("filter")
		fmt.Fprint(w, `{"sectionItems":[]}`)
	})
	if _, err := s.ListSectionItems(context.Background(), ListSectionItemsInput{Space: "spaces/A"}); err != nil {
		t.Fatalf("ListSectionItems: %v", err)
	}
	if !strings.HasSuffix(path, "/users/me/sections/-/items") {
		t.Errorf("path = %q, want the wildcard parent", path)
	}
	if filter != "space = spaces/A" {
		t.Errorf("filter = %q, want the unquoted form Google accepts", filter)
	}
}

// Google documents the wildcard parent only alongside a space filter,
// so an unfiltered wildcard listing is not a shape to promise.
func TestListSectionItemsNeedsASelector(t *testing.T) {
	s := newService(t, ok(`{"sectionItems":[]}`))
	for _, tc := range []struct {
		name string
		in   ListSectionItemsInput
	}{
		{"neither", ListSectionItemsInput{}},
		{"a section name that is not one", ListSectionItemsInput{Section: "sections/S1"}},
		{"a space that is not one", ListSectionItemsInput{Space: "users/me"}},
		{"limit above the maximum", ListSectionItemsInput{Space: "spaces/A", Limit: 5000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ListSectionItems(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
}

// An item that is not a space is a member type this server does not
// model. It is reported rather than dropped, because the caller can
// still see that the section holds something.
func TestListSectionItemsKeepsAnItemWithNoSpace(t *testing.T) {
	s := newService(t, ok(`{"sectionItems":[
	  {"name":"users/me/sections/S1/items/other"},
	  {"space":"spaces/B"}
	]}`))
	got, err := s.ListSectionItems(context.Background(), ListSectionItemsInput{Section: "users/me/sections/S1"})
	if err != nil {
		t.Fatalf("ListSectionItems: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Space != "" {
		t.Errorf("items = %+v, want the unmodelled item kept with no space", got.Items)
	}
	if got.Unparsed != 1 {
		t.Errorf("unparsed = %d, want the nameless row counted", got.Unparsed)
	}
}

func TestSectionListingsNameTheirScope(t *testing.T) {
	s := newService(t, status(http.StatusForbidden,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.ListSections(context.Background(), ListSectionsInput{})
	assertClass(t, err, ClassScope)
	if !strings.Contains(err.Error(), "chat.users.sections.readonly") {
		t.Errorf("error = %v", err)
	}
	_, err = s.ListSectionItems(context.Background(), ListSectionItemsInput{Space: "spaces/A"})
	assertClass(t, err, ClassScope)
}

func TestCreateSectionAddsACustomOne(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"users/123/sections/S","displayName":"Clients","type":"CUSTOM_SECTION"}`))
	got, err := s.CreateSection(context.Background(), CreateSectionInput{DisplayName: "  Clients  "})
	if err != nil {
		t.Fatalf("CreateSection: %v", err)
	}
	if got.Name != "users/123/sections/S" || got.DisplayName != "Clients" {
		t.Errorf("result = %+v, want the label trimmed", got)
	}
	if sent := rec.last(t); !strings.Contains(sent.Body, `"type":"CUSTOM_SECTION"`) {
		t.Errorf("body = %s", sent.Body)
	}
}

func TestRenameSectionRenames(t *testing.T) {
	s, rec := recorded(t, ok(`{"name":"users/123/sections/S","displayName":"Clients"}`))
	got, err := s.RenameSection(context.Background(), RenameSectionInput{
		Section: "users/me/sections/S", DisplayName: "Clients",
	})
	if err != nil {
		t.Fatalf("RenameSection: %v", err)
	}
	// Google canonicalises users/me to the numeric id, and the name it
	// returns is the one a caller should use next.
	if got.Name != "users/123/sections/S" {
		t.Errorf("name = %q", got.Name)
	}
	if sent := rec.last(t); sent.Query != "updateMask=displayName" {
		t.Errorf("query = %q", sent.Query)
	}
}

func TestSectionNamesAreBounded(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	long := strings.Repeat("x", maxSectionDisplayName+1)
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"no label", func() error {
			_, err := s.CreateSection(context.Background(), CreateSectionInput{})
			return err
		}},
		{"a label of whitespace", func() error {
			_, err := s.CreateSection(context.Background(), CreateSectionInput{DisplayName: "   "})
			return err
		}},
		{"a label above the cap", func() error {
			_, err := s.CreateSection(context.Background(), CreateSectionInput{DisplayName: long})
			return err
		}},
		{"a section name that is not one", func() error {
			_, err := s.RenameSection(context.Background(), RenameSectionInput{Section: "spaces/A", DisplayName: "Clients"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertClass(t, tc.run(), ClassInvalid) })
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

// A section that is already gone is the state the caller asked for. A
// plain refusal is not: that is Google declining to delete a system
// section, which the caller has to see.
func TestDeleteSectionSeparatesGoneFromRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr Class
	}{
		{"already deleted", 404, `{"error":{"status":"NOT_FOUND","message":"gone"}}`, ""},
		{"a system section", 403, `{"error":{"status":"PERMISSION_DENIED","message":"cannot delete"}}`, ClassForbidden},
		{"a missing scope", 403,
			`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`, ClassScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, status(tc.status, tc.body))
			got, err := s.DeleteSection(context.Background(), DeleteSectionInput{Section: "users/me/sections/S"})
			if tc.wantErr != "" {
				assertClass(t, err, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("a second delete is not a failure: %v", err)
			}
			if got.Deleted {
				t.Error("deleted = true, want false for a section that was already gone")
			}
		})
	}
}

func TestSectionWriteDryRunsChangeNothing(t *testing.T) {
	order := 3
	for _, tc := range []struct {
		name string
		run  func(*Service) (map[string]any, error)
	}{
		{"create", func(s *Service) (map[string]any, error) {
			got, err := s.CreateSection(context.Background(), CreateSectionInput{DisplayName: "Clients", DryRun: true})
			if err != nil {
				return nil, err
			}
			return got.Rendered, nil
		}},
		{"rename", func(s *Service) (map[string]any, error) {
			got, err := s.RenameSection(context.Background(), RenameSectionInput{
				Section: "users/me/sections/S", DisplayName: "Clients", DryRun: true,
			})
			if err != nil {
				return nil, err
			}
			return got.Rendered, nil
		}},
		{"position", func(s *Service) (map[string]any, error) {
			got, err := s.PositionSection(context.Background(), PositionSectionInput{
				Section: "users/me/sections/S", SortOrder: &order, DryRun: true,
			})
			if err != nil {
				return nil, err
			}
			if got.SortOrder != nil {
				t.Errorf("sort order = %v, want none: nothing moved", *got.SortOrder)
			}
			return got.Rendered, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := recorded(t, ok(`{}`))
			body, err := tc.run(s)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if rec.len() != 0 {
				t.Errorf("a dry run made %d requests", rec.len())
			}
			if len(body) == 0 {
				t.Error("a dry run must show the body it would have sent")
			}
		})
	}

	t.Run("delete", func(t *testing.T) {
		s, rec := recorded(t, ok(`{}`))
		got, err := s.DeleteSection(context.Background(), DeleteSectionInput{
			Section: "users/me/sections/S", DryRun: true,
		})
		if err != nil {
			t.Fatalf("DeleteSection: %v", err)
		}
		if rec.len() != 0 || got.Deleted || !got.DryRun {
			t.Errorf("%d requests, result = %+v", rec.len(), got)
		}
	})
}

// Google models the two positioning fields as a union and answers 400
// for both, so the refusal happens here where the argument names are
// still known.
func TestPositionSectionTakesOneWayOfSayingWhere(t *testing.T) {
	order, zero := 2, 0
	s, rec := recorded(t, ok(`{"section":{"sortOrder":2}}`))
	for _, tc := range []struct {
		name string
		in   PositionSectionInput
	}{
		{"neither", PositionSectionInput{Section: "users/me/sections/S"}},
		{"both", PositionSectionInput{Section: "users/me/sections/S", SortOrder: &order, Relative: PositionStart}},
		{"a rank below one", PositionSectionInput{Section: "users/me/sections/S", SortOrder: &zero}},
		{"a position that is not one", PositionSectionInput{Section: "users/me/sections/S", Relative: "MIDDLE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PositionSection(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

func TestPositionSectionReportsTheRankGoogleGives(t *testing.T) {
	s, rec := recorded(t, ok(`{"section":{"name":"users/123/sections/S","sortOrder":2}}`))
	got, err := s.PositionSection(context.Background(), PositionSectionInput{
		Section: "users/me/sections/S", Relative: PositionStart,
	})
	if err != nil {
		t.Fatalf("PositionSection: %v", err)
	}
	if got.SortOrder == nil || *got.SortOrder != 2 {
		t.Errorf("sort order = %v", got.SortOrder)
	}
	if sent := rec.last(t); sent.Body != `{"relativePosition":"START"}` {
		t.Errorf("body = %s", sent.Body)
	}
}

// The move has already landed by the time the rank is read, so a
// response without one must not turn it into an error a caller retries.
func TestPositionSectionSurvivesAResponseWithNoRank(t *testing.T) {
	s := newService(t, ok(`{}`))
	got, err := s.PositionSection(context.Background(), PositionSectionInput{
		Section: "users/me/sections/S", Relative: PositionEnd,
	})
	if err != nil {
		t.Fatalf("PositionSection: %v", err)
	}
	if got.SortOrder != nil {
		t.Errorf("sort order = %v, want none reported", *got.SortOrder)
	}
}

func TestMoveSpaceToSectionFilesASpace(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"sectionItem":{"name":"users/123/sections/T/items/spaces%2FA","space":"spaces/A"}}`)
			return
		}
		fmt.Fprint(w, `{"sectionItems":[{"name":"users/123/sections/S/items/spaces%2FA","space":"spaces/A"}]}`)
	}))
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T",
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if !got.Moved || got.From != "users/123/sections/S" {
		t.Errorf("result = %+v", got)
	}
	// The move renames the item, so the name this call arrived with now
	// answers 404 and returning it would break the bulk sort the tool
	// tells callers to build.
	if got.Item != "users/123/sections/T/items/spaces%2FA" {
		t.Errorf("item = %q, want the name after the move", got.Item)
	}
}

// The item's id survives a move, so a response this server cannot read
// still yields the right name rather than an error.
func TestMoveSpaceToSectionDerivesTheNewNameWhenGoogleGivesNone(t *testing.T) {
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`)
	})
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T",
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if got.Item != "users/123/sections/T/items/III" {
		t.Errorf("item = %q", got.Item)
	}
}

// A space already where it is wanted is not moved again. Google
// canonicalises users/me to a numeric id, so comparing the whole name
// would call every already-filed space a move and write to all of them.
func TestMoveSpaceToSectionIsANoOpWhenTheSpaceIsAlreadyFiled(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(ok(
		`{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`)))
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/me/sections/S",
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if got.Moved {
		t.Error("moved = true for a space that was already there")
	}
	for _, sent := range c.all() {
		if sent.Method == "POST" {
			t.Errorf("a move was written anyway: %s", sent.Path)
		}
	}
}

func TestSameSectionComparesTheSectionNotTheSpelling(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"users/me/sections/S", "users/123/sections/S", true},
		{"users/123/sections/S", "users/me/sections/S", true},
		{"users/123/sections/S", "users/123/sections/S", true},
		{"users/123/sections/S", "users/123/sections/T", false},
		// Two different people's sections are not the same section,
		// even with the same id.
		{"users/123/sections/S", "users/999/sections/S", false},
	} {
		if got := sameSection(tc.a, tc.b); got != tc.want {
			t.Errorf("sameSection(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// The hint exists to skip a lookup, so taking it must cost no request.
func TestMoveSpaceToSectionTrustsTheItemHint(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(ok(`{"sectionItem":{"name":"users/123/sections/T/items/III"}}`)))
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T", Item: "users/123/sections/S/items/III",
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if !got.Moved || got.From != "users/123/sections/S" {
		t.Errorf("result = %+v", got)
	}
	if c.len() != 1 {
		t.Errorf("%d requests, want only the move", c.len())
	}
}

// The lookup trusts a server-side filter and what follows is a write.
// If Google ever stops honouring the filter, the first row back is an
// arbitrary space and this would file it somewhere new.
func TestMoveSpaceToSectionRefusesAnItemForTheWrongSpace(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(ok(
		`{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/OTHER"}]}`)))
	_, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T",
	})
	if err == nil {
		t.Fatal("a lookup that answered about another space must not be acted on")
	}
	if !strings.Contains(err.Error(), "spaces/OTHER") {
		t.Errorf("error = %q, want it to name what came back", err)
	}
	for _, sent := range c.all() {
		if sent.Method == "POST" {
			t.Error("a move was written anyway")
		}
	}
}

func TestMoveSpaceToSectionReportsASpaceWithNoSidebarEntry(t *testing.T) {
	s := newService(t, ok(`{"sectionItems":[]}`))
	_, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T",
	})
	assertClass(t, err, ClassNotFound)
}

// A dry run reads where the space sits, because that is what the
// preview is for, and writes nothing.
func TestMoveSpaceToSectionDryRunLooksButDoesNotWrite(t *testing.T) {
	c := &calls{}
	s := newService(t, c.wrap(ok(
		`{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`)))
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/T", DryRun: true,
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if got.Moved || got.From != "users/123/sections/S" {
		t.Errorf("result = %+v", got)
	}
	if got.Rendered["targetSection"] != "users/123/sections/T" {
		t.Errorf("rendered = %v", got.Rendered)
	}
	for _, sent := range c.all() {
		if sent.Method != "GET" {
			t.Errorf("a dry run made a %s request", sent.Method)
		}
	}
}

// Moved false means both "skipped" and "dry run", so the rendered body
// is the only thing that tells them apart.
func TestAnAlreadyFiledDryRunShowsNoBody(t *testing.T) {
	s := newService(t, ok(`{"sectionItems":[{"name":"users/123/sections/S/items/III","space":"spaces/A"}]}`))
	got, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/123/sections/S", DryRun: true,
	})
	if err != nil {
		t.Fatalf("MoveSpaceToSection: %v", err)
	}
	if got.Rendered != nil {
		t.Errorf("rendered = %v, want none: there is nothing to preview", got.Rendered)
	}
}

func TestMoveSpaceToSectionRejectsBadInput(t *testing.T) {
	s, rec := recorded(t, ok(`{}`))
	for _, tc := range []struct {
		name string
		in   MoveSpaceToSectionInput
	}{
		{"no space", MoveSpaceToSectionInput{Section: "users/me/sections/S"}},
		{"no section", MoveSpaceToSectionInput{Space: "spaces/A"}},
		{"a section that is not one", MoveSpaceToSectionInput{Space: "spaces/A", Section: "spaces/B"}},
		{"an item that is not one", MoveSpaceToSectionInput{
			Space: "spaces/A", Section: "users/me/sections/S", Item: "users/me/sections/S",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.MoveSpaceToSection(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
	if rec.len() != 0 {
		t.Errorf("bad arguments reached Google %d times", rec.len())
	}
}

func TestSectionWritesNameTheirScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"create", func() error {
			_, err := s.CreateSection(context.Background(), CreateSectionInput{DisplayName: "Clients"})
			return err
		}},
		{"rename", func() error {
			_, err := s.RenameSection(context.Background(), RenameSectionInput{
				Section: "users/me/sections/S", DisplayName: "Clients",
			})
			return err
		}},
		{"delete", func() error {
			_, err := s.DeleteSection(context.Background(), DeleteSectionInput{Section: "users/me/sections/S"})
			return err
		}},
		{"position", func() error {
			_, err := s.PositionSection(context.Background(), PositionSectionInput{
				Section: "users/me/sections/S", Relative: PositionStart,
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertScope(t, tc.run(), scopes.UserSections) })
	}
}

// The lookup could get by on the read-only sections scope, but the move
// behind it cannot. Naming readonly would have the person grant it and
// be refused one call later.
func TestTheSectionItemLookupNamesTheWriteScope(t *testing.T) {
	s := newService(t, status(403,
		`{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`))
	_, err := s.MoveSpaceToSection(context.Background(), MoveSpaceToSectionInput{
		Space: "spaces/A", Section: "users/me/sections/S",
	})
	assertScope(t, err, scopes.UserSections)
}
