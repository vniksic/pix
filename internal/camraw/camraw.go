// Package camraw finds what pix shows of a camera raw file: the JPEG
// previews the camera embedded in it, the orientation they are shown
// with, and the date taken. Sensor data is never decoded. Only container
// structure is read (TIFF IFDs and SubIFDs, the Olympus maker note, Canon
// CR3 boxes, the Fujifilm RAF header), and every offset and length taken
// from the file is bounds-checked, so a corrupt file costs a few small
// reads and gives no or fewer previews, never a panic or a big allocation.
package camraw

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
)

// Info describes a camera raw file.
type Info struct {
	// Format is "CR2", "CR3", "NEF", "ARW", "DNG", "PEF", "ORF", "RW2",
	// "RAF", or "raw" for another TIFF-based raw file.
	Format string
	// Orient is the EXIF orientation (1..8) the previews are shown with:
	// the raw file's own (IFD0, or CR3's CMT1, or for RAF the Exif of its
	// JPEG), 1 when absent or invalid. The previews' own Exif, which some
	// carry, is not looked at.
	Orient int
	// Taken is the EXIF DateTimeOriginal, else DateTime, as stored
	// ("2006:01:02 15:04:05"); "" when there is neither.
	Taken string
	// JPEGs are the embedded JPEGs in the order found. Each lies within
	// the file and starts with a JPEG SOI marker; whether it decodes (raw
	// sensor data can be lossless JPEG) is for the caller to find out.
	JPEGs []Region
	// Thumb says IFD0 is an uncompressed RGB image that a TIFF decoder
	// reads as the file's first image (the thumbnail of a DNG without a
	// JPEG preview).
	Thumb bool
}

// Region is a byte range of the file.
type Region struct{ Off, Len int64 }

// Bounds on what a damaged or hostile file can make Parse do.
const (
	maxIFDs    = 64      // IFDs visited per file, all kinds together
	maxEntries = 1000    // entries in one IFD
	maxSubIFDs = 8       // SubIFD offsets followed per IFD
	maxDepth   = 2       // SubIFDs of SubIFDs, no deeper
	maxRead    = 1 << 16 // bytes read for one IFD or tag value
	maxBoxes   = 256     // boxes listed in one CR3 box
	maxJPEGs   = 16      // previews collected
)

// TIFF tags Parse reads.
const (
	tagPreviewRW2   = 0x002e // Panasonic JpgFromRaw (UNDEFINED, a whole JPEG)
	tagCompression  = 0x0103
	tagPhotometric  = 0x0106
	tagMake         = 0x010f
	tagStripOffsets = 0x0111
	tagOrientation  = 0x0112
	tagStripCounts  = 0x0117
	tagDateTime     = 0x0132
	tagSubIFDs      = 0x014a
	tagJPEGOffset   = 0x0201 // JPEGInterchangeFormat
	tagJPEGLength   = 0x0202
	tagExifIFD      = 0x8769
	tagDateOriginal = 0x9003
	tagMakerNote    = 0x927c
	tagDNGVersion   = 0xc612

	tagOlyCamera      = 0x2020 // Olympus CameraSettings IFD
	tagOlyPreviewOff  = 0x0101
	tagOlyPreviewSize = 0x0102

	photoRGB    = 2
	photoCFA    = 32803 // colour filter array: sensor data
	photoLinear = 34892 // DNG LinearRaw: demosaiced but not rendered
)

// Parse reports whether the file r of the given size is a camera raw
// file, and describes it. Raw files are told apart by content, never by
// name: ORF, RW2, RAF and CR3 by their signatures, CR2 by the "CR" after
// its TIFF header, DNG by its DNGVersion tag, and the other TIFF-based
// raws (NEF, ARW, PEF, ...) by an IFD holding sensor data (a CFA or
// LinearRaw photometric interpretation). A plain TIFF is not a raw file.
func Parse(r io.ReaderAt, size int64) (Info, bool) {
	var h [16]byte
	if n, _ := r.ReadAt(h[:], 0); n < len(h) {
		return Info{}, false
	}
	switch {
	case string(h[:]) == "FUJIFILMCCD-RAW ":
		return parseRAF(r, size), true
	case string(h[4:12]) == "ftypcrx ":
		return parseCR3(r, size), true
	}
	w := newWalker(r, size)
	switch string(h[:4]) {
	case "II*\x00", "MM\x00*":
	case "IIRO", "IIRS", "MMOR":
		w.info.Format = "ORF"
	case "IIU\x00":
		w.info.Format = "RW2"
	default:
		return Info{}, false
	}
	t, ifd0, ok := newTIFF(r, 0, size)
	if !ok {
		return Info{}, false
	}
	w.walk(t, ifd0, 0)
	switch m := strings.ToUpper(w.make); {
	case w.info.Format != "":
	case string(h[8:11]) == "CR\x02":
		w.info.Format = "CR2"
	case w.dng:
		w.info.Format = "DNG"
	case !w.sensor:
		return Info{}, false
	case strings.HasPrefix(m, "NIKON"):
		w.info.Format = "NEF"
	case strings.HasPrefix(m, "SONY"):
		w.info.Format = "ARW"
	case strings.HasPrefix(m, "PENTAX"), strings.HasPrefix(m, "RICOH"):
		w.info.Format = "PEF"
	default:
		w.info.Format = "raw"
	}
	return w.done(), true
}

