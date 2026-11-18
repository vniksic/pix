// Package imgload decodes images of every supported format into
// *image.NRGBA (EXIF orientation applied) and caches them.
package imgload

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/heic"
	_ "github.com/gen2brain/jpegxl"
	"github.com/rwcarlsen/goexif/exif"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"pix/internal/camraw"
)

// maxPixels guards against headers that would make the decoder allocate
// absurd amounts of memory (a crash cannot be recovered from).
const maxPixels = 1 << 28 // 268 MP, ~1 GiB as NRGBA

// Decode opens and decodes path with every supported format registered
// (image/jpeg, image/png, image/gif, x/image/webp, x/image/tiff,
// x/image/bmp, gen2brain/heic, gen2brain/avif, gen2brain/jpegxl), then
// applies EXIF orientation (rwcarlsen/goexif) for JPEG/TIFF/WebP where present.
// Result is always *image.NRGBA (convert if needed) so the renderer has
// one fast path. Animated GIF: first frame only.
//
// HEIC, AVIF and JPEG XL orientation is applied by their decoders. The
// result's bounds always start at (0,0).
//
// A camera raw file (see camraw.Parse; recognised by content before any
// other format, so one with a TIFF header never reaches x/image/tiff) is
// shown by its largest embedded JPEG preview: see decodeRaw.
func Decode(path string) (*image.NRGBA, error) {
	return decodeFile(path, nil)
}

// decodeWithMeta is Decode that also extracts Meta from the bytes it has
// already read (no second read of the file). Meta is filled even when
// decoding fails.
func decodeWithMeta(path string) (*image.NRGBA, Meta, error) {
	var m Meta
	img, err := decodeFile(path, &m)
	return img, m, err
}

// decodeFile implements Decode, filling *meta when meta is non-nil.
// A camera raw file is read only where its structure and preview are;
// any other file is read whole.
func decodeFile(path string, meta *Meta) (img *image.NRGBA, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("decode %s: panic: %v", path, r)
		}
	}()
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
			if info, ok := camraw.Parse(f, fi.Size()); ok {
				if meta != nil {
					*meta = rawMeta(info)
				}
				return decodeRaw(f, fi.Size(), info)
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if meta != nil {
		*meta = metaFrom(bytes.NewReader(data), int64(len(data)))
	}
	m, format, err := decodeBytes(data)
	if err != nil {
		return nil, err
	}
	out := toNRGBA(m)
	switch format {
	case "jpeg", "tiff", "webp":
		out = orient(out, exifOrientation(format, data))
	}
	return out, nil
}

// decodeBytes picks the decoder. ISOBMFF files (HEIC/AVIF) are dispatched
// by brand because AVIF and HEIC share brands like mif1, which the HEIC
// package does not register, and AVIF needs AutoRotate.
func decodeBytes(data []byte) (image.Image, string, error) {
	r := bytes.NewReader(data)
	switch isobmffKind(data) {
	case "avif":
		if err := checkConfig(avif.DecodeConfig(r)); err != nil {
			return nil, "", err
		}
		m, err := avif.Decode(bytes.NewReader(data), avif.Options{AutoRotate: true})
		return m, "avif", err
	case "heic":
		if err := checkConfig(heic.DecodeConfig(r)); err != nil {
			return nil, "", err
		}
		m, err := heic.Decode(bytes.NewReader(data))
		return m, "heic", err
	}
	cfg, _, err := image.DecodeConfig(r)
	if err := checkConfig(cfg, err); err != nil {
		return nil, "", err
	}
	return image.Decode(bytes.NewReader(data))
}

