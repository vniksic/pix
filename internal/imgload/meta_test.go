package imgload

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// exifDates builds a little-endian TIFF block with DateTime (IFD0) and
// DateTimeOriginal (Exif sub-IFD); an empty string leaves that tag out.
func exifDates(dateTimeOriginal, dateTime string) []byte {
	le := binary.LittleEndian
	ascii := func(s string) []byte { return append([]byte(s), 0) }
	type tag struct {
		id    uint16
		typ   uint16
		count uint32
		value uint32 // inline value or offset, patched below
		data  []byte // out-of-line data, nil for inline
	}
	var ifd0 []tag
	if dateTime != "" {
		ifd0 = append(ifd0, tag{id: 0x0132, typ: 2, count: uint32(len(dateTime) + 1), data: ascii(dateTime)})
	}
	if dateTimeOriginal != "" {
		ifd0 = append(ifd0, tag{id: 0x8769, typ: 4, count: 1}) // Exif IFD pointer
	}
	ifdSize := func(n int) uint32 { return uint32(2 + 12*n + 4) }

	// Layout: header, IFD0, IFD0 data, Exif IFD, Exif IFD data.
	off := 8 + ifdSize(len(ifd0))
	for i := range ifd0 {
		if ifd0[i].data != nil {
			ifd0[i].value = off
			off += uint32(len(ifd0[i].data))
		}
	}
	exifOff := off
	var exifIFD []tag
	if dateTimeOriginal != "" {
		for i := range ifd0 {
			if ifd0[i].id == 0x8769 {
				ifd0[i].value = exifOff
			}
		}
		exifIFD = []tag{{id: 0x9003, typ: 2, count: uint32(len(dateTimeOriginal) + 1),
			value: exifOff + ifdSize(1), data: ascii(dateTimeOriginal)}}
	}
	writeIFD := func(b []byte, tags []tag) []byte {
		b = le.AppendUint16(b, uint16(len(tags)))
		for _, t := range tags {
			b = le.AppendUint16(b, t.id)
			b = le.AppendUint16(b, t.typ)
			b = le.AppendUint32(b, t.count)
			b = le.AppendUint32(b, t.value)
		}
		b = le.AppendUint32(b, 0)
		for _, t := range tags {
			b = append(b, t.data...)
		}
		return b
	}
	b := []byte("II*\x00\x08\x00\x00\x00")
	b = writeIFD(b, ifd0)
	if exifIFD != nil {
		b = writeIFD(b, exifIFD)
	}
	return b
}

// jpegWithExif inserts an APP1 Exif segment holding tiff right after SOI.
func jpegWithExif(t *testing.T, tiff []byte) []byte {
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	j := encode(t, m, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	return append(append(j[:2:2], seg...), j[2:]...)
}

func localTime(y, mo, d, h, mi, s int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, s, 0, time.Local)
}

func TestReadMetaJPEG(t *testing.T) {
	want := localTime(2024, 7, 14, 18, 30, 5)
	cases := []struct {
		name    string
		dto, dt string
		want    time.Time
	}{
		{"original", "2024:07:14 18:30:05", "", want},
		{"original wins", "2024:07:14 18:30:05", "2025:01:01 00:00:00", want},
		{"datetime fallback", "", "2024:07:14 18:30:05", want},
		{"zero original falls back", "0000:00:00 00:00:00", "2024:07:14 18:30:05", want},
		{"all zero", "0000:00:00 00:00:00", "0000:00:00 00:00:00", time.Time{}},
		{"blank", "    :  :     :  :  ", "", time.Time{}},
		{"garbage", "yesterday", "", time.Time{}},
		{"invalid month", "2024:13:14 18:30:05", "", time.Time{}},
	}
	for _, c := range cases {
		path := writeFile(t, "d.jpg", jpegWithExif(t, exifDates(c.dto, c.dt)))
		got := ReadMeta(path).Taken
		if !got.Equal(c.want) {
			t.Errorf("%s: Taken = %v, want %v", c.name, got, c.want)
		}
		if !got.IsZero() && got.Location() != time.Local {
			t.Errorf("%s: location %v, want Local", c.name, got.Location())
		}
		tk, ok := Taken(path)
		if ok != !c.want.IsZero() || !tk.Equal(c.want) {
			t.Errorf("%s: Taken() = %v, %v", c.name, tk, ok)
		}
	}
}

func TestReadMetaAbsent(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	files := map[string][]byte{
		"plain.jpg":  encode(t, m, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }),
		"plain.png":  encode(t, m, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }),
		"orient.jpg": jpegWithOrientation(t, m, 6), // EXIF without dates
		"empty.jpg":  nil,
		"text.txt":   []byte("hello"),
		"soi.jpg":    []byte("\xff\xd8"),
	}
	for name, data := range files {
		if got := ReadMeta(writeFile(t, name, data)); got != (Meta{}) {
			t.Errorf("%s: %+v, want zero", name, got)
		}
	}
	if got := ReadMeta(filepath.Join(t.TempDir(), "missing.jpg")); got != (Meta{}) {
		t.Errorf("missing: %+v", got)
	}
	if got := ReadMeta(t.TempDir()); got != (Meta{}) {
		t.Errorf("directory: %+v", got)
	}
	if _, ok := Taken(writeFile(t, "p.png", files["plain.png"])); ok {
		t.Error("Taken reported a date for a PNG")
	}
}

