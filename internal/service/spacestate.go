package service

import (
	"context"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
)

// Pins, read state and notification settings.
//
// The read state and the notification setting belong to the caller
// rather than to the space, so two people reading the same space have
// different answers. Everything here says "you" for that reason.

// Pin limits.
const (
	defaultPinLimit = 50
	maxPinLimit     = gchat.MaxPinsPageSize
)

// Pin is one pinned message.
type Pin struct {
	// Name is the pin, "spaces/{s}/messagePins/{p}".
	Name string
	// Message is what it points at.
	Message string
}

// ListPinsInput selects a page of a space's pins.
type ListPinsInput struct {
	Space     string
	Limit     int
	PageToken string
}

// ListPinsResult is a page of pins.
type ListPinsResult struct {
	Pins          []Pin
	NextPageToken string
}

// ListPins returns the messages pinned in a space.
func (s *Service) ListPins(ctx context.Context, in ListPinsInput) (*ListPinsResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	limit, err := clampLimit("limit", in.Limit, defaultPinLimit, maxPinLimit)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.ListPins(ctx, gchat.ListPinsOptions{
		Space: space, PageSize: limit, PageToken: in.PageToken,
	})
	if err != nil {
		return nil, Classify(err)
	}
	out := &ListPinsResult{
		Pins:          make([]Pin, 0, len(resp.MessagePins)),
		NextPageToken: resp.NextPageToken,
	}
	for _, p := range resp.MessagePins {
		out.Pins = append(out.Pins, Pin{Name: p.Name, Message: p.Message})
	}
	return out, nil
}

// PinInput names a message to pin or unpin.
type PinInput struct {
	Message string
	DryRun  bool
}

// PinResult says what happened.
type PinResult struct {
	// Name is the pin, empty after a dry run.
	Name    string
	Message string
	// Pinned and Unpinned are false after a dry run, and Unpinned is
	// false when the message was not pinned to begin with.
	Pinned   bool
	Unpinned bool
	DryRun   bool
	Rendered map[string]any
}

// PinMessage pins a message in its own space.
//
// The space is taken from the message rather than asked for: a pin
// belongs to the space the message is in, and a second argument that
// has to agree with the first is a second way to be wrong.
func (s *Service) PinMessage(ctx context.Context, in PinInput) (*PinResult, error) {
	message, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	space := messageSpace(message)

	body := &gchat.MessagePin{Message: message}
	out := &PinResult{Message: message, DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		return out, nil
	}

	pin, err := s.client.PinMessage(ctx, space, message)
	if err != nil {
		if gchat.IsAlreadyExists(err) {
			return nil, Failf(ClassInvalid, "%s is already pinned.", message)
		}
		return nil, Classify(err)
	}
	out.Name, out.Pinned = pin.Name, true
	return out, nil
}

// UnpinMessage removes a message's pin.
//
// The pin's id is the message's id — Google says so in the reference —
// so a caller who can name the message can unpin it without looking the
// pin up first.
func (s *Service) UnpinMessage(ctx context.Context, in PinInput) (*PinResult, error) {
	message, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}
	pin := pinName(message)

	out := &PinResult{Name: pin, Message: message, DryRun: in.DryRun}
	if in.DryRun {
		return out, nil
	}
	unpinned, err := deleteIdempotent(ctx, pin, s.client.UnpinMessage, nil)
	if err != nil {
		return nil, err
	}
	out.Unpinned = unpinned
	return out, nil
}

// ReadState is how far the caller has read.
type ReadState struct {
	// Name is the read state's own resource name.
	Name string
	// LastReadTime is zero when Google has recorded none, which is what
	// a space nobody has opened looks like.
	LastReadTime time.Time
}

// GetSpaceReadState returns how far the caller has read in a space.
func (s *Service) GetSpaceReadState(ctx context.Context, space string) (*ReadState, error) {
	name, err := requireSpace(space)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetSpaceReadState(ctx, name)
	if err != nil {
		return nil, Classify(err)
	}
	return &ReadState{Name: got.Name, LastReadTime: parseTime(got.LastReadTime)}, nil
}

// GetThreadReadState returns how far the caller has read in a thread.
func (s *Service) GetThreadReadState(ctx context.Context, space, thread string) (*ReadState, error) {
	parent, err := requireSpace(space)
	if err != nil {
		return nil, err
	}
	name, err := requireThread(parent, thread)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetThreadReadState(ctx, name)
	if err != nil {
		return nil, Classify(err)
	}
	return &ReadState{Name: got.Name, LastReadTime: parseTime(got.LastReadTime)}, nil
}

// MarkReadInput moves the caller's read mark in a space.
type MarkReadInput struct {
	Space string
	// From is required when marking unread, and is the time to rewind
	// to: every top-level message after it becomes unread. Ignored when
	// marking read.
	From   string
	DryRun bool
}

// MarkReadResult is where the mark now stands.
type MarkReadResult struct {
	Name string
	// LastReadTime is what Google recorded, which is not always what
	// was asked for: marking read is coerced back to the newest
	// message's own time.
	LastReadTime time.Time
	Unread       bool
	DryRun       bool
}

