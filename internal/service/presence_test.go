package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Do not disturb is the one state Google requires an expiry for, and
// the arguments that carry one make no sense for the others.
func TestSetAvailabilityChecksTheExpiry(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, tc := range []struct {
		name string
		in   SetAvailabilityInput
	}{
		{"do not disturb with no expiry", SetAvailabilityInput{State: "DO_NOT_DISTURB"}},
		{"both kinds of expiry", SetAvailabilityInput{State: "DO_NOT_DISTURB", Minutes: 30, Until: "2026-02-01T00:00:00Z"}},
		{"an expiry on away", SetAvailabilityInput{State: "AWAY", Minutes: 30}},
		{"more than a year", SetAvailabilityInput{State: "DO_NOT_DISTURB", Minutes: 600000}},
		{"a state Google decides", SetAvailabilityInput{State: "IDLE"}},
		{"no state", SetAvailabilityInput{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SetAvailability(context.Background(), tc.in)
			assertClass(t, err, ClassInvalid)
		})
	}
}

// Each state has its own endpoint, and minutes become the duration
// Google's own type wants.
func TestSetAvailabilityCallsTheRightVerb(t *testing.T) {
	for _, tc := range []struct {
		in   SetAvailabilityInput
		path string
		ttl  string
	}{
		{SetAvailabilityInput{State: "ACTIVE"}, "/v1/users/me/availability:markAsActive", ""},
		{SetAvailabilityInput{State: "AWAY"}, "/v1/users/me/availability:markAsAway", ""},
		{SetAvailabilityInput{State: "DO_NOT_DISTURB", Minutes: 30}, "/v1/users/me/availability:markAsDoNotDisturb", "1800s"},
	} {
		t.Run(tc.in.State, func(t *testing.T) {
			var path string
			var body map[string]any
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&body)
				fmt.Fprint(w, `{"name":"users/123/availability","state":"`+tc.in.State+`"}`)
			})
			got, err := s.SetAvailability(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("SetAvailability: %v", err)
			}
			if path != tc.path {
				t.Errorf("path = %q, want %q", path, tc.path)
			}
			if tc.ttl != "" && body["ttl"] != tc.ttl {
				t.Errorf("ttl = %v, want %q", body["ttl"], tc.ttl)
			}
			if got.State != tc.in.State {
				t.Errorf("state = %q", got.State)
			}
		})
	}
}

// The custom status fields are the ones the reference names, and the
// two expiries are spelled differently on purpose.
func TestAvailabilityReadsTheStatusFields(t *testing.T) {
	s := newService(t, ok(`{"name":"users/123/availability","state":"DO_NOT_DISTURB",
	  "customStatus":{"text":"heads down","emoji":{"unicode":"🎧"},"expireTime":"2026-01-02T03:04:05Z"},
	  "doNotDisturbMetadata":{"expirationTime":"2026-01-02T04:00:00Z"}}`))
	got, err := s.GetAvailability(context.Background())
	if err != nil {
		t.Fatalf("GetAvailability: %v", err)
	}
	if got.StatusText != "heads down" || got.StatusEmoji != "🎧" {
		t.Errorf("status = %+v", got)
	}
	if got.StatusExpires.IsZero() || got.DoNotDisturbUntil.IsZero() {
		t.Errorf("times = %+v, want both read", got)
	}
}

// The image is checked before anything is sent: a rejection after a
// 256 KB upload is a slow way to learn a filename was wrong.
func TestCreateCustomEmojiChecksTheImageFirst(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "parrot.png")
	if err := os.WriteFile(good, []byte("not really a png, but small"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, make([]byte, 300*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongType := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(wrongType, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTransferService(t, dir, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	outside := filepath.Join(t.TempDir(), "elsewhere.png")
	if err := os.WriteFile(outside, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		in    CreateCustomEmojiInput
		class Class
	}{
		{"a name without colons", CreateCustomEmojiInput{EmojiName: "parrot", ImagePath: good}, ClassInvalid},
		{"a name with capitals", CreateCustomEmojiInput{EmojiName: ":Parrot:", ImagePath: good}, ClassInvalid},
		{"no image", CreateCustomEmojiInput{EmojiName: ":parrot:"}, ClassInvalid},
		{"a file that is not an image", CreateCustomEmojiInput{EmojiName: ":parrot:", ImagePath: wrongType}, ClassInvalid},
		{"a file that is too big", CreateCustomEmojiInput{EmojiName: ":parrot:", ImagePath: big}, ClassInvalid},
		{"a directory", CreateCustomEmojiInput{EmojiName: ":parrot:", ImagePath: dir}, ClassInvalid},
		{"a file that is not there", CreateCustomEmojiInput{EmojiName: ":parrot:",
			ImagePath: filepath.Join(dir, "nope.png")}, ClassNotFound},
		// The same rule the attachment tools follow: this tool used to
		// read any path on the machine and send it to Google.
		{"a file outside the local directory", CreateCustomEmojiInput{EmojiName: ":parrot:",
			ImagePath: outside}, ClassForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateCustomEmoji(context.Background(), tc.in)
			assertClass(t, err, tc.class)
		})
	}
}

// The image travels base64-encoded inside the JSON body, because Google
// has no upload endpoint for an emoji.
func TestCreateCustomEmojiSendsTheImageInline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parrot.png")
	if err := os.WriteFile(path, []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	s := newTransferService(t, dir, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"customEmojis/AAAAemoji1","emojiName":":parrot:"}`)
	})
	got, err := s.CreateCustomEmoji(context.Background(), CreateCustomEmojiInput{
		EmojiName: ":parrot:", ImagePath: path,
	})
	if err != nil {
		t.Fatalf("CreateCustomEmoji: %v", err)
	}
	payload, _ := body["payload"].(map[string]any)
	if payload == nil || payload["filename"] != "parrot.png" || payload["fileContent"] != "aW1hZ2UgYnl0ZXM=" {
		t.Errorf("payload = %v", payload)
	}
	if got.Name != "customEmojis/AAAAemoji1" || got.Bytes != len("image bytes") {
		t.Errorf("result = %+v", got)
	}
}

// Deleting a space destroys everyone's messages, so it asks twice and
// refuses a confirmation that does not match.
func TestDeleteSpaceAsksTwice(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an unconfirmed delete must not reach Google")
	})
	for _, in := range []DeleteSpaceInput{
		{Space: "spaces/AAAAspace1"},
		{Space: "spaces/AAAAspace1", Confirm: "yes"},
		{Space: "spaces/AAAAspace1", Confirm: "spaces/AAAAspace2"},
	} {
		_, err := s.DeleteSpace(context.Background(), in)
		assertClass(t, err, ClassInvalid)
	}

	var method, path string
	live := newService(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		fmt.Fprint(w, `{}`)
	})
	got, err := live.DeleteSpace(context.Background(), DeleteSpaceInput{
		Space: "spaces/AAAAspace1", Confirm: "spaces/AAAAspace1",
	})
	if err != nil {
		t.Fatalf("DeleteSpace: %v", err)
	}
	if method != http.MethodDelete || path != "/v1/spaces/AAAAspace1" {
		t.Errorf("%s %s", method, path)
	}
	if !got.Deleted {
		t.Errorf("result = %+v", got)
	}
}
