package fileops

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

const (
	testDateTime = "2024:07:14 18:30:05"
	testDTO      = "2023:01:02 03:04:05"
)

// baseJPEG is a small asymmetric picture encoded by image/jpeg (no APP
// segments at all).
func baseJPEG(t *testing.T) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range 16 {
		for x := range 24 {
			m.SetNRGBA(x, y, color.NRGBA{uint8(x * 10), uint8(y * 15), uint8(x * y), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// segment builds a JPEG marker segment.
func segment(marker byte, payload []byte) []byte {
	s := []byte{0xff, marker}
	s = binary.BigEndian.AppendUint16(s, uint16(len(payload)+2))
	return append(s, payload...)
}

// withSegments inserts segs right after the SOI of j.
func withSegments(j []byte, segs ...[]byte) []byte {
	out := bytes.Clone(j[:2])
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, j[2:]...)
}

var jfif = segment(0xe0, []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00"))

// testTIFF is the layout of a TIFF block built by exifTIFF.
type testTIFF struct {
	data []byte
	ifd1 uint32 // offset of IFD1, the old IFD0's next pointer
}

// exifTIFF builds a TIFF block like a camera's: IFD0 with Make (inline),
// Orientation (when o > 0), DateTime and an Exif IFD pointer; an Exif IFD
// with DateTimeOriginal; IFD1 with Compression; then pad filler bytes.
func exifTIFF(order binary.ByteOrder, o, pad int) testTIFF {
	n := 3
	if o > 0 {
		n = 4
	}
	ifdLen := func(n int) int { return 2 + 12*n + 4 }
	dtAt := 8 + ifdLen(n)
	exifAt := dtAt + 20
	dtoAt := exifAt + ifdLen(1)
	ifd1At := dtoAt + 20
	b := make([]byte, ifd1At+ifdLen(1)+pad)
	if order == binary.ByteOrder(binary.LittleEndian) {
		copy(b, "II")
	} else {
		copy(b, "MM")
	}
	order.PutUint16(b[2:], 42)
	order.PutUint32(b[4:], 8)
	at := 8
	entry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(b[at:], tag)
		order.PutUint16(b[at+2:], typ)
		order.PutUint32(b[at+4:], count)
		if typ == 3 && count == 1 {
			order.PutUint16(b[at+8:], uint16(val))
		} else {
			order.PutUint32(b[at+8:], val)
		}
		at += 12
	}
	order.PutUint16(b[at:], uint16(n))
	at += 2
	order.PutUint16(b[at:], 0x010f) // Make, "Pix\0" inline
	order.PutUint16(b[at+2:], 2)
	order.PutUint32(b[at+4:], 4)
	copy(b[at+8:], "Pix\x00")
	at += 12
	if o > 0 {
		entry(0x0112, 3, 1, uint32(o))
	}
	entry(0x0132, 2, 20, uint32(dtAt))
	entry(0x8769, 4, 1, uint32(exifAt))
	order.PutUint32(b[at:], uint32(ifd1At))
	copy(b[dtAt:], testDateTime+"\x00")

	at = exifAt
	order.PutUint16(b[at:], 1)
	at += 2
	entry(0x9003, 2, 20, uint32(dtoAt))
	order.PutUint32(b[at:], 0)
	copy(b[dtoAt:], testDTO+"\x00")

	at = ifd1At
	order.PutUint16(b[at:], 1)
	at += 2
	entry(0x0103, 3, 1, 6) // Compression: JPEG thumbnail
	order.PutUint32(b[at:], 0)
	for i := range pad {
		b[len(b)-pad+i] = 0xa5
	}
	return testTIFF{data: b, ifd1: uint32(ifd1At)}
}

func exifSegment(tiff []byte) []byte {
	return segment(0xe1, append([]byte("Exif\x00\x00"), tiff...))
}

// decodePixels decodes a JPEG with image/jpeg (no orientation applied).
func decodePixels(t *testing.T, j []byte) image.Image {
	t.Helper()
	m, err := jpeg.Decode(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}

// exifOf reads the copy's EXIF with goexif, the way imgload does.
func exifOf(t *testing.T, j []byte) *exif.Exif {
	t.Helper()
	x, err := exif.Decode(bytes.NewReader(j))
	if x == nil || (err != nil && exif.IsCriticalError(err)) {
		t.Fatalf("exif: %v", err)
	}
	return x
}

func orientationOf(t *testing.T, x *exif.Exif) int {
	t.Helper()
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		t.Fatalf("no orientation: %v", err)
	}
	v, err := tag.Int(0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func stringOf(t *testing.T, x *exif.Exif, name exif.FieldName) string {
	t.Helper()
	tag, err := x.Get(name)
	if err != nil {
		t.Fatalf("no %s: %v", name, err)
	}
	s, err := tag.StringVal()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// copyRotated writes src under a fresh dir, copies it with q quarter
// turns into another one and returns the result and the copy's bytes. It
// checks the source is left as it was and the copy keeps its mtime.
func copyRotated(t *testing.T, name string, src []byte, q int) (CopyResult, []byte) {
	t.Helper()
	mt := time.Date(2022, 5, 6, 7, 8, 9, 0, time.Local)
	path := writeFile(t, filepath.Join(t.TempDir(), name), string(src), mt)
	res := CopyAll([]string{path}, t.TempDir(), func(string) int { return q })
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("copy: %+v", res)
	}
	if got := readFile(t, path); got != string(src) {
		t.Fatal("source changed")
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(mt) {
		t.Fatalf("source mtime changed: %v %v", fi, err)
	}
	if fi, err := os.Stat(res[0].Dst); err != nil || !fi.ModTime().Equal(mt) {
		t.Fatalf("copy mtime: %v %v", fi, err)
	}
	return res[0], []byte(readFile(t, res[0].Dst))
}

func TestRotateOrientation(t *testing.T) {
	// One clockwise turn: rotations cycle 1→6→3→8, mirrored ones 2→7→4→5.
	for o, want := range map[int]int{1: 6, 6: 3, 3: 8, 8: 1, 2: 7, 7: 4, 4: 5, 5: 2} {
		if got := RotateOrientation(o, 1); got != want {
			t.Errorf("RotateOrientation(%d, 1) = %d, want %d", o, got, want)
		}
	}
	for o := 1; o <= 8; o++ {
		if RotateOrientation(o, 0) != o || RotateOrientation(o, 4) != o || RotateOrientation(o, -1) != RotateOrientation(o, 3) {
			t.Errorf("orientation %d: turns not taken mod 4", o)
		}
		for a := range 4 {
			for b := range 4 {
				if RotateOrientation(RotateOrientation(o, a), b) != RotateOrientation(o, a+b) {
					t.Errorf("orientation %d: %d then %d turns differs from %d", o, a, b, a+b)
				}
			}
		}
	}
	if RotateOrientation(0, 1) != 6 || RotateOrientation(9, 2) != 3 {
		t.Error("invalid orientation not taken as 1")
	}
}

// Case a: an Orientation tag in IFD0 is patched in place.
func TestCopyRotatedPatchesOrientation(t *testing.T) {
	base := baseJPEG(t)
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, o := range []int{1, 3, 6, 2, 7} {
			src := withSegments(base, jfif, exifSegment(exifTIFF(order, o, 0).data))
			r, out := copyRotated(t, "a.jpg", src, 1)
			if !r.Rotated || r.RotLost != "" || r.Skipped {
				t.Errorf("%v %d: %+v", order, o, r)
			}
			if len(out) != len(src) {
				t.Fatalf("%v %d: length %d, want %d", order, o, len(out), len(src))
			}
			// Only the two bytes of the value may differ.
			first, last := -1, -1
			for i := range src {
				if src[i] != out[i] {
					if first < 0 {
						first = i
					}
					last = i
				}
			}
			if first < 0 || last-first > 1 {
				t.Errorf("%v %d: bytes %d..%d changed", order, o, first, last)
			}
			x := exifOf(t, out)
			if got, want := orientationOf(t, x), RotateOrientation(o, 1); got != want {
				t.Errorf("%v %d: orientation %d, want %d", order, o, got, want)
			}
			if !samePixels(decodePixels(t, out), decodePixels(t, src)) {
				t.Errorf("%v %d: pixels differ", order, o)
			}
		}
	}
}

// Case b: no Exif segment, so a minimal one is inserted after SOI, or
// after a leading JFIF APP0.
func TestCopyRotatedInsertsExif(t *testing.T) {
	base := baseJPEG(t)
	xmp := segment(0xe1, []byte("http://ns.adobe.com/xap/1.0/\x00<x/>"))
	for _, c := range []struct {
		name     string
		src      []byte
		insertAt int
	}{
		{"bare", base, 2},
		{"jfif", withSegments(base, jfif), 2 + len(jfif)},
		{"jfif+xmp", withSegments(base, jfif, xmp), 2 + len(jfif)},
	} {
		for q := 1; q <= 3; q++ {
			r, out := copyRotated(t, "b.jpg", c.src, q)
			if !r.Rotated || r.RotLost != "" {
				t.Errorf("%s q%d: %+v", c.name, q, r)
			}
			if !bytes.Equal(out[:c.insertAt], c.src[:c.insertAt]) || out[c.insertAt] != 0xff || out[c.insertAt+1] != 0xe1 {
				t.Errorf("%s q%d: Exif not at %d", c.name, q, c.insertAt)
			}
			segLen := 2 + int(binary.BigEndian.Uint16(out[c.insertAt+2:]))
			if !bytes.Equal(out[c.insertAt+segLen:], c.src[c.insertAt:]) {
				t.Errorf("%s q%d: the rest is not the source", c.name, q)
			}
			if got := orientationOf(t, exifOf(t, out)); got != RotateOrientation(1, q) {
				t.Errorf("%s q%d: orientation %d", c.name, q, got)
			}
			if !samePixels(decodePixels(t, out), decodePixels(t, c.src)) {
				t.Errorf("%s q%d: pixels differ", c.name, q)
			}
		}
	}
}

// Case c: Exif without Orientation gets a new IFD0 appended to the TIFF
// block; every old tag stays readable.
func TestCopyRotatedAppendsIFD(t *testing.T) {
	base := baseJPEG(t)
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, pad := range []int{0, 1} { // even and odd TIFF block lengths
			tt := exifTIFF(order, 0, pad)
			seg := exifSegment(tt.data)
			src := withSegments(base, jfif, seg)
			r, out := copyRotated(t, "c.jpg", src, 3)
			if !r.Rotated || r.RotLost != "" {
				t.Errorf("%v pad %d: %+v", order, pad, r)
			}
			x := exifOf(t, out)
			if got := orientationOf(t, x); got != 8 {
				t.Errorf("%v pad %d: orientation %d, want 8", order, pad, got)
			}
			for name, want := range map[exif.FieldName]string{
				exif.DateTime: testDateTime, exif.DateTimeOriginal: testDTO, exif.Make: "Pix",
			} {
				if got := stringOf(t, x, name); got != want {
					t.Errorf("%v pad %d: %s = %q, want %q", order, pad, name, got, want)
				}
			}
			if !samePixels(decodePixels(t, out), decodePixels(t, src)) {
				t.Errorf("%v pad %d: pixels differ", order, pad)
			}

			// The old TIFF bytes are where they were; only the segment
			// length and the first-IFD offset changed.
			segAt := 2 + len(jfif)
			tiffAt := segAt + 10
			oldEnd := segAt + len(seg)
			newLen := 2 + int(binary.BigEndian.Uint16(out[segAt+2:]))
			if !bytes.Equal(out[segAt+newLen:], src[oldEnd:]) {
				t.Errorf("%v pad %d: the rest is not the source", order, pad)
			}
			for i := segAt; i < oldEnd; i++ {
				changed := i == segAt+2 || i == segAt+3 || (i >= tiffAt+4 && i < tiffAt+8)
				if !changed && out[i] != src[i] {
					t.Errorf("%v pad %d: old byte %d changed", order, pad, i-segAt)
					break
				}
			}
			ifd := int(order.Uint32(out[tiffAt+4:]))
			if ifd%2 != 0 || ifd < len(tt.data) {
				t.Errorf("%v pad %d: new IFD0 at %d (block %d)", order, pad, ifd, len(tt.data))
			}
			n := int(order.Uint16(out[tiffAt+ifd:]))
			var tags []uint16
			for i := range n {
				tags = append(tags, order.Uint16(out[tiffAt+ifd+2+12*i:]))
			}
			if want := []uint16{0x010f, 0x0112, 0x0132, 0x8769}; !slices.Equal(tags, want) {
				t.Errorf("%v pad %d: tags %x, want %x", order, pad, tags, want)
			}
			if next := order.Uint32(out[tiffAt+ifd+2+12*n:]); next != tt.ifd1 {
				t.Errorf("%v pad %d: next IFD %d, want %d", order, pad, next, tt.ifd1)
			}
		}
	}
}

// An Exif segment with no room for a bigger IFD0, a format other than
// JPEG, or a JPEG pix does not understand: copied unchanged, RotLost set.
func TestCopyRotatedFallsBack(t *testing.T) {
	base := baseJPEG(t)
	full := exifTIFF(binary.LittleEndian, 0, 0)
	full.data = append(full.data, make([]byte, 0xffff-2-6-len(full.data)-40)...)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewNRGBA(image.Rect(0, 0, 4, 2))); err != nil {
		t.Fatal(err)
	}
	badOrder := exifTIFF(binary.LittleEndian, 6, 0)
	copy(badOrder.data, "XX")
	longOrient := exifTIFF(binary.BigEndian, 6, 0)
	binary.BigEndian.PutUint16(longOrient.data[8+2+12+2:], 4) // Orientation as LONG
	badIFD := exifTIFF(binary.BigEndian, 0, 0)
	binary.BigEndian.PutUint32(badIFD.data[4:], 60000)
	truncSeg := withSegments(base, jfif)
	truncSeg = truncSeg[:2+len(jfif)+4] // header ends inside a segment
	rst := withSegments(base, []byte{0xff, 0xd0})

	for _, c := range []struct {
		name, file string
		src        []byte
		lost       string
	}{
		{"full", "f.jpg", withSegments(base, exifSegment(full.data)), "jpeg: no room in EXIF"},
		{"png", "p.jpg", pngBuf.Bytes(), "png"},
		{"text", "t.jpg", []byte("not an image"), "unknown format"},
		{"byte order", "o.jpg", withSegments(base, exifSegment(badOrder.data)), "jpeg: unexpected EXIF"},
		{"long orientation", "l.jpg", withSegments(base, exifSegment(longOrient.data)), "jpeg: unexpected EXIF"},
		{"IFD out of bounds", "i.jpg", withSegments(base, exifSegment(badIFD.data)), "jpeg: unexpected EXIF"},
		{"truncated", "u.jpg", truncSeg, "jpeg: unexpected header"},
		{"standalone marker", "s.jpg", rst, "jpeg: unexpected header"},
	} {
		r, out := copyRotated(t, c.file, c.src, 1)
		if r.Rotated || r.RotLost != c.lost {
			t.Errorf("%s: %+v, want RotLost %q", c.name, r, c.lost)
		}
		if !bytes.Equal(out, c.src) {
			t.Errorf("%s: copy differs from the source", c.name)
		}
	}

	// Just under the limit still fits.
	fits := exifTIFF(binary.LittleEndian, 0, 0)
	// (The new IFD0 is word aligned, so the longest segment is 0xfffe.)
	fits.data = append(fits.data, make([]byte, 0xffff-2-6-len(fits.data)-(2+12*4+4)-1)...)
	if r, _ := copyRotated(t, "fits.jpg", withSegments(base, exifSegment(fits.data)), 1); !r.Rotated {
		t.Errorf("segment that just fits: %+v", r)
	}

	// No rotation (or whole turns): a plain copy, nothing lost.
	for _, q := range []int{0, 4, -4} {
		r, out := copyRotated(t, "z.png", pngBuf.Bytes(), q)
		if r.Rotated || r.RotLost != "" || !bytes.Equal(out, pngBuf.Bytes()) {
			t.Errorf("q %d: %+v", q, r)
		}
	}
}

// orientJPEG never panics on a header cut short anywhere.
func TestOrientJPEGTruncated(t *testing.T) {
	base := baseJPEG(t)
	for _, src := range [][]byte{
		withSegments(base, jfif, exifSegment(exifTIFF(binary.LittleEndian, 0, 1).data)),
		withSegments(base, exifSegment(exifTIFF(binary.BigEndian, 3, 0).data)),
		withSegments(base, jfif),
	} {
		for n := range min(len(src), 600) {
			out, cut, err := orientJPEG(src[:n], 1)
			if err == nil && (cut > n || len(out) < cut) {
				t.Errorf("len %d: cut %d, out %d", n, cut, len(out))
			}
		}
	}
}

// The identical-file check compares with the rotated bytes: the same
// rotation again is skipped, another one (or none) gets a new name.
func TestCopyRotatedDedup(t *testing.T) {
	src := withSegments(baseJPEG(t), exifSegment(exifTIFF(binary.LittleEndian, 1, 0).data))
	path := writeFile(t, filepath.Join(t.TempDir(), "d.jpg"), string(src), time.Time{})
	out := t.TempDir()
	turn := func(q int) func(string) int { return func(string) int { return q } }

	for _, c := range []struct {
		q       int
		dst     string
		skipped bool
	}{
		{1, "d.jpg", false},
		{1, "d.jpg", true},
		{2, "d-2.jpg", false},
		{0, "d-3.jpg", false},
		{2, "d-2.jpg", true},
		{0, "d-3.jpg", true},
		{5, "d.jpg", true}, // 5 quarter turns = 1
	} {
		r := CopyAll([]string{path}, out, turn(c.q))[0]
		if r.Err != nil || r.Dst != filepath.Join(out, c.dst) || r.Skipped != c.skipped || r.Rotated != (c.q%4 != 0) {
			t.Errorf("q %d: %+v, want %s skipped %v", c.q, r, c.dst, c.skipped)
		}
	}
	if readFile(t, path) != string(src) {
		t.Error("source changed")
	}
	if got := readFile(t, filepath.Join(out, "d-3.jpg")); got != string(src) {
		t.Error("unrotated copy differs from the source")
	}
	for _, n := range dirNames(t, out) {
		if strings.HasPrefix(n, ".") {
			t.Errorf("leftover %s", n)
		}
	}
}
