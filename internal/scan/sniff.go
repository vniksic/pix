package scan

import (
	"encoding/binary"
	"io"
	"os"
)

// IsImage sniffs the first 64 bytes. It recognises JPEG, PNG, GIF, WebP,
// BMP, TIFF, HEIC/HEIF, AVIF and JPEG XL. It never looks at the extension.
func IsImage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [64]byte
	n, err := io.ReadFull(f, buf[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	return sniff(buf[:n])
}

var isobmffBrands = map[string]bool{
	"heic": true, "heix": true, "hevc": true, "mif1": true, "msf1": true, // HEIC/HEIF
	"avif": true, "avis": true, // AVIF
}

func sniff(b []byte) bool {
	has := func(off int, sig string) bool {
		return len(b) >= off+len(sig) && string(b[off:off+len(sig)]) == sig
	}
	switch {
	case has(0, "\xff\xd8\xff"), // JPEG
		has(0, "\x89PNG\r\n\x1a\n"),
		has(0, "GIF87a"), has(0, "GIF89a"),
		has(0, "RIFF") && has(8, "WEBP"),
		has(0, "II*\x00"), has(0, "MM\x00*"), // TIFF
		has(0, "\xff\x0a"),                       // JPEG XL codestream
		has(0, "\x00\x00\x00\x0cJXL \r\n\x87\n"): // JPEG XL container
		return true
	case has(0, "BM"):
		return isBMP(b)
	case has(4, "ftyp"):
		return hasImageBrand(b)
	}
	return false
}

// isBMP checks the DIB header size, since "BM" alone matches plenty of text.
func isBMP(b []byte) bool {
	if len(b) < 18 {
		return false
	}
	switch binary.LittleEndian.Uint32(b[14:18]) {
	case 12, 16, 40, 52, 56, 64, 108, 124:
		return true
	}
	return false
}

// hasImageBrand checks the major and compatible brands of an ISOBMFF ftyp box.
func hasImageBrand(b []byte) bool {
	size := int(binary.BigEndian.Uint32(b[0:4]))
	if size < 16 || size > len(b) {
		size = len(b)
	}
	if size >= 12 && isobmffBrands[string(b[8:12])] {
		return true
	}
	// Compatible brands start after major brand (8..12) and minor version (12..16).
	for i := 16; i+4 <= size; i += 4 {
		if isobmffBrands[string(b[i:i+4])] {
			return true
		}
	}
	return false
}
