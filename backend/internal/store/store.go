// Package store tracks which remote photos are cached locally and which have
// been rendered for the current panel geometry.
//
// Entries are keyed by remote href, which makes the sync diff fall out
// naturally: whatever the server no longer lists has been untagged or deleted,
// and its local copies go with it.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one photo's local state.
type Entry struct {
	Href     string    `json:"href"`
	Name     string    `json:"name"`
	ETag     string    `json:"etag"`
	Modified time.Time `json:"modified"`

	// Blob is the rendered RGB565 file, empty until it has been produced for
	// the current geometry.
	Blob   string `json:"blob,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Stride int    `json:"stride,omitempty"`

	SourceBytes int64     `json:"sourceBytes"`
	BlobBytes   int64     `json:"blobBytes"`
	LastShown   time.Time `json:"lastShown,omitempty"`

	// Skip marks a file that could not be decoded, so it is not retried on
	// every sync. Cleared if its ETag changes.
	Skip       bool   `json:"skip,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

// ID is the stable identity the frontend echoes back as "last".
func (e Entry) ID() string { return BlobID(e.ETag, e.Width, e.Height) }

// BlobID keys the render cache on content and geometry, so changing the panel
// resolution re-renders while a restart does not.
func BlobID(etag string, width, height int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%dx%d", etag, width, height)))
	return hex.EncodeToString(sum[:16])
}

type Store struct {
	mu      sync.Mutex
	entries map[string]*Entry
	path    string
	budget  int64
	dirty   bool
}

func Open(manifestPath string, budgetBytes int64) (*Store, error) {
	s := &Store{
		entries: map[string]*Entry{},
		path:    manifestPath,
		budget:  budgetBytes,
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		// A corrupt manifest costs a re-sync, not data: the photos still live on
		// the server. Starting clean beats refusing to boot.
		return s, nil
	}
	for _, e := range entries {
		s.entries[e.Href] = e
	}
	return s, nil
}

// Save writes the manifest atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	entries := make([]*Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.dirty = false
	s.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].Href < entries[j].Href })

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".manifest-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Get returns a copy. The store owns every *Entry it holds: handing out
// pointers would let a caller mutate fields that Ready and Renderable read under
// the lock, which is a data race the mutex cannot see.
func (s *Store) Get(href string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[href]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Put stores a copy, so the caller's value stays independent afterwards.
func (s *Store) Put(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := e
	s.entries[e.Href] = &stored
	s.dirty = true
}

// RetainOnly drops every entry whose href is absent from keep, deleting the
// files it owned. This is how untagging a photo removes it from the frame.
func (s *Store) RetainOnly(keep map[string]struct{}) []string {
	s.mu.Lock()
	var removed []string
	for href, entry := range s.entries {
		if _, ok := keep[href]; ok {
			continue
		}
		removed = append(removed, entry.Name)
		s.removeFiles(entry)
		delete(s.entries, href)
		s.dirty = true
	}
	s.mu.Unlock()
	return removed
}

// removeFiles deletes an entry's rendered blob. Caller holds the lock.
func (s *Store) removeFiles(e *Entry) {
	if e.Blob != "" {
		os.Remove(e.Blob)
	}
}

// Renderable lists entries still needing a blob at the given geometry,
// newest first so a fresh frame has something to show as soon as possible.
func (s *Store) Renderable(width, height int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var pending []Entry
	for _, e := range s.entries {
		if e.Skip {
			continue
		}
		if e.Blob != "" && e.Width == width && e.Height == height {
			continue
		}
		pending = append(pending, *e)
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].Modified.After(pending[j].Modified)
	})
	return pending
}

// Ready lists entries with a usable blob for the given geometry.
func (s *Store) Ready(width, height int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ready []Entry
	for _, e := range s.entries {
		if e.Blob != "" && e.Width == width && e.Height == height && !e.Skip {
			ready = append(ready, *e)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Href < ready[j].Href })
	return ready
}

// ClearBlob forgets an entry's rendered file so it is produced again. Called
// when the blob turns out to be missing from disk.
func (s *Store) ClearBlob(href string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[href]; ok && e.Blob != "" {
		e.Blob, e.BlobBytes = "", 0
		e.Width, e.Height, e.Stride = 0, 0, 0
		s.dirty = true
	}
}

// Verify reconciles the manifest against the disk, clearing the record of any
// blob that is no longer there, and returns how many it dropped.
//
// The manifest and the render directory can diverge — a wiped cache, a restored
// state directory, a full SD card — and a stale record is worse than no record:
// the frame would keep serving a path the frontend cannot map, once per
// interval, forever. Clearing it puts the photo back in the render queue.
func (s *Store) Verify() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	dropped := 0
	for _, e := range s.entries {
		if e.Blob == "" {
			continue
		}
		if _, err := os.Stat(e.Blob); err != nil {
			e.Blob, e.BlobBytes = "", 0
			e.Width, e.Height, e.Stride = 0, 0, 0
			s.dirty = true
			dropped++
		}
	}
	return dropped
}

func (s *Store) MarkShown(href string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[href]; ok {
		e.LastShown = time.Now()
		s.dirty = true
	}
}

// Evict deletes rendered blobs, least recently shown first, until the cache fits
// its budget. Originals are never kept on disk, so blobs are the whole budget.
//
// Only blobs are dropped, never manifest entries: the photo is still selected on
// the server, so it should be re-rendered rather than forgotten.
func (s *Store) Evict() (freed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var total int64
	var candidates []*Entry
	for _, e := range s.entries {
		if e.Blob == "" {
			continue
		}
		total += e.BlobBytes
		candidates = append(candidates, e)
	}
	if total <= s.budget {
		return 0
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastShown.Before(candidates[j].LastShown)
	})

	for _, e := range candidates {
		if total <= s.budget {
			break
		}
		s.removeFiles(e)
		total -= e.BlobBytes
		freed += e.BlobBytes
		e.Blob, e.BlobBytes = "", 0
		s.dirty = true
	}
	return freed
}

// Len is the number of photos the manifest tracks, for logging at startup.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
