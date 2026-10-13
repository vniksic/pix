package fileops

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Storing a view rotation in a copy. The compressed image data is never
// touched: only a JPEG's EXIF Orientation tag is set (or added), so the
// copy opens the way it was turned in pix. Other formats are copied as
// they are.

// cw[o] is the EXIF orientation that displays like orientation o followed
// by one clockwise quarter turn. The rotations cycle 1→6→3→8→1 and the
// mirrored ones 2→7→4→5→2 (a mirror then a turn is still a mirror).
var cw = [9]int{0, 6, 7, 8, 5, 2, 3, 4, 1}

// RotateOrientation returns the EXIF orientation (1..8) that displays like
// orientation o followed by q clockwise quarter turns. An o outside 1..8
// counts as 1, as it does for the decoder.
func RotateOrientation(o, q int) int {
	if o < 1 || o > 8 {
		o = 1
	}
	for range ((q % 4) + 4) % 4 {
		o = cw[o]
	}
	return o
}

// jpegHeadMax bounds how much of a JPEG is searched for its EXIF segment:
// the segments before the image data are small, and an Exif APP1 cannot
// exceed 64 KiB.
const jpegHeadMax = 1 << 20

const (
	tagOrientation = 0x0112
	typeShort      = 3
	entrySize      = 12     // bytes in an IFD entry
	maxSegment     = 0xffff // largest JPEG segment length (it counts its own 2 bytes)
)

// byteOrder is a TIFF block's byte order (binary.LittleEndian or BigEndian).
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

var (
	errNotJPEG  = errors.New("not a JPEG")
	errBadJPEG  = errors.New("jpeg: unexpected header")
	errBadExif  = errors.New("jpeg: unexpected EXIF")
	errExifFull = errors.New("jpeg: no room in EXIF")
	exifSig     = []byte("Exif\x00\x00")
)

// orientJPEG works out a copy of the JPEG whose first bytes are head (the
// whole segment header must be in it) that displays turned by q clockwise
// quarter turns: the copy is out followed by the source's bytes from cut
// on. cut is never past the header, so the image data is always copied
// verbatim.
//
//   - Orientation tag in IFD0: its value is patched in place.
//   - No Exif APP1 segment: a minimal one (IFD0 with only Orientation) is
//     inserted after SOI, or after leading APP0 segments (JFIF wants to be
//     first, a JFXX extension right after it).
//   - Exif without Orientation: a new IFD0 (the old entries verbatim plus
//     Orientation) is appended to the TIFF block and the header pointed at
//     it. Shifting bytes inside the block would break every offset in it;
//     appending moves nothing, so the old entries' offsets stay valid.
//
// Anything unexpected is an error and the caller copies unchanged.
func orientJPEG(head []byte, q int) (out []byte, cut int, err error) {
	if len(head) < 4 || head[0] != 0xff || head[1] != 0xd8 || head[2] != 0xff {
		return nil, 0, errNotJPEG
	}
	pos, insertAt := 2, 2
	for {
		if pos+2 > len(head) || head[pos] != 0xff {
			return nil, 0, errBadJPEG
		}
		marker := head[pos+1]
		switch {
		case marker == 0xff: // fill byte
			pos++
			continue
		case marker == 0xda: // start of scan: no Exif segment before the image data
			return insertExif(head, insertAt, q)
		case marker == 0x00, marker == 0x01, marker >= 0xd0 && marker <= 0xd9:
			// No standalone markers belong in the header.
			return nil, 0, errBadJPEG
		}
		if pos+4 > len(head) {
			return nil, 0, errBadJPEG
		}
		end := pos + 2 + int(binary.BigEndian.Uint16(head[pos+2:]))
		if end < pos+4 || end > len(head) {
			return nil, 0, errBadJPEG
		}
		payload := head[pos+4 : end]
		switch {
		case marker == 0xe0 && pos == insertAt:
			insertAt = end // keep leading APP0s (JFIF, JFXX) first
		case marker == 0xe1 && bytes.HasPrefix(payload, exifSig):
			return patchExif(head[:end], pos, q)
		}
		pos = end
	}
}

// insertExif is orientJPEG for a JPEG without Exif: a minimal Exif APP1
// segment (IFD0 holding only the orientation for q) goes in at insertAt.
func insertExif(head []byte, insertAt, q int) ([]byte, int, error) {
	o := RotateOrientation(1, q)
	be := binary.BigEndian
	seg := []byte{0xff, 0xe1, 0, 0}
	seg = append(seg, exifSig...)
	seg = append(seg, "MM\x00\x2a"...)
	seg = be.AppendUint32(seg, 8) // IFD0 right after the header
	seg = be.AppendUint16(seg, 1)
	seg = appendShortEntry(seg, be, tagOrientation, uint16(o))
	seg = be.AppendUint32(seg, 0) // no next IFD
	be.PutUint16(seg[2:], uint16(len(seg)-2))
	out := make([]byte, 0, insertAt+len(seg))
	out = append(out, head[:insertAt]...)
	out = append(out, seg...)
	return out, insertAt, nil
}

