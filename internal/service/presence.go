package service

import (
	"context"
	"encoding/base64"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mmedum/google-chat-mcp/v2/internal/gchat"
)

// Availability, custom emoji and deleting a space.

// Presence is the caller's own availability.
type Presence struct {
	State string
	// StatusText and StatusEmoji are the custom status beside the name,
	// empty when there is none.
	StatusText  string
	StatusEmoji string
	// StatusExpires and DoNotDisturbUntil are zero when Google sent no
	// time, which is what "no expiry" looks like.
	StatusExpires     time.Time
	DoNotDisturbUntil time.Time
}

// presenceStates is Google's vocabulary, narrowed.
var presenceStates = []string{
	gchat.StateActive, gchat.StateIdle, gchat.StateAway, gchat.StateDoNotDisturb,
}

// GetAvailability returns the caller's own presence.
//
// Self only: Google's endpoint answers for the authenticated user, and
// there is no shape here for asking about anyone else.
func (s *Service) GetAvailability(ctx context.Context) (*Presence, error) {
	got, err := s.client.GetAvailability(ctx)
	if err != nil {
		return nil, Classify(err)
	}
	return presenceOf(got), nil
}

// SetAvailabilityInput asks for a presence state.
type SetAvailabilityInput struct {
	// State is ACTIVE, AWAY or DO_NOT_DISTURB. IDLE is Google's to
	// report, not a caller's to set.
	State string
	// Until and Minutes are the do-not-disturb expiry, exactly one of
	// them, and only for that state. Google requires one and allows at
	// most a year.
	Until   string
	Minutes int
	DryRun  bool
}

// SetAvailabilityResult is the presence as it now stands.
type SetAvailabilityResult struct {
	*Presence
	DryRun bool
}

// maxDoNotDisturb is Google's cap on how far out an expiry may sit.
const maxDoNotDisturb = 365 * 24 * time.Hour

// SetAvailability sets the caller's own presence.
func (s *Service) SetAvailability(ctx context.Context, in SetAvailabilityInput) (*SetAvailabilityResult, error) {
	if err := requireEnum("state", in.State, gchat.StateActive, gchat.StateAway, gchat.StateDoNotDisturb); err != nil {
		return nil, err
	}
	body, err := doNotDisturbBody(in)
	if err != nil {
		return nil, err
	}

	if in.DryRun {
		return &SetAvailabilityResult{Presence: &Presence{State: in.State}, DryRun: true}, nil
	}

	var got *gchat.Availability
	switch in.State {
	case gchat.StateActive:
		got, err = s.client.MarkActive(ctx)
	case gchat.StateAway:
		got, err = s.client.MarkAway(ctx)
	default:
		got, err = s.client.MarkDoNotDisturb(ctx, body)
	}
	if err != nil {
		return nil, Classify(err)
	}
	return &SetAvailabilityResult{Presence: presenceOf(got)}, nil
}

// doNotDisturbBody works out the expiry, and refuses the arguments that
// only make sense for a state the caller did not ask for.
func doNotDisturbBody(in SetAvailabilityInput) (*gchat.DNDRequest, error) {
	asked := in.Until != "" || in.Minutes > 0
	if in.State != gchat.StateDoNotDisturb {
		if asked {
			return nil, Invalidf("until and minutes belong to DO_NOT_DISTURB; %s takes neither", in.State)
		}
		return nil, nil
	}
	switch {
	case in.Until != "" && in.Minutes > 0:
		return nil, Invalidf("pass until or minutes, not both")
	case in.Until != "":
		at, err := parseArgTime("until", in.Until)
		if err != nil {
			return nil, err
		}
		if until := time.Until(at); until > maxDoNotDisturb {
			return nil, Invalidf("until is more than a year away, which Google refuses")
		}
		return &gchat.DNDRequest{ExpireTime: at.UTC().Format(time.RFC3339)}, nil
	case in.Minutes > 0:
		if time.Duration(in.Minutes)*time.Minute > maxDoNotDisturb {
			return nil, Invalidf("minutes is more than a year, which Google refuses")
		}
		return &gchat.DNDRequest{TTL: googleDuration(in.Minutes)}, nil
	default:
		return nil, Invalidf("DO_NOT_DISTURB needs an expiry: pass minutes, or until as a timestamp. " +
			"Google requires one and allows at most a year")
	}
}

