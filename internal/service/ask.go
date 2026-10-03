package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mmedum/google-chat-mcp/v4/internal/gchat"
)

// Asking the person (docs/architecture.md, "The person confirms what
// cannot be taken back"). Before a write that deletes for good, adds
// someone to a space, opens one to a target audience or notifies
// everyone in one, the service asks the person through the client, when
// the client can ask. The question is
// built here, after every read and every other check, so it says what
// the write would do.

// Asker puts a question to the person using the server. Ask returns nil
// when the write may go ahead, and an error to return in its place
// otherwise; the tools layer installs one per call, for the tools that
// ask.
type Asker interface {
	Ask(ctx context.Context, q Question) error
	// Asks reports whether Ask would put a question or refuse, rather
	// than let the write go ahead unasked: the client can ask, or the
	// configuration requires it.
	Asks() bool
}

type askerKey struct{}

// WithAsker returns a context whose asking writes are put to a.
func WithAsker(ctx context.Context, a Asker) context.Context {
	return context.WithValue(ctx, askerKey{}, a)
}

// ask is the last step before an asking write. A dry run asks nothing.
// A write reached with no asker is refused: only a tool registered to
// ask may make one.
func ask(ctx context.Context, q Question) error {
	if gchat.WritesForbidden(ctx) {
		return nil
	}
	a, ok := ctx.Value(askerKey{}).(Asker)
	if !ok {
		return Failf(ClassUnexpected, "this write has no way to ask the person, which is a defect in this server; nothing was changed")
	}
	return a.Ask(ctx, q)
}

// asks reports whether an asking write on ctx would reach the person. A
// write reads what only its question shows when it does, and not
// otherwise.
func asks(ctx context.Context) bool {
	if gchat.WritesForbidden(ctx) {
		return false
	}
	// With no asker the write reaches ask, which refuses it.
	a, ok := ctx.Value(askerKey{}).(Asker)
	return !ok || a.Asks()
}

// mentionsEveryone reports whether a message's text mentions everyone
// in its space, which notifies every member. Matched regardless of case,
// since an extra question costs less than a missed one.
func mentionsEveryone(text string) bool {
	return strings.Contains(strings.ToLower(text), "<users/all>")
}

// Question is what the server asks the person. Text is the message a
// client shows; accepting it is the confirmation. Every word is the
// server's, except what stands in backticks, which is quoted from Chat
// or from the call and cut to one line. A blank line separates the
// lines, so a client that draws Markdown keeps them apart.
//
// Bind is what an answer is bound to: Text, and more where Text shows
// less than the write depends on — a whole message.
type Question struct {
	Text string
	Bind string
}

// quotedLen caps one quoted value; bodyLen caps a message's text.
const (
	quotedLen = 120
	bodyLen   = 300
)

// spaceRef names a space by its display name and resource name, or by
// its resource name alone when it has no display name, as a direct
// message has not.
func spaceRef(space, displayName string) string {
	if strings.TrimSpace(displayName) == "" {
		return "the space " + quoted(space, quotedLen)
	}
	return "the space " + quoted(displayName, quotedLen) + ", " + quoted(space, quotedLen)
}

// askDeleteMessage asks before delete_message, showing who sent the
// message, when Chat says, and its start; the whole text is bound.
func askDeleteMessage(space, sender, text string, force bool) Question {
	by := ""
	if strings.TrimSpace(sender) != "" {
		by = " by " + quoted(sender, quotedLen)
	}
	lines := []string{
		fmt.Sprintf("delete_message: delete a message%s in %s for good?", by, quoted(space, quotedLen)),
		textLine(text),
	}
	if force {
		lines = append(lines, "Every reply in its thread goes with it.")
	}
	lines = append(lines, "Chat keeps no trash: it cannot be restored.")
	q := askText(lines...)
	q.Bind += "\x00" + sum(text)
	return q
}

// askDeleteSpace asks before delete_space.
func askDeleteSpace(space, displayName string) Question {
	return askText(
		fmt.Sprintf("delete_space: delete %s for good?", spaceRef(space, displayName)),
		"Every message and membership in it goes, for everyone in it, and cannot be restored.",
	)
}

