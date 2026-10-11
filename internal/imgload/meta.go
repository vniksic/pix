package imgload

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// Meta is the metadata pix shows and sorts by.
type Meta struct {
	// Taken is the EXIF DateTimeOriginal (falling back to DateTime) in
	// local time; zero when absent, invalid or all zeros.
	Taken time.Time
}

// metaPrefix bounds how much of a file ReadMeta looks at: segment and
// chunk walks stop beyond it, and TIFF/HEIC/AVIF only get this prefix.
const metaPrefix = 1 << 20

// exifTimeLayout is the EXIF date format ("YYYY:MM:DD HH:MM:SS").
const exifTimeLayout = "2006:01:02 15:04:05"

// ReadMeta reads only the EXIF block of path (JPEG APP1, TIFF header, WebP
// EXIF chunk, or an "Exif" item found in the first MiB of a HEIC/AVIF
// file) and never decodes pixels. Any error yields a zero Meta; it never
// panics.
func ReadMeta(path string) (m Meta) {
	defer func() {
		if recover() != nil {
			m = Meta{}
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return Meta{}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return Meta{}
	}
	return metaFrom(f, fi.Size())
}

// Taken returns the date path was taken (see ReadMeta) and whether it has
// one. It has the signature of scan.Options.Taken.
func Taken(path string) (time.Time, bool) {
	t := ReadMeta(path).Taken
	return t, !t.IsZero()
}

// metaFrom extracts Meta from an image of the given size. It recovers from
// panics in the EXIF parser.
func metaFrom(r io.ReaderAt, size int64) (m Meta) {
	defer func() {
		if recover() != nil {
			m = Meta{}
		}
	}()
	raw := locateExif(r, size)
	if raw == nil {
		return Meta{}
	}
	return Meta{Taken: exifTaken(raw)}
}

// locateExif returns the EXIF block of an image in a form exif.Decode
// accepts ("Exif\0\0" + TIFF, or a bare TIFF), or nil.
func locateExif(r io.ReaderAt, size int64) []byte {
	var head [16]byte
	n, _ := r.ReadAt(head[:], 0)
	h := head[:n]
	switch {
	case bytes.HasPrefix(h, []byte("\xff\xd8")):
		return jpegExif(r, size)
	case len(h) >= 12 && string(h[:4]) == "RIFF" && string(h[8:12]) == "WEBP":
		return webpExifAt(r, size)
	case bytes.HasPrefix(h, []byte("II*\x00")), bytes.HasPrefix(h, []byte("MM\x00*")):
		return readPrefix(r, size)
	case len(h) >= 8 && string(h[4:8]) == "ftyp":
		// HEIC/AVIF keep EXIF as an item in mdat, usually written as a
		// 4-byte offset, "Exif\0\0" and the TIFF header. Find it by its
		// signature in the prefix instead of parsing the item boxes.
		data := readPrefix(r, size)
		if isobmffKind(data) == "" {
			return nil
		}
		for _, sig := range []string{"Exif\x00\x00II*\x00", "Exif\x00\x00MM\x00*"} {
			if i := bytes.Index(data, []byte(sig)); i >= 0 {
				return data[i:]
			}
		}
	}
	return nil
}

// readPrefix reads at most metaPrefix bytes from the start of r.
func readPrefix(r io.ReaderAt, size int64) []byte {
	buf := make([]byte, min(size, metaPrefix))
	n, _ := r.ReadAt(buf, 0)
	return buf[:n]
}

// jpegExif walks the JPEG marker segments before the scan data (within
// metaPrefix) and returns the payload of the first APP1 "Exif" segment.
func jpegExif(r io.ReaderAt, size int64) []byte {
	pos := int64(2)
	var hdr [4]byte
	for pos+4 <= min(size, metaPrefix) {
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return nil
		}
		if hdr[0] != 0xff {
			return nil
		}
		marker := hdr[1]
		switch {
		case marker == 0xff: // fill byte
			pos++
			continue
		case marker == 0x01, marker >= 0xd0 && marker <= 0xd8: // no length
			pos += 2
			continue
		case marker == 0xda, marker == 0xd9: // start of scan, end of image
			return nil
		}
		length := int64(binary.BigEndian.Uint16(hdr[2:]))
		if length < 2 {
			return nil
		}
		if marker == 0xe1 && length >= 8 && pos+2+length <= size {
			buf := make([]byte, length-2)
			if _, err := r.ReadAt(buf, pos+4); err != nil {
				return nil
			}
			if bytes.HasPrefix(buf, []byte("Exif\x00\x00")) {
				return buf
			}
		}
		pos += 2 + length
	}
	return nil
}

// webpExifAt returns the payload of the EXIF chunk of a RIFF/WebP file of
// the given size, seeking over the other chunks (EXIF usually follows the
// image data). Chunks larger than metaPrefix are ignored.
func webpExifAt(r io.ReaderAt, size int64) []byte {
	var hdr [8]byte
	for pos := int64(12); pos+8 <= size; {
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return nil
		}
		n := int64(binary.LittleEndian.Uint32(hdr[4:]))
		if n > size-pos-8 {
			return nil
		}
		if string(hdr[:4]) == "EXIF" {
			if n > metaPrefix {
				return nil
			}
			buf := make([]byte, n)
			if _, err := r.ReadAt(buf, pos+8); err != nil {
				return nil
			}
			return buf
		}
		pos += 8 + n + n&1 // chunks are padded to even size
	}
	return nil
}

// exifTaken parses an EXIF block and returns DateTimeOriginal, else
// DateTime, in local time; zero when neither is a valid date.
func exifTaken(raw []byte) time.Time {
	x, err := exif.Decode(bytes.NewReader(raw))
	if x == nil || (err != nil && exif.IsCriticalError(err)) {
		return time.Time{}
	}
	for _, name := range []exif.FieldName{exif.DateTimeOriginal, exif.DateTime} {
		tag, err := x.Get(name)
		if err != nil {
			continue
		}
		s, err := tag.StringVal()
		if err != nil {
			continue
		}
		if t, ok := parseExifTime(s); ok {
			return t
		}
	}
	return time.Time{}
}

// parseExifTime parses "2006:01:02 15:04:05" (trailing NULs, spaces or
// sub-second digits ignored) in local time. All-zero, blank and otherwise
// invalid dates are rejected.
func parseExifTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.TrimRight(s, "\x00"))
	if len(s) > len(exifTimeLayout) {
		s = s[:len(exifTimeLayout)]
	}
	t, err := time.ParseInLocation(exifTimeLayout, s, time.Local)
	if err != nil || t.Year() == 0 {
		return time.Time{}, false
	}
	return t, true
}
