package frame

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivermaier/picture-frame/backend/internal/auth"
	"github.com/olivermaier/picture-frame/backend/internal/config"
	"github.com/olivermaier/picture-frame/backend/internal/store"
)

// fakeNextcloud implements just enough of Nextcloud to take a frame all the way
// from unpaired to displaying a photo: Login Flow v2, the system tag vocabulary,
// a filter-files REPORT, and the file itself.
type fakeNextcloud struct {
	*httptest.Server
	approved atomic.Bool
	// listed controls whether the REPORT returns the photo, so a test can
	// simulate the user untagging it.
	listed atomic.Bool
	// revoked makes every request fail with 401, standing in for the user
	// revoking the app password. A flag rather than a handler swap: replacing a
	// live server's Handler races with its in-flight request goroutines.
	revoked atomic.Bool
	polls   atomic.Int32
}

func newFakeNextcloud(t *testing.T) *fakeNextcloud {
	t.Helper()

	fake := &fakeNextcloud{}
	fake.listed.Store(true)

	photo := jpegBytes(t, 400, 200)

	mux := http.NewServeMux()
	fake.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if fake.revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		}))
	t.Cleanup(fake.Close)

	mux.HandleFunc("/index.php/login/v2", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"poll":{"token":"tok","endpoint":"`+fake.URL+
			`/index.php/login/v2/poll"},"login":"`+fake.URL+`/login/v2/flow/abc"}`)
	})

	mux.HandleFunc("/index.php/login/v2/poll", func(w http.ResponseWriter, r *http.Request) {
		fake.polls.Add(1)
		if !fake.approved.Load() {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"server":"`+fake.URL+
			`","loginName":"alice","appPassword":"app-secret"}`)
	})

	mux.HandleFunc("/remote.php/dav/systemtags/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response><d:href>/remote.php/dav/systemtags/17</d:href><d:propstat><d:prop>
    <oc:id>17</oc:id><oc:display-name>Frame</oc:display-name>
    <oc:user-assignable>true</oc:user-assignable>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)
	})

	mux.HandleFunc("/remote.php/dav/files/alice/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" {
			w.WriteHeader(http.StatusMultiStatus)
			if !fake.listed.Load() {
				io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"></d:multistatus>`)
				return
			}
			io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/alice/holiday.jpg</d:href>
    <d:propstat><d:prop>
      <d:getetag>&quot;etag-1&quot;</d:getetag>
      <d:getcontentlength>1024</d:getcontentlength>
      <d:getcontenttype>image/jpeg</d:getcontenttype>
      <d:getlastmodified>Wed, 12 Mar 2025 10:04:00 GMT</d:getlastmodified>
    </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`)
			return
		}

		// GET of the photo itself.
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(photo)
	})

	return fake
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func testConfig(t *testing.T, server string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Server = server
	cfg.StateDir = t.TempDir()
	cfg.SyncInterval = 200 * time.Millisecond
	cfg.Hostname = "testframe"
	return cfg
}

// startFrame runs the coordinator and, crucially, waits for it to stop before
// the test's temp directories are removed.
//
// t.Cleanup is LIFO and t.TempDir() registered its removal earlier (inside
// testConfig), so this runs first. Without it the sync goroutine is still
// writing blobs while TempDir tries to delete the directory underneath it.
func startFrame(t *testing.T, f *Frame) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitFor polls until cond holds, so the tests track real progress instead of
// sleeping for a guessed duration.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPairThenSyncThenServe(t *testing.T) {
	fake := newFakeNextcloud(t)
	cfg := testConfig(t, fake.URL)

	st, err := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	f := New(cfg, st)
	startFrame(t, f)

	// Before approval the frame must advertise a QR code, and it must do so
	// without blocking: the frontend gives up after five seconds.
	waitFor(t, "the pairing screen", func() bool {
		return f.Next(400, 200, "").Kind == ResultPairing
	})

	result := f.Next(400, 200, "")
	if result.LoginURL == "" {
		t.Error("pairing result carries no URL, so there would be nothing to encode")
	}
	if result.Host != "testframe" {
		t.Errorf("host = %q, want testframe (this is how two frames are told apart)", result.Host)
	}

	// The user scans the code and approves in their browser.
	fake.approved.Store(true)

	waitFor(t, "a rendered photo", func() bool {
		return f.Next(400, 200, "").Kind == ResultImage
	})

	image := f.Next(400, 200, "")
	entry := image.Entry
	if entry.Width != 400 || entry.Height != 200 {
		t.Errorf("rendered %dx%d, want the panel geometry 400x200", entry.Width, entry.Height)
	}
	if entry.Stride != 400*2 {
		t.Errorf("stride = %d, want %d for packed RGB565", entry.Stride, 400*2)
	}

	// The blob must be exactly the size the frontend will map, or mmap fails.
	info, err := os.Stat(entry.Blob)
	if err != nil {
		t.Fatalf("stat blob: %v", err)
	}
	if want := int64(entry.Stride) * int64(entry.Height); info.Size() != want {
		t.Errorf("blob is %d bytes, want stride*height = %d", info.Size(), want)
	}

	// The credential must be on disk at 0600 so a restart does not re-pair.
	authInfo, err := os.Stat(cfg.AuthPath())
	if err != nil {
		t.Fatalf("credentials were not persisted: %v", err)
	}
	if perm := authInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("auth.json mode = %o, want 600", perm)
	}
}

func TestUntaggingRemovesThePhotoAndItsBlob(t *testing.T) {
	fake := newFakeNextcloud(t)
	fake.approved.Store(true)
	cfg := testConfig(t, fake.URL)

	st, _ := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	f := New(cfg, st)

	startFrame(t, f)

	waitFor(t, "the first photo", func() bool {
		return f.Next(400, 200, "").Kind == ResultImage
	})
	blob := f.Next(400, 200, "").Entry.Blob

	// The user removes the tag in Nextcloud. Removal is the half of sync that is
	// easy to forget, and it is what makes tag curation feel live.
	fake.listed.Store(false)

	waitFor(t, "the photo to leave the rotation", func() bool {
		return f.Next(400, 200, "").Kind == ResultEmpty
	})

	if _, err := os.Stat(blob); !os.IsNotExist(err) {
		t.Error("blob of the untagged photo was left on disk")
	}
	if msg := f.Next(400, 200, "").Message; msg == "" {
		t.Error("the empty state must explain how to get photos onto the frame")
	}
}

func TestRevokedCredentialsReturnToPairing(t *testing.T) {
	fake := newFakeNextcloud(t)
	fake.approved.Store(true)
	cfg := testConfig(t, fake.URL)

	st, _ := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	f := New(cfg, st)

	startFrame(t, f)

	waitFor(t, "pairing to complete", func() bool {
		_, err := os.Stat(cfg.AuthPath())
		return err == nil
	})

	// Simulate the user revoking the app password in their security settings:
	// every subsequent request is rejected.
	fake.revoked.Store(true)

	// Recovery must be self-service: back to the QR screen, no shell needed.
	waitFor(t, "the frame to drop back to pairing", func() bool {
		if _, err := os.Stat(cfg.AuthPath()); !os.IsNotExist(err) {
			return false
		}
		kind := f.Next(400, 200, "").Kind
		return kind == ResultPairing || kind == ResultEmpty
	})
}

func TestUndecodableFileIsSkippedNotRetriedForever(t *testing.T) {
	// A standalone server rather than a mutated fakeNextcloud: this one has to
	// serve a non-image where the photo should be.
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.php/login/v2":
			io.WriteString(w, `{"poll":{"token":"t","endpoint":"`+server.URL+
				`/index.php/login/v2/poll"},"login":"`+server.URL+`/flow"}`)

		case r.URL.Path == "/index.php/login/v2/poll":
			io.WriteString(w, `{"server":"`+server.URL+
				`","loginName":"alice","appPassword":"s"}`)

		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response><d:href>/remote.php/dav/systemtags/17</d:href><d:propstat><d:prop>
    <oc:id>17</oc:id><oc:display-name>Frame</oc:display-name>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)

		case r.Method == "REPORT":
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response><d:href>/remote.php/dav/files/alice/broken.jpg</d:href>
  <d:propstat><d:prop><d:getetag>&quot;bad&quot;</d:getetag>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)

		default:
			io.WriteString(w, "definitely not a photo")
		}
	}))
	t.Cleanup(server.Close)

	cfg := testConfig(t, server.URL)
	st, _ := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	f := New(cfg, st)

	startFrame(t, f)

	// Ask a few times over several sync intervals. One broken file must not
	// wedge the frame or hammer the server on every cycle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.Next(400, 200, "").Kind == ResultImage {
			t.Fatal("a non-image file was served as a photo")
		}
		time.Sleep(50 * time.Millisecond)
	}

	entries, _ := filepath.Glob(filepath.Join(cfg.RenderDir(), "*.rgb565"))
	if len(entries) != 0 {
		t.Errorf("undecodable file produced %d blob(s)", len(entries))
	}
}

