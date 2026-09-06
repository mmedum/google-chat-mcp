package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A pin belongs to the space its message is in, so the space is taken
// from the message rather than asked for twice.
func TestPinMessageTakesTheSpaceFromTheMessage(t *testing.T) {
	var path string
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messagePins/AAAAmsg1","message":"spaces/AAAAspace1/messages/AAAAmsg1"}`)
	})
	got, err := s.PinMessage(context.Background(), PinInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"})
	if err != nil {
		t.Fatalf("PinMessage: %v", err)
	}
	if path != "/v1/spaces/AAAAspace1/messagePins" {
		t.Errorf("path = %q", path)
	}
	if body["message"] != "spaces/AAAAspace1/messages/AAAAmsg1" {
		t.Errorf("body = %v", body)
	}
	if !got.Pinned || got.Name == "" {
		t.Errorf("result = %+v", got)
	}
}

// Google gives a pin the message's own id, so unpinning needs no
// lookup — and a message that was not pinned is a no-op, not a failure.
func TestUnpinAddressesThePinByTheMessageID(t *testing.T) {
	var path, method string
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		path, method = r.URL.Path, r.Method
		fmt.Fprint(w, `{}`)
	})
	got, err := s.UnpinMessage(context.Background(), PinInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"})
	if err != nil {
		t.Fatalf("UnpinMessage: %v", err)
	}
	if method != http.MethodDelete || path != "/v1/spaces/AAAAspace1/messagePins/AAAAmsg1" {
		t.Errorf("%s %s", method, path)
	}
	if !got.Unpinned {
		t.Errorf("result = %+v", got)
	}

	gone := newService(t, status(404, `{"error":{"status":"NOT_FOUND","message":"no such pin"}}`))
	out, err := gone.UnpinMessage(context.Background(), PinInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"})
	if err != nil {
		t.Fatalf("a message that was not pinned is not a failure: %v", err)
	}
	if out.Unpinned {
		t.Error("nothing was unpinned, and the answer should say so")
	}
}

// The read state and the notification setting hang off the caller, not
// off the space: users/me/spaces/{id}/...
func TestThePerPersonResourcesNestUnderTheCaller(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Service) error
		want string
	}{
		{"space read state", func(s *Service) error {
			_, err := s.GetSpaceReadState(context.Background(), "spaces/AAAAspace1")
			return err
		}, "/v1/users/me/spaces/AAAAspace1/spaceReadState"},
		{"thread read state", func(s *Service) error {
			_, err := s.GetThreadReadState(context.Background(),
				"spaces/AAAAspace1", "spaces/AAAAspace1/threads/AAAAthread1")
			return err
		}, "/v1/users/me/spaces/AAAAspace1/threads/AAAAthread1/threadReadState"},
		{"notification setting", func(s *Service) error {
			_, err := s.GetSpaceNotificationSetting(context.Background(), "spaces/AAAAspace1")
			return err
		}, "/v1/users/me/spaces/AAAAspace1/spaceNotificationSetting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				fmt.Fprint(w, `{"name":"x","lastReadTime":"2026-01-02T03:04:05Z","notificationSetting":"ALL","muteSetting":"UNMUTED"}`)
			})
			if err := tc.call(s); err != nil {
				t.Fatalf("call: %v", err)
			}
			if path != tc.want {
				t.Errorf("path = %q, want %q", path, tc.want)
			}
		})
	}
}