// SetCustomStatusInput is the text and emoji beside the caller's name.
type SetCustomStatusInput struct {
	// Text is what the status says. Empty with Clear set removes it.
	Text string
	// Emoji is the Unicode character beside the text. Google requires
	// one alongside the text and refuses a custom emoji here.
	Emoji string
	// Until and Minutes are an optional expiry, at most one of them.
	Until   string
	Minutes int
	// Clear removes the status instead of setting one.
	Clear  bool
	DryRun bool
}

// SetCustomStatusResult is the presence as it now stands.
type SetCustomStatusResult struct {
	*Presence
	Cleared  bool
	DryRun   bool
	Rendered map[string]any
}

// SetCustomStatus writes or clears the caller's own custom status.
//
// Google requires the text and the emoji together, so both are asked
// for and neither alone is accepted; the alternative is a 400 that says
// less than this does. The expiry is optional, unlike do-not-disturb's.
func (s *Service) SetCustomStatus(ctx context.Context, in SetCustomStatusInput) (*SetCustomStatusResult, error) {
	status, err := customStatusBody(in)
	if err != nil {
		return nil, err
	}

	if in.DryRun {
		out := &SetCustomStatusResult{Presence: &Presence{}, Cleared: in.Clear, DryRun: true}
		if out.Rendered, err = renderBody(gchat.Availability{CustomStatus: status}); err != nil {
			return nil, err
		}
		return out, nil
	}

	got, err := s.client.SetCustomStatus(ctx, status)
	if err != nil {
		return nil, Classify(err)
	}
	return &SetCustomStatusResult{Presence: presenceOf(got), Cleared: in.Clear}, nil
}

// customStatusBody turns the arguments into what Google takes, or says
// why they are not enough.
func customStatusBody(in SetCustomStatusInput) (*gchat.CustomStatus, error) {
	text := strings.TrimSpace(in.Text)
	emoji := strings.TrimSpace(in.Emoji)

	if in.Clear {
		if text != "" || emoji != "" || in.Until != "" || in.Minutes > 0 {
			return nil, Invalidf("clear removes the status; it takes no text, emoji or expiry")
		}
		// A nil status with the field named in the mask is how a field
		// mask spells "remove this".
		return nil, nil
	}
	switch {
	case text == "" && emoji == "":
		return nil, Invalidf("text and emoji are both required. To remove the status you have, pass clear")
	case text == "" || emoji == "":
		return nil, Invalidf("Google wants text and emoji together; one without the other is refused")
	case utf8.RuneCountInString(text) > gchat.MaxCustomStatusText:
		return nil, Invalidf("text is %d characters; Google's limit is %d",
			utf8.RuneCountInString(text), gchat.MaxCustomStatusText)
	case in.Until != "" && in.Minutes > 0:
		return nil, Invalidf("pass until or minutes, not both")
	}

	status := &gchat.CustomStatus{Text: text, Emoji: &gchat.Emoji{Unicode: emoji}}
	switch {
	case in.Until != "":
		at, err := parseArgTime("until", in.Until)
		if err != nil {
			return nil, err
		}
		if time.Until(at) > maxDoNotDisturb {
			return nil, Invalidf("until is more than a year away, which Google refuses")
		}
		status.ExpireTime = at.UTC().Format(time.RFC3339)
	case in.Minutes > 0:
		if time.Duration(in.Minutes)*time.Minute > maxDoNotDisturb {
			return nil, Invalidf("minutes is more than a year, which Google refuses")
		}
		status.TTL = googleDuration(in.Minutes)
	}
	return status, nil
}

// googleDuration renders minutes the way a protobuf Duration wants
// them: whole seconds with an "s".
func googleDuration(minutes int) string {
	return strconv.Itoa(minutes*60) + "s"
}

