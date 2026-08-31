package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func pixelAt(t *testing.T, b *Blob, x, y int) uint16 {
	t.Helper()
	offset := y*b.Stride + x*BytesPerPixel
	return binary.LittleEndian.Uint16(b.Pixels[offset : offset+2])
}

func TestPackProducesLittleEndianRGB565(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 3, 1))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	canvas.Set(1, 0, color.RGBA{B: 255, A: 255})
	canvas.Set(2, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	blob := Pack(canvas)

	if got, want := blob.Stride, 3*BytesPerPixel; got != want {
		t.Fatalf("stride = %d, want %d", got, want)
	}
	// The frontend maps these bytes straight into an lv_img_dsc_t, so the exact
	// bit layout is load-bearing: red occupies the top 5 bits.
	for _, tc := range []struct {
		x    int
		want uint16
		name string
	}{
		{0, 0xF800, "red"},
		{1, 0x001F, "blue"},
		{2, 0xFFFF, "white"},
	} {
		if got := pixelAt(t, blob, tc.x, 0); got != tc.want {
			t.Errorf("%s = %#04x, want %#04x", tc.name, got, tc.want)
		}
	}

	// Byte order must be little-endian, not just the right 16-bit value.
	if blob.Pixels[0] != 0x00 || blob.Pixels[1] != 0xF8 {
		t.Errorf("red bytes = %#02x %#02x, want 0x00 0xf8", blob.Pixels[0], blob.Pixels[1])
	}
}

func TestOrientationTransformSwapsDimensionsWhenTransposing(t *testing.T) {
	const w, h = 200, 100
	for orientation := 1; orientation <= 8; orientation++ {
		_, gotW, gotH := orientationTransform(orientation, w, h)

		wantW, wantH := w, h
		if orientation >= 5 {
			wantW, wantH = h, w
		}
		if gotW != wantW || gotH != wantH {
			t.Errorf("orientation %d: got %dx%d, want %dx%d", orientation, gotW, gotH, wantW, wantH)
		}
	}
}

// halfLit builds a w x h image whose left half is white and right half black,
// which makes it obvious where each edge ended up after a transform.
func halfLit(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}
	return img
}

func TestFitRotatesLeftEdgeToTopForOrientation6(t *testing.T) {
	// Orientation 6 is "rotate 90 clockwise", under which the left edge of the
	// original becomes the top edge.
	src := halfLit(200, 100)

	blob, err := Fit(src, 6, 100, 200)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	if top := pixelAt(t, blob, 50, 40); top != 0xFFFF {
		t.Errorf("top half = %#04x, want white; left edge did not rotate to the top", top)
	}
	if bottom := pixelAt(t, blob, 50, 160); bottom != 0x0000 {
		t.Errorf("bottom half = %#04x, want black", bottom)
	}
}

func TestFitLetterboxesRatherThanCrops(t *testing.T) {
	// A 100x50 source into a 200x200 panel scales by 2 to 200x100, leaving 50
	// rows of black above and below.
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			src.Set(x, y, color.White)
		}
	}

	blob, err := Fit(src, 1, 200, 200)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if blob.Width != 200 || blob.Height != 200 {
		t.Fatalf("got %dx%d, want 200x200", blob.Width, blob.Height)
	}

	if got := pixelAt(t, blob, 100, 10); got != 0x0000 {
		t.Errorf("top bar = %#04x, want black", got)
	}
	if got := pixelAt(t, blob, 100, 100); got != 0xFFFF {
		t.Errorf("centre = %#04x, want white (content should fill the middle band)", got)
	}
	if got := pixelAt(t, blob, 100, 190); got != 0x0000 {
		t.Errorf("bottom bar = %#04x, want black", got)
	}
}

func TestDecodeRejectsOversizedImages(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatalf("encode: %v", err)
	}

	if _, err := Decode(buf.Bytes(), 5000); err == nil {
		t.Fatal("expected 10000-pixel image to be rejected by a 5000-pixel cap")
	}
	if _, err := Decode(buf.Bytes(), 20000); err != nil {
		t.Fatalf("expected image within cap to decode: %v", err)
	}
}

// TestHEICOrientation pins down whether the HEIC decoder resolves the
// container's rotation itself, which decides whether we may also apply EXIF.
// Getting this wrong rotates photos twice.
//
// Fixtures are generated with macOS's sips, so this only runs where that exists.
func TestHEICOrientation(t *testing.T) {
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skip("sips not available; HEIC orientation must be verified on macOS or with device fixtures")
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.png")

	// Deliberately non-square, so a stray transpose is visible in the geometry.
	var buf bytes.Buffer
	if err := png.Encode(&buf, halfLit(200, 100)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(srcPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	heicPath := filepath.Join(dir, "src.heic")
	if out, err := exec.Command("sips", "-s", "format", "heic", srcPath, "--out", heicPath).CombinedOutput(); err != nil {
		t.Skipf("sips could not produce HEIC (%v): %s", err, out)
	}

	data, err := os.ReadFile(heicPath)
	if err != nil {
		t.Fatalf("read heic: %v", err)
	}

	decoded, err := Decode(data, 0)
	if err != nil {
		t.Fatalf("decode heic: %v", err)
	}
	if decoded.Format != "heic" {
		t.Fatalf("format = %q, want heic", decoded.Format)
	}

	bounds := decoded.Image.Bounds()
	if bounds.Dx() != 200 || bounds.Dy() != 100 {
		t.Errorf("decoded %dx%d, want 200x100: the decoder transposed the image unexpectedly",
			bounds.Dx(), bounds.Dy())
	}
	if decoded.Orientation != 1 {
		t.Errorf("orientation = %d, want 1: applying it again would double-rotate",
			decoded.Orientation)
	}
}
