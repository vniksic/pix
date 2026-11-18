package imgload

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/tiff"

	"pix/internal/camraw"
)

// TestRawSamples decodes every file in $PIX_RAW_SAMPLES (real camera
// files are too big for testdata) and logs what pix makes of it: run with
// PIX_RAW_SAMPLES=dir go test -run RawSamples -v ./internal/imgload
func TestRawSamples(t *testing.T) {
	dir := os.Getenv("PIX_RAW_SAMPLES")
	if dir == "" {
		t.Skip("PIX_RAW_SAMPLES not set")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no samples in %s: %v", dir, err)
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := f.Stat()
		info, ok := camraw.Parse(f, fi.Size())
		var cands []string
		for _, j := range info.JPEGs {
			c, err := jpeg.DecodeConfig(io.NewSectionReader(f, j.Off, j.Len))
			if err != nil {
				cands = append(cands, fmt.Sprintf("@%d %v", j.Off, err))
				continue
			}
			cands = append(cands, fmt.Sprintf("@%d %dx%d", j.Off, c.Width, c.Height))
		}
		f.Close()
		start := time.Now()
		img, meta, err := decodeWithMeta(p)
		el := time.Since(start).Round(time.Millisecond)
		name := filepath.Base(p)
		if err != nil {
			t.Logf("%-18s raw=%v %-4s orient=%d thumb=%v taken=%q candidates=%v -> error: %v", name, ok, info.Format, info.Orient, info.Thumb, info.Taken, cands, err)
			continue
		}
		b := img.Bounds()
		t.Logf("%-18s raw=%v %-4s orient=%d taken=%s shown=%dx%d (%v) candidates=%v", name, ok, meta.Raw, info.Orient,
			meta.Taken.Format("2006-01-02 15:04:05"), b.Dx(), b.Dy(), el, cands)
		if rm := ReadMeta(p); rm != meta {
			t.Errorf("%s: ReadMeta %+v, decode's Meta %+v", name, rm, meta)
		}
	}
}

// ifdEntry appends a little-endian IFD entry (inline value or offset).
func ifdEntry(b []byte, tag, typ uint16, count, val uint32) []byte {
	le := binary.LittleEndian
	return le.AppendUint32(le.AppendUint32(le.AppendUint16(le.AppendUint16(b, tag), typ), count), val)
}

// cr2File is shaped like a Canon CR2: IFD0 (with the orientation) and the
// IFDs chained after it each hold one strip, here strips[i].
func cr2File(orient uint16, strips ...[]byte) []byte {
	le := binary.LittleEndian
	b := []byte("II*\x00\x10\x00\x00\x00CR\x02\x00\x00\x00\x00\x00")
	ifdLen := func(i int) int { // IFD0 also has the orientation
		if i == 0 {
			return 2 + 3*12 + 4
		}
		return 2 + 2*12 + 4
	}
	data := len(b)
	for i := range strips {
		data += ifdLen(i)
	}
	at := len(b)
	for i, s := range strips {
		at += ifdLen(i)
		next := uint32(at)
		if i == len(strips)-1 {
			next = 0
		}
		if i == 0 {
			b = le.AppendUint16(b, 3)
			b = ifdEntry(b, 0x0112, 3, 1, uint32(orient))
		} else {
			b = le.AppendUint16(b, 2)
		}
		b = ifdEntry(b, 0x0111, 4, 1, uint32(data))
		b = ifdEntry(b, 0x0117, 4, 1, uint32(len(s)))
		b = le.AppendUint32(b, next)
		data += len(s)
	}
	for _, s := range strips {
		b = append(b, s...)
	}
	return b
}

// rw2File is shaped like a Panasonic RW2: IFD0 holds the orientation and
// the preview JPEG as tag 0x2e.
func rw2File(orient uint16, jpg []byte) []byte {
	b := []byte("IIU\x00\x08\x00\x00\x00\x02\x00")
	b = ifdEntry(b, 0x0112, 3, 1, uint32(orient))
	b = ifdEntry(b, 0x002e, 7, uint32(len(jpg)), 8+2+2*12+4)
	return append(binary.LittleEndian.AppendUint32(b, 0), jpg...)
}

