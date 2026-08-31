package nextcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivermaier/picture-frame/backend/internal/logging"
)

type Client struct {
	base     string // scheme://host, no trailing slash
	user     string
	password string
	http     *http.Client
}

func New(server, user, password string) *Client {
	return &Client{
		base:     strings.TrimSuffix(server, "/"),
		user:     user,
		password: password,
		http: &http.Client{
			Timeout: 2 * time.Minute, // generous: photos can be large and links slow
		},
	}
}

// filesRoot is the DAV path for this user's own files.
func (c *Client) filesRoot() string {
	return "/remote.php/dav/files/" + url.PathEscape(c.user) + "/"
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.password)
	req.Header.Set("User-Agent", "picture-frame")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		logging.Debugf("%s %s failed after %s: %v",
			method, path, time.Since(started).Round(time.Microsecond), err)
		return nil, err
	}
	logging.Debugf("%s %s -> %s in %s",
		method, path, resp.Status, time.Since(started).Round(time.Microsecond))
	// Only 401 means the app password was rejected. 403 must not be treated the
	// same way: Nextcloud uses it for ordinary refusals — a forbidden path, a
	// rate limit — and unpairing on one would wipe a working credential and send
	// the frame back to the QR screen for no reason.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	return resp, nil
}

// davRequest performs a request expected to return 207 Multi-Status.
func (c *Client) davRequest(ctx context.Context, method, path, body string, headers map[string]string) (*multistatus, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/xml; charset=utf-8"

	resp, err := c.do(ctx, method, path, []byte(body), headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("%s %s: unexpected status %s", method, path, resp.Status)
	}
	return parseMultistatus(payload)
}

// fileProps must include resourcetype: without it every response parses as a
// plain file, and a tagged directory is admitted as though it were a photo.
const fileProps = `<d:getetag/><d:getlastmodified/><d:getcontentlength/><d:getcontenttype/><d:resourcetype/><oc:fileid/>`

// Bounds on descending into a selection. A tagged album can nest arbitrarily,
// and the frame has 512 MB of RAM and one SD card.
const (
	maxWalkDepth = 8
	maxWalkFiles = 5000
)

// propfind lists one collection, one level deep, by its DAV href.
func (c *Client) propfind(ctx context.Context, href string) ([]File, error) {
	body := `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>` + fileProps + `</d:prop>
</d:propfind>`

	ms, err := c.davRequest(ctx, "PROPFIND", href, body, map[string]string{"Depth": "1"})
	if err != nil {
		return nil, err
	}
	return ms.toEntries(href), nil
}

// expand turns a mixed listing into image files only, descending into every
// collection it contains.
//
// This is what makes tagging a whole album work. Nextcloud applies a system tag
// to the directory object alone — it is not inherited by the contents — so the
// REPORT for a tagged album returns exactly one entry: the directory. Without
// descending, the frame would try to display the folder itself.
//
// Nextcloud refuses Depth: infinity, so this recurses a level at a time.
func (c *Client) expand(ctx context.Context, entries []File, depth int) ([]File, error) {
	var files []File
	for _, entry := range entries {
		if !entry.IsDir {
			if entry.IsImage() {
				files = append(files, entry)
			}
			continue
		}
		if depth >= maxWalkDepth || len(files) >= maxWalkFiles {
			log.Printf("not descending into %s: reached the %d-level, %d-file limit",
				entry.Href, maxWalkDepth, maxWalkFiles)
			continue
		}

		logging.Debugf("descending into tagged folder %s (depth %d)", entry.Href, depth)
		children, err := c.propfind(ctx, entry.Href)
		if err != nil {
			// A credential problem is fatal to the whole sync; one unreadable
			// subfolder is not, so the rest of the album still reaches the frame.
			if errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
				return nil, err
			}
			log.Printf("skipping folder %s: %v", entry.Href, err)
			continue
		}
		nested, err := c.expand(ctx, children, depth+1)
		if err != nil {
			return nil, err
		}
		files = append(files, nested...)
	}
	if len(files) > maxWalkFiles {
		files = files[:maxWalkFiles]
	}
	return files, nil
}

// ListFolder walks a directory and everything below it. The fallback selection
// mode, and the shape people expect from a photos folder with albums in it.
func (c *Client) ListFolder(ctx context.Context, folder string) ([]File, error) {
	href := c.filesRoot() + strings.TrimPrefix(strings.TrimSuffix(folder, "/"), "/") + "/"

	entries, err := c.propfind(ctx, href)
	if err != nil {
		return nil, err
	}
	return c.expand(ctx, entries, 0)
}