// presenceOf shapes an availability answer.
func presenceOf(got *gchat.Availability) *Presence {
	out := &Presence{State: narrowEnum(got.State, presenceStates, "STATE_UNSPECIFIED")}
	if got.CustomStatus != nil {
		out.StatusText = got.CustomStatus.Text
		out.StatusExpires = parseTime(got.CustomStatus.ExpireTime)
		if got.CustomStatus.Emoji != nil {
			out.StatusEmoji = got.CustomStatus.Emoji.Unicode
		}
	}
	if got.DoNotDisturb != nil {
		out.DoNotDisturbUntil = parseTime(got.DoNotDisturb.ExpirationTime)
	}
	return out
}

// Custom emoji.

// emojiNameShape is Google's rule, quoted in the reference: it starts
// and ends with colons, is lowercase, and holds letters, digits,
// hyphens and underscores.
var emojiNameShape = regexp.MustCompile(`^:[a-z0-9_-]+:$`)

// emojiImageTypes are the file types Google accepts.
var emojiImageTypes = []string{".png", ".jpg", ".jpeg", ".gif"}

// CustomEmoji is one of an organisation's own emoji.
type CustomEmoji struct {
	Name string
	// EmojiName is the :shortcode: form.
	EmojiName string
	// TemporaryURI is a link Google says is good for at least ten
	// minutes. It is not stored anywhere.
	TemporaryURI string
}

// ListCustomEmojisInput selects a page.
type ListCustomEmojisInput struct {
	// MineOnly narrows to the emoji this account created.
	MineOnly  bool
	Limit     int
	PageToken string
}

// ListCustomEmojisResult is a page of emoji.
type ListCustomEmojisResult struct {
	Emojis        []CustomEmoji
	NextPageToken string
}

// Emoji listing limits.
const (
	defaultEmojiLimit = 50
	maxEmojiLimit     = 200
)

// ListCustomEmojis returns the organisation's own emoji.
func (s *Service) ListCustomEmojis(ctx context.Context, in ListCustomEmojisInput) (*ListCustomEmojisResult, error) {
	limit, err := clampLimit("limit", in.Limit, defaultEmojiLimit, maxEmojiLimit)
	if err != nil {
		return nil, err
	}
	opts := gchat.ListCustomEmojisOptions{PageSize: limit, PageToken: in.PageToken}
	if in.MineOnly {
		opts.Filter = `creator("users/me")`
	}
	resp, err := s.client.ListCustomEmojis(ctx, opts)
	if err != nil {
		return nil, Classify(err)
	}
	out := &ListCustomEmojisResult{
		Emojis:        make([]CustomEmoji, 0, len(resp.CustomEmojis)),
		NextPageToken: resp.NextPageToken,
	}
	for _, e := range resp.CustomEmojis {
		out.Emojis = append(out.Emojis, emojiOfWire(e))
	}
	return out, nil
}

// GetCustomEmoji returns one by resource name.
func (s *Service) GetCustomEmoji(ctx context.Context, name string) (*CustomEmoji, error) {
	emoji, err := requireCustomEmoji("name", name)
	if err != nil {
		return nil, err
	}
	got, err := s.client.GetCustomEmoji(ctx, emoji)
	if err != nil {
		return nil, Classify(err)
	}
	shaped := emojiOfWire(*got)
	return &shaped, nil
}

// CreateCustomEmojiInput names the emoji and the image to make it from.
type CreateCustomEmojiInput struct {
	// EmojiName is the :shortcode: form, colons included.
	EmojiName string
	// ImagePath is a file on this machine.
	ImagePath string
	DryRun    bool
}

// CreateCustomEmojiResult is what was made.
type CreateCustomEmojiResult struct {
	*CustomEmoji
	// Bytes is the image's size, which a dry run reports so a caller
	// learns about the cap before sending anything.
	Bytes  int
	DryRun bool
}

