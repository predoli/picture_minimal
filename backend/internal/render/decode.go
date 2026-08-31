package render

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"strconv"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"

	// Format registrations. Each adds itself to image.Decode's sniffer, so
	// dispatch is by magic bytes rather than by file extension.
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/heic"
	_ "golang.org/x/image/webp"
)

// heicAppliesOrientation records whether the HEIC decoder has already applied
// the container's irot/imir transform boxes by the time it returns an image.
//
// This is the single most likely source of upside-down photos. HEIF stores
// rotation in container properties *and* may carry an EXIF orientation tag; if
// the decoder honours the former and we then apply the latter, the image is
// rotated twice. The default assumes the decoder resolves orientation itself.
//
// Override with FRAME_HEIC_APPLY_EXIF=1 to apply EXIF on top, without a rebuild,
// if fixtures from a real device show otherwise.
var heicAppliesOrientation = os.Getenv("FRAME_HEIC_APPLY_EXIF") != "1"

// Decoded is an image plus the EXIF orientation still left to apply.
type Decoded struct {
	Image image.Image
	// Orientation is an EXIF orientation value (1-8) that the caller must still
	// apply. It is 1 when the decoder already did the work.
	Orientation int
	Format      string
}

// Decode reads an image, refusing anything larger than maxPixels.
//
// The size check runs against the header before the full decode, which is the
// only place it can usefully happen: by the time a 12 MP frame is decoded, the
// ~36 MB allocation has already landed on a 512 MB board.
func Decode(data []byte, maxPixels int64) (*Decoded, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read image header: %w", err)
	}

	if pixels := int64(cfg.Width) * int64(cfg.Height); maxPixels > 0 && pixels > maxPixels {
		return nil, fmt.Errorf("%s image is %dx%d (%d pixels), over the %d pixel cap",
			format, cfg.Width, cfg.Height, pixels, maxPixels)
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", format, err)
	}

	orientation := 1
	if format != "heic" || !heicAppliesOrientation {
		orientation = exifOrientation(data)
	}

	return &Decoded{Image: img, Orientation: orientation, Format: format}, nil
}

// exifOrientation returns the EXIF orientation, defaulting to 1 when absent or
// unreadable. A missing tag is entirely normal, so it is not an error.
func exifOrientation(data []byte) int {
	meta, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return 1
	}
	tag, err := meta.Get(exif.Orientation)
	if err != nil {
		return 1
	}

	var value int
	switch tag.Format() {
	case tiff.IntVal:
		v, err := tag.Int(0)
		if err != nil {
			return 1
		}
		value = v
	case tiff.StringVal:
		s, err := tag.StringVal()
		if err != nil {
			return 1
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 1
		}
		value = v
	default:
		return 1
	}

	if value < 1 || value > 8 {
		return 1
	}
	return value
}