// tiff reads one TIFF structure: offsets in it count from base (where its
// "II"/"MM" header is), and nothing it points at may extend past end.
type tiff struct {
	r         io.ReaderAt
	base, end int64
	order     binary.ByteOrder
}

// newTIFF reads the TIFF header at base and returns the structure and the
// offset of its first IFD. The version word is not checked (ORF and RW2
// have their own).
func newTIFF(r io.ReaderAt, base, end int64) (*tiff, int64, bool) {
	t := &tiff{r: r, base: base, end: end}
	h := t.read(0, 8)
	if h == nil {
		return nil, 0, false
	}
	switch string(h[:2]) {
	case "II":
		t.order = binary.LittleEndian
	case "MM":
		t.order = binary.BigEndian
	default:
		return nil, 0, false
	}
	return t, int64(t.order.Uint32(h[4:])), true
}

// read returns the n bytes at off, or nil when they are not all there.
func (t *tiff) read(off, n int64) []byte {
	if off < 0 || n < 0 || n > maxRead || off > t.end-t.base-n {
		return nil
	}
	b := make([]byte, n)
	if _, err := t.r.ReadAt(b, t.base+off); err != nil {
		return nil
	}
	return b
}

// entry is one IFD entry; field is its 4-byte value-or-offset.
type entry struct {
	tag, typ uint16
	count    uint32
	field    []byte
}

// typeSize is the size of one value of each TIFF type (0: unknown type).
var typeSize = [...]int64{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4}

// ifd returns the entries of the IFD at off and the next IFD's offset.
func (t *tiff) ifd(off int64) ([]entry, int64, bool) {
	b := t.read(off, 2)
	if b == nil {
		return nil, 0, false
	}
	n := int64(t.order.Uint16(b))
	if n == 0 || n > maxEntries {
		return nil, 0, false
	}
	b = t.read(off+2, n*12+4)
	if b == nil {
		return nil, 0, false
	}
	es := make([]entry, n)
	for i := range es {
		p := b[i*12:]
		es[i] = entry{tag: t.order.Uint16(p), typ: t.order.Uint16(p[2:]), count: t.order.Uint32(p[4:]), field: p[8:12]}
	}
	return es, int64(t.order.Uint32(b[n*12:])), true
}

// span returns the offset (from base) and length of an entry's value.
func (t *tiff) span(e entry) (off, n int64, ok bool) {
	if int(e.typ) >= len(typeSize) || typeSize[e.typ] == 0 {
		return 0, 0, false
	}
	n = typeSize[e.typ] * int64(e.count)
	if n <= 4 {
		return -1, n, true // inline in field
	}
	return int64(t.order.Uint32(e.field)), n, true
}

// value returns an entry's value bytes, or nil.
func (t *tiff) value(e entry) []byte {
	off, n, ok := t.span(e)
	switch {
	case !ok:
		return nil
	case off < 0:
		return e.field[:n]
	}
	return t.read(off, n)
}

// uints returns the values of a BYTE, SHORT, LONG or IFD entry.
func (t *tiff) uints(e entry) []int64 {
	b := t.value(e)
	var v []int64
	switch e.typ {
	case 1:
		for _, c := range b {
			v = append(v, int64(c))
		}
	case 3:
		for i := 0; i+2 <= len(b); i += 2 {
			v = append(v, int64(t.order.Uint16(b[i:])))
		}
	case 4, 13:
		for i := 0; i+4 <= len(b); i += 4 {
			v = append(v, int64(t.order.Uint32(b[i:])))
		}
	}
	return v
}