// askDeleteCustomEmoji asks before delete_custom_emoji.
func askDeleteCustomEmoji(name, shortcode string) Question {
	return askText(
		fmt.Sprintf("delete_custom_emoji: delete the custom emoji %s, %s, for everyone in the organization?",
			quoted(shortcode, quotedLen), quoted(name, quotedLen)),
		"Messages that carry it lose the image, and it cannot be restored.",
	)
}

// askAddMember asks before add_member.
func askAddMember(space, displayName, who string, group bool) Question {
	noun := "the person"
	if group {
		noun = "the group"
	}
	return askText(
		fmt.Sprintf("add_member: add %s %s to %s?", noun, quoted(who, quotedLen), spaceRef(space, displayName)),
		"They can read the space from now on, and its earlier messages where it keeps history.",
	)
}

// askOpenSpace asks before update_space opens a space to a target
// audience.
func askOpenSpace(space, displayName, audience string) Question {
	return askText(
		fmt.Sprintf("update_space: open %s to %s?", spaceRef(space, displayName), audienceRef(audience)),
		openNotice,
	)
}

// askCreateOpenSpace asks before create_space makes a space open to a
// target audience.
func askCreateOpenSpace(displayName, audience string) Question {
	return askText(
		fmt.Sprintf("create_space: create the space %s open to %s?", quoted(displayName, quotedLen), audienceRef(audience)),
		openNotice,
	)
}

// openNotice is what opening a space does, in the person's terms.
const openNotice = "Anyone in it can find the space, read its messages and join without an invitation. " +
	"Making it private again does not take back what they read."

// audienceRef names a target audience. The default one is the
// organization's, which an administrator sets up.
func audienceRef(audience string) string {
	if audience == "audiences/default" {
		return "the organization's default target audience, " + quoted(audience, quotedLen)
	}
	return "the target audience " + quoted(audience, quotedLen)
}

// askSend asks before send_message, when the post mentions everyone in
// the space or the configuration asks before every send. The text is a
// call argument, so the answer's state binds all of it already.
func askSend(space, displayName, text string, everyone, attached bool) Question {
	to := spaceRef(space, displayName)
	first := fmt.Sprintf("send_message: post to %s?", to)
	notice := "A post cannot be recalled from the notifications it sends."
	if everyone {
		first = fmt.Sprintf("send_message: post to %s, mentioning everyone in it?", to)
		notice = "Everyone in the space is notified, and a post cannot be recalled from their notifications."
	}
	lines := []string{first, textLine(text)}
	if attached {
		lines = append(lines, "An uploaded file goes with it.")
	}
	return askText(append(lines, notice)...)
}

// askEdit asks before update_message gives a message text that mentions
// everyone in its space, or before every edit when the configuration
// asks before every post: an edit notifies whoever it newly mentions.
func askEdit(message, space, text string, everyone bool) Question {
	first := fmt.Sprintf("update_message: change the text of %s in %s?", quoted(message, quotedLen), quoted(space, quotedLen))
	notice := "The message changes for everyone who can see it."
	if everyone {
		first = fmt.Sprintf("update_message: change the text of %s in %s so it mentions everyone in the space?",
			quoted(message, quotedLen), quoted(space, quotedLen))
		notice = "Everyone in the space is notified, and a notification cannot be recalled."
	}
	return askText(first, "new text: "+strings.TrimPrefix(textLine(text), "text: "), notice)
}