func TestReadMetaTruncatedNeverPanics(t *testing.T) {
	full := jpegWithExif(t, exifDates("2024:07:14 18:30:05", "2024:07:14 18:30:05"))
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jpg")
	for n := 0; n < 200 && n < len(full); n++ {
		if err := os.WriteFile(path, full[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		ReadMeta(path)
		// Corrupt one byte inside the EXIF block too.
		bad := bytes.Clone(full)
		bad[n] ^= 0xff
		if err := os.WriteFile(path, bad, 0o644); err != nil {
			t.Fatal(err)
		}
		ReadMeta(path)
	}
}

func TestReadMetaOtherFormats(t *testing.T) {
	want := localTime(2021, 2, 3, 4, 5, 6)
	tiff := exifDates("2021:02:03 04:05:06", "")

	// A TIFF file is its own EXIF block.
	if got := ReadMeta(writeFile(t, "d.tif", tiff)).Taken; !got.Equal(want) {
		t.Errorf("tiff: %v, want %v", got, want)
	}

	// WebP: EXIF chunk after the image data.
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
	body = append(body, tinyWebP[12:]...)
	body = append(body, chunk("EXIF", append([]byte("Exif\x00\x00"), tiff...))...)
	webp := append(binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body))), body...)
	if got := ReadMeta(writeFile(t, "d.webp", webp)).Taken; !got.Equal(want) {
		t.Errorf("webp: %v, want %v", got, want)
	}
	if got := ReadMeta(writeFile(t, "t.webp", tinyWebP)); got != (Meta{}) {
		t.Errorf("webp without exif: %+v", got)
	}

	// HEIC: Exif item in mdat; the sample has no date tags, which must
	// simply give a zero Meta.
	heic, err := os.ReadFile("testdata/orient6.heic")
	if err != nil {
		t.Fatal(err)
	}
	ReadMeta(writeFile(t, "o.heic", heic))
	// Synthetic HEIC-like file: ftyp heic + an "Exif" item with a date.
	ftyp := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")
	item := append([]byte("\x00\x00\x00\x06Exif\x00\x00"), tiff...)
	if got := ReadMeta(writeFile(t, "d.heic", append(ftyp, item...))).Taken; !got.Equal(want) {
		t.Errorf("heic: %v, want %v", got, want)
	}
}

func TestReadMetaBoundedPrefix(t *testing.T) {
	// An APP1 Exif segment starting beyond the first MiB is not looked for.
	j := jpegWithExif(t, exifDates("2024:07:14 18:30:05", ""))
	pad := []byte{}
	for range 20 { // 20 APP2 segments of ~64 KiB
		seg := []byte{0xff, 0xe2, 0xff, 0xff}
		pad = append(pad, append(seg, make([]byte, 0xffff-2)...)...)
	}
	far := append(append(j[:2:2], pad...), j[2:]...)
	if got := ReadMeta(writeFile(t, "far.jpg", far)); got != (Meta{}) {
		t.Errorf("exif beyond prefix: %+v", got)
	}
	// Within the prefix it is found after other segments.
	near := append(append(j[:2:2], pad[:4*(0xffff+2)]...), j[2:]...)
	if got := ReadMeta(writeFile(t, "near.jpg", near)).Taken; got.IsZero() {
		t.Error("exif after a few APP2 segments not found")
	}
}

func TestLoaderGetMeta(t *testing.T) {
	path := writeFile(t, "d.jpg", jpegWithExif(t, exifDates("2024:07:14 18:30:05", "")))
	want := localTime(2024, 7, 14, 18, 30, 5)

	l := NewLoader(6, 2)
	img, err := l.Get(path)
	if err != nil || img.Rect.Dx() != 8 {
		t.Fatalf("Get = %v, %v", img, err)
	}
	// Meta was cached with the image: no file access needed any more.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	img2, meta, err := l.GetMeta(path)
	if err != nil || img2 != img {
		t.Fatalf("GetMeta = %v, %v; want the cached image", img2, err)
	}
	if !meta.Taken.Equal(want) {
		t.Errorf("GetMeta Taken = %v, want %v", meta.Taken, want)
	}

	// Prefetched entries carry Meta too; errors give a zero Meta.
	path2 := writeFile(t, "e.jpg", jpegWithExif(t, exifDates("", "2020:01:02 03:04:05")))
	l.Prefetch(path2)
	if _, meta, err := l.GetMeta(path2); err != nil || !meta.Taken.Equal(localTime(2020, 1, 2, 3, 4, 5)) {
		t.Errorf("prefetched GetMeta = %v, %v", meta, err)
	}
	if _, meta, err := l.GetMeta(filepath.Join(t.TempDir(), "missing.jpg")); err == nil || meta != (Meta{}) {
		t.Errorf("missing: %v, %v", meta, err)
	}
}