// uint returns the first value of an integer entry, or -1.
func (t *tiff) uint(e entry) int64 {
	if v := t.uints(e); len(v) > 0 {
		return v[0]
	}
	return -1
}

// str returns an ASCII entry's value up to its first NUL.
func (t *tiff) str(e entry) string {
	if e.typ != 2 {
		return ""
	}
	b := t.value(e)
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// walker collects an Info from the IFDs of one file.
type walker struct {
	r      io.ReaderAt
	size   int64
	info   Info
	make   string
	date   string // IFD0 DateTime
	orig   string // Exif DateTimeOriginal
	sensor bool   // an IFD holds sensor data
	dng    bool
	visits int
	seen   map[int64]bool // absolute IFD offsets visited
}

func newWalker(r io.ReaderAt, size int64) *walker {
	return &walker{r: r, size: size, info: Info{Orient: 1}, seen: make(map[int64]bool)}
}

// done returns the Info collected.
func (w *walker) done() Info {
	w.info.Taken = w.orig
	if w.info.Taken == "" {
		w.info.Taken = w.date
	}
	return w.info
}

// visit reads the IFD at off unless it was read before or the IFD budget
// is spent.
func (w *walker) visit(t *tiff, off int64) ([]entry, int64, bool) {
	if off <= 0 || w.seen[t.base+off] || w.visits >= maxIFDs {
		return nil, 0, false
	}
	w.seen[t.base+off] = true
	w.visits++
	return t.ifd(off)
}

// walk reads the IFD at off, the ones chained after it and their SubIFDs.
// At depth 0 the first one is IFD0.
func (w *walker) walk(t *tiff, off int64, depth int) {
	for first := true; ; first = false {
		es, next, ok := w.visit(t, off)
		if !ok {
			return
		}
		w.dir(t, es, depth == 0 && first, depth)
		off = next
	}
}

// dir takes what pix needs from one IFD.
func (w *walker) dir(t *tiff, es []entry, ifd0 bool, depth int) {
	get := func(tag uint16) (entry, bool) {
		for _, e := range es {
			if e.tag == tag {
				return e, true
			}
		}
		return entry{}, false
	}
	photo := int64(-1)
	if e, ok := get(tagPhotometric); ok {
		photo = t.uint(e)
	}
	sensor := photo == photoCFA || photo == photoLinear
	w.sensor = w.sensor || sensor
	if ifd0 {
		if e, ok := get(tagOrientation); ok {
			if o := t.uint(e); o >= 1 && o <= 8 {
				w.info.Orient = int(o)
			}
		}
		if e, ok := get(tagDateTime); ok {
			w.date = t.str(e)
		}
		if e, ok := get(tagMake); ok {
			w.make = t.str(e)
		}
		if _, ok := get(tagDNGVersion); ok {
			w.dng = true
		}
		if e, ok := get(tagCompression); ok && photo == photoRGB && t.uint(e) == 1 {
			w.info.Thumb = true
		}
	}
	if e, ok := get(tagExifIFD); ok {
		w.exif(t, t.uint(e))
	}
	if e, ok := get(tagSubIFDs); ok && depth < maxDepth {
		subs := t.uints(e)
		for _, off := range subs[:min(len(subs), maxSubIFDs)] {
			w.walk(t, off, depth+1)
		}
	}
	if sensor {
		return // its strips are sensor data, never a preview
	}
	pair := func(offTag, lenTag uint16) {
		o, ok1 := get(offTag)
		n, ok2 := get(lenTag)
		if ok1 && ok2 && o.count == 1 && n.count == 1 {
			if off := t.uint(o); off >= 0 {
				w.jpeg(t.base+off, t.uint(n))
			}
		}
	}
	pair(tagJPEGOffset, tagJPEGLength)
	pair(tagStripOffsets, tagStripCounts) // a single strip: CR2's IFD0, DNG previews
	if e, ok := get(tagPreviewRW2); ok && e.typ == 7 && w.info.Format == "RW2" {
		if off, n, ok := t.span(e); ok && off >= 0 {
			w.jpeg(t.base+off, n)
		}
	}
}

// exif reads the Exif IFD at off: the date taken, and for ORF the
// maker note's preview.
func (w *walker) exif(t *tiff, off int64) {
	es, _, ok := w.visit(t, off)
	if !ok {
		return
	}
	for _, e := range es {
		switch e.tag {
		case tagDateOriginal:
			w.orig = t.str(e)
		case tagMakerNote:
			if off, n, ok := t.span(e); ok && off >= 0 && w.info.Format == "ORF" {
				w.olympus(t.base+off, n)
			}
		}
	}
}

// olympus finds the preview in an Olympus maker note at mn (absolute, n
// bytes): its CameraSettings IFD has PreviewImageStart and
// PreviewImageLength. In the "OLYMPUS\0" and "OM SYSTEM" maker notes all
// offsets count from the start of the note. (The old "OLYMP\0" notes of
// the first ORF cameras are not read.)
func (w *walker) olympus(mn, n int64) {
	var h [16]byte
	if n < int64(len(h)) {
		return
	}
	if _, err := w.r.ReadAt(h[:], mn); err != nil {
		return
	}
	var hdr int64
	switch {
	case string(h[:8]) == "OLYMPUS\x00":
		hdr = 8
	case string(h[:12]) == "OM SYSTEM\x00\x00\x00":
		hdr = 12
	default:
		return
	}
	t := &tiff{r: w.r, base: mn, end: w.size}
	switch string(h[hdr : hdr+2]) {
	case "II":
		t.order = binary.LittleEndian
	case "MM":
		t.order = binary.BigEndian
	default:
		return
	}
	es, _, ok := w.visit(t, hdr+4)
	if !ok {
		return
	}
	for _, e := range es {
		if e.tag != tagOlyCamera {
			continue
		}
		// An IFD/LONG pointer to the sub-IFD, or the sub-IFD stored as
		// UNDEFINED data: either way the field holds its offset.
		cs, _, ok := w.visit(t, int64(t.order.Uint32(e.field)))
		if !ok {
			return
		}
		start, length := int64(-1), int64(-1)
		for _, c := range cs {
			switch c.tag {
			case tagOlyPreviewOff:
				start = t.uint(c)
			case tagOlyPreviewSize:
				length = t.uint(c)
			}
		}
		if start > 0 {
			w.jpeg(mn+start, length)
		}
		return
	}
}

// jpeg records the n bytes at off (absolute) as a preview if they lie in
// the file and start with a JPEG SOI marker.
func (w *walker) jpeg(off, n int64) {
	if r, ok := jpegAt(w.r, w.size, off, n); ok && len(w.info.JPEGs) < maxJPEGs {
		w.info.JPEGs = append(w.info.JPEGs, r)
	}
}

// jpegAt checks that n bytes at off lie within the file and start with
// a JPEG SOI marker.
func jpegAt(r io.ReaderAt, size, off, n int64) (Region, bool) {
	if off < 0 || n < 4 || off > size-n {
		return Region{}, false
	}
	var soi [3]byte
	if _, err := r.ReadAt(soi[:], off); err != nil || string(soi[:]) != "\xff\xd8\xff" {
		return Region{}, false
	}
	return Region{off, n}, true
}

// parseRAF reads a Fujifilm RAF: the header gives the JPEG preview's
// offset and length (big-endian at 84 and 88), and that JPEG's Exif is
// the file's metadata.
func parseRAF(r io.ReaderAt, size int64) Info {
	w := newWalker(r, size)
	w.info.Format = "RAF"
	var h [8]byte
	if _, err := r.ReadAt(h[:], 84); err != nil {
		return w.done()
	}
	w.jpeg(int64(binary.BigEndian.Uint32(h[:4])), int64(binary.BigEndian.Uint32(h[4:])))
	if len(w.info.JPEGs) == 0 {
		return w.done()
	}
	j := w.info.JPEGs[0]
	// The segments before the image data; the Exif APP1 comes first.
	for pos, i := j.Off+2, 0; pos+4 <= j.Off+j.Len && i < 32; i++ {
		var seg [10]byte
		if _, err := r.ReadAt(seg[:], pos); err != nil || seg[0] != 0xff || seg[1] == 0xda {
			break
		}
		end := pos + 2 + int64(binary.BigEndian.Uint16(seg[2:]))
		if seg[1] == 0xe1 && string(seg[4:10]) == "Exif\x00\x00" {
			if t, ifd0, ok := newTIFF(r, pos+10, min(end, size)); ok {
				w.walk(t, ifd0, 0)
			}
			break
		}
		pos = end
	}
	return w.done()
}

// UUIDs of the CR3 boxes holding the metadata and the small preview.
const (
	cr3Meta    = "\x85\xc0\xb6\x87\x82\x0f\x11\xe0\x81\x11\xf4\xce\x46\x2b\x6a\x48"
	cr3Preview = "\xea\xf4\x2b\x5e\x1c\x98\x4b\x88\xb9\xfb\xb7\xdc\x40\x6e\x4d\x16"
)

// box is an ISOBMFF box: its type and where its payload lies.
type box struct {
	typ      string
	off, end int64
}

// boxes lists the boxes laid out in [off, end).
func boxes(r io.ReaderAt, off, end int64) []box {
	var out []box
	for off <= end-8 && len(out) < maxBoxes {
		var h [16]byte
		if _, err := r.ReadAt(h[:8], off); err != nil {
			return out
		}
		size, hdr := int64(binary.BigEndian.Uint32(h[:4])), int64(8)
		switch size {
		case 0: // to the end
			size = end - off
		case 1: // 64-bit size follows
			if _, err := r.ReadAt(h[8:], off+8); err != nil {
				return out
			}
			size, hdr = int64(binary.BigEndian.Uint64(h[8:])), 16
		}
		if size < hdr || size > end-off {
			return out
		}
		out = append(out, box{string(h[4:8]), off + hdr, off + size})
		off += size
	}
	return out
}

// child returns the first box of type typ directly inside b.
func child(r io.ReaderAt, b box, typ string) (box, bool) {
	for _, c := range boxes(r, b.off, b.end) {
		if c.typ == typ {
			return c, true
		}
	}
	return box{}, false
}

// isUUID reports whether b is a uuid box with the given UUID.
func isUUID(r io.ReaderAt, b box, uuid string) bool {
	var u [16]byte
	if b.typ != "uuid" || b.end-b.off < 16 {
		return false
	}
	_, err := r.ReadAt(u[:], b.off)
	return err == nil && string(u[:]) == uuid
}

// parseCR3 reads a Canon CR3 (ISOBMFF, brand "crx "). moov holds the
// Canon uuid box, whose CMT1 is a TIFF with IFD0 and CMT2 one with the
// Exif IFD, and the tracks: the first one's single sample is a full-size
// JPEG. A top-level uuid box holds PRVW, a 1620×1080 JPEG after a 24-byte
// header.
func parseCR3(r io.ReaderAt, size int64) Info {
	w := newWalker(r, size)
	w.info.Format = "CR3"
	for _, b := range boxes(r, 0, size) {
		switch {
		case b.typ == "moov":
			for _, c := range boxes(r, b.off, b.end) {
				switch {
				case isUUID(r, c, cr3Meta):
					for _, d := range boxes(r, c.off+16, c.end) {
						t, ifd, ok := newTIFF(r, d.off, d.end)
						switch {
						case !ok:
						case d.typ == "CMT1":
							w.walk(t, ifd, 0)
						case d.typ == "CMT2":
							w.exif(t, ifd)
						}
					}
				case c.typ == "trak":
					w.trackSample(c)
				}
			}
		case isUUID(r, b, cr3Preview):
			var h [24]byte
			at := b.off + 16 + 8 // uuid, then 8 bytes before the PRVW box
			if _, err := r.ReadAt(h[:], at); err == nil && string(h[4:8]) == "PRVW" {
				w.jpeg(at+24, int64(binary.BigEndian.Uint32(h[20:])))
			}
		}
	}
	return w.done()
}

// trackSample records the first sample of a CR3 track if it is a JPEG:
// its size from stsz, its offset from co64 (or stco).
func (w *walker) trackSample(trak box) {
	b := trak
	for _, typ := range []string{"mdia", "minf", "stbl"} {
		var ok bool
		if b, ok = child(w.r, b, typ); !ok {
			return
		}
	}
	off, n := int64(-1), int64(-1)
	for _, c := range boxes(w.r, b.off, b.end) {
		var h [16]byte
		k := min(c.end-c.off, int64(len(h)))
		if k < 12 {
			continue
		}
		if _, err := w.r.ReadAt(h[:k], c.off); err != nil {
			continue
		}
		switch c.typ {
		case "stsz": // version+flags, sample size (0: per sample), count, [sizes]
			n = int64(binary.BigEndian.Uint32(h[4:]))
			if n == 0 && k >= 16 {
				n = int64(binary.BigEndian.Uint32(h[12:]))
			}
		case "co64": // version+flags, count, offsets
			if k >= 16 {
				off = int64(binary.BigEndian.Uint64(h[8:]))
			}
		case "stco":
			off = int64(binary.BigEndian.Uint32(h[8:]))
		}
	}
	w.jpeg(off, n)
}
