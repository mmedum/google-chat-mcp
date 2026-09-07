package leakcheck

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The values below are the shapes of real ones, invented for the test.
// Every rule is checked from both sides: a gate that reports nothing is
// indistinguishable from a gate that does nothing.
func TestWhatMustNeverLand(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"an address at a domain someone owns", `contact janedoe@acmecorp.example.io for access`},
		{"a real-looking account id", `"user_sub": "112372014885281997253"`},
		{"an OAuth client id", `618125823634-0u3uumhqqvap1i1nsnebufg5sk24775a.apps.googleusercontent.com`},
		{"a profile photo", `"picture_url": "https://lh3.googleusercontent.com/a/ACg8ocJx=s96-c"`},
		{"a space id off a real account", `space := "spaces/AAQATCYW6MY"`},
		{"a message id, which carries its space", `"spaces/AAQATCYW6MY/messages/abc.def"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text("doc.md", tc.line); len(got) == 0 {
				t.Errorf("nothing reported for %q", tc.line)
			}
		})
	}
}

// A section item id is base64url of a space resource name, so a real id
// reaches a fixture looking like noise. Decoding is the only way to see
// it, and this is the rule most likely to be quietly lost in a refactor.
func TestARealIDHiddenInBase64IsFound(t *testing.T) {
	// base64url of "spaces/AAQATCYW6MY", as list_section_items returns.
	line := `"item_name": "users/1/sections/S/items/c3BhY2VzL0FBUUFUQ1lXNk1Z"`
	got := Text("fixture.go", line)
	if len(got) == 0 {
		t.Fatal("an encoded space id went unnoticed")
	}
	if !strings.Contains(got[0].What, "base64") {
		t.Errorf("what = %q, want it to say the id was encoded", got[0].What)
	}
	if !strings.Contains(got[0].Value, "spaces/") {
		t.Errorf("value = %q, want the decoded id", got[0].Value)
	}
}

// The convention has to leave room to write tests and documentation, or
// it gets switched off rather than followed.
func TestWhatIsAllowed(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{"a documentation address", `janedoe@example.com sent it`},
		{"a reserved test domain", `bob@nas.local and me@b.test`},
		{"the published contact address", `email the maintainer at mmedum@gmail.com`},
		{"a short fixture id", `space := "spaces/AAA"`},
		{"an id that says it is made up", `space := "spaces/AAAAspace1-example"`},
		{"a placeholder account id", `signed in as janedoe@example.com (000000000000000000000)`},
		{"a base64 blob that is not a resource name", `sha256: q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6`},
		{"prose about resource names", `every space is addressed as spaces/{space}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text("doc.md", tc.line); len(got) != 0 {
				t.Errorf("false positive on %q: %+v", tc.line, got)
			}
		})
	}
}

