package nextcloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A realistic filter-files response, namespaces and all. The XML namespace
// handling is the easiest thing here to get subtly wrong, and a mistake would
// look like "the album is empty" rather than like a parse error.
const tagReportBody = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/alice/Photos/IMG_4021.jpg</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;abc123&quot;</d:getetag>
        <d:getlastmodified>Wed, 12 Mar 2025 10:04:00 GMT</d:getlastmodified>
        <d:getcontentlength>2048576</d:getcontentlength>
        <d:getcontenttype>image/jpeg</d:getcontenttype>
        <oc:fileid>12345</oc:fileid>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/alice/Holiday%20Photos/caf%C3%A9.heic</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;def456&quot;</d:getetag>
        <d:getcontentlength>8388608</d:getcontentlength>
        <d:getcontenttype>image/heic</d:getcontenttype>
        <oc:fileid>12346</oc:fileid>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
    <d:propstat>
      <d:prop><d:getlastmodified/></d:prop>
      <d:status>HTTP/1.1 404 Not Found</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/alice/Photos/</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

// What a PROPFIND on the tagged album returns: the collection itself, a photo,
// and a video. Tagging a folder is the common case — Nextcloud applies the tag
// to the directory object, not to its contents — so the client has to descend,
// and albums routinely hold things that are not photos.
const albumBody = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/alice/Photos/</d:href>
    <d:propstat>
      <d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/alice/Photos/IMG_9000.jpg</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;ghi789&quot;</d:getetag>
        <d:getcontenttype>image/jpeg</d:getcontenttype>
        <d:resourcetype/>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/alice/Photos/clip.mp4</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;vid&quot;</d:getetag>
        <d:getcontenttype>video/mp4</d:getcontenttype>
        <d:resourcetype/>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

const tagsBody = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/systemtags/17</d:href>
    <d:propstat>
      <d:prop>
        <oc:id>17</oc:id>
        <oc:display-name>Frame-Livingroom</oc:display-name>
        <oc:user-visible>true</oc:user-visible>
        <oc:user-assignable>true</oc:user-assignable>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/systemtags/4</d:href>
    <d:propstat>
      <d:prop>
        <oc:id>4</oc:id>
        <oc:display-name>Invoices</oc:display-name>
        <oc:user-assignable>false</oc:user-assignable>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

func TestListByTagParsesMultistatus(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)

		switch r.Method {
		case "REPORT":
			if want := "/remote.php/dav/files/alice/"; r.URL.Path != want {
				t.Errorf("path = %s, want %s", r.URL.Path, want)
			}
			gotBody = string(body)
			io.WriteString(w, tagReportBody)
		case "PROPFIND":
			if want := "/remote.php/dav/files/alice/Photos/"; r.URL.Path != want {
				t.Errorf("descended into %s, want %s", r.URL.Path, want)
			}
			io.WriteString(w, albumBody)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	files, err := New(server.URL, "alice", "secret").ListByTag(context.Background(), 17)
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}

	if !strings.Contains(gotBody, "<oc:systemtag>17</oc:systemtag>") {
		t.Errorf("request did not carry the tag filter:\n%s", gotBody)
	}

	// Two loose files, plus the one photo inside the tagged album. The album
	// itself must never appear as a photo, and neither must the video in it.
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3 (two loose, one from inside the tagged album): %+v",
			len(files), files)
	}
	if files[2].Name() != "IMG_9000.jpg" {
		t.Errorf("third file = %q, want IMG_9000.jpg from inside the tagged album", files[2].Name())
	}
	for _, f := range files {
		if f.IsDir || !f.IsImage() {
			t.Errorf("%s should not have been listed", f.Href)
		}
	}

	if files[0].ETag != "abc123" {
		t.Errorf("etag = %q, want abc123 (quotes should be stripped)", files[0].ETag)
	}
	if files[0].Size != 2048576 {
		t.Errorf("size = %d, want 2048576", files[0].Size)
	}
	if files[0].Name() != "IMG_4021.jpg" {
		t.Errorf("name = %q", files[0].Name())
	}
	if files[0].Modified.IsZero() {
		t.Error("modified time was not parsed")
	}

	// A response may carry a 404 propstat alongside its 200 one; the file is
	// still perfectly usable.
	if files[1].ETag != "def456" {
		t.Errorf("second etag = %q, want def456", files[1].ETag)
	}
	if got := files[1].Name(); got != "caf%C3%A9.heic" {
		t.Errorf("name = %q; hrefs stay percent-encoded so they can be re-used verbatim", got)
	}
}

func TestResolveTagMatchesByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, tagsBody)
	}))
	defer server.Close()

	client := New(server.URL, "alice", "secret")

	id, found, err := client.ResolveTag(context.Background(), "frame-livingroom")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	if !found || id != 17 {
		t.Errorf("got id=%d found=%v, want 17/true (match should be case-insensitive)", id, found)
	}

	if _, found, err := client.ResolveTag(context.Background(), "Nonexistent"); err != nil || found {
		t.Errorf("got found=%v err=%v, want false/nil for an unknown tag", found, err)
	}
}

func TestUnauthorizedIsDistinguishable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	// The frame drops back to the QR screen on this specific error, so it must
	// not be flattened into a generic network failure.
	_, err := New(server.URL, "alice", "revoked").ListFavorites(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestLoginFlowPollPendingThenSuccess(t *testing.T) {
	var polls int
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/index.php/login/v2", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("start method = %s, want POST", r.Method)
		}
		io.WriteString(w, `{"poll":{"token":"tok","endpoint":"`+server.URL+
			`/index.php/login/v2/poll"},"login":"`+server.URL+`/login/v2/flow/abc"}`)
	})
	mux.HandleFunc("/index.php/login/v2/poll", func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"server":"`+server.URL+
			`","loginName":"alice","appPassword":"app-secret"}`)
	})

	flow, err := StartFlow(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	if !strings.Contains(flow.LoginURL, "/login/v2/flow/") {
		t.Errorf("login URL = %q; this is what gets rendered as the QR code", flow.LoginURL)
	}

	if _, err := flow.Poll(context.Background()); !errors.Is(err, ErrFlowPending) {
		t.Fatalf("first poll err = %v, want ErrFlowPending", err)
	}

	session, err := flow.Poll(context.Background())
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if session.LoginName != "alice" || session.AppPassword != "app-secret" {
		t.Errorf("session = %+v", session)
	}
}

func TestRedirectedLoginFlowExplainsItself(t *testing.T) {
	// A server that redirects http:// to https:// is the common real-world case.
	// Go turns the redirected POST into a GET, Nextcloud answers 405, and the
	// bare status gives no clue what to change.
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"poll":{"token":"t","endpoint":"`+target.URL+
				`/poll"},"login":"`+target.URL+`/flow"}`)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	_, err := StartFlow(context.Background(), redirector.URL)
	if err == nil {
		t.Fatal("expected the redirected flow to fail")
	}
	if !strings.Contains(err.Error(), "redirected") || !strings.Contains(err.Error(), target.URL) {
		t.Errorf("error does not name the redirect target, so it is not actionable: %v", err)
	}
}

// A 403 is not a rejected credential. Nextcloud uses it for ordinary refusals,
// and treating it as revocation would wipe a working app password and drop the
// frame back to the QR screen.
func TestForbiddenIsNotTreatedAsRevocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := New(server.URL, "alice", "secret").ListFavorites(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("403 was reported as revocation: %v", err)
	}
}

// An unreadable subfolder must not take the whole album down with it.
func TestExpandSkipsUnreadableSubfolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/Photos/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, tagReportBody)
	}))
	defer server.Close()

	files, err := New(server.URL, "alice", "secret").ListByTag(context.Background(), 17)
	if err != nil {
		t.Fatalf("ListByTag: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("got %d files, want the 2 readable ones: %+v", len(files), files)
	}
}
