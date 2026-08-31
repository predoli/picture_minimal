// Package frame coordinates pairing, syncing, and rendering.
//
// It owns the frame's whole lifecycle: unpaired frames advertise a QR code and
// poll for approval; paired frames enumerate the user's selection, render what
// is missing, and serve whatever is ready. Requests are answered from state that
// is already prepared, so the socket never blocks behind a download or a decode.
package frame

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olivermaier/picture-frame/backend/internal/auth"
	"github.com/olivermaier/picture-frame/backend/internal/config"
	"github.com/olivermaier/picture-frame/backend/internal/logging"
	"github.com/olivermaier/picture-frame/backend/internal/nextcloud"
	"github.com/olivermaier/picture-frame/backend/internal/render"
	"github.com/olivermaier/picture-frame/backend/internal/store"
)

// pollInterval is how often an unpaired frame asks whether the user has
// approved. Brisk enough that photos appear right after they tap Grant access.
const pollInterval = 3 * time.Second

type ResultKind int

const (
	ResultImage ResultKind = iota
	ResultPairing
	ResultEmpty
)

// Result is what the IPC layer turns into a wire reply.
type Result struct {
	Kind     ResultKind
	Entry    store.Entry
	LoginURL string
	Host     string
	Message  string
}

type Frame struct {
	cfg   config.Config
	store *store.Store

	mu     sync.Mutex
	creds  auth.Credentials
	client *nextcloud.Client
	flow   *nextcloud.Flow
	notice string // surfaced on screen when there is nothing to show

	// Geometry is declared by the frontend, so nothing can be rendered until it
	// has asked at least once.
	width, height int

	wake chan struct{}
}

func New(cfg config.Config, st *store.Store) *Frame {
	f := &Frame{cfg: cfg, store: st, wake: make(chan struct{}, 1)}

	creds, err := auth.Load(cfg.AuthPath())
	switch {
	case err != nil:
		log.Printf("not paired yet; will show a QR code")

	case !sameServer(creds.Server, cfg.Server):
		// An app password is issued by, and only valid for, one server. Keeping
		// it after the configured server changes would leave the frame talking
		// to the old host forever, ignoring the edit with no explanation.
		log.Printf("stored credential is for %s but the config says %s; discarding it and re-pairing",
			creds.Server, cfg.Server)
		if err := auth.Clear(cfg.AuthPath()); err != nil {
			log.Printf("clear stale credentials: %v", err)
		}

	default:
		f.creds = creds
		f.client = nextcloud.New(creds.Server, creds.LoginName, creds.AppPassword)
		log.Printf("paired as %s on %s", creds.LoginName, creds.Server)
	}
	return f
}

// sameServer compares server URLs the way Nextcloud reports them, which may
// differ from the configured value by a trailing slash or by case in the host.
func sameServer(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "/"), strings.TrimSuffix(b, "/"))
}

func (f *Frame) paired() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.client != nil
}

func (f *Frame) geometry() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.width, f.height
}

// Run drives pairing and syncing until ctx is cancelled.
func (f *Frame) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if !f.paired() {
			f.pairStep(ctx)
			continue
		}

		started := time.Now()
		if dropped := f.store.Verify(); dropped > 0 {
			log.Printf("re-rendering %d photo(s) whose blob was missing from disk", dropped)
		}
		f.syncOnce(ctx)
		f.renderPending(ctx)

		if freed := f.store.Evict(); freed > 0 {
			log.Printf("evicted %d MiB of rendered images to stay within budget", freed>>20)
		}
		if err := f.store.Save(); err != nil {
			log.Printf("save manifest: %v", err)
		}

		if ctx.Err() == nil {
			log.Printf("sync cycle finished in %s; next in %s",
				time.Since(started).Round(time.Millisecond), f.cfg.SyncInterval)
		}

		select {
		case <-ctx.Done():
		case <-time.After(f.cfg.SyncInterval):
		case <-f.wake: // geometry became known or changed
		}
	}
}

// signal nudges the worker without blocking if one is already queued.
func (f *Frame) signal() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// --- pairing ---

