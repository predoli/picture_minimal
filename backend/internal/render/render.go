// Package render turns a decoded photo into the exact bytes the frontend maps.
//
// Everything expensive happens here rather than on the frontend: a 12 MP phone
// photo is ~36 MB decoded, which is most of a Pi Zero 2 W's memory. What crosses
// the socket is a blob already scaled to the panel, so the C++ side never
// allocates more than the finished image.
package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// BytesPerPixel is fixed by the frontend's LV_COLOR_DEPTH 16.
const BytesPerPixel = 2

// Blob is a packed RGB565 image at the panel's exact geometry.
type Blob struct {
	Pixels []byte
	Width  int
	Height int
	Stride int
}

// orientationTransform returns the source-to-oriented matrix for an EXIF
// orientation value, along with the dimensions after orienting.
//
// Values 5-8 transpose the image, so width and height swap.
func orientationTransform(orientation, w, h int) (f64.Aff3, int, int) {
	fw, fh := float64(w), float64(h)

	switch orientation {
	case 2: // mirror horizontal
		return f64.Aff3{-1, 0, fw, 0, 1, 0}, w, h
	case 3: // rotate 180
		return f64.Aff3{-1, 0, fw, 0, -1, fh}, w, h
	case 4: // mirror vertical
		return f64.Aff3{1, 0, 0, 0, -1, fh}, w, h
	case 5: // transpose
		return f64.Aff3{0, 1, 0, 1, 0, 0}, h, w
	case 6: // rotate 90 clockwise
		return f64.Aff3{0, -1, fh, 1, 0, 0}, h, w
	case 7: // transverse
		return f64.Aff3{0, -1, fh, -1, 0, fw}, h, w
	case 8: // rotate 270 clockwise
		return f64.Aff3{0, 1, 0, -1, 0, fw}, h, w
	default: // 1, and anything unrecognised
		return f64.Aff3{1, 0, 0, 0, 1, 0}, w, h
	}
}

// Fit orients, scales, and letterboxes src onto a dstW x dstH black field.
//
// Letterboxing rather than cropping is deliberate: the frame's background is
// black, so bars are invisible, whereas cropping would silently cut people out
// of the photo.
func Fit(src image.Image, orientation, dstW, dstH int) (*Blob, error) {
	if dstW <= 0 || dstH <= 0 {
		return nil, fmt.Errorf("invalid target geometry %dx%d", dstW, dstH)
	}
	bounds := src.Bounds()
	if bounds.Empty() {
		return nil, fmt.Errorf("source image is empty")
	}

	orient, ow, oh := orientationTransform(orientation, bounds.Dx(), bounds.Dy())

	// Scale to fit inside the panel, preserving aspect ratio.
	scale := min(float64(dstW)/float64(ow), float64(dstH)/float64(oh))
	offsetX := (float64(dstW) - float64(ow)*scale) / 2
	offsetY := (float64(dstH) - float64(oh)*scale) / 2

	// Compose orient, then scale, then centre, into a single affine transform so
	// the resampler runs exactly once.
	m := f64.Aff3{
		scale * orient[0], scale * orient[1], scale*orient[2] + offsetX,
		scale * orient[3], scale * orient[4], scale*orient[5] + offsetY,
	}

	canvas := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)

	// CatmullRom is the best-looking of the stock kernels for downscaling, and
	// this runs once per photo in the background, not per frame.
	xdraw.CatmullRom.Transform(canvas, m, src, bounds, xdraw.Over, nil)

	return Pack(canvas), nil
}

// Pack converts an opaque RGBA canvas into little-endian RGB565.
//
// The byte layout must match lv_color_t under LV_COLOR_DEPTH 16 with
// LV_COLOR_16_SWAP 0: red in the high 5 bits, then green's 6, then blue's 5,
// stored as a native little-endian uint16.
func Pack(canvas *image.RGBA) *Blob {
	width := canvas.Bounds().Dx()
	height := canvas.Bounds().Dy()
	stride := width * BytesPerPixel

	out := make([]byte, stride*height)
	for y := 0; y < height; y++ {
		srcRow := canvas.Pix[y*canvas.Stride:]
		dstRow := out[y*stride:]
		for x := 0; x < width; x++ {
			r := srcRow[x*4]
			g := srcRow[x*4+1]
			b := srcRow[x*4+2]
			value := uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(b>>3)
			dstRow[x*2] = byte(value)
			dstRow[x*2+1] = byte(value >> 8)
		}
	}

	return &Blob{Pixels: out, Width: width, Height: height, Stride: stride}
}