// MarkSpaceRead marks a space as read up to now.
//
// Google coerces a time later than the newest message back to that
// message's own time, so "now" is the honest way to say "all of it"
// without inventing a future timestamp.
func (s *Service) MarkSpaceRead(ctx context.Context, in MarkReadInput) (*MarkReadResult, error) {
	// time.Now, not a fixed future date: Google coerces anything later
	// than the newest message back to that message's own time, so a
	// far-future value would be recorded as the same thing while
	// reading like a mistake.
	return s.setReadState(ctx, in, time.Now(), false)
}

// MarkSpaceUnread rewinds the caller's read mark, so everything posted
// after the given time is unread again.
//
// A time is required. Google's rule is that the mark sits before the
// message you want unread, and only this caller can know which message
// that is — get_messages reports each one's timestamp.
func (s *Service) MarkSpaceUnread(ctx context.Context, in MarkReadInput) (*MarkReadResult, error) {
	from, err := parseArgTime("from_time", in.From)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		return nil, Invalidf("from_time is required: it is the point to rewind to, and every top-level " +
			"message after it becomes unread. get_messages reports each message's timestamp")
	}
	return s.setReadState(ctx, in, from, true)
}

// setReadState is the half the two share.
func (s *Service) setReadState(ctx context.Context, in MarkReadInput, at time.Time, unread bool) (*MarkReadResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	out := &MarkReadResult{LastReadTime: at, Unread: unread, DryRun: in.DryRun}
	if in.DryRun {
		return out, nil
	}
	got, err := s.client.UpdateSpaceReadState(ctx, space, at.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, Classify(err)
	}
	out.Name = got.Name
	if recorded := parseTime(got.LastReadTime); !recorded.IsZero() {
		out.LastReadTime = recorded
	}
	return out, nil
}

// Notification settings, narrowed to Google's own vocabulary.
var (
	notificationSettings = []string{"ALL", "MAIN_CONVERSATIONS", "FOR_YOU", "OFF"}
	muteSettings         = []string{"UNMUTED", "MUTED"}
)

// NotificationSetting is the caller's notification choice for a space.
type NotificationSetting struct {
	Name string
	// Notify is ALL, MAIN_CONVERSATIONS, FOR_YOU or OFF.
	Notify string
	// Mute is MUTED or UNMUTED, and overrides Notify when it is MUTED.
	Mute string
}

// GetSpaceNotificationSetting returns the caller's choice for a space.
func (s *Service) GetSpaceNotificationSetting(ctx context.Context, space string) (*NotificationSetting, error) {
	name, err := requireSpace(space)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetSpaceNotificationSetting(ctx, name)
	if err != nil {
		return nil, Classify(err)
	}
	return notificationOf(got), nil
}

// UpdateNotificationInput changes one or both settings.
type UpdateNotificationInput struct {
	Space string
	// Notify and Mute are empty for "leave this alone". Sending a mask
	// for a field with no value would clear it.
	Notify string
	Mute   string
	DryRun bool
}

// UpdateNotificationResult is the setting as it now stands.
type UpdateNotificationResult struct {
	*NotificationSetting
	DryRun   bool
	Rendered map[string]any
}

// UpdateSpaceNotificationSetting changes what Chat tells the caller
// about a space.
func (s *Service) UpdateSpaceNotificationSetting(
	ctx context.Context, in UpdateNotificationInput,
) (*UpdateNotificationResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}

	body := &gchat.SpaceNotificationSetting{}
	var mask []string
	if in.Notify != "" {
		if err := requireEnum("notification_setting", in.Notify, notificationSettings...); err != nil {
			return nil, err
		}
		body.NotificationSetting, mask = in.Notify, append(mask, "notificationSetting")
	}
	if in.Mute != "" {
		if err := requireEnum("mute_setting", in.Mute, muteSettings...); err != nil {
			return nil, err
		}
		body.MuteSetting, mask = in.Mute, append(mask, "muteSetting")
	}
	if len(mask) == 0 {
		return nil, Invalidf("pass notification_setting, mute_setting, or both")
	}

	out := &UpdateNotificationResult{DryRun: in.DryRun}
	if in.DryRun {
		rendered, err := renderBody(body)
		if err != nil {
			return nil, err
		}
		out.Rendered = rendered
		out.NotificationSetting = &NotificationSetting{Notify: in.Notify, Mute: in.Mute}
		return out, nil
	}

	got, err := s.client.UpdateSpaceNotificationSetting(ctx, space, body, strings.Join(mask, ","))
	if err != nil {
		return nil, Classify(err)
	}
	out.NotificationSetting = notificationOf(got)
	return out, nil
}

// notificationOf shapes a setting, degrading a value this server has
// not learned rather than failing the answer it arrived on.
func notificationOf(got *gchat.SpaceNotificationSetting) *NotificationSetting {
	return &NotificationSetting{
		Name:   got.Name,
		Notify: narrowEnum(got.NotificationSetting, notificationSettings, "NOTIFICATION_SETTING_UNSPECIFIED"),
		Mute:   narrowEnum(got.MuteSetting, muteSettings, "MUTE_SETTING_UNSPECIFIED"),
	}
}

// messageSpace is the space a message belongs to.
func messageSpace(message string) string {
	space, _, _ := strings.Cut(message, "/messages/")
	return space
}

// pinName is the pin that would hold this message. Google assigns the
// pin the message's own id, which the reference states outright.
func pinName(message string) string {
	space, id, _ := strings.Cut(message, "/messages/")
	return space + "/messagePins/" + id
}