func (f *Frame) pairStep(ctx context.Context) {
	f.mu.Lock()
	flow := f.flow
	f.mu.Unlock()

	if flow == nil || flow.Expired() {
		// A code nobody scanned within the token's lifetime is worse than
		// useless: it looks scannable but cannot be redeemed.
		started, err := nextcloud.StartFlow(ctx, f.cfg.Server)
		if err != nil {
			f.setNotice(fmt.Sprintf("Cannot reach %s", f.cfg.Server))
			log.Printf("start login flow: %v", err)
			sleep(ctx, pollInterval)
			return
		}
		f.mu.Lock()
		f.flow = started
		f.notice = ""
		f.mu.Unlock()
		log.Printf("pairing: waiting for approval at %s", started.LoginURL)
		return
	}

	session, err := flow.Poll(ctx)
	switch {
	case errors.Is(err, nextcloud.ErrFlowPending):
		sleep(ctx, pollInterval)
		return
	case errors.Is(err, nextcloud.ErrFlowExpired):
		f.mu.Lock()
		f.flow = nil
		f.mu.Unlock()
		return
	case err != nil:
		log.Printf("poll login flow: %v", err)
		sleep(ctx, pollInterval)
		return
	}

	// The app password is returned exactly once. Persist before anything else
	// can fail, or the frame is left believing it paired with nothing to show
	// for it.
	creds := auth.Credentials{
		Server:      session.Server,
		LoginName:   session.LoginName,
		AppPassword: session.AppPassword,
	}
	if err := auth.Save(f.cfg.AuthPath(), creds); err != nil {
		log.Printf("FATAL: paired but could not save credentials: %v", err)
		f.setNotice("Paired, but the credential could not be saved")
		sleep(ctx, pollInterval)
		return
	}

	f.mu.Lock()
	f.creds = creds
	f.client = nextcloud.New(creds.Server, creds.LoginName, creds.AppPassword)
	f.flow = nil
	f.notice = ""
	f.mu.Unlock()

	log.Printf("paired as %s", creds.LoginName)
}

// unpair drops the credential and returns to the QR screen. This is what a user
// revoking the app password in their Nextcloud security settings looks like.
func (f *Frame) unpair(reason string) {
	log.Printf("credentials rejected (%s); returning to pairing", reason)
	if err := auth.Clear(f.cfg.AuthPath()); err != nil {
		log.Printf("clear credentials: %v", err)
	}
	f.mu.Lock()
	f.creds = auth.Credentials{}
	f.client = nil
	f.flow = nil
	f.mu.Unlock()
}

// --- syncing ---

func (f *Frame) syncOnce(ctx context.Context) {
	f.mu.Lock()
	client := f.client
	f.mu.Unlock()
	if client == nil {
		return
	}

	started := time.Now()
	files, err := f.list(ctx, client)
	if err != nil {
		if errors.Is(err, nextcloud.ErrUnauthorized) {
			f.unpair("listing rejected")
			return
		}
		log.Printf("list: %v", err)
		return
	}

	log.Printf("%s selected %d photo(s) in %s", f.describeSelection(), len(files),
		time.Since(started).Round(time.Millisecond))

	var added, changed int
	keep := make(map[string]struct{}, len(files))
	for _, file := range files {
		keep[file.Href] = struct{}{}
		logging.Debugf("listed %s (%d bytes, etag %s, %s)",
			file.Href, file.Size, file.ETag, file.ContentType)

		existing, ok := f.store.Get(file.Href)
		if ok && existing.ETag == file.ETag {
			continue
		}
		if ok {
			changed++
			log.Printf("%s changed on the server (etag %s -> %s); it will be rendered again",
				file.Name(), existing.ETag, file.ETag)
		} else {
			added++
			log.Printf("new photo %s%s", file.Name(), sizeHint(file.Size))
		}

		entry := store.Entry{
			Href:        file.Href,
			Name:        file.Name(),
			ETag:        file.ETag,
			Modified:    file.Modified,
			SourceBytes: file.Size,
		}
		if ok && existing.Blob != "" {
			os.Remove(existing.Blob) // the file changed; its render is stale
		}
		f.store.Put(entry)
	}

	// Anything the server no longer lists has been untagged or deleted. Removal
	// is the half that is easy to forget, and it is what makes tag-based
	// curation feel live.
	if removed := f.store.RetainOnly(keep); len(removed) > 0 {
		log.Printf("dropped %d photo(s) no longer selected: %v", len(removed), removed)
	}
	if added == 0 && changed == 0 {
		logging.Debugf("nothing new; %d photo(s) already tracked", len(files))
	}

	if len(files) == 0 {
		f.setNotice(f.emptyHint())
	} else {
		f.setNotice("")
	}
}