// CreateCustomEmoji adds an emoji to the organisation from a local
// image.
//
// The image is read here and travels base64-encoded inside the JSON
// body: Google has no upload endpoint for this. Everything the
// reference states about the file is checked before anything is sent —
// the type, and the 256 KB cap — because a rejection after a 256 KB
// upload is a slow way to learn a filename was wrong.
func (s *Service) CreateCustomEmoji(ctx context.Context, in CreateCustomEmojiInput) (*CreateCustomEmojiResult, error) {
	if !emojiNameShape.MatchString(strings.TrimSpace(in.EmojiName)) {
		return nil, Invalidf("emoji_name must start and end with a colon and hold only lowercase letters, " +
			"digits, hyphens and underscores, as in :party-parrot:")
	}
	name := strings.TrimSpace(in.EmojiName)

	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(in.ImagePath)))
	if !slices.Contains(emojiImageTypes, ext) {
		return nil, Invalidf("image_path must name a %s file; Google accepts no others",
			strings.Join(emojiImageTypes, ", "))
	}

	// The same local-file door the attachment tools go through: inside
	// GCM_LOCAL_DIR, symlinks resolved. This used to read any path the
	// caller named, which made one tool able to send any file on the
	// machine to Google while its neighbours could not.
	//
	// The size is checked from the stat, so an enormous file is refused
	// rather than pulled into memory to be refused afterwards.
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	file, info, err := files.Open("image_path", in.ImagePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if info.Size() > gchat.MaxCustomEmojiBytes {
		return nil, Invalidf("the image is %d bytes; Google's limit is %d",
			info.Size(), gchat.MaxCustomEmojiBytes)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, Invalidf("image_path: %v", err)
	}

	out := &CreateCustomEmojiResult{
		CustomEmoji: &CustomEmoji{EmojiName: name},
		Bytes:       len(raw),
		DryRun:      in.DryRun,
	}
	if in.DryRun {
		return out, nil
	}

	got, err := s.client.CreateCustomEmoji(ctx, &gchat.CustomEmoji{
		EmojiName: name,
		Payload: &gchat.EmojiPayload{
			FileContent: base64.StdEncoding.EncodeToString(raw),
			Filename:    filepath.Base(file.Name()),
		},
	})
	if err != nil {
		return nil, Classify(err)
	}
	shaped := emojiOfWire(*got)
	out.CustomEmoji = &shaped
	return out, nil
}

// DeleteCustomEmojiInput names one to remove.
type DeleteCustomEmojiInput struct {
	Name   string
	DryRun bool
}

// DeleteCustomEmojiResult says whether anything was removed.
type DeleteCustomEmojiResult struct {
	Name    string
	Deleted bool
	DryRun  bool
}

// DeleteCustomEmoji removes one from the organisation.
//
// It is gone for everyone, and any message already carrying it loses
// the image. The tool says so; this only reports what happened.
func (s *Service) DeleteCustomEmoji(ctx context.Context, in DeleteCustomEmojiInput) (*DeleteCustomEmojiResult, error) {
	name, err := requireCustomEmoji("name", in.Name)
	if err != nil {
		return nil, err
	}
	out := &DeleteCustomEmojiResult{Name: name, DryRun: in.DryRun}
	if in.DryRun {
		return out, nil
	}
	deleted, err := deleteIdempotent(ctx, name, s.client.DeleteCustomEmoji, nil)
	if err != nil {
		return nil, err
	}
	out.Deleted = deleted
	return out, nil
}

// DeleteSpaceInput names a space to delete.
type DeleteSpaceInput struct {
	Space string
	// Confirm has to carry the space's own resource name. Google
	// cascades the delete over every message and membership, and this
	// is the one call in the surface that destroys other people's work
	// irrecoverably.
	Confirm string
	DryRun  bool
}

// DeleteSpaceResult says whether the space is gone.
type DeleteSpaceResult struct {
	Space   string
	Deleted bool
	DryRun  bool
}

// DeleteSpace removes a space and everything in it.
func (s *Service) DeleteSpace(ctx context.Context, in DeleteSpaceInput) (*DeleteSpaceResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Confirm) != space {
		return nil, Invalidf("deleting %s destroys every message and membership in it, and cannot be undone. "+
			"Pass confirm_space_id with the same value as space_id to go ahead", space)
	}

	out := &DeleteSpaceResult{Space: space, DryRun: in.DryRun}
	if in.DryRun {
		return out, nil
	}
	deleted, err := deleteIdempotent(ctx, space, s.client.DeleteSpace, nil)
	if err != nil {
		return nil, err
	}
	out.Deleted = deleted
	return out, nil
}

// emojiOfWire shapes one emoji.
func emojiOfWire(e gchat.CustomEmoji) CustomEmoji {
	return CustomEmoji{Name: e.Name, EmojiName: e.EmojiName, TemporaryURI: e.TemporaryURI}
}