// The check that keeps the check honest: every tracked file, every
// rule. This is the one that fails a build.
// A warning for anyone checking this gate rather than reading it: the
// fixture convention this repository documents, `spaces/AAAAspace1`, is
// itself one of the shapes the gate counts as invented. Probing it with
// that value returns a clean result by design, which reads exactly like
// a gate that cannot see the file it was pointed at. Use a
// realistic-shaped id, and nothing real.
//
// It is a property of the convention rather than of this
// implementation: google-sheets-mcp reached the same false clean
// against its own gate, the same way, on the same day. A gate that
// recognises made-up ids by a run of one letter is most permissive
// toward exactly the fixture someone types when testing it in a hurry.
func TestTheRepositoryIsClean(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := strings.TrimSpace(string(root))

	listed, err := exec.Command("git", "-C", dir, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}

	var findings []Finding
	var tracked, scanned, binary int
	var unreadable []string
	for _, path := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		if path == "" {
			continue
		}
		tracked++
		// This package names the shapes it looks for, so it is the one
		// file guaranteed to contain them.
		if strings.HasPrefix(path, "internal/leakcheck/") {
			continue
		}
		content, err := readText(filepath.Join(dir, path))
		switch {
		case errors.Is(err, errBinary):
			// Legitimately nothing to read out of: an image, a
			// compiled fixture. Counted so it cannot hide a hole.
			binary++
			continue
		case err != nil:
			unreadable = append(unreadable, path+": "+err.Error())
			continue
		}
		scanned++
		findings = append(findings, Text(path, content)...)
	}

	// Asserted, not logged, and derived from what git listed rather than
	// from a number typed here. Zero findings and zero inputs are the
	// same output, so a scan that read nothing has to fail rather than
	// pass quietly — the failure this whole package exists to avoid, and
	// the one the history scan below already guards against.
	t.Logf("scanned %d of %d tracked files, %d binary", scanned, tracked, binary)
	if tracked > 0 && scanned == 0 {
		t.Fatalf("%d tracked files and none read: the scan is looking at nothing", tracked)
	}
	// A file git tracks that this cannot open is a file nobody is
	// checking. Binary is a reason; anything else is a hole.
	for _, why := range unreadable {
		t.Errorf("tracked but unread, so unchecked: %s", why)
	}

	for _, f := range findings {
		t.Errorf("%s:%d: %s: %s", f.Path, f.Line, f.What, f.Value)
	}
	if len(findings) > 0 {
		t.Log("use janedoe@example.com, spaces/AAAAspace1 and the like instead")
	}
}

// reviewedBlobs are historical blobs whose findings were read and are
// not identifiers. Keyed by blob sha, so it exempts one reviewed version
// of one file and nothing else: a new commit is a new blob and is
// scanned. Each needs the verdict written down, because an exemption
// nobody can audit is the same as a rule nobody enforces.
var reviewedBlobs = map[string]bool{
	// internal/service/names_test.go, before c589a42 sanitised it. The
	// value is "spaces/AAAQ_1-a.b", a fixture exercising the characters
	// an id may contain — a dot, an underscore and a hyphen. It trips
	// the rule because AAAQ is the shape of a real id, which is exactly
	// why CLAUDE.md tells fixtures not to use it, and why the commit
	// that added this package changed it. No real space.
	"dbec0fd7": true,
}