func (f *Frame) list(ctx context.Context, client *nextcloud.Client) ([]nextcloud.File, error) {
	switch f.cfg.Selection {
	case config.SelectionFavorites:
		return client.ListFavorites(ctx)

	case config.SelectionFolder:
		return client.ListFolder(ctx, f.cfg.Folder)

	default: // tag
		// Resolved every sync rather than cached forever: a tag deleted and
		// recreated keeps its name but gets a new id.
		id, found, err := client.ResolveTag(ctx, f.cfg.Tag)
		if err != nil {
			return nil, err
		}
		if found {
			logging.Debugf("tag %q resolved to id %d", f.cfg.Tag, id)
		}
		if !found {
			created, err := client.CreateTag(ctx, f.cfg.Tag)
			if err != nil {
				// An administrator may have restricted tag creation. Say so on
				// screen instead of retrying silently forever.
				f.setNotice(fmt.Sprintf("Ask an administrator to create the tag %q", f.cfg.Tag))
				return nil, err
			}
			log.Printf("created tag %q (id %d)", f.cfg.Tag, created)
			id = created
		}
		return client.ListByTag(ctx, id)
	}
}

// sizeHint renders a byte count for a log line, or nothing at all when the
// server did not report one — "(0 KiB)" reads like a broken file.
func sizeHint(bytes int64) string {
	if bytes <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d KiB)", bytes>>10)
}

// lastShownAgo renders how long ago a photo was last on screen, for the log.
func lastShownAgo(e store.Entry) string {
	if e.LastShown.IsZero() {
		return "never"
	}
	return time.Since(e.LastShown).Round(time.Second).String() + " ago"
}

// describeSelection names the selection mode for a log line.
func (f *Frame) describeSelection() string {
	switch f.cfg.Selection {
	case config.SelectionFavorites:
		return "favorites"
	case config.SelectionFolder:
		return fmt.Sprintf("folder %q", f.cfg.Folder)
	default:
		return fmt.Sprintf("tag %q", f.cfg.Tag)
	}
}

func (f *Frame) emptyHint() string {
	switch f.cfg.Selection {
	case config.SelectionFavorites:
		return "Mark photos as favourites in Nextcloud to see them here."
	case config.SelectionFolder:
		return fmt.Sprintf("Put photos in %s in Nextcloud to see them here.", f.cfg.Folder)
	default:
		return fmt.Sprintf("Tag photos with %q in Nextcloud to see them here.", f.cfg.Tag)
	}
}

// --- rendering ---

func (f *Frame) renderPending(ctx context.Context) {
	width, height := f.geometry()
	if width == 0 || height == 0 {
		return // the frontend has not told us the panel size yet
	}

	f.mu.Lock()
	client := f.client
	f.mu.Unlock()
	if client == nil {
		return
	}

	if err := os.MkdirAll(f.cfg.RenderDir(), 0o755); err != nil {
		log.Printf("create render dir: %v", err)
		return
	}

	pending := f.store.Renderable(width, height)
	if len(pending) == 0 {
		logging.Debugf("nothing to render at %dx%d", width, height)
		return
	}
	log.Printf("rendering %d photo(s) for a %dx%d panel, newest first", len(pending), width, height)

	for i, entry := range pending {
		if ctx.Err() != nil {
			log.Printf("rendering interrupted after %d of %d", i, len(pending))
			return
		}
		// entry is this loop's own copy; renderOne fills it in and Put writes it
		// back under the store's lock.
		if err := f.renderOne(ctx, client, &entry, width, height); err != nil {
			if errors.Is(err, nextcloud.ErrUnauthorized) {
				f.unpair("download rejected")
				return
			}
			// One bad photo must not stall the rest. Remember the failure so it
			// is not retried on every sync, but keep the entry: the file is
			// still selected on the server.
			log.Printf("skipping %s (%d of %d): %v", entry.Name, i+1, len(pending), err)
			entry.Skip = true
			entry.SkipReason = err.Error()
			f.store.Put(entry)
			continue
		}
		f.store.Put(entry)
		log.Printf("rendered %s (%d of %d) to %dx%d, %d KiB",
			entry.Name, i+1, len(pending), entry.Width, entry.Height, entry.BlobBytes>>10)
		if err := f.store.Save(); err != nil {
			log.Printf("save manifest: %v", err)
		}
	}
}

