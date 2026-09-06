// Package directory turns a Chat user id into an email address and a
// display name, and remembers what it learned.
//
// Every consumer of this package treats the answer as enrichment. A
// lookup that fails costs the email field and nothing else: no row is
// ever dropped and no list is ever emptied because the People API
// refused. That rule is the whole reason this is a package rather than
// a call site. It was learned the expensive way: a 403 for a scope
// nobody had granted once turned "who is in this space" into an empty
// list, which a reader takes to mean the space is empty.
package directory

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Person is what a lookup yields. An empty Email means unresolved,
// which is a normal answer for an external or deleted account.
type Person struct {
	Email       string
	DisplayName string
}

// cacheVersion is bumped when the file layout changes. A file from a
// different version is discarded rather than migrated: it holds only
// answers that can be fetched again.
const cacheVersion = 1

type entry struct {
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]entry `json:"entries"`
}

// Cache is an on-disk map from "users/{id}" to the person behind it.
//
// It lives on disk rather than in memory because a stdio server is
// started fresh for every client session. A per-process cache would
// re-resolve the same colleagues on every conversation, which is both
// slower and a larger share of the People API quota than the answers
// are worth.
//
// Every operation is best effort. A cache that cannot be read or
// written costs a round trip, never a result.
type Cache struct {
	path string
	ttl  time.Duration
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	entries map[string]entry
	loaded  bool
}

// NewCache builds a cache backed by path. An empty path disables
// persistence and keeps the answers in memory for this process only.
func NewCache(path string, ttl time.Duration, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Cache{path: path, ttl: ttl, log: log, now: time.Now, entries: map[string]entry{}}
}

// Get returns the unexpired entries among ids.
func (c *Cache) Get(ids []string) map[string]Person {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	out := make(map[string]Person, len(ids))
	cutoff := c.now().Add(-c.ttl)
	for _, id := range ids {
		e, ok := c.entries[id]
		if !ok || e.FetchedAt.Before(cutoff) {
			continue
		}
		out[id] = Person{Email: e.Email, DisplayName: e.DisplayName}
	}
	return out
}

// Put records what a lookup learned.
//
// A miss is remembered too, but only for this process: saveLocked
// leaves it out of the file. Someone outside the directory — an
// external sender, a deleted account, a Chat app — would otherwise cost
// a round trip on every tool call for the whole conversation, and
// writing that down would hide the day they join.
func (c *Cache) Put(people map[string]Person) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	now := c.now()
	var resolved int
	for id, p := range people {
		c.entries[id] = entry{Email: p.Email, DisplayName: p.DisplayName, FetchedAt: now}
		if p.Email != "" {
			resolved++
		}
	}
	if resolved == 0 {
		return
	}
	c.saveLocked()
}

// loadLocked reads the file once. A missing, unreadable or corrupt file
// leaves the cache empty, and the next write replaces it.
func (c *Cache) loadLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	if c.path == "" {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.log.Debug("directory_cache_unreadable", "error", err.Error())
		}
		return
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != cacheVersion {
		c.log.Debug("directory_cache_discarded", "path", c.path)
		return
	}
	for id, e := range f.Entries {
		c.entries[id] = e
	}
}

// saveLocked writes the cache, dropping entries that have expired and
// the ones that resolved to nobody.
func (c *Cache) saveLocked() {
	if c.path == "" {
		return
	}
	cutoff := c.now().Add(-c.ttl)
	live := make(map[string]entry, len(c.entries))
	for id, e := range c.entries {
		if e.FetchedAt.Before(cutoff) {
			delete(c.entries, id)
			continue
		}
		if e.Email == "" {
			continue
		}
		live[id] = e
	}
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Entries: live})
	if err != nil {
		c.log.Debug("directory_cache_unwritable", "error", err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		c.log.Debug("directory_cache_unwritable", "error", err.Error())
		return
	}
	// The file holds colleagues' names and email addresses, so it is
	// owner-only like everything else in the profile directory.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		c.log.Debug("directory_cache_unwritable", "error", err.Error())
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		c.log.Debug("directory_cache_unwritable", "error", err.Error())
	}
}