// rafFile is shaped like a Fujifilm RAF: a header pointing at the JPEG.
func rafFile(jpg []byte) []byte {
	b := make([]byte, 100)
	copy(b, "FUJIFILMCCD-RAW 0201FF159501X-T3")
	binary.BigEndian.PutUint32(b[84:], 100)
	binary.BigEndian.PutUint32(b[88:], uint32(len(jpg)))
	return append(b, jpg...)
}

func q95(t *testing.T, m image.Image) []byte {
	return encode(t, m, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, &jpeg.Options{Quality: 95}) })
}

func decodeAs(t *testing.T, name string, data []byte, want *image.NRGBA) {
	t.Helper()
	img, err := Decode(writeFile(t, "x.raw", data))
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	if img.Rect != want.Rect || !nearQuads(quadColors(img), quadColors(want)) {
		t.Errorf("%s: %v %v, want %v %v", name, img.Rect, quadColors(img), want.Rect, quadColors(want))
	}
}

// The raw file's orientation is applied to the preview exactly once: the
// preview's own Exif orientation, which some carry, is not applied too.
func TestDecodeRawOrientation(t *testing.T) {
	src := quadrants(32, 16)
	plain := q95(t, src)
	for o := 1; o <= 8; o++ {
		decodeAs(t, fmt.Sprintf("rw2 orientation %d", o), rw2File(uint16(o), plain), orient(src, o))
	}
	own6 := jpegWithOrientation(t, src, 6)
	decodeAs(t, "rw2 6, preview 6", rw2File(6, own6), orient(src, 6))
	decodeAs(t, "rw2 1, preview 6", rw2File(1, own6), src)
	decodeAs(t, "cr2 8", cr2File(8, plain), orient(src, 8))
	// RAF has no orientation of its own: its JPEG's Exif is the file's.
	decodeAs(t, "raf, preview 6", rafFile(own6), orient(src, 6))
	decodeAs(t, "raf, preview 1", rafFile(plain), src)
}

// The largest JPEG that decodes wins; sensor data stored as lossless
// JPEG (Go cannot decode it) is passed over.
func TestDecodeRawLargest(t *testing.T) {
	small, big := q95(t, quadrants(8, 4)), q95(t, quadrants(32, 16))
	lossless := []byte("\xff\xd8\xff\xc3\x00\x0b\x10\x10\x00\x20\x00\x01\x01\x11\x00\xff\xd9")
	img, meta, err := decodeWithMeta(writeFile(t, "a.cr2", cr2File(1, small, lossless, big)))
	if err != nil || img.Rect.Dx() != 32 || meta.Raw != "CR2" {
		t.Fatalf("got %+v %v, want the 32×16 preview of a CR2", meta, err)
	}
	if _, err := Decode(writeFile(t, "b.cr2", cr2File(1, lossless))); err == nil || !strings.Contains(err.Error(), "no embedded preview in this CR2") {
		t.Errorf("only sensor data: err %v", err)
	}
}

// A preview is held to the same limits as any image, and damage gives an
// error, not a panic.
func TestDecodeRawBad(t *testing.T) {
	// A JPEG header claiming 60000×60000 (SOF0, SOS), and nothing else.
	huge := []byte("\xff\xd8\xff\xc0\x00\x0b\x08\xea\x60\xea\x60\x01\x01\x11\x00\xff\xda\x00\x08\x01\x01\x00\x00\x3f\x00\xff\xd9")
	if _, err := Decode(writeFile(t, "h.cr2", cr2File(1, huge))); err == nil || !strings.Contains(err.Error(), "unsupported image size") {
		t.Errorf("huge preview: err %v", err)
	}
	full := cr2File(6, q95(t, quadrants(32, 16)))
	for n := 0; n < len(full); n += 7 {
		Decode(writeFile(t, "t.cr2", full[:n])) // must not panic
	}
	bad := slices.Clone(full)
	for i := len(bad) - 200; i < len(bad)-2; i++ {
		bad[i] = 0x55
	}
	Decode(writeFile(t, "c.cr2", bad)) // must not panic
}

