package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode-only, for phone screenshots
)

const (
	thumbMax     = 512              // longest edge of a generated thumbnail
	maxPixels    = 80 * 1000 * 1000 // reject decoding absurdly large images
	maxPhotoSize = 25 << 20         // 25 MiB per uploaded photo
)

// safeName guards the photo file server: we only ever generate names matching
// this, so anything else is a traversal attempt or a stale link.
var safeName = regexp.MustCompile(`^[a-f0-9]{16}\.(jpg|png|gif|webp)$`)

type PhotoStore struct {
	dir string // <data>/photos
}

func NewPhotoStore(dir string) (*PhotoStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "thumb"), 0o755); err != nil {
		return nil, err
	}
	return &PhotoStore{dir: dir}, nil
}

func (p *PhotoStore) Path(name string) string      { return filepath.Join(p.dir, name) }
func (p *PhotoStore) ThumbPath(name string) string { return filepath.Join(p.dir, "thumb", name+".jpg") }

// Save streams an upload to disk, then writes a downscaled, orientation-
// corrected thumbnail next to it. It returns the generated filename.
func (p *PhotoStore) Save(r io.Reader, origName string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPhotoSize+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxPhotoSize {
		return "", fmt.Errorf("photo is larger than %d MiB", maxPhotoSize>>20)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("empty upload")
	}

	// The container is identified from its magic bytes rather than from
	// whether Go can decode the pixels. Plenty of real camera and shop images
	// are valid JPEGs that image/jpeg rejects -- "unsupported JPEG feature:
	// luma/chroma subsampling ratio" is the common one -- and every browser
	// renders them fine. Refusing those would lose the photo for no reason.
	ext := sniffFormat(raw)
	if ext == "" {
		return "", fmt.Errorf("%s is not a supported image (jpeg, png, gif or webp)", origName)
	}
	// The pixel guard only applies when the dimensions are actually readable.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		if cfg.Width*cfg.Height > maxPixels {
			return "", fmt.Errorf("%s is too large to process (%dx%d)", origName, cfg.Width, cfg.Height)
		}
	}

	name, err := randomName(ext)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(p.Path(name), raw, 0o644); err != nil {
		return "", err
	}
	if err := p.writeThumb(raw, name); err != nil {
		// A missing thumbnail should not lose the photo; the grid falls back
		// to the original.
		os.Remove(p.ThumbPath(name))
	}
	return name, nil
}

func (p *PhotoStore) writeThumb(raw []byte, name string) error {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := 1.0
	if w > thumbMax || h > thumbMax {
		if w >= h {
			scale = float64(thumbMax) / float64(w)
		} else {
			scale = float64(thumbMax) / float64(h)
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)

	// Browsers auto-rotate the original from its EXIF tag, so the thumbnail
	// has to be rotated to match or the grid disagrees with the detail view.
	out := applyOrientation(dst, exifOrientation(raw))

	f, err := os.Create(p.ThumbPath(name))
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, out, &jpeg.Options{Quality: 82})
}

// RebuildThumbs regenerates any thumbnail that is missing. A backup only
// carries the originals -- thumbnails are derived data and would double the
// archive for nothing -- so a restore calls this to fill them back in.
// Originals that Go cannot decode are skipped silently: the grid already falls
// back to serving those full size.
func (p *PhotoStore) RebuildThumbs() int {
	names, err := p.Names()
	if err != nil {
		return 0
	}
	rebuilt := 0
	for _, name := range names {
		if _, err := os.Stat(p.ThumbPath(name)); err == nil {
			continue
		}
		raw, err := os.ReadFile(p.Path(name))
		if err != nil {
			continue
		}
		if err := p.writeThumb(raw, name); err != nil {
			os.Remove(p.ThumbPath(name))
			continue
		}
		rebuilt++
	}
	return rebuilt
}

func (p *PhotoStore) Remove(name string) {
	if !safeName.MatchString(name) {
		return
	}
	os.Remove(p.Path(name))
	os.Remove(p.ThumbPath(name))
}

// sniffFormat identifies the image container from its leading bytes, returning
// the file extension to store it under, or "" if it is not an image we serve.
func sniffFormat(raw []byte) string {
	switch {
	case len(raw) >= 3 && bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return "jpg"
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return "gif"
	case len(raw) >= 12 && bytes.HasPrefix(raw, []byte("RIFF")) && bytes.Equal(raw[8:12], []byte("WEBP")):
		return "webp"
	}
	return ""
}

func randomName(ext string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + "." + strings.ToLower(ext), nil
}

// applyOrientation rotates/flips img according to an EXIF orientation value
// (1..8). Value 1 and anything unrecognised are returned untouched.
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	swap := o >= 5 // orientations 5-8 transpose the axes
	outW, outH := w, h
	if swap {
		outW, outH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2: // mirror horizontal
				nx, ny = w-1-x, y
			case 3: // rotate 180
				nx, ny = w-1-x, h-1-y
			case 4: // mirror vertical
				nx, ny = x, h-1-y
			case 5: // transpose
				nx, ny = y, x
			case 6: // rotate 90 CW
				nx, ny = h-1-y, x
			case 7: // transverse
				nx, ny = h-1-y, w-1-x
			case 8: // rotate 270 CW
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// exifOrientation digs the orientation tag (0x0112) out of a JPEG's APP1
// segment. It returns 1 (no rotation) for every other format and for images
// with no usable EXIF, which keeps this dependency-free.
func exifOrientation(raw []byte) int {
	const noRotation = 1
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return noRotation
	}
	i := 2
	for i+4 <= len(raw) {
		if raw[i] != 0xFF {
			return noRotation
		}
		marker := raw[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end of image
			return noRotation
		}
		size := int(binary.BigEndian.Uint16(raw[i+2:]))
		if size < 2 || i+2+size > len(raw) {
			return noRotation
		}
		if marker == 0xE1 { // APP1
			seg := raw[i+4 : i+2+size]
			if o, ok := orientationFromTIFF(seg); ok {
				return o
			}
		}
		i += 2 + size
	}
	return noRotation
}

func orientationFromTIFF(seg []byte) (int, bool) {
	if len(seg) < 14 || !bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
		return 0, false
	}
	tiff := seg[6:]

	var bo binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("II")):
		bo = binary.LittleEndian
	case bytes.HasPrefix(tiff, []byte("MM")):
		bo = binary.BigEndian
	default:
		return 0, false
	}
	if len(tiff) < 8 {
		return 0, false
	}
	offset := int(bo.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > len(tiff) {
		return 0, false
	}
	count := int(bo.Uint16(tiff[offset:]))
	entries := tiff[offset+2:]
	for e := 0; e < count; e++ {
		start := e * 12
		if start+12 > len(entries) {
			return 0, false
		}
		if bo.Uint16(entries[start:]) == 0x0112 {
			// SHORT value, stored inline in the first 2 bytes of the value field
			v := int(bo.Uint16(entries[start+8:]))
			if v >= 1 && v <= 8 {
				return v, true
			}
			return 0, false
		}
	}
	return 0, false
}

// Keep the stdlib decoders registered even though only image.Decode uses them.
var _ = []any{gif.Decode, png.Decode, jpeg.Decode}
