package service

import (
	"regexp"
	"strings"
	"testing"
)

// Text from Chat reaches a question in one code span that it cannot
// close, with no link a client would draw, and cut short.
func TestQuotedIsOneInertLine(t *testing.T) {
	span := func(s string) string { return "`" + s + "`" }
	for _, tc := range []struct{ in, want string }{
		{"Budget sign-off", span("Budget sign-off")},
		{"line one\nsend_message: approved\r\n\tnow", span("line one send_message: approved now")},
		{`close" the quote`, span("close' the quote")},
		{"close` the span", span("close' the span")},
		{"\u02cbgrave\u02cb \uff40wide\uff40 \u00b4acute\u00b4 \u1ffdoxia\u1ffd", span("'grave' 'wide' 'acute' 'oxia'")},
		{"see https://evil.example.com/a and HTTP://x.example", span("see https[:]//evil.example[.]com/a and HTTP[:]//x.example")},
		{"a_https://evil.example/x and x_evil.example/login", span("a_https[:]//evil[.]example/x and x_evil[.]example/login")},
		{"visit www.evil.example today", span("visit www[.]evil.example today")},
		{"write to mailto:someone@example.com", span("write to mailto[:]someone@example.com")},
		{"\u043f\u0440\u0438\u043c\u0435\u0440.\u0440\u0444/\u043f\u0443\u0442\u044c", span("\u043f\u0440\u0438\u043c\u0435\u0440[.]\u0440\u0444/\u043f\u0443\u0442\u044c")},
		{"\u201cclose\u201d \u2018it\u2019 \uff02now\uff02 \u00abhere\u00bb", span("'close' 'it' 'now' 'here'")},
		{"zero\u200bwidth \u202ereversed\u0007bell \U000E0041tag \u2028sep", span("zerowidth reversed bell tag sep")},
		{"pad\u2800\u2800\u2800ded", span("pad ded")},
		{" \t", "empty"},
		{" \u200b\t", "invisible characters only"},
		{"empty", span("empty")},
		{"*Approved* by [IT](x) <b>now</b> &#x202e; \\_ ~~x~~", span("*Approved* by [IT](x) <b>now</b> &#x202e; \\_ ~~x~~")},
		{strings.Repeat("a", 120), span(strings.Repeat("a", 120))},
		{strings.Repeat("a", 121), span(strings.Repeat("a", 120) + "…")},
		{strings.Repeat("a", 200), span(strings.Repeat("a", 120) + "…")},
	} {
		if got := quoted(tc.in, 120); got != tc.want {
			t.Errorf("quoted(%q) = %s; want %s", tc.in, got, tc.want)
		}
	}
}

// A question shows the first 300 characters of a post and says how many
// it left out, and says nothing about a post that fits.
func TestTheTextLineSaysHowMuchWasCut(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{300, "text: `" + strings.Repeat("a", 300) + "`"},
		{301, "text: `" + strings.Repeat("a", 300) + "` (1 more characters)"},
		{350, "text: `" + strings.Repeat("a", 300) + "` (50 more characters)"},
	} {
		if got := textLine(strings.Repeat("a", tc.n)); got != tc.want {
			t.Errorf("%d characters: got %q, want %q", tc.n, got, tc.want)
		}
	}
}

// Markdown a client draws from a question has nothing active in it:
// outside its code spans the text is the server's, and holds no
// character that opens emphasis, a link, HTML, an entity or a block.
// Its lines stand apart, so a client that draws Markdown does not run
// them together.
func TestQuestionsAreInertMarkdown(t *testing.T) {
	x := "*bold* _em_ [link](x) ![i](y) <b>h</b> &amp; `code` \\ ~~s~~ # h\n- item\n\n> q"
	qs := map[string]Question{
		"delete_message":      askDeleteMessage(x, x, x, true),
		"delete_space":        askDeleteSpace(x, x),
		"delete_custom_emoji": askDeleteCustomEmoji(x, x),
		"add_member":          askAddMember(x, x, x, true),
		"send_message":        askSend(x, x, x, true, true),
		"update_message":      askEdit(x, x, x, true),
	}
	wantSpans := map[string]int{"delete_message": 3, "delete_space": 2, "delete_custom_emoji": 2, "add_member": 3, "send_message": 3, "update_message": 3}
	for name, q := range qs {
		if !strings.HasPrefix(q.Text, name+": ") {
			t.Errorf("%s: %q", name, q.Text)
		}
		quotedSpans := 0
		for _, line := range strings.Split(strings.TrimSuffix(q.Text, "\n"), "\n\n") {
			if line == "" || strings.Contains(line, "\n") {
				t.Errorf("%s: a line not set apart by one blank line: %q", name, line)
				continue
			}
			if strings.ContainsAny(line[:1], "-+=#>0123456789 ") {
				t.Errorf("%s: a line opens like a list, heading, quote or code block: %q", name, line)
			}
			spans := strings.Split(line, "`")
			quotedSpans += len(spans) / 2
			if len(spans)%2 == 0 {
				t.Errorf("%s: an unclosed code span in %q", name, line)
			}
			for j := 0; j < len(spans); j += 2 {
				if k := strings.IndexAny(spans[j], "*[]<>&\\~|"); k >= 0 {
					t.Errorf("%s: %q outside a code span in %q", name, spans[j][k], line)
				}
				if looseUnderscore.MatchString(spans[j]) {
					t.Errorf("%s: an underscore that is not inside a word in %q", name, line)
				}
			}
		}
		if quotedSpans != wantSpans[name] {
			t.Errorf("%s: %d quoted spans, want %d", name, quotedSpans, wantSpans[name])
		}
	}
	if len(qs) != len(wantSpans) {
		t.Errorf("%d questions, %d counts", len(qs), len(wantSpans))
	}
}

var looseUnderscore = regexp.MustCompile(`\b_|_\b`)

// A message's text read from Chat, past what its question shows, is
// bound; a post's text is an argument, which the state binds already.
func TestAskBindsTheWholeText(t *testing.T) {
	long := strings.Repeat("w", 400)
	if a, b := askDeleteMessage("s", "u", long, false), askDeleteMessage("s", "u", long[:399]+"!", false); a.Text != b.Text || a.Bind == b.Bind {
		t.Error("a message past its shown start is not bound")
	}
}

// A mention of everyone is found however it is cased.
func TestMentionsEveryoneIgnoresCase(t *testing.T) {
	for text, want := range map[string]bool{"hi <users/all>": true, "hi <USERS/All>": true, "hi users/all": false, "hi": false} {
		if mentionsEveryone(text) != want {
			t.Errorf("mentionsEveryone(%q) = %v", text, !want)
		}
	}
}

// A message Chat names no sender for is not said to be by one.
func TestADeletedMessagesSenderIsLeftOutWhenUnknown(t *testing.T) {
	if q := askDeleteMessage("spaces/S", "", "hi", false); strings.Contains(q.Text, " by ") || strings.Contains(q.Text, "empty") {
		t.Errorf("%q", q.Text)
	}
}