// The tree scan answers "is what we would publish clean". This answers
// a different question: was anything internal ever committed and edited
// out later? A public repository carries its whole history, so an id
// removed in a later commit is still there for anyone who clones.
// gitleaks does not close this — it looks for credentials, and an id is
// not a credential.
//
// It scans the commits a push would ADD — everything on a branch or tag
// that no remote already has — rather than all of history. That is the
// question a gate can answer: the past is published and cannot be
// changed by a test failing, while what has not left this machine still
// can be. LEAKCHECK_HISTORY=all widens it to every commit, which is the
// mode to use when deciding whether the published past needs rewriting.
//
// Only branches and tags either way. A tool that checkpoints sessions
// into refs of its own once kept its transcripts here, and those are
// full of real ids by their nature — they record what was actually said
// to this server. They were never pushed, and scanning them reported a
// hundred and fifty findings about a file nobody would ever receive.
// Any local-only ref is that same case: unpushable, so out of scope.
//
// Behind LEAKCHECK_HISTORY because it walks blobs rather than the tree.
// CI sets it; `go test ./...` on a laptop skips it. Raised by the
// google-docs-mcp session, which found the same hole in its own gate.
func TestTheHistoryIsClean(t *testing.T) {
	mode := os.Getenv("LEAKCHECK_HISTORY")
	if mode == "" {
		t.Skip("set LEAKCHECK_HISTORY=1 for the unpublished commits, or =all for every one")
	}
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := strings.TrimSpace(string(root))

	// The range: every commit, or only the ones a push would add.
	// Against origin/main where there is one, because a CI checkout has
	// no local branches and "--not --remotes" would then scan nothing at
	// all and pass — a gate that looks at nothing is the failure this
	// package exists to avoid.
	revs := []string{"--branches", "--tags"}
	switch {
	case mode == "all":
	case exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "origin/main").Run() == nil:
		revs = []string{"HEAD", "--not", "origin/main"}
	default:
		revs = append(revs, "--not", "--remotes")
	}

	listed, err := exec.Command("git",
		append([]string{"-C", dir, "rev-list", "--objects"}, revs...)...).Output()
	if err != nil {
		t.Fatalf("git rev-list: %v", err)
	}
	counted, err := exec.Command("git",
		append([]string{"-C", dir, "rev-list", "--count"}, revs...)...).Output()
	if err != nil {
		t.Fatalf("git rev-list --count: %v", err)
	}
	commits, err := strconv.Atoi(strings.TrimSpace(string(counted)))
	if err != nil {
		t.Fatalf("commit count %q: %v", counted, err)
	}

	paths := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		sha, path, ok := strings.Cut(line, " ")
		if !ok || path == "" {
			continue // a commit or a tree: no content of its own
		}
		if strings.HasPrefix(path, "internal/leakcheck/") {
			// The one file that names the shapes it looks for.
			continue
		}
		if reviewedBlobs[sha[:8]] {
			continue
		}
		paths[sha] = path
	}

	// One cat-file for the whole walk. A process per blob turns seconds
	// into minutes once a repository has any history.
	shas := make([]string, 0, len(paths))
	for sha := range paths {
		shas = append(shas, sha)
	}
	batch := exec.Command("git", "-C", dir, "cat-file", "--batch")
	batch.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	out, err := batch.Output()
	if err != nil {
		t.Fatalf("git cat-file: %v", err)
	}

	// Asserted, not logged. Zero blobs and a clean history read the same
	// from the outside, and a log line nobody reads is not a check —
	// google-docs-mcp's wording, and the right correction. A branch with
	// nothing new legitimately has no commits, so the expectation comes
	// from the range rather than from a fixed floor.
	t.Logf("scanned %d blobs across %d commits", len(paths), commits)
	if commits > 0 && len(paths) == 0 {
		t.Fatalf("%d commits to scan and no blobs read: the scan is looking at nothing", commits)
	}

	var findings []Finding
	for rest := out; len(rest) > 0; {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		header := strings.Fields(string(rest[:nl]))
		rest = rest[nl+1:]
		if len(header) != 3 {
			continue // "missing", or a header this does not understand
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size > len(rest) {
			break
		}
		content := rest[:size]
		rest = rest[min(size+1, len(rest)):]
		if !utf8.Valid(content) {
			continue // a binary carries nothing to read
		}
		findings = append(findings, Text(paths[header[0]]+"@"+header[0][:8], string(content))...)
	}

	for _, f := range findings {
		t.Errorf("%s:%d: %s: %s", f.Path, f.Line, f.What, f.Value)
	}
	if len(findings) > 0 {
		t.Log("these commits have not been pushed, so rewriting the branch still fixes them; " +
			"editing the file in a later commit does not")
	}
}

// buildOutputMagic is the first few bytes of something this repository
// builds and must never commit, spelled as the phrase the failure names.
// Matched on the magic rather than on "is this file text", because a PNG
// fixture is binary and belongs here — internal/tools tracks one — while
// an ELF never does.
//
// The archives are here because the executables alone were not enough.
// `gates mcpb-pack` writes a .mcpb, which is a deflate zip, and
// goreleaser writes .tar.gz and .zip archives; both take the output
// directory as an argument, so `dist/` being ignored is the first line
// and not the whole guard. google-drive-mcp refuses the same class by a
// NUL byte in the first few kilobytes, the way git decides, which
// catches strictly more and cannot say what it caught. Naming the format
// is worth more here than the extra reach: the message is what tells
// somebody which target to add an ignore rule beside.
var buildOutputMagic = map[string][]byte{
	"an ELF executable":                         {0x7f, 'E', 'L', 'F'},
	"a Mach-O 32-bit executable":                {0xfe, 0xed, 0xfa, 0xce},
	"a Mach-O 64-bit executable":                {0xfe, 0xed, 0xfa, 0xcf},
	"a Mach-O 32-bit executable, little-endian": {0xce, 0xfa, 0xed, 0xfe},
	"a Mach-O 64-bit executable, little-endian": {0xcf, 0xfa, 0xed, 0xfe},
	"a Mach-O universal binary":                 {0xca, 0xfe, 0xba, 0xbe},
	"a static library":                          {'!', '<', 'a', 'r'},
	"a zip archive, which is what a .mcpb is":   {'P', 'K', 0x03, 0x04},
	"a gzip archive":                            {0x1f, 0x8b},
}