// A DNG without a JPEG preview shows the RGB thumbnail in IFD0, turned
// by the DNG's orientation.
func TestDecodeRawThumb(t *testing.T) {
	src := quadrants(8, 4)
	le := binary.LittleEndian
	const n = 12
	data := 8 + 2 + n*12 + 4
	b := le.AppendUint16([]byte("II*\x00\x08\x00\x00\x00"), n)
	b = ifdEntry(b, 256, 3, 1, 8)              // ImageWidth
	b = ifdEntry(b, 257, 3, 1, 4)              // ImageLength
	b = ifdEntry(b, 258, 3, 3, uint32(data))   // BitsPerSample 8,8,8
	b = ifdEntry(b, 259, 3, 1, 1)              // no compression
	b = ifdEntry(b, 262, 3, 1, 2)              // RGB
	b = ifdEntry(b, 273, 4, 1, uint32(data+6)) // StripOffsets
	b = ifdEntry(b, 274, 3, 1, 6)              // Orientation
	b = ifdEntry(b, 277, 3, 1, 3)              // SamplesPerPixel
	b = ifdEntry(b, 278, 3, 1, 4)              // RowsPerStrip
	b = ifdEntry(b, 279, 4, 1, 8*4*3)          // StripByteCounts
	b = ifdEntry(b, 284, 3, 1, 1)              // PlanarConfiguration
	b = ifdEntry(b, 0xc612, 1, 4, 0x00000401)  // DNGVersion 1.4.0.0
	b = le.AppendUint32(b, 0)
	b = append(b, 8, 0, 8, 0, 8, 0)
	for i := 0; i < len(src.Pix); i += 4 {
		b = append(b, src.Pix[i:i+3]...)
	}
	img, meta, err := decodeWithMeta(writeFile(t, "t.dng", b))
	if err != nil || meta.Raw != "DNG" {
		t.Fatalf("got %+v %v", meta, err)
	}
	if want := orient(src, 6); img.Rect != want.Rect || quadColors(img) != quadColors(want) {
		t.Errorf("thumbnail %v %v, want %v %v", img.Rect, quadColors(img), want.Rect, quadColors(want))
	}
}

// ReadMeta reads a raw file's date like decoding does; a genuine TIFF is
// still decoded as a TIFF, not as a raw file.
func TestReadMetaRaw(t *testing.T) {
	path := writeFile(t, "a.raf", rafFile(jpegWithExif(t, exifDates("2019:04:10 21:53:47", "2000:01:01 00:00:00"))))
	want := Meta{Taken: localTime(2019, 4, 10, 21, 53, 47), Raw: "RAF"}
	if m := ReadMeta(path); m != want {
		t.Errorf("ReadMeta %+v, want %+v", m, want)
	}
	if _, m, err := decodeWithMeta(path); err != nil || m != want {
		t.Errorf("decodeWithMeta %+v %v, want %+v", m, err, want)
	}
	if _, ok := Taken(writeFile(t, "b.cr2", cr2File(1))); ok {
		t.Error("CR2 without a date has one")
	}

	src := quadrants(16, 8)
	tif := encode(t, src, func(b *bytes.Buffer, m image.Image) error { return tiff.Encode(b, m, nil) })
	img, m, err := decodeWithMeta(writeFile(t, "c.tif", tif))
	if err != nil || m.Raw != "" || img.Rect != src.Rect || quadColors(img) != quadColors(src) {
		t.Errorf("TIFF: %+v %v", m, err)
	}
}
