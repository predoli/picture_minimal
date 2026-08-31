// Command fake-nextcloud serves just enough of Nextcloud to run the picture
// frame end to end with no server and no account.
//
// It auto-approves pairing, publishes a fixed tag vocabulary, answers the
// filter-files REPORT, and serves generated photos. Development only; it is
// excluded from release builds, which package only cmd/picture-backend.
//
//	go run ./cmd/fake-nextcloud -addr 127.0.0.1:8631
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"strings"
)

// gradient makes a photo whose colour varies along both axes, so a mis-packed
// RGB565 blob or a bad orientation transform is obvious on screen rather than
// subtle.
func gradient(w, h int, blue uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / w),
				G: uint8(y * 255 / h),
				B: blue,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		log.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// collection writes a directory response, with the resourcetype that tells the
// client it must descend rather than try to display it.
func collection(w io.Writer, href string) {
	fmt.Fprintf(w, `<d:response>
  <d:href>%s</d:href>
  <d:propstat><d:prop>
    <d:getetag>&quot;etag-dir&quot;</d:getetag>
    <d:resourcetype><d:collection/></d:resourcetype>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response>`, href)
}

func photo(w io.Writer, name string) {
	fmt.Fprintf(w, `<d:response>
  <d:href>/remote.php/dav/files/alice/%s</d:href>
  <d:propstat><d:prop>
    <d:getetag>&quot;etag-%s&quot;</d:getetag>
    <d:getcontenttype>image/jpeg</d:getcontenttype>
    <d:getlastmodified>Wed, 12 Mar 2025 10:04:00 GMT</d:getlastmodified>
    <d:resourcetype/>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response>`, name, name)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8631", "listen address")
	flag.Parse()

	base := "http://" + *addr

	// A landscape and a portrait photo, so letterboxing is visible both ways.
	// They live inside an album, and it is the album that carries the tag —
	// which is how people actually tag things, and the case that needs
	// exercising: Nextcloud tags the directory object, not its contents.
	photos := map[string][]byte{
		"Album/landscape.jpg": gradient(1600, 900, 40),
		"Album/portrait.jpg":  gradient(900, 1600, 200),
	}
	const albumHref = "/remote.php/dav/files/alice/Album/"

	mux := http.NewServeMux()

	mux.HandleFunc("/index.php/login/v2", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"poll":{"token":"t","endpoint":"%s/index.php/login/v2/poll"},"login":"%s/flow/abc"}`,
			base, base)
	})

	// Auto-approves, so the frame pairs itself without anyone scanning. To
	// rehearse the real QR flow instead, return 404 here until you are ready.
	mux.HandleFunc("/index.php/login/v2/poll", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"server":"%s","loginName":"alice","appPassword":"dev-password"}`, base)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%-8s %s", r.Method, r.URL.Path)

		switch {
		case r.Method == "PROPFIND" && strings.Contains(r.URL.Path, "/systemtags"):
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response><d:href>/remote.php/dav/systemtags/17</d:href><d:propstat><d:prop>
    <oc:id>17</oc:id><oc:display-name>Frame</oc:display-name>
    <oc:user-assignable>true</oc:user-assignable>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)

		// Descending into the tagged album. The self-entry is included, exactly
		// as a real server does, so the client's self-filtering is exercised.
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
			collection(w, albumHref)
			for name := range photos {
				photo(w, name)
			}
			fmt.Fprint(w, `</d:multistatus>`)

		// The tag REPORT returns the directory alone: a system tag applies to
		// the folder object and is not inherited by what is inside it.
		case r.Method == "REPORT":
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
			collection(w, albumHref)
			fmt.Fprint(w, `</d:multistatus>`)

		default:
			for name, data := range photos {
				if strings.HasSuffix(r.URL.Path, name) {
					w.Header().Set("Content-Type", "image/jpeg")
					w.Write(data)
					return
				}
			}
			http.NotFound(w, r)
		}
	})

	log.Printf("fake Nextcloud on %s (%d photos in a tagged album, tag \"Frame\", pairing auto-approves)",
		base, len(photos))
	log.Fatal(http.ListenAndServe(*addr, mux))
}
