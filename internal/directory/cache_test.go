package directory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// tempCache builds a cache under a temporary directory. No test in this
// package may touch the real profile directory.
func tempCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	return NewCache(filepath.Join(t.TempDir(), "directory-cache.json"), ttl, nil)
}

func TestCacheRoundTripsThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	first := NewCache(path, time.Hour, nil)
	first.Put(map[string]Person{"users/1": {Email: "janedoe@example.com", DisplayName: "Jane Doe"}})

	// A second cache over the same file is the next run of the server.
	second := NewCache(path, time.Hour, nil)
	got := second.Get([]string{"users/1"})
	if got["users/1"].Email != "janedoe@example.com" {
		t.Errorf("cache = %+v, want the entry the first run wrote", got)
	}
	if got["users/1"].DisplayName != "Jane Doe" {
		t.Errorf("display name = %q", got["users/1"].DisplayName)
	}
}

// The file holds colleagues' names and addresses, so it is owner-only
// like everything else in the profile directory.
func TestCacheFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply")
	}
	path := filepath.Join(t.TempDir(), "sub", "directory-cache.json")
	NewCache(path, time.Hour, nil).Put(map[string]Person{"users/1": {Email: "janedoe@example.com"}})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
}

func TestCacheExpiresAnEntry(t *testing.T) {
	c := tempCache(t, time.Hour)
	c.Put(map[string]Person{"users/1": {Email: "janedoe@example.com"}})

	// Two hours on, with a one-hour lifetime, the answer is stale.
	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if got := c.Get([]string{"users/1"}); len(got) != 0 {
		t.Errorf("cache = %+v, want the expired entry left out", got)
	}
}

// A person outside the directory is remembered for this process, so
// they do not cost a round trip on every tool call of a conversation.
// They are not written down: that would hide the day they join.
func TestAMissIsRememberedButNotWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	c := NewCache(path, time.Hour, nil)
	c.Put(map[string]Person{"users/1": {}, "users/2": {Email: "janedoe@example.com"}})

	if got := c.Get([]string{"users/1"}); len(got) != 1 {
		t.Errorf("cache = %+v, want the miss remembered in memory", got)
	}
	if got := NewCache(path, time.Hour, nil).Get([]string{"users/1"}); len(got) != 0 {
		t.Errorf("file = %+v, want the miss left out of it", got)
	}
	if got := NewCache(path, time.Hour, nil).Get([]string{"users/2"}); len(got) != 1 {
		t.Errorf("file = %+v, want the resolved person written down", got)
	}
}

// A page of nothing but misses has nothing worth writing, so it must
// not rewrite the file.
func TestAPageOfMissesWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	NewCache(path, time.Hour, nil).Put(map[string]Person{"users/1": {}})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat = %v, want no file written", err)
	}
}

// A cache is an optimisation. Anything wrong with the file costs a
// round trip, never a result.
func TestCacheSurvivesABrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewCache(path, time.Hour, nil)
	if got := c.Get([]string{"users/1"}); len(got) != 0 {
		t.Errorf("cache = %+v, want a corrupt file to read as empty", got)
	}
	c.Put(map[string]Person{"users/1": {Email: "janedoe@example.com"}})
	if got := NewCache(path, time.Hour, nil).Get([]string{"users/1"}); len(got) != 1 {
		t.Error("a corrupt file should be replaced by the next write")
	}
}

// A file from an older layout holds nothing that cannot be fetched
// again, so it is discarded rather than migrated.
func TestCacheDiscardsAnUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"entries":{"users/1":{"email":"x@example.com"}}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := NewCache(path, time.Hour, nil).Get([]string{"users/1"}); len(got) != 0 {
		t.Errorf("cache = %+v, want a file from another version ignored", got)
	}
}

// An empty path is a cache that cannot persist. It must still answer.
func TestCacheWithoutAPathStaysInMemory(t *testing.T) {
	c := NewCache("", time.Hour, nil)
	c.Put(map[string]Person{"users/1": {Email: "janedoe@example.com"}})
	if got := c.Get([]string{"users/1"}); got["users/1"].Email != "janedoe@example.com" {
		t.Errorf("cache = %+v", got)
	}
}

// Expired entries are dropped when the file is rewritten, so a cache
// does not grow for the life of the profile.
func TestCacheDropsExpiredEntriesOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory-cache.json")
	c := NewCache(path, time.Hour, nil)
	c.Put(map[string]Person{"users/old": {Email: "old@example.com"}})

	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	c.Put(map[string]Person{"users/new": {Email: "new@example.com"}})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) == "" {
		t.Fatal("empty cache file")
	}
	if strings.Contains(string(data), "users/old") {
		t.Errorf("the expired entry is still in the file: %s", data)
	}
	if !strings.Contains(string(data), "users/new") {
		t.Errorf("the fresh entry is missing: %s", data)
	}
}