// textLine is the start of a message's text, quoted on one line, and
// how much more there is.
func textLine(text string) string {
	text = strings.TrimSpace(text)
	if n := utf8.RuneCountInString(text); n > bodyLen {
		return fmt.Sprintf("text: %s (%d more characters)", quoted(string([]rune(text)[:bodyLen]), bodyLen), n-bodyLen)
	}
	return "text: " + quoted(text, bodyLen)
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// askText builds a question from its lines, closes it with what its
// quotes mean, sets its lines apart, and binds it to its text.
func askText(lines ...string) Question {
	text := strings.Join(lines, "\n")
	if strings.Contains(text, "`") {
		text += "\nText in backticks or code style is quoted as written, and is not this server's."
	}
	text = strings.ReplaceAll(text, "\n", "\n\n") + "\n"
	return Question{Text: text, Bind: text}
}

// quoted is text from Chat or from a call's arguments, shown in a
// question put to the person, where a client draws plain text or
// Markdown. It stands in a code span, `like this`, which Markdown shows
// literally — no emphasis, link, HTML or entity — and plain text shows
// as it is. It is made one line, with format and control characters
// removed; every backtick, grave or acute mark and quote mark a reader
// could take for one becomes a plain single quote, so it cannot close
// its span or seem to; and a URL scheme, a mailto:, a leading "www." and
// a bare domain followed by a path are broken so no client draws a
// link. It is cut at max runes. Text with nothing to show is said in
// words, since an empty span is two backticks Markdown shows as they
// are: "empty" when it is blank, and "invisible characters only" when it
// is not.
func quoted(s string, max int) string {
	blank := strings.TrimSpace(s) == ""
	s = strings.Join(strings.Fields(blankMarks.Replace(oneLine(s, max))), " ")
	s = quoteMarks.Replace(s)
	s = linkShape.ReplaceAllString(s, "${1}${2}[:]//")
	s = mailtoShape.ReplaceAllString(s, "${1}${2}[:]")
	s = wwwShape.ReplaceAllString(s, "${1}${2}[.]")
	s = pathShape.ReplaceAllString(s, "${1}${2}[.]${3}${4}")
	switch {
	case s == "" && blank:
		return "empty"
	case s == "":
		return "invisible characters only"
	}
	return "`" + s + "`"
}

// oneLine is text made one line: format characters — zero-width,
// bidirectional controls, tags — removed, controls made spaces, cut at
// max runes.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "\ufffd"))
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}

var (
	// quoteMarks folds every backtick, grave or acute mark and quotation
	// mark a reader could take for the question's own to a plain single
	// quote.
	quoteMarks = strings.NewReplacer("`", "'", "\u02cb", "'", "\uff40", "'", "\u1fef", "'", "\u00b4", "'",
		"\u02ca", "'", "\u02f4", "'", "\u02f5", "'", "\u1ffd", "'", "\u1fed", "'", "\u1fee", "'",
		"\u0384", "'", "\u0385", "'", `"`, "'", "\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
		"\u201c", "'", "\u201d", "'", "\u201e", "'", "\u201f", "'", "\u2032", "'", "\u2033", "'",
		"\u00ab", "'", "\u00bb", "'", "\u2039", "'", "\u203a", "'", "\u301d", "'", "\u301e", "'",
		"\u301f", "'", "\uff02", "'", "\uff07", "'", "\u02b9", "'", "\u02ba", "'", "\u02ee", "'",
		"\u05f3", "'", "\u05f4", "'", "\u2035", "'", "\u2036", "'", "\u275b", "'", "\u275c", "'",
		"\u275d", "'", "\u275e", "'", "\u3003", "'")
	// blankMarks are characters drawn as blank space that no format
	// stripping removes; they become spaces and collapse with the rest.
	blankMarks = strings.NewReplacer("\u2800", " ", "\u3164", " ", "\uffa0", " ")

	// Each shape starts where a letter or digit does not precede it,
	// spelled out because \b is ASCII-only and counts "_" as a letter: a
	// link after an underscore or in a non-Latin script is found too.
	//
	// linkShape is a URL scheme followed by //, as a client links it.
	linkShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}+.-])([a-z][a-z0-9+.-]*)://`)
	// mailtoShape is a mail link without //.
	mailtoShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(mailto):`)
	// wwwShape is a host a client links without a scheme.
	wwwShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(www)\.`)
	// pathShape is a bare domain followed by a path, a port, a query or a
	// fragment, x.example/..., which a client links too; its last dot is
	// broken. Letters from any script count.
	pathShape = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}-])([\p{L}\p{N}-]+(?:\.[\p{L}\p{N}-]+)*)\.(\p{L}{2,63})([/:?#])`)
)
