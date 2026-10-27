package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// spaceWorld answers the reads checkSpace makes: the space itself, the
// direct message with an address, the members, and who the caller is.
func spaceWorld(space, dmWith string, members ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/oidc/userinfo"):
			fmt.Fprint(w, `{"sub":"111"}`)
		case strings.HasSuffix(p, "spaces:findDirectMessage"):
			if r.URL.Query().Get("name") != "users/"+dmWith {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"no"}}`)
				return
			}
			fmt.Fprint(w, `{"name":"spaces/A","spaceType":"DIRECT_MESSAGE"}`)
		case strings.HasSuffix(p, "/members"):
			rows := make([]string, 0, len(members))
			for i, email := range members {
				rows = append(rows, fmt.Sprintf(`{"name":"spaces/A/members/%d","state":"JOINED",`+
					`"member":{"name":"users/%d","type":"HUMAN","email":%q}}`, i, 111+i*111, email))
			}
			fmt.Fprintf(w, `{"memberships":[%s]}`, strings.Join(rows, ","))
		default:
			fmt.Fprint(w, space)
		}
	}
}

// A write that names a space says which space it is, and the space is
// read to hold it: a named space by its name, a direct message by the
// other person's address, a group chat by the other members' addresses.
func TestCheckSpaceHoldsTheNameToTheSpace(t *testing.T) {
	const named = `{"name":"spaces/A","spaceType":"SPACE","displayName":"Team standup"}`
	const dm = `{"name":"spaces/A","spaceType":"DIRECT_MESSAGE"}`
	const group = `{"name":"spaces/A","spaceType":"GROUP_CHAT"}`
	for _, tc := range []struct {
		name, space, witness string
		dmWith               string
		members              []string
		wantErr              Class
	}{
		{name: "a named space by its name, in any case", space: named, witness: " team STANDUP "},
		{name: "a named space by another name", space: named, witness: "Team", wantErr: ClassStale},
		{name: "no name at all", space: named, witness: "  ", wantErr: ClassInvalid},
		{name: "a direct message by the other person", space: dm, witness: "jane@example.com", dmWith: "jane@example.com"},
		{name: "a direct message by someone else", space: dm, witness: "john@example.com", dmWith: "jane@example.com",
			wantErr: ClassStale},
		{name: "a direct message by its label", space: dm, witness: "(direct message)", dmWith: "jane@example.com",
			wantErr: ClassInvalid},
		{name: "a group chat by its other members, in any order", space: group, witness: "John@example.com, jane@example.com",
			members: []string{"me@example.com", "jane@example.com", "john@example.com"}},
		{name: "a group chat naming the caller too", space: group, witness: "me@example.com, jane@example.com, john@example.com",
			members: []string{"me@example.com", "jane@example.com", "john@example.com"}},
		{name: "a group chat missing someone", space: group, witness: "jane@example.com",
			members: []string{"me@example.com", "jane@example.com", "john@example.com"}, wantErr: ClassStale},
		{name: "a group chat naming a stranger", space: group, witness: "jane@example.com, john@example.com, eve@example.com",
			members: []string{"me@example.com", "jane@example.com", "john@example.com"}, wantErr: ClassStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(t, spaceWorld(tc.space, tc.dmWith, tc.members...))
			err := s.checkSpace(context.Background(), "spaces/A", tc.witness)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSpace: %v", err)
				}
				return
			}
			assertClass(t, err, tc.wantErr)
		})
	}
}

// A space that cannot be read is an error of its own class, not a pass.
func TestCheckSpaceRefusesWhatItCannotRead(t *testing.T) {
	s := newService(t, status(404, `{"error":{"code":404,"status":"NOT_FOUND","message":"no"}}`))
	assertClass(t, s.checkSpace(context.Background(), "spaces/A", "Team"), ClassNotFound)
}