// listFiltered issues a filter-files REPORT against the user's whole account.
//
// One request replaces walking a folder tree, which matters on a Pi. It carries
// no "changed since" notion, so the caller still diffs against its manifest.
func (c *Client) listFiltered(ctx context.Context, rules string) ([]File, error) {
	body := `<?xml version="1.0"?>
<oc:filter-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>` + fileProps + `</d:prop>
  <oc:filter-rules>` + rules + `</oc:filter-rules>
</oc:filter-files>`

	ms, err := c.davRequest(ctx, "REPORT", c.filesRoot(), body, nil)
	if err != nil {
		return nil, err
	}
	return c.expand(ctx, ms.toEntries(c.filesRoot()), 0)
}

// ListByTag returns every file carrying the given system tag.
func (c *Client) ListByTag(ctx context.Context, tagID int) ([]File, error) {
	return c.listFiltered(ctx, "<oc:systemtag>"+strconv.Itoa(tagID)+"</oc:systemtag>")
}

// ListFavorites returns every file the user has starred.
func (c *Client) ListFavorites(ctx context.Context) ([]File, error) {
	return c.listFiltered(ctx, "<oc:favorite>1</oc:favorite>")
}

// Tag is an entry in the server-wide collaborative tag vocabulary.
type Tag struct {
	ID             int
	Name           string
	UserAssignable bool
}

// Tags lists the tag vocabulary visible to this user.
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	const path = "/remote.php/dav/systemtags/"

	body := `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <oc:id/><oc:display-name/><oc:user-visible/><oc:user-assignable/>
  </d:prop>
</d:propfind>`

	ms, err := c.davRequest(ctx, "PROPFIND", path, body, map[string]string{"Depth": "1"})
	if err != nil {
		return nil, err
	}

	tags := make([]Tag, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		prop, ok := resp.ok()
		if !ok || prop.DisplayName == "" {
			continue
		}
		id, err := strconv.Atoi(prop.TagID)
		if err != nil {
			continue
		}
		tags = append(tags, Tag{
			ID:             id,
			Name:           prop.DisplayName,
			UserAssignable: prop.UserAssignable == "true",
		})
	}
	return tags, nil
}

// ResolveTag finds a tag by display name.
//
// The filter-files REPORT matches on numeric id, not name, and a tag deleted and
// recreated keeps its name but gets a new id — so this is re-run whenever a
// listing stops behaving, rather than resolved once and cached forever.
func (c *Client) ResolveTag(ctx context.Context, name string) (int, bool, error) {
	tags, err := c.Tags(ctx)
	if err != nil {
		return 0, false, err
	}
	for _, tag := range tags {
		if strings.EqualFold(tag.Name, name) {
			return tag.ID, true, nil
		}
	}
	return 0, false, nil
}

// CreateTag adds a user-assignable tag to the vocabulary.
//
// Servers may forbid this: an admin can restrict tag creation, in which case the
// tag has to be created once by an admin. The error is surfaced so the frame can
// say so on screen rather than looping silently.
func (c *Client) CreateTag(ctx context.Context, name string) (int, error) {
	payload, err := json.Marshal(map[string]any{
		"name":           name,
		"userVisible":    true,
		"userAssignable": true,
	})
	if err != nil {
		return 0, err
	}

	resp, err := c.do(ctx, "POST", "/remote.php/dav/systemtags/", payload,
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("create tag %q: %s (an administrator may have restricted tag creation)",
			name, resp.Status)
	}

	// The new tag's id is the last segment of Content-Location.
	if location := resp.Header.Get("Content-Location"); location != "" {
		parts := strings.Split(strings.TrimSuffix(location, "/"), "/")
		if id, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return id, nil
		}
	}

	// Fall back to looking it up; the tag exists either way.
	id, found, err := c.ResolveTag(ctx, name)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("created tag %q but could not resolve its id", name)
	}
	return id, nil
}

// Download fetches a file by the href returned in a listing.
//
// Hrefs arrive percent-encoded and already absolute-path, so they are used
// as-is rather than re-escaped, which would double-encode any space or accent.
func (c *Client) Download(ctx context.Context, href string, limit int64) ([]byte, error) {
	resp, err := c.do(ctx, "GET", href, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", href, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
