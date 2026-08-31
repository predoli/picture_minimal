package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newEntry(t *testing.T, dir, href, etag string, blobBytes int64, lastShown time.Time) Entry {
	t.Helper()
	blob := filepath.Join(dir, etag+".rgb565")
	if err := os.WriteFile(blob, make([]byte, blobBytes), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return Entry{
		Href:      href,
		Name:      filepath.Base(href),
		ETag:      etag,
		Blob:      blob,
		Width:     100,
		Height:    100,
		Stride:    200,
		BlobBytes: blobBytes,
		LastShown: lastShown,
	}
}

func TestRetainOnlyDropsDeselectedPhotosAndTheirBlobs(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "manifest.json"), 1<<30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	kept := newEntry(t, dir, "/dav/a.jpg", "aaa", 16, time.Now())
	dropped := newEntry(t, dir, "/dav/b.jpg", "bbb", 16, time.Now())
	st.Put(kept)
	st.Put(dropped)

	// Untagging a photo in Nextcloud shows up here as an href absent from the
	// next listing. Removal is the half of sync that is easy to forget.
	removed := st.RetainOnly(map[string]struct{}{"/dav/a.jpg": {}})

	if len(removed) != 1 || removed[0] != "b.jpg" {
		t.Errorf("removed = %v, want [b.jpg]", removed)
	}
	if _, ok := st.Get("/dav/b.jpg"); ok {
		t.Error("deselected entry is still in the manifest")
	}
	if _, ok := st.Get("/dav/a.jpg"); !ok {
		t.Error("selected entry was dropped")
	}
	if _, err := os.Stat(dropped.Blob); !os.IsNotExist(err) {
		t.Error("blob of the deselected photo was left on disk")
	}
	if _, err := os.Stat(kept.Blob); err != nil {
		t.Errorf("blob of the kept photo was deleted: %v", err)
	}
}

func TestEvictDropsLeastRecentlyShownUntilWithinBudget(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "manifest.json"), 40)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	now := time.Now()
	oldest := newEntry(t, dir, "/dav/old.jpg", "old", 20, now.Add(-72*time.Hour))
	middle := newEntry(t, dir, "/dav/mid.jpg", "mid", 20, now.Add(-1*time.Hour))
	newest := newEntry(t, dir, "/dav/new.jpg", "new", 20, now)
	for _, e := range []Entry{oldest, middle, newest} {
		st.Put(e)
	}

	if freed := st.Evict(); freed != 20 {
		t.Errorf("freed = %d, want 20 (60 bytes held against a 40 byte budget)", freed)
	}

	// The entry survives eviction even though its blob does not: the photo is
	// still selected on the server, so it should be re-rendered, not forgotten.
	entry, ok := st.Get("/dav/old.jpg")
	if !ok {
		t.Fatal("evicting a blob should not remove the manifest entry")
	}
	if entry.Blob != "" {
		t.Errorf("evicted entry still points at a blob: %q", entry.Blob)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.rgb565")); !os.IsNotExist(err) {
		t.Error("evicted blob still on disk")
	}
	if e, _ := st.Get("/dav/new.jpg"); e.Blob == "" {
		t.Error("most recently shown photo was evicted first")
	}
}

func TestBlobIDVariesWithGeometry(t *testing.T) {
	// Changing the panel resolution must re-render; a restart must not.
	if BlobID("etag", 1920, 1080) == BlobID("etag", 1024, 600) {
		t.Error("blob id ignores geometry, so a resolution change would reuse stale renders")
	}
	if BlobID("etag", 1920, 1080) != BlobID("etag", 1920, 1080) {
		t.Error("blob id is unstable across calls, so every restart would re-render everything")
	}
	if BlobID("etag1", 1920, 1080) == BlobID("etag2", 1920, 1080) {
		t.Error("blob id ignores content, so an edited photo would keep its old render")
	}
}

func TestReadyAndRenderablePartitionByGeometry(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(filepath.Join(dir, "manifest.json"), 1<<30)

	st.Put(newEntry(t, dir, "/dav/a.jpg", "aaa", 16, time.Now()))
	st.Put(Entry{Href: "/dav/b.jpg", ETag: "bbb"})                              // never rendered
	st.Put(Entry{Href: "/dav/c.jpg", ETag: "ccc", Skip: true, SkipReason: "x"}) // undecodable

	if got := len(st.Ready(100, 100)); got != 1 {
		t.Errorf("Ready = %d, want 1", got)
	}
	if got := len(st.Ready(1920, 1080)); got != 0 {
		t.Errorf("Ready at a different geometry = %d, want 0", got)
	}

	// The skipped file must not be retried on every sync.
	pending := st.Renderable(100, 100)
	if len(pending) != 1 || pending[0].Href != "/dav/b.jpg" {
		t.Errorf("Renderable = %+v, want only b.jpg", pending)
	}
}

func TestManifestSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	st, _ := Open(path, 1<<30)
	st.Put(newEntry(t, dir, "/dav/a.jpg", "aaa", 16, time.Now()))
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := Open(path, 1<<30)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entry, ok := reopened.Get("/dav/a.jpg")
	if !ok || entry.ETag != "aaa" {
		t.Fatalf("entry did not survive a restart: %+v", entry)
	}
}

func TestCorruptManifestStartsCleanRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A corrupt manifest costs a re-sync, not data. Refusing to boot would leave
	// a wall-mounted frame black until someone SSHes in.
	st, err := Open(path, 1<<30)
	if err != nil {
		t.Fatalf("Open on corrupt manifest returned an error: %v", err)
	}
	if got := len(st.Ready(100, 100)); got != 0 {
		t.Errorf("expected empty store, got %d entries", got)
	}
}

// The manifest and the render directory can drift apart — a cleared cache, a
// restored state directory. A record naming a file that is gone must be cleared
// rather than served, or the frontend fails to map it on every single request.
func TestVerifyClearsBlobsMissingFromDisk(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "manifest.json"), 1<<30)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	present := filepath.Join(dir, "present.rgb565")
	if err := os.WriteFile(present, []byte{0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}

	st.Put(Entry{Href: "/a", Name: "a", Blob: present, Width: 4, Height: 2, BlobBytes: 2})
	st.Put(Entry{Href: "/b", Name: "b", Blob: filepath.Join(dir, "gone.rgb565"), Width: 4, Height: 2})

	if dropped := st.Verify(); dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	if ready := st.Ready(4, 2); len(ready) != 1 || ready[0].Href != "/a" {
		t.Fatalf("ready = %+v, want only the entry whose blob exists", ready)
	}
	// Cleared, not forgotten: the photo is still selected, so it must queue for
	// rendering again.
	if pending := st.Renderable(4, 2); len(pending) != 1 || pending[0].Href != "/b" {
		t.Fatalf("renderable = %+v, want the entry whose blob went missing", pending)
	}
}
