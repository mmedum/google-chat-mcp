// Package leakcheck finds anything in the repository that could
// identify a real person, account or space.
//
// The rule it enforces is that nothing internal ever lands here: no
// real email addresses, account ids, space or message ids, OAuth client
// ids or profile photo URLs. Fixtures are synthetic.
//
// It exists because this server is built by running it against a live
// Workspace account. Every id in this file's subject matter reaches a
// terminal during ordinary development — a doctor run, a smoke test, a
// pasted error — and from there it is one careless copy into a fixture
// or a commit message. A rule kept by remembering to look is not a
// rule; this is the same reasoning that put the dry-run guard under the
// handler rather than in it.
//
// gitleaks already covers credentials. This covers identifiers, which
// are not secrets and so pass it untouched: a space id in a test
// fixture leaks who somebody works with rather than a password.
//
// Every rule below is an allow-list. A deny-list naming the
// organisation, domain or account to watch for would itself be the leak
// it is meant to prevent.
package leakcheck

import (
	"encoding/base64"
	"errors"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Finding is one thing that should not be in the repository.
type Finding struct {
	Path  string
	Line  int
	What  string
	Value string
}

// published are addresses meant to be read by strangers. The maintainer
// publishes one so a code-of-conduct report can be made privately.
var published = map[string]bool{
	"mmedum@gmail.com":      true,
	"noreply@anthropic.com": true,
}

var (
	email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})`)

	// Reserved by RFC 2606, 6761 and 6762. Nobody can own one, so an
	// address under it cannot belong to a real person.
	safeDomain = regexp.MustCompile(`(?i)(?:^|\.)(?:example\.(?:com|org|net)|test|invalid|example|localhost|local)$`)

	// A Google account id is 21 digits and appears bare, with nothing
	// around it to key on.
	subject = regexp.MustCompile(`(?:^|[^0-9])([0-9]{21})(?:[^0-9]|$)`)

	// An OAuth client id, and the host every profile photo comes from.
	googleIdentifier = regexp.MustCompile(`[0-9]{6,}-[a-z0-9]{16,}\.apps\.googleusercontent\.com|lh[0-9]\.googleusercontent\.com`)

	spaceID = regexp.MustCompile(`spaces/([A-Za-z0-9._-]+)`)

	// Says out loud that it was made up. Real ids never do, which is
	// what makes this the convention to write fixtures to: a made-up id
	// has to admit it.
	synthetic = regexp.MustCompile(`(?i)example|placeholder|test|fake|dummy|sample|someone|space[0-9]`)

	// Long enough to be an encoded resource name rather than a word.
	blob = regexp.MustCompile(`[A-Za-z0-9_-]{16,}`)
)

// shortID is how long a space id can be before it stops looking made
// up. Google's are eleven characters or more; the fixtures here are a
// handful of letters.
const shortID = 8

// Text returns everything in one file's content that looks like it
// identifies something real.
func Text(path, content string) []Finding {
	// Not preallocated on purpose: a clean file is the whole point, so
	// almost every call returns nothing and sizing to the line count
	// would allocate for every file in the repository to hold zero
	// findings.
	var out []Finding //nolint:prealloc // the empty case is the common one
	for n, line := range strings.Split(content, "\n") {
		out = append(out, scanLine(path, n+1, line)...)
	}
	return out
}

// scanLine applies every rule to one line.
func scanLine(path string, n int, line string) []Finding {
	var out []Finding
	add := func(what, value string) {
		out = append(out, Finding{Path: path, Line: n, What: what, Value: value})
	}

	for _, m := range email.FindAllStringSubmatch(line, -1) {
		if published[strings.ToLower(m[0])] || safeDomain.MatchString(m[1]) {
			continue
		}
		add("an email under a domain someone could own", m[0])
	}
	for _, m := range subject.FindAllStringSubmatch(line, -1) {
		if looksMadeUp(m[1]) {
			continue
		}
		add("a 21-digit Google account id", m[1])
	}
	for _, m := range googleIdentifier.FindAllString(line, -1) {
		add("a Google client id or profile photo host", m)
	}

	out = append(out, spaceIDs(path, n, line, "")...)
	// A section item id is base64url of a space resource name, so a real
	// id can arrive already encoded and read as noise.
	for _, encoded := range blob.FindAllString(line, -1) {
		decoded, ok := decodeBase64URL(encoded)
		if !ok || !strings.Contains(decoded, "spaces/") {
			continue
		}
		out = append(out, spaceIDs(path, n, decoded, ", once base64 is decoded")...)
	}
	return out
}

// spaceIDs reports the resource ids in text that do not look invented.
func spaceIDs(path string, n int, text, note string) []Finding {
	var out []Finding
	for _, m := range spaceID.FindAllStringSubmatch(text, -1) {
		if len(m[1]) <= shortID || synthetic.MatchString(m[1]) {
			continue
		}
		out = append(out, Finding{
			Path:  path,
			Line:  n,
			What:  "a space id that does not look synthetic" + note,
			Value: "spaces/" + m[1],
		})
	}
	return out
}

// looksMadeUp reports whether a 21-digit run is obviously a
// placeholder. A real account id has many distinct digits; a stand-in
// is a run of zeros or ones.
func looksMadeUp(digits string) bool {
	seen := map[rune]bool{}
	for _, r := range digits {
		seen[r] = true
	}
	return len(seen) < 4
}

// decodeBase64URL decodes a blob, reporting whether it was text at all.
func decodeBase64URL(s string) (string, bool) {
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil || !utf8.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

// readText reads a file when it is text. A binary carries nothing this
// package can read, and decoding one as UTF-8 would report noise.
func readText(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the caller supplies tracked paths
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", errBinary
	}
	return string(raw), nil
}

// errBinary means the file held bytes that are not text.
var errBinary = errors.New("leakcheck: not a text file")