// patchExif sets the orientation for q in the Exif APP1 segment that
// starts at pos and ends head.
func patchExif(head []byte, pos, q int) ([]byte, int, error) {
	tiffAt := pos + 4 + len(exifSig)
	tiff := head[tiffAt:]
	if len(tiff) < 8 {
		return nil, 0, errBadExif
	}
	var order byteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, 0, errBadExif
	}
	if order.Uint16(tiff[2:]) != 42 {
		return nil, 0, errBadExif
	}
	off := order.Uint32(tiff[4:])
	if off < 8 || off > uint32(len(tiff)-2) {
		return nil, 0, errBadExif
	}
	ifd := int(off)
	n := int(order.Uint16(tiff[ifd:]))
	entries := ifd + 2
	next := entries + n*entrySize // the next-IFD pointer
	if next+4 > len(tiff) {
		return nil, 0, errBadExif
	}

	at := n // where Orientation goes among the entries, kept sorted by tag
	for i := range n {
		e := tiff[entries+i*entrySize:]
		tag := order.Uint16(e)
		if tag == tagOrientation {
			if order.Uint16(e[2:]) != typeShort || order.Uint32(e[4:]) != 1 {
				return nil, 0, errBadExif
			}
			// Patch in place: same length, nothing moves.
			out := bytes.Clone(head)
			o := RotateOrientation(int(order.Uint16(e[8:])), q)
			order.PutUint16(out[tiffAt+entries+i*entrySize+8:], uint16(o))
			return out, len(head), nil
		}
		if tag > tagOrientation && at == n {
			at = i
		}
	}

	// No Orientation: append a new IFD0 at the end of the TIFF block
	// (word aligned, as TIFF wants) and point the header at it.
	newAt := len(tiff) + len(tiff)&1
	newLen := 2 + (n+1)*entrySize + 4
	if 2+len(exifSig)+newAt+newLen > maxSegment {
		return nil, 0, errExifFull
	}
	out := make([]byte, 0, tiffAt+newAt+newLen)
	out = append(out, head...)
	if len(tiff)&1 != 0 {
		out = append(out, 0)
	}
	old := tiff[entries:next]
	split := at * entrySize
	out = order.AppendUint16(out, uint16(n+1))
	out = append(out, old[:split]...)
	out = appendShortEntry(out, order, tagOrientation, uint16(RotateOrientation(1, q)))
	out = append(out, old[split:]...)
	out = append(out, tiff[next:next+4]...) // the old IFD0's next-IFD pointer
	binary.BigEndian.PutUint16(out[pos+2:], uint16(len(out)-pos-2))
	order.PutUint32(out[tiffAt+4:], uint32(newAt))
	return out, len(head), nil
}

// appendShortEntry appends an IFD entry holding one SHORT, which sits
// left-justified in the 4-byte value field.
func appendShortEntry(b []byte, order byteOrder, tag, v uint16) []byte {
	b = order.AppendUint16(b, tag)
	b = order.AppendUint16(b, typeShort)
	b = order.AppendUint32(b, 1)
	b = order.AppendUint16(b, v)
	return append(b, 0, 0)
}

// formatName names the image format of a file starting with head, for
// saying which copies could not keep a rotation.
func formatName(head []byte) string {
	h := string(head[:min(len(head), 16)])
	switch {
	case len(h) >= 3 && h[:3] == "\xff\xd8\xff":
		return "jpeg"
	case len(h) >= 8 && h[:8] == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(h) >= 4 && h[:4] == "GIF8":
		return "gif"
	case len(h) >= 12 && h[:4] == "RIFF" && h[8:12] == "WEBP":
		return "webp"
	case len(h) >= 2 && h[:2] == "BM":
		return "bmp"
	case len(h) >= 4 && (h[:4] == "II*\x00" || h[:4] == "MM\x00*"):
		return "tiff"
	case len(h) >= 12 && h[4:8] == "ftyp" && (h[8:12] == "avif" || h[8:12] == "avis"):
		return "avif"
	case len(h) >= 8 && h[4:8] == "ftyp":
		return "heic"
	case len(h) >= 2 && h[:2] == "\xff\x0a", len(h) >= 12 && h[4:12] == "JXL \r\n\x87\n":
		return "jxl"
	}
	return "unknown format"
}
