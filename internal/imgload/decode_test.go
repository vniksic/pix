package imgload

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/jpegxl"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

var (
	red   = color.NRGBA{255, 0, 0, 255}
	green = color.NRGBA{0, 255, 0, 255}
	blue  = color.NRGBA{0, 0, 255, 255}
	white = color.NRGBA{255, 255, 255, 255}
)

// quadrants returns a w×h image: red TL, green TR, blue BL, white BR.
func quadrants(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := [2][2]color.NRGBA{{red, green}, {blue, white}}[2*y/h][2*x/w]
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

// quadColors samples the centre of each quadrant: TL, TR, BL, BR.
func quadColors(m image.Image) [4]color.NRGBA {
	b := m.Bounds()
	var out [4]color.NRGBA
	for i, p := range []image.Point{{1, 1}, {3, 1}, {1, 3}, {3, 3}} {
		c := m.At(b.Min.X+p.X*b.Dx()/4, b.Min.Y+p.Y*b.Dy()/4)
		out[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
	}
	return out
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return max(x, y)-min(x, y) < 40 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func nearQuads(a, b [4]color.NRGBA) bool {
	for i := range a {
		if !near(a[i], b[i]) {
			return false
		}
	}
	return true
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func encode(t *testing.T, m image.Image, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, m); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tinyWebP is a 1×1 lossless WebP (VP8L).
var tinyWebP, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

func TestDecodeFormats(t *testing.T) {
	src := quadrants(32, 16)
	want := quadColors(src)
	pal := image.NewPaletted(src.Rect, color.Palette{red, green, blue, white})
	for y := range 16 {
		for x := range 32 {
			pal.Set(x, y, src.At(x, y))
		}
	}
	animated := encode(t, nil, func(b *bytes.Buffer, _ image.Image) error {
		other := image.NewPaletted(src.Rect, color.Palette{blue})
		return gif.EncodeAll(b, &gif.GIF{Image: []*image.Paletted{pal, other}, Delay: []int{10, 10}})
	})

	cases := []struct {
		name string
		data []byte
	}{
		{"png", encode(t, src, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })},
		{"jpeg", encode(t, src, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, &jpeg.Options{Quality: 95}) })},
		{"gif-animated", animated},
		{"bmp", encode(t, src, func(b *bytes.Buffer, m image.Image) error { return bmp.Encode(b, m) })},
		{"tiff", encode(t, src, func(b *bytes.Buffer, m image.Image) error { return tiff.Encode(b, m, nil) })},
		{"avif", encode(t, src, func(b *bytes.Buffer, m image.Image) error {
			return avif.Encode(b, m, avif.Options{Lossless: true, Speed: 10})
		})},
		{"jxl", encode(t, src, func(b *bytes.Buffer, m image.Image) error {
			return jpegxl.Encode(b, m, jpegxl.Options{Lossless: true, Effort: 1})
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img, err := Decode(writeFile(t, "img.bin", c.data)) // no extension on purpose
			if err != nil {
				t.Fatal(err)
			}
			if img.Rect != image.Rect(0, 0, 32, 16) {
				t.Fatalf("bounds %v", img.Rect)
			}
			if got := quadColors(img); !nearQuads(got, want) {
				t.Errorf("colors %v, want %v", got, want)
			}
		})
	}

	t.Run("gray-jpeg", func(t *testing.T) {
		g := image.NewGray(image.Rect(0, 0, 8, 8))
		for i := range g.Pix {
			g.Pix[i] = 200
		}
		data := encode(t, g, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
		img, err := Decode(writeFile(t, "g.jpg", data))
		if err != nil {
			t.Fatal(err)
		}
		if c := img.NRGBAAt(4, 4); !near(c, color.NRGBA{200, 200, 200, 255}) {
			t.Errorf("gray pixel %v", c)
		}
	})

	t.Run("webp", func(t *testing.T) {
		img, err := Decode(writeFile(t, "w", tinyWebP))
		if err != nil {
			t.Fatal(err)
		}
		if img.Rect != image.Rect(0, 0, 1, 1) {
			t.Errorf("bounds %v", img.Rect)
		}
	})

	t.Run("heic", func(t *testing.T) {
		// Fixture from github.com/gen2brain/heic (MIT): EXIF orientation 6,
		// which the HEIC decoder applies itself.
		img, err := Decode("testdata/orient6.heic")
		if err != nil {
			t.Fatal(err)
		}
		if img.Rect != image.Rect(0, 0, 480, 640) {
			t.Errorf("bounds %v, want 480x640", img.Rect)
		}
	})
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing file: no error")
	}
	if _, err := Decode(writeFile(t, "junk", []byte("not an image at all"))); err == nil {
		t.Error("junk: no error")
	}
	png := encode(t, quadrants(4, 4), func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
	if _, err := Decode(writeFile(t, "trunc.png", png[:len(png)/2])); err == nil {
		t.Error("truncated png: no error")
	}
	// A valid PNG header claiming 100000×100000 must be refused, not allocated.
	huge := bytes.Clone(png)
	binary.BigEndian.PutUint32(huge[16:], 100000)
	binary.BigEndian.PutUint32(huge[20:], 100000)
	binary.BigEndian.PutUint32(huge[29:], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := Decode(writeFile(t, "huge.png", huge)); err == nil {
		t.Error("huge png: no error")
	}
	// A HEIC/AVIF-branded file with garbage must error, not panic.
	if _, err := Decode(writeFile(t, "bad.heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heicgarbage"))); err == nil {
		t.Error("bad heic: no error")
	}
	if _, err := Decode(writeFile(t, "bad.avif", []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avifgarbage"))); err == nil {
		t.Error("bad avif: no error")
	}
}

// exifTIFF is a minimal little-endian TIFF structure holding only the
// Orientation tag.
func exifTIFF(orientation uint16) []byte {
	b := []byte("II*\x00\x08\x00\x00\x00")
	b = binary.LittleEndian.AppendUint16(b, 1)      // one IFD entry
	b = binary.LittleEndian.AppendUint16(b, 0x0112) // Orientation
	b = binary.LittleEndian.AppendUint16(b, 3)      // SHORT
	b = binary.LittleEndian.AppendUint32(b, 1)      // count
	b = binary.LittleEndian.AppendUint16(b, orientation)
	b = append(b, 0, 0)                           // value padding
	return binary.LittleEndian.AppendUint32(b, 0) // no next IFD
}

// jpegWithOrientation inserts an APP1 Exif segment right after SOI.
func jpegWithOrientation(t *testing.T, m image.Image, o uint16) []byte {
	j := encode(t, m, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, &jpeg.Options{Quality: 95}) })
	payload := append([]byte("Exif\x00\x00"), exifTIFF(o)...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	return append(append(j[:2:2], seg...), j[2:]...)
}

func TestDecodeJPEGOrientation(t *testing.T) {
	src := quadrants(32, 16)
	for o := 1; o <= 8; o++ {
		img, err := Decode(writeFile(t, "o.jpg", jpegWithOrientation(t, src, uint16(o))))
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		want := orient(src, o)
		if img.Rect != want.Rect {
			t.Errorf("orientation %d: bounds %v, want %v", o, img.Rect, want.Rect)
			continue
		}
		if got, w := quadColors(img), quadColors(want); !nearQuads(got, w) {
			t.Errorf("orientation %d: colors %v, want %v", o, got, w)
		}
	}
	// Orientation 6 = rotate 90° clockwise: blue (bottom-left) ends top-left.
	img, _ := Decode(writeFile(t, "o6.jpg", jpegWithOrientation(t, src, 6)))
	if q := quadColors(img); !near(q[0], blue) || !near(q[1], red) {
		t.Errorf("orientation 6 colors %v", q)
	}
}

func TestExifOrientation(t *testing.T) {
	if o := exifOrientation("tiff", exifTIFF(7)); o != 7 {
		t.Errorf("tiff: %d, want 7", o)
	}
	if o := exifOrientation("jpeg", []byte("\xff\xd8\xff\xd9")); o != 1 {
		t.Errorf("no exif: %d, want 1", o)
	}
	if o := exifOrientation("tiff", exifTIFF(42)); o != 1 {
		t.Errorf("out of range: %d, want 1", o)
	}
	if o := exifOrientation("jpeg", nil); o != 1 {
		t.Errorf("empty: %d, want 1", o)
	}

	// Extended WebP: VP8X (EXIF flag) + the tiny VP8L image + EXIF chunk.
	chunk := func(fourcc string, data []byte) []byte {
		b := binary.LittleEndian.AppendUint32([]byte(fourcc), uint32(len(data)))
		b = append(b, data...)
		if len(data)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	body := []byte("WEBP")
	body = append(body, chunk("VP8X", []byte{0x08, 0, 0, 0, 0, 0, 0, 0, 0, 0})...)
	body = append(body, tinyWebP[12:]...) // VP8L chunk, already padded
	body = append(body, chunk("EXIF", append([]byte("Exif\x00\x00"), exifTIFF(6)...))...)
	webp := append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)

	if o := exifOrientation("webp", webp); o != 6 {
		t.Errorf("webp: %d, want 6", o)
	}
	if o := exifOrientation("webp", tinyWebP); o != 1 {
		t.Errorf("webp without exif: %d, want 1", o)
	}
	if _, err := Decode(writeFile(t, "x.webp", webp)); err != nil {
		t.Errorf("decode extended webp: %v", err)
	}
}

func TestOrient(t *testing.T) {
	// Source 3×2:  a b c
	//              d e f
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range 6 {
		src.Pix[4*i] = byte('a' + i)
	}
	want := map[int][]string{
		1: {"abc", "def"},
		2: {"cba", "fed"},
		3: {"fed", "cba"},
		4: {"def", "abc"},
		5: {"ad", "be", "cf"},
		6: {"da", "eb", "fc"},
		7: {"fc", "eb", "da"},
		8: {"cf", "be", "ad"},
		0: {"abc", "def"}, // invalid values are ignored
		9: {"abc", "def"},
	}
	for o, rows := range want {
		got := orient(src, o)
		var gotRows []string
		for y := range got.Rect.Dy() {
			var s []byte
			for x := range got.Rect.Dx() {
				s = append(s, got.NRGBAAt(x, y).R)
			}
			gotRows = append(gotRows, string(s))
		}
		if !slices.Equal(gotRows, rows) {
			t.Errorf("orient %d = %q, want %q", o, gotRows, rows)
		}
	}
}

func TestToNRGBA(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(10, 10, 12, 11))
	rgba.SetRGBA(10, 10, color.RGBA{64, 0, 32, 128}) // premultiplied
	rgba.SetRGBA(11, 10, color.RGBA{10, 20, 30, 255})
	n := toNRGBA(rgba)
	if n.Rect != image.Rect(0, 0, 2, 1) {
		t.Fatalf("bounds %v", n.Rect)
	}
	if c := n.NRGBAAt(0, 0); !near(c, color.NRGBA{127, 0, 63, 128}) {
		t.Errorf("unpremultiplied %v", c)
	}
	if c := n.NRGBAAt(1, 0); c != (color.NRGBA{10, 20, 30, 255}) {
		t.Errorf("opaque %v", c)
	}

	cmyk := image.NewCMYK(image.Rect(0, 0, 1, 1))
	cmyk.SetCMYK(0, 0, color.CMYK{0, 255, 255, 0}) // red
	if c := toNRGBA(cmyk).NRGBAAt(0, 0); c != red {
		t.Errorf("cmyk %v", c)
	}

	sub := quadrants(4, 4).SubImage(image.Rect(2, 2, 4, 4))
	if n := toNRGBA(sub); n.Rect != image.Rect(0, 0, 2, 2) || n.NRGBAAt(0, 0) != white {
		t.Errorf("sub-image %v %v", n.Rect, n.NRGBAAt(0, 0))
	}

	same := quadrants(2, 2)
	if toNRGBA(same) != same {
		t.Error("NRGBA at origin was copied")
	}
}