func TestCredentialForADifferentServerIsDiscarded(t *testing.T) {
	fake := newFakeNextcloud(t)
	cfg := testConfig(t, fake.URL)

	// A credential left behind by a previous server, exactly what happens when
	// someone edits `server` in config.toml or switches a dev frame between a
	// stub and their real Nextcloud.
	if err := auth.Save(cfg.AuthPath(), auth.Credentials{
		Server:      "http://127.0.0.1:1",
		LoginName:   "alice",
		AppPassword: "stale",
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	st, _ := store.Open(cfg.ManifestPath(), cfg.CacheBudgetBytes)
	f := New(cfg, st)

	// It must not be adopted: doing so would leave the frame calling the old
	// host forever, silently ignoring the configured one.
	if f.paired() {
		t.Fatal("adopted a credential issued for a different server")
	}
	if _, err := os.Stat(cfg.AuthPath()); !os.IsNotExist(err) {
		t.Error("stale credential file was left on disk")
	}

	// And it must recover on its own by pairing against the configured server.
	startFrame(t, f)

	fake.approved.Store(true)
	waitFor(t, "re-pairing against the configured server", func() bool {
		return f.Next(400, 200, "").Kind == ResultImage
	})
}

func TestSameServerIgnoresTrailingSlashAndCase(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://cloud.example.com", "https://cloud.example.com/", true},
		{"https://Cloud.Example.com", "https://cloud.example.com", true},
		{"https://cloud.example.com", "http://cloud.example.com", false},
		{"https://a.example.com", "https://b.example.com", false},
	} {
		if got := sameServer(tc.a, tc.b); got != tc.want {
			t.Errorf("sameServer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