// decodeRaw shows a camera raw file by its largest embedded JPEG that
// decodes (sensor data is never demosaiced), turned upright by the raw
// file's orientation. The JPEG's own Exif orientation, which some carry,
// is not applied as well: that would turn it twice. Without a usable JPEG
// an uncompressed RGB thumbnail in IFD0 (DNG) is the image.
func decodeRaw(r io.ReaderAt, size int64, info camraw.Info) (*image.NRGBA, error) {
	var best camraw.Region
	var cfg image.Config
	for _, j := range info.JPEGs {
		c, err := jpeg.DecodeConfig(io.NewSectionReader(r, j.Off, j.Len))
		if err == nil && c.Width*c.Height > cfg.Width*cfg.Height {
			best, cfg = j, c
		}
	}
	var m image.Image
	var err error
	switch {
	case best.Len > 0:
		if err := checkConfig(cfg, nil); err != nil {
			return nil, err
		}
		buf := make([]byte, best.Len) // within the file: camraw checked
		if _, err := r.ReadAt(buf, best.Off); err != nil {
			return nil, err
		}
		m, err = jpeg.Decode(bytes.NewReader(buf))
	case info.Thumb:
		if err := checkConfig(tiff.DecodeConfig(io.NewSectionReader(r, 0, size))); err != nil {
			return nil, fmt.Errorf("%s thumbnail: %w", info.Format, err)
		}
		m, err = tiff.Decode(io.NewSectionReader(r, 0, size))
	default:
		return nil, fmt.Errorf("no embedded preview in this %s file", info.Format)
	}
	if err != nil {
		return nil, fmt.Errorf("%s preview: %w", info.Format, err)
	}
	return orient(toNRGBA(m), info.Orient), nil
}

func checkConfig(cfg image.Config, err error) error {
	if err != nil {
		return err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return fmt.Errorf("unsupported image size %dx%d", cfg.Width, cfg.Height)
	}
	return nil
}

// isobmffKind returns "avif", "heic" or "" from the ftyp box brands.
func isobmffKind(data []byte) string {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return ""
	}
	size := int(binary.BigEndian.Uint32(data[0:4]))
	if size < 12 || size > len(data) {
		size = min(len(data), 64)
	}
	brands := []string{string(data[8:12])}
	for i := 16; i+4 <= size; i += 4 {
		brands = append(brands, string(data[i:i+4]))
	}
	kind := ""
	for _, b := range brands {
		switch b {
		case "avif", "avis":
			return "avif"
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			kind = "heic"
		}
	}
	return kind
}

// toNRGBA converts any image to *image.NRGBA with bounds at the origin.
func toNRGBA(m image.Image) *image.NRGBA {
	b := m.Bounds()
	if n, ok := m.(*image.NRGBA); ok && b.Min == (image.Point{}) {
		return n
	}
	r := image.Rect(0, 0, b.Dx(), b.Dy())
	switch src := m.(type) {
	case *image.YCbCr, *image.Gray, *image.CMYK:
		// Always opaque, so premultiplied RGBA has the same bytes as NRGBA
		// and image/draw has fast paths into *image.RGBA.
		dst := image.NewRGBA(r)
		draw.Draw(dst, r, src, b.Min, draw.Src)
		return &image.NRGBA{Pix: dst.Pix, Stride: dst.Stride, Rect: r}
	case *image.RGBA:
		dst := image.NewNRGBA(r)
		for y := 0; y < r.Dy(); y++ {
			s := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):][:4*r.Dx()]
			d := dst.Pix[y*dst.Stride:][:4*r.Dx()]
			for i := 0; i < len(s); i += 4 {
				a := uint32(s[i+3])
				switch a {
				case 0xff:
					copy(d[i:i+4], s[i:i+4])
				case 0:
					// leave transparent black
				default:
					d[i+0] = uint8(uint32(s[i+0]) * 0xff / a)
					d[i+1] = uint8(uint32(s[i+1]) * 0xff / a)
					d[i+2] = uint8(uint32(s[i+2]) * 0xff / a)
					d[i+3] = uint8(a)
				}
			}
		}
		return dst
	}
	dst := image.NewNRGBA(r)
	draw.Draw(dst, r, m, b.Min, draw.Src)
	return dst
}

// exifOrientation returns the EXIF Orientation tag (1..8), or 1 when there
// is no usable EXIF data.
func exifOrientation(format string, data []byte) (o int) {
	defer func() {
		if recover() != nil {
			o = 1
		}
	}()
	if format == "webp" {
		data = webpExif(data)
		if data == nil {
			return 1
		}
	}
	x, err := exif.Decode(bytes.NewReader(data))
	if x == nil || (err != nil && exif.IsCriticalError(err)) {
		return 1
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	v, err := tag.Int(0)
	if err != nil || v < 1 || v > 8 {
		return 1
	}
	return v
}

// webpExif returns the payload of the EXIF chunk of a RIFF/WebP file.
func webpExif(data []byte) []byte {
	return webpExifAt(bytes.NewReader(data), int64(len(data)))
}