// Build output is the one thing here that no content scanner can see,
// because it is defined by being content none of them will read.
// The tree scan above counts a binary file and moves on; gitleaks looks
// for credentials, not size; and CI stayed green on all of it.
//
// It is not hypothetical. `go build ./scripts/gates` leaves a 10 MB
// `gates` at the repository root, and it was committed three times into
// public main before anything noticed — 18.4 MiB packed, against 447 KiB
// for the whole rest of the repository. What would have caught it is not
// a better reader. It is this: a rule that knows an executable by its
// first four bytes.
//
// Untracked files are checked too, and that half is the point. A file
// that is untracked and not ignored is one `git add -A` from the
// history, so refusing it here fails before it can be swept in rather
// than after — which is the difference between an ignore rule and a
// rewrite of published history.
//
// It lives beside the identifier scan because that is where the tree
// enumeration already is, and because both answer one question: what
// must never be in this repository.
func TestNoBuildOutputInTheTree(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	dir := strings.TrimSpace(string(root))

	list := func(args ...string) []string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		var paths []string
		for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if p != "" {
				paths = append(paths, p)
			}
		}
		return paths
	}
	// Tracked, and untracked-but-not-ignored. The second is the half
	// that fires before a mistake can be committed.
	paths := append(list("ls-files"), list("ls-files", "--others", "--exclude-standard")...)

	examined := 0
	for _, path := range paths {
		f, err := os.Open(filepath.Join(dir, path)) //nolint:gosec // paths git listed
		if err != nil {
			// A listed path that is gone is a race with the working
			// tree, not a finding.
			continue
		}
		head := make([]byte, 4)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		examined++
		for format, magic := range buildOutputMagic {
			if n >= len(magic) && bytes.Equal(head[:len(magic)], magic) {
				t.Errorf("%s is %s. Nothing else in this repository will look inside it, so add "+
					"it to .gitignore beside the target that builds it, or delete it. Committing "+
					"one costs a rewrite of published history to undo.", path, format)
			}
		}
	}
	// Zero findings and zero inputs look the same from here.
	if examined == 0 {
		t.Fatal("no files examined: this gate is looking at nothing")
	}
	t.Logf("examined %d tracked and untracked files", examined)
}

// The rule has to fire on a real build artefact, not just on a fixture
// shaped like one, and it has to leave the PNG that legitimately ships.
func TestBuildOutputIsRecognisedByItsMagic(t *testing.T) {
	tests := []struct {
		name string
		head []byte
		want bool
	}{
		{name: "a Linux binary", head: []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, want: true},
		{name: "a macOS binary", head: []byte{0xcf, 0xfa, 0xed, 0xfe, 12, 0}, want: true},
		{name: "a universal binary", head: []byte{0xca, 0xfe, 0xba, 0xbe, 0, 0}, want: true},
		{name: "a .mcpb bundle", head: []byte{'P', 'K', 0x03, 0x04, 20, 0}, want: true},
		{name: "a release tarball", head: []byte{0x1f, 0x8b, 8, 0}, want: true},
		{name: "a PNG fixture", head: []byte{0x89, 'P', 'N', 'G', 13, 10}},
		{name: "Go source", head: []byte("package main\n")},
		{name: "a short file", head: []byte("hi")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := false
			for _, magic := range buildOutputMagic {
				if len(tt.head) >= len(magic) && bytes.Equal(tt.head[:len(magic)], magic) {
					got = true
				}
			}
			if got != tt.want {
				t.Errorf("recognised as build output = %v, want %v", got, tt.want)
			}
		})
	}
}