// Marking read sends a time and reports what Google recorded, which is
// pulled back to the newest message rather than kept as sent.
func TestMarkSpaceReadReportsWhatGoogleRecorded(t *testing.T) {
	var mask string
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		mask = r.URL.Query().Get("updateMask")
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"users/123/spaces/AAAAspace1/spaceReadState","lastReadTime":"2026-01-02T03:04:05Z"}`)
	})
	got, err := s.MarkSpaceRead(context.Background(), MarkReadInput{Space: "spaces/AAAAspace1"})
	if err != nil {
		t.Fatalf("MarkSpaceRead: %v", err)
	}
	if mask != "lastReadTime" {
		t.Errorf("updateMask = %q", mask)
	}
	if body["lastReadTime"] == nil {
		t.Error("no lastReadTime was sent")
	}
	if !got.LastReadTime.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("last read = %s, want what Google recorded", got.LastReadTime)
	}
	if got.Unread {
		t.Error("marking read is not marking unread")
	}
}

// Rewinding needs the point to rewind to, and only the caller knows
// which message that is.
func TestMarkSpaceUnreadNeedsATime(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	_, err := s.MarkSpaceUnread(context.Background(), MarkReadInput{Space: "spaces/AAAAspace1"})
	assertClass(t, err, ClassInvalid)
	_, err = s.MarkSpaceUnread(context.Background(), MarkReadInput{
		Space: "spaces/AAAAspace1", From: "last tuesday",
	})
	assertClass(t, err, ClassInvalid)
}

// The mask names what was asked for and nothing else: a mask for a
// field left empty would clear it.
func TestNotificationUpdateMasksOnlyWhatWasAsked(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   UpdateNotificationInput
		want string
	}{
		{"one field", UpdateNotificationInput{Space: "spaces/AAAAspace1", Mute: "MUTED"}, "muteSetting"},
		{
			"both", UpdateNotificationInput{Space: "spaces/AAAAspace1", Notify: "OFF", Mute: "MUTED"},
			"notificationSetting,muteSetting",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mask string
			s := newService(t, func(w http.ResponseWriter, r *http.Request) {
				mask = r.URL.Query().Get("updateMask")
				fmt.Fprint(w, `{"name":"x","notificationSetting":"OFF","muteSetting":"MUTED"}`)
			})
			if _, err := s.UpdateSpaceNotificationSetting(context.Background(), tc.in); err != nil {
				t.Fatalf("update: %v", err)
			}
			if mask != tc.want {
				t.Errorf("updateMask = %q, want %q", mask, tc.want)
			}
		})
	}
}

// Neither setting is a call with nothing to do, and a value Google does
// not have is the caller's mistake.
func TestNotificationUpdateChecksItsArguments(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, in := range []UpdateNotificationInput{
		{Space: "spaces/AAAAspace1"},
		{Space: "spaces/AAAAspace1", Notify: "SOMETIMES"},
		{Space: "spaces/AAAAspace1", Mute: "SILENCED"},
	} {
		_, err := s.UpdateSpaceNotificationSetting(context.Background(), in)
		assertClass(t, err, ClassInvalid)
	}
}

// A value Google adds later must not fail the answer it arrived on.
func TestNotificationEnumsDegrade(t *testing.T) {
	s := newService(t, ok(`{"name":"x","notificationSetting":"WHEN_THE_MOON_IS_FULL","muteSetting":"SORT_OF"}`))
	got, err := s.GetSpaceNotificationSetting(context.Background(), "spaces/AAAAspace1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Notify != "NOTIFICATION_SETTING_UNSPECIFIED" || got.Mute != "MUTE_SETTING_UNSPECIFIED" {
		t.Errorf("setting = %+v, want the unknown values narrowed", got)
	}
}

// Every write here has a dry run, and a dry run reaches nothing.
func TestSpaceStateDryRunsSendNothing(t *testing.T) {
	s := newService(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run must not reach Google")
	})
	if _, err := s.PinMessage(context.Background(), PinInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1", DryRun: true,
	}); err != nil {
		t.Errorf("pin: %v", err)
	}
	if _, err := s.UnpinMessage(context.Background(), PinInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1", DryRun: true,
	}); err != nil {
		t.Errorf("unpin: %v", err)
	}
	if _, err := s.MarkSpaceRead(context.Background(), MarkReadInput{
		Space: "spaces/AAAAspace1", DryRun: true,
	}); err != nil {
		t.Errorf("mark read: %v", err)
	}
	got, err := s.UpdateSpaceNotificationSetting(context.Background(), UpdateNotificationInput{
		Space: "spaces/AAAAspace1", Mute: "MUTED", DryRun: true,
	})
	if err != nil {
		t.Errorf("notification: %v", err)
	}
	if got != nil && got.Rendered["muteSetting"] != "MUTED" {
		t.Errorf("dry run = %+v, want the body it would have sent", got)
	}
}
