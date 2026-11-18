package camraw

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"slices"
	"testing"

	xtiff "golang.org/x/image/tiff"
)

// tb assembles a TIFF structure: data and IFDs are appended, offsets
// count from the start of buf (the header), and IFD0's offset is set last.
type tb struct {
	order order
	buf   []byte
}

type order interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// ent is an IFD entry: val is the inline value (SHORT, LONG) or the offset
// of the data.
type ent struct {
	tag, typ uint16
	count    uint32
	val      uint32
}

func newTB(o order, magic string) *tb {
	b := &tb{order: o}
	b.buf = append(b.buf, magic...)
	b.buf = append(b.buf, 0, 0, 0, 0) // IFD0 offset, set by first
	return b
}

// blob appends data (word aligned) and returns its offset.
func (b *tb) blob(data []byte) uint32 {
	if len(b.buf)%2 != 0 {
		b.buf = append(b.buf, 0)
	}
	off := uint32(len(b.buf))
	b.buf = append(b.buf, data...)
	return off
}

// ifd appends an IFD and returns its offset.
func (b *tb) ifd(next uint32, es ...ent) uint32 {
	var d []byte
	d = b.order.AppendUint16(d, uint16(len(es)))
	for _, e := range es {
		d = b.order.AppendUint16(d, e.tag)
		d = b.order.AppendUint16(d, e.typ)
		d = b.order.AppendUint32(d, e.count)
		if e.typ == 3 && e.count == 1 {
			d = b.order.AppendUint16(d, uint16(e.val))
			d = append(d, 0, 0)
		} else {
			d = b.order.AppendUint32(d, e.val)
		}
	}
	d = b.order.AppendUint32(d, next)
	return b.blob(d)
}

func (b *tb) first(off uint32) []byte {
	b.order.PutUint32(b.buf[4:], off)
	return b.buf
}

func short(tag uint16, v uint32) ent { return ent{tag, 3, 1, v} }
func long(tag uint16, v uint32) ent  { return ent{tag, 4, 1, v} }

// ascii stores s (NUL-terminated) out of line.
func (b *tb) ascii(tag uint16, s string) ent {
	return ent{tag, 2, uint32(len(s) + 1), b.blob(append([]byte(s), 0))}
}

// jpegPair is a JPEGInterchangeFormat/Length pair for data stored now.
func (b *tb) jpegPair(data []byte) []ent {
	return []ent{long(tagJPEGOffset, b.blob(data)), long(tagJPEGLength, uint32(len(data)))}
}

// stripPair stores data as a single strip.
func (b *tb) stripPair(data []byte) []ent {
	return []ent{long(tagStripOffsets, b.blob(data)), long(tagStripCounts, uint32(len(data)))}
}

func tinyJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	date = "2021:05:10 15:58:48"
	orig = "2020:01:02 03:04:05"
)

// regions returns the regions of the given data blobs inside file.
func regions(t *testing.T, file []byte, blobs ...[]byte) []Region {
	t.Helper()
	var rs []Region
	for _, b := range blobs {
		i := bytes.Index(file, b)
		if i < 0 {
			t.Fatal("blob not in file")
		}
		rs = append(rs, Region{int64(i), int64(len(b))})
	}
	return rs
}

func parse(file []byte) (Info, bool) { return Parse(bytes.NewReader(file), int64(len(file))) }

// exifIFD stores an Exif IFD holding DateTimeOriginal (and more entries).
func (b *tb) exifIFD(more ...ent) ent {
	es := append([]ent{b.ascii(tagDateOriginal, orig)}, more...)
	return long(tagExifIFD, b.ifd(0, es...))
}

// cr2 is shaped like a Canon CR2: IFD0 holds the big preview as a strip,
// IFD1 a thumbnail, IFD3 the sensor data as lossless JPEG (here only its
// first bytes, which pass the SOI check).
func cr2(t testing.TB, big, small, lossless []byte, orient uint32) []byte {
	b := newTB(binary.LittleEndian, "II*\x00")
	b.buf = append(b.buf, "CR\x02\x00\x00\x00\x00\x00"...)
	ifd3 := b.ifd(0, b.stripPair(lossless)...)
	ifd1 := b.ifd(ifd3, b.jpegPair(small)...)
	es := []ent{b.ascii(tagMake, "Canon"), short(tagOrientation, orient), b.ascii(tagDateTime, date), b.exifIFD()}
	return b.first(b.ifd(ifd1, append(es, b.stripPair(big)...)...))
}

