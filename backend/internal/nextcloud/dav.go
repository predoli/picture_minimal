// Package nextcloud talks to a Nextcloud server over WebDAV.
//
// The WebDAV layer is hand-rolled rather than delegated to a library because the
// interesting request here is REPORT/filter-files (tag and favourite selection),
// which the usual Go WebDAV clients do not implement. Sharing one multistatus
// parser across PROPFIND and REPORT is simpler than bolting REPORT onto a
// library that only covers the rest.
package nextcloud

import (
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized means the app password was rejected. From the frame's point of
// view this is what a user revoking the credential in their security settings
// looks like, so it must be distinguishable from an ordinary network failure.
var ErrUnauthorized = errors.New("nextcloud: credentials rejected")

// File is one remote entry, as returned by a listing. Collections are kept
// rather than dropped at parse time: a user tagging a whole album tags the
// directory object, and the caller has to descend into it.
type File struct {
	Href        string
	ETag        string
	Size        int64
	Modified    time.Time
	ContentType string
	FileID      string
	IsDir       bool
}

// Name returns the final path segment, for logging and display.
func (f File) Name() string {
	trimmed := strings.TrimSuffix(f.Href, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// --- multistatus (RFC 4918) ---

type multistatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []davResponse `xml:"DAV: response"`
}

type davResponse struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string  `xml:"DAV: status"`
	Prop   davProp `xml:"DAV: prop"`
}

type davProp struct {
	ETag         string       `xml:"DAV: getetag"`
	Size         string       `xml:"DAV: getcontentlength"`
	Modified     string       `xml:"DAV: getlastmodified"`
	ContentType  string       `xml:"DAV: getcontenttype"`
	ResourceType resourceType `xml:"DAV: resourcetype"`

	FileID string `xml:"http://owncloud.org/ns fileid"`

	// System tag properties, used when listing /remote.php/dav/systemtags/.
	TagID          string `xml:"http://owncloud.org/ns id"`
	DisplayName    string `xml:"http://owncloud.org/ns display-name"`
	UserVisible    string `xml:"http://owncloud.org/ns user-visible"`
	UserAssignable string `xml:"http://owncloud.org/ns user-assignable"`
}

type resourceType struct {
	Collection *struct{} `xml:"DAV: collection"`
}

// ok returns the first propstat carrying a 2xx status, since a response may
// also list properties the server could not supply.
func (r davResponse) ok() (davProp, bool) {
	for _, ps := range r.Propstats {
		if strings.Contains(ps.Status, " 200 ") {
			return ps.Prop, true
		}
	}
	return davProp{}, false
}

func (r davResponse) isCollection() bool {
	for _, ps := range r.Propstats {
		if ps.Prop.ResourceType.Collection != nil {
			return true
		}
	}
	// Belt and braces: Nextcloud always ends a collection's href in a slash, so
	// a server (or a prop set) that omits resourcetype is still recognised.
	return strings.HasSuffix(r.Href, "/")
}

// imageExtensions is the fallback when a server reports no content type. The
// decoder dispatches on magic bytes, so this only has to be good enough to
// avoid downloading videos and documents.
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".heic": true, ".heif": true, ".bmp": true, ".tif": true, ".tiff": true,
}

// IsImage reports whether this entry is worth downloading. A selection may well
// contain videos or documents — a tagged album usually does — and those must be
// passed over quietly rather than downloaded and failed on.
func (f File) IsImage() bool {
	if f.IsDir {
		return false
	}
	if mediaType, _, err := mime.ParseMediaType(f.ContentType); err == nil && mediaType != "" {
		return strings.HasPrefix(mediaType, "image/")
	}
	return imageExtensions[strings.ToLower(path.Ext(f.Name()))]
}

func parseMultistatus(body []byte) (*multistatus, error) {
	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("parse multistatus: %w", err)
	}
	return &ms, nil
}

// toEntries converts a multistatus into entries, dropping only the self-entry
// that PROPFIND includes for the requested path itself. Collections are kept and
// flagged; it is the caller that decides whether to descend or skip.
func (ms *multistatus) toEntries(selfHref string) []File {
	files := make([]File, 0, len(ms.Responses))
	for _, resp := range ms.Responses {
		if selfHref != "" && strings.TrimSuffix(resp.Href, "/") == strings.TrimSuffix(selfHref, "/") {
			continue
		}
		prop, ok := resp.ok()
		if !ok {
			continue
		}

		file := File{
			Href:        resp.Href,
			ETag:        strings.Trim(prop.ETag, `"`),
			ContentType: prop.ContentType,
			FileID:      prop.FileID,
			IsDir:       resp.isCollection(),
		}
		if prop.Size != "" {
			file.Size, _ = strconv.ParseInt(prop.Size, 10, 64)
		}
		if prop.Modified != "" {
			if t, err := http.ParseTime(prop.Modified); err == nil {
				file.Modified = t
			}
		}
		files = append(files, file)
	}
	return files
}

// toFiles is toEntries without the collections, for listings that never descend.
func (ms *multistatus) toFiles(selfHref string) []File {
	entries := ms.toEntries(selfHref)
	files := entries[:0]
	for _, e := range entries {
		if !e.IsDir {
			files = append(files, e)
		}
	}
	return files
}