func (f *Frame) renderOne(ctx context.Context, client *nextcloud.Client, entry *store.Entry, width, height int) error {
	// Originals are fetched and discarded rather than cached: the rendered blob
	// is what gets displayed, and keeping both would double SD-card wear and
	// space for a file that is only re-read if the panel geometry changes.
	const maxDownload = 256 << 20
	started := time.Now()
	data, err := client.Download(ctx, entry.Href, maxDownload)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	logging.Debugf("downloaded %s: %d KiB in %s",
		entry.Name, len(data)>>10, time.Since(started).Round(time.Millisecond))

	decodedAt := time.Now()
	decoded, err := render.Decode(data, f.cfg.MaxSourcePixels)
	if err != nil {
		return err
	}
	bounds := decoded.Image.Bounds()
	logging.Debugf("decoded %s: %s %dx%d, exif orientation %d, in %s",
		entry.Name, decoded.Format, bounds.Dx(), bounds.Dy(), decoded.Orientation,
		time.Since(decodedAt).Round(time.Millisecond))

	fitAt := time.Now()
	blob, err := render.Fit(decoded.Image, decoded.Orientation, width, height)
	if err != nil {
		return err
	}
	logging.Debugf("fitted %s to %dx%d in %s",
		entry.Name, blob.Width, blob.Height, time.Since(fitAt).Round(time.Millisecond))

	path := filepath.Join(f.cfg.RenderDir(), store.BlobID(entry.ETag, width, height)+".rgb565")
	if err := writeAtomic(path, blob.Pixels); err != nil {
		return fmt.Errorf("write blob: %w", err)
	}

	if entry.Blob != "" && entry.Blob != path {
		os.Remove(entry.Blob)
	}
	entry.Blob = path
	entry.Width = blob.Width
	entry.Height = blob.Height
	entry.Stride = blob.Stride
	entry.BlobBytes = int64(len(blob.Pixels))
	entry.Skip = false
	entry.SkipReason = ""
	return nil
}

// writeAtomic keeps a half-written blob from ever being mapped by the frontend.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".blob-*")
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
	return os.Rename(tmp.Name(), path)
}

// --- serving ---

// Next answers one frontend request. It never blocks on the network: whatever is
// ready is served, and whatever is not simply has not been rendered yet.
func (f *Frame) Next(width, height int, last string) Result {
	f.mu.Lock()
	if width > 0 && height > 0 && (width != f.width || height != f.height) {
		log.Printf("panel geometry is %dx%d", width, height)
		f.width, f.height = width, height
		defer f.signal() // wake the worker so rendering can start
	}
	flow := f.flow
	client := f.client
	notice := f.notice
	f.mu.Unlock()

	logging.Debugf("request: %dx%d, last=%q", width, height, last)

	if client == nil {
		if flow != nil {
			return Result{Kind: ResultPairing, LoginURL: flow.LoginURL, Host: f.cfg.Hostname}
		}
		message := notice
		if message == "" {
			message = "Connecting to " + f.cfg.Server
		}
		return Result{Kind: ResultEmpty, Message: message}
	}

	ready := f.store.Ready(width, height)
	if len(ready) == 0 {
		message := notice
		if message == "" {
			message = "Preparing photos..."
		}
		return Result{Kind: ResultEmpty, Message: message}
	}

	entry := pick(ready, last)

	// The manifest can outlive the file it names. Serving a path the frontend
	// cannot open would repeat on every request until the next sync, so drop the
	// record here and let the worker render it again.
	if _, err := os.Stat(entry.Blob); err != nil {
		log.Printf("%s: rendered blob is gone, will render it again", entry.Name)
		f.store.ClearBlob(entry.Href)
		f.signal()
		return Result{Kind: ResultEmpty, Message: "Preparing photos..."}
	}

	f.store.MarkShown(entry.Href)
	logging.Debugf("serving %s, chosen from %d ready (last shown %s)",
		entry.Name, len(ready), lastShownAgo(entry))
	return Result{Kind: ResultImage, Entry: entry}
}

// pick chooses the next photo: least recently shown first, with enough
// randomness that the rotation does not feel mechanical, and never repeating the
// image already on screen when there is an alternative.
func pick(ready []store.Entry, last string) store.Entry {
	candidates := make([]store.Entry, 0, len(ready))
	for _, e := range ready {
		if e.ID() != last {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		candidates = ready
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastShown.Before(candidates[j].LastShown)
	})

	// Draw from the staler quarter so every photo comes round, without the order
	// being identical on every cycle.
	window := len(candidates) / 4
	if window < 1 {
		window = 1
	}
	return candidates[rand.Intn(window)]
}

func (f *Frame) setNotice(text string) {
	f.mu.Lock()
	f.notice = text
	f.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
