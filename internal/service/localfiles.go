package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// localFiles is the one door to the operator's disk.
//
// Every read and every write this server makes to a local file goes
// through a method here, so the containment check is not something a
// tool has to remember. That matters more than it looks:
// create_custom_emoji shipped reading any path the caller named and
// sending it to Google, which is what happens when a rule lives in a
// convention rather than in a type.
type localFiles struct{ dir string }

// files returns the disk this server may touch, or the refusal that
// says how to turn it on.
//
// No directory configured means no file transfer at all, which is the
// default: a server that can read and write anywhere on the machine is
// a different thing from one that can talk to Chat. The symlinks are
// resolved once, here, so every check below compares real paths.
func (s *Service) files() (*localFiles, error) {
	if s.cfg.LocalDir == "" {
		return nil, Failf(ClassUnsupported, "This server cannot read or write local files: it was started "+
			"without GCM_LOCAL_DIR. Setting that to a directory turns download_attachment, upload_attachment "+
			"and create_custom_emoji on.")
	}
	dir, err := filepath.EvalSymlinks(s.cfg.LocalDir)
	if err != nil {
		return nil, Failf(ClassInvalid, "The directory this server was given as GCM_LOCAL_DIR does not exist "+
			"or cannot be read: %s", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, Failf(ClassInvalid, "GCM_LOCAL_DIR is not a directory.")
	}
	return &localFiles{dir: dir}, nil
}

// Dir is the directory, for a message that has to name it.
func (f *localFiles) Dir() string { return f.dir }

// Open resolves a path a caller gave and opens it for reading.
//
// The symlinks are resolved before the directory is checked: a link
// inside the directory pointing at the private key beside it would
// otherwise pass a check on the name alone. A bare name is taken as
// being in the directory, which is what a caller who has just
// downloaded something means.
func (f *localFiles) Open(field, path string) (*os.File, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, Invalidf("%s is required: name a file inside %s.", field, f.dir)
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(f.dir, candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, Failf(ClassNotFound, "There is no file at %s. This server reads from %s.", path, f.dir)
		}
		return nil, nil, Invalidf("Cannot read %s: %s", path, err)
	}
	if !within(f.dir, resolved) {
		return nil, nil, Failf(ClassForbidden, "%s is outside %s, the one directory this server may read. "+
			"Move the file there and try again.", path, f.dir)
	}
	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return nil, nil, Invalidf("Cannot read %s: %s", path, err)
	case info.IsDir():
		return nil, nil, Invalidf("%s is a directory. Send one file at a time.", path)
	case !info.Mode().IsRegular():
		return nil, nil, Invalidf("%s is not a regular file.", path)
	case info.Size() == 0:
		return nil, nil, Invalidf("%s is empty.", path)
	}
	file, err := os.Open(resolved) //nolint:gosec // resolved inside f.dir above
	if err != nil {
		return nil, nil, Invalidf("Cannot open %s: %s", path, err)
	}
	return file, info, nil
}

// maxNameCollisions bounds the search for a free file name.
const maxNameCollisions = 100

// Create makes a file to download into, and hands back where it went.
//
// It never overwrites: the file already there belongs to whoever put it
// there, so a name already taken gains a counter. The name is the
// attachment's own, made safe — no id is mixed in, so nothing internal
// to a space lands in a file name. O_EXCL is what makes the choice and
// the creation one step, rather than a check something else can win
// between.
func (f *localFiles) Create(name string) (*os.File, string, error) {
	base := safeName(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := filepath.Join(f.dir, base)
	for n := 2; ; n++ {
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			return file, candidate, nil
		case !os.IsExist(err):
			return nil, "", Invalidf("Cannot write %s: %s", candidate, err)
		case n > maxNameCollisions:
			return nil, "", Invalidf("A hundred files in %s are already named like %s.", f.dir, base)
		}
		candidate = filepath.Join(f.dir, fmt.Sprintf("%s-%d%s", stem, n, ext))
	}
}

// within reports whether path is dir itself or something inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// maxSafeNameRunes bounds a generated file name. Every file system in
// use limits a component to 255 bytes, and a name that long is
// unreadable anyway.
const maxSafeNameRunes = 80

// safeName turns an attachment's name into one component of a path: no
// separators, no control characters, no leading dot, and short enough
// to live on any file system.
func safeName(name string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == 0:
			return '-'
		case unicode.IsControl(r):
			// Dropped: a newline in a file name is a name that lies
			// about how many files a listing has.
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, name)
	// Fields collapses the runs, including any left behind by a control
	// character dropped between two spaces.
	out := strings.Join(strings.Fields(mapped), " ")
	out = strings.TrimLeft(out, ".")
	// Windows refuses a component ending in a dot or a space.
	out = strings.TrimRight(out, ". ")
	if r := []rune(out); len(r) > maxSafeNameRunes {
		out = strings.TrimRight(string(r[:maxSafeNameRunes]), ". ")
	}
	if out == "" {
		return "attachment"
	}
	return out
}