func TestParseCR2(t *testing.T) {
	big, small := tinyJPEG(t, 64, 48), tinyJPEG(t, 16, 12)
	lossless := []byte("\xff\xd8\xff\xc4 lossless sensor data")
	file := cr2(t, big, small, lossless, 6)
	info, ok := parse(file)
	if !ok || info.Format != "CR2" || info.Orient != 6 || info.Taken != orig || info.Thumb {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, big, small, lossless); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

// An ARW keeps its preview in IFD0 and the sensor data in a SubIFD; strips
// of a CFA IFD are never previews, even if they start like a JPEG.
func TestParseARW(t *testing.T) {
	prev := tinyJPEG(t, 32, 24)
	b := newTB(binary.LittleEndian, "II*\x00")
	sensor := b.ifd(0, append([]ent{short(tagPhotometric, photoCFA)}, b.stripPair([]byte("\xff\xd8\xff\xc3 not a preview"))...)...)
	es := []ent{b.ascii(tagMake, "SONY"), long(tagSubIFDs, sensor)}
	file := b.first(b.ifd(0, append(es, b.jpegPair(prev)...)...))
	info, ok := parse(file)
	if !ok || info.Format != "ARW" || info.Orient != 1 || info.Taken != "" {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, prev); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

// A big-endian NEF: an RGB thumbnail in IFD0, SubIFDs with the full-size
// JPEG and the sensor data; IFD0's DateTime stands in for a missing
// DateTimeOriginal.
func TestParseNEF(t *testing.T) {
	prev := tinyJPEG(t, 32, 24)
	b := newTB(binary.BigEndian, "MM\x00*")
	sensor := b.ifd(0, short(tagPhotometric, photoCFA))
	jpg := b.ifd(0, b.jpegPair(prev)...)
	subs := b.blob(binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, jpg), sensor))
	es := []ent{
		b.ascii(tagMake, "NIKON CORPORATION"), short(tagOrientation, 8), b.ascii(tagDateTime, date),
		short(tagCompression, 1), short(tagPhotometric, photoRGB), {tagSubIFDs, 4, 2, subs},
	}
	file := b.first(b.ifd(0, append(es, b.stripPair(make([]byte, 48))...)...))
	info, ok := parse(file)
	if !ok || info.Format != "NEF" || info.Orient != 8 || info.Taken != date || !info.Thumb {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, prev); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

func TestParseDNGAndPlainTIFF(t *testing.T) {
	prev := tinyJPEG(t, 32, 24)
	b := newTB(binary.LittleEndian, "II*\x00")
	pv := b.ifd(0, append([]ent{short(tagCompression, 7), short(tagPhotometric, 6)}, b.stripPair(prev)...)...)
	file := b.first(b.ifd(0, ent{tagDNGVersion, 1, 4, 0x00000401}, b.ascii(tagMake, "Canon"), long(tagSubIFDs, pv), short(tagOrientation, 3)))
	info, ok := parse(file)
	if !ok || info.Format != "DNG" || info.Orient != 3 || len(info.JPEGs) != 1 {
		t.Errorf("DNG: got %+v %v", info, ok)
	}

	// A TIFF of a camera's make, with a SubIFD and an embedded JPEG, but
	// no sensor data, is just a TIFF.
	b = newTB(binary.LittleEndian, "II*\x00")
	sub := b.ifd(0, short(tagPhotometric, photoRGB))
	es := append([]ent{b.ascii(tagMake, "NIKON"), short(tagPhotometric, photoRGB), long(tagSubIFDs, sub)}, b.jpegPair(prev)...)
	if info, ok := parse(b.first(b.ifd(0, es...))); ok {
		t.Errorf("TIFF with a JPEG: raw %+v", info)
	}
	var buf bytes.Buffer
	if err := xtiff.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if info, ok := parse(buf.Bytes()); ok {
		t.Errorf("x/image/tiff output: raw %+v", info)
	}
}

// olympusNote is an "OLYMPUS\0" maker note: header, IFD0 (one entry,
// 18 bytes) pointing at CameraSettings (two entries, 30 bytes) pointing
// at prev. Its offsets count from the note's start.
func olympusNote(prev []byte) []byte {
	n := &tb{order: binary.LittleEndian, buf: []byte("OLYMPUS\x00II\x03\x00")}
	const cs, at = 12 + 18, 12 + 18 + 30
	n.ifd(0, ent{tagOlyCamera, 13, 1, cs})
	n.ifd(0, long(tagOlyPreviewOff, at), long(tagOlyPreviewSize, uint32(len(prev))))
	if n.blob(prev) != at {
		panic("olympusNote layout")
	}
	return n.buf
}

func TestParseORF(t *testing.T) {
	prev := tinyJPEG(t, 40, 30)
	b := newTB(binary.LittleEndian, "IIRO")
	note := olympusNote(prev)
	es := []ent{short(tagOrientation, 8), b.exifIFD(ent{tagMakerNote, 7, uint32(len(note)), b.blob(note)})}
	file := b.first(b.ifd(0, es...))
	info, ok := parse(file)
	if !ok || info.Format != "ORF" || info.Orient != 8 || info.Taken != orig {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, prev); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

func TestParseRW2(t *testing.T) {
	prev := tinyJPEG(t, 40, 30)
	b := newTB(binary.LittleEndian, "IIU\x00")
	file := b.first(b.ifd(0, short(tagOrientation, 3), ent{tagPreviewRW2, 7, uint32(len(prev)), b.blob(prev)}))
	info, ok := parse(file)
	if !ok || info.Format != "RW2" || info.Orient != 3 {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, prev); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

// exifJPEG is jpg with an Exif APP1 segment (IFD0 orientation, Exif IFD
// with DateTimeOriginal) after its SOI.
func exifJPEG(jpg []byte, orient uint32) []byte {
	b := newTB(binary.BigEndian, "MM\x00*")
	x := b.first(b.ifd(0, short(tagOrientation, orient), b.exifIFD()))
	seg := []byte{0xff, 0xe1, 0, 0}
	seg = append(seg, "Exif\x00\x00"...)
	seg = append(seg, x...)
	binary.BigEndian.PutUint16(seg[2:], uint16(len(seg)-2))
	return slices.Concat(jpg[:2], seg, jpg[2:])
}

func raf(prev []byte) []byte {
	file := make([]byte, 100)
	copy(file, "FUJIFILMCCD-RAW 0201FF159501X-T3")
	binary.BigEndian.PutUint32(file[84:], uint32(len(file)))
	binary.BigEndian.PutUint32(file[88:], uint32(len(prev)))
	return append(file, prev...)
}

func TestParseRAF(t *testing.T) {
	prev := exifJPEG(tinyJPEG(t, 40, 30), 6)
	file := raf(prev)
	info, ok := parse(file)
	if !ok || info.Format != "RAF" || info.Orient != 6 || info.Taken != orig {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, prev); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

// mkbox is an ISOBMFF box.
func mkbox(typ string, payload ...[]byte) []byte {
	p := slices.Concat(payload...)
	return slices.Concat(binary.BigEndian.AppendUint32(nil, uint32(8+len(p))), []byte(typ), p)
}

// cr3 is shaped like a Canon CR3: ftyp, mdat with the full-size JPEG
// (track 1's sample), moov with the metadata and the track, and the uuid
// box with PRVW.
func cr3(full, small []byte, orient uint32) []byte {
	ftyp := mkbox("ftyp", []byte("crx \x00\x00\x00\x01crx isom"))
	mdat := mkbox("mdat", full)
	fullAt := uint64(len(ftyp) + 8)

	t1 := newTB(binary.LittleEndian, "II*\x00")
	cmt1 := t1.first(t1.ifd(0, t1.ascii(tagMake, "Canon"), short(tagOrientation, orient), t1.ascii(tagDateTime, date)))
	t2 := newTB(binary.LittleEndian, "II*\x00")
	cmt2 := t2.first(t2.ifd(0, t2.ascii(tagDateOriginal, orig)))
	meta := mkbox("uuid", []byte(cr3Meta), mkbox("CMT1", cmt1), mkbox("CMT2", cmt2))

	be := binary.BigEndian
	stsz := mkbox("stsz", be.AppendUint32(be.AppendUint32(be.AppendUint32(nil, 0), uint32(len(full))), 1))
	co64 := mkbox("co64", be.AppendUint64(be.AppendUint32(be.AppendUint32(nil, 0), 1), fullAt))
	trak := mkbox("trak", mkbox("mdia", mkbox("minf", mkbox("stbl", mkbox("stsd", make([]byte, 8)), stsz, co64))))
	// A second track whose sample is not a JPEG.
	co64b := mkbox("co64", be.AppendUint64(be.AppendUint32(be.AppendUint32(nil, 0), 1), 0))
	trak2 := mkbox("trak", mkbox("mdia", mkbox("minf", mkbox("stbl", stsz, co64b))))
	moov := mkbox("moov", meta, trak, trak2)

	prvw := slices.Concat(be.AppendUint32(nil, uint32(24+len(small))), []byte("PRVW"),
		[]byte{0, 0, 0, 0, 0, 1, 0, 40, 0, 30, 0, 1}, be.AppendUint32(nil, uint32(len(small))), small)
	prev := mkbox("uuid", []byte(cr3Preview), make([]byte, 8), prvw)
	return slices.Concat(ftyp, mdat, moov, prev)
}

func TestParseCR3(t *testing.T) {
	full, small := tinyJPEG(t, 64, 48), tinyJPEG(t, 40, 30)
	file := cr3(full, small, 8)
	info, ok := parse(file)
	if !ok || info.Format != "CR3" || info.Orient != 8 || info.Taken != orig {
		t.Errorf("got %+v %v", info, ok)
	}
	if want := regions(t, file, full, small); !slices.Equal(info.JPEGs, want) {
		t.Errorf("JPEGs %v, want %v", info.JPEGs, want)
	}
}

func TestParseNotRaw(t *testing.T) {
	for _, data := range []string{"", "II*", "hello, world, this is text", "\xff\xd8\xff\xe0 a jpeg header..",
		"\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"} {
		if info, ok := parse([]byte(data)); ok {
			t.Errorf("%q: raw %+v", data, info)
		}
	}
}

// fixtures returns one synthetic file of each kind.
func fixtures(t testing.TB) map[string][]byte {
	big, small := tinyJPEG(t, 64, 48), tinyJPEG(t, 16, 12)
	b := newTB(binary.LittleEndian, "IIRO")
	note := olympusNote(small)
	orf := b.first(b.ifd(0, short(tagOrientation, 8), b.exifIFD(ent{tagMakerNote, 7, uint32(len(note)), b.blob(note)})))
	return map[string][]byte{
		"cr2": cr2(t, big, small, []byte("\xff\xd8\xff\xc4...."), 6),
		"cr3": cr3(big, small, 6),
		"raf": raf(exifJPEG(small, 6)),
		"orf": orf,
	}
}

// checkRegions fails if a region lies outside a file of the given size.
func checkRegions(t *testing.T, name string, info Info, ok bool, size int) {
	t.Helper()
	for _, r := range info.JPEGs {
		if r.Off < 0 || r.Len <= 0 || r.Off+r.Len > int64(size) {
			t.Fatalf("%s: region %+v outside a file of %d bytes", name, r, size)
		}
	}
	if ok && (info.Orient < 1 || info.Orient > 8) {
		t.Fatalf("%s: orientation %d", name, info.Orient)
	}
}

// Cut short anywhere, or with any byte replaced, a file never makes Parse
// panic or report a preview outside the file.
func TestParseDamaged(t *testing.T) {
	for name, file := range fixtures(t) {
		for n := range len(file) {
			info, ok := parse(file[:n])
			checkRegions(t, name, info, ok, n)
		}
		for i := range file {
			for _, v := range []byte{0x00, 0xff, 0x7f} {
				bad := slices.Clone(file)
				bad[i] = v
				info, ok := parse(bad)
				checkRegions(t, name, info, ok, len(bad))
			}
		}
	}
}

// Loops and absurd sizes in the structure end quickly.
func TestParseHostile(t *testing.T) {
	// An IFD that is its own next IFD and its own SubIFD.
	b := newTB(binary.LittleEndian, "II*\x00")
	b.buf = append(b.buf, "CR\x02\x00\x00\x00\x00\x00"...)
	self := uint32(len(b.buf))
	file := b.first(b.ifd(self, long(tagSubIFDs, self), long(tagExifIFD, self)))
	if info, ok := parse(file); !ok || info.Format != "CR2" || len(info.JPEGs) != 0 {
		t.Errorf("self-referencing IFD: %+v %v", info, ok)
	}

	// A tag claiming 4G values, and a preview longer than the file.
	b = newTB(binary.LittleEndian, "II*\x00")
	b.buf = append(b.buf, "CR\x02\x00\x00\x00\x00\x00"...)
	jpg := b.blob([]byte("\xff\xd8\xff\xdb"))
	file = b.first(b.ifd(0, ent{tagMake, 2, 0xffffffff, 8}, long(tagJPEGOffset, jpg), long(tagJPEGLength, 0x7fffffff)))
	if info, ok := parse(file); !ok || len(info.JPEGs) != 0 {
		t.Errorf("huge counts: %+v %v", info, ok)
	}

	// CR3 boxes with a 64-bit size beyond the file, and a zero size.
	ftyp := mkbox("ftyp", []byte("crx \x00\x00\x00\x01crx isom"))
	huge := slices.Concat([]byte{0, 0, 0, 1}, []byte("moov"), []byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	if info, ok := parse(append(slices.Clone(ftyp), huge...)); !ok || len(info.JPEGs) != 0 {
		t.Errorf("huge box: %+v %v", info, ok)
	}
	zero := slices.Concat([]byte{0, 0, 0, 0}, []byte("moov"), mkbox("trak"))
	if info, ok := parse(append(slices.Clone(ftyp), zero...)); !ok || len(info.JPEGs) != 0 {
		t.Errorf("zero-size box: %+v %v", info, ok)
	}
}

// Whatever the bytes, Parse returns previews inside the file and a valid
// orientation: go test -fuzz=FuzzParse ./internal/camraw
func FuzzParse(f *testing.F) {
	for _, file := range fixtures(f) {
		f.Add(file)
	}
	f.Fuzz(func(t *testing.T, file []byte) {
		info, ok := parse(file)
		checkRegions(t, "fuzz", info, ok, len(file))
	})
}
