package imgload

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"

	"pix/internal/fileops"
)

// turnCW turns m by q clockwise quarter turns (orientation 6 is one).
func turnCW(m *image.NRGBA, q int) *image.NRGBA {
	for range q {
		m = orient(m, 6)
	}
	return m
}

func sameNRGBA(a, b *image.NRGBA) bool {
	return a.Rect == b.Rect && bytes.Equal(a.Pix, b.Pix)
}

// fileops.RotateOrientation is the orientation that displays like the
// old one followed by the user's turns, as orient applies them.
func TestRotateOrientationMatchesOrient(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 5, 3)) // every pixel different
	for y := range 3 {
		for x := range 5 {
			src.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(10*x + y), 255})
		}
	}
	for o := 1; o <= 8; o++ {
		for q := range 4 {
			want := turnCW(orient(src, o), q)
			if got := orient(src, fileops.RotateOrientation(o, q)); !sameNRGBA(got, want) {
				t.Errorf("orientation %d + %d turns: RotateOrientation gives %d, which shows differently",
					o, q, fileops.RotateOrientation(o, q))
			}
		}
	}
}

// jpegWithTIFF encodes m and puts an Exif segment holding tiff (none when
// nil) right after SOI.
func jpegWithTIFF(t *testing.T, m image.Image, tiff []byte) []byte {
	j := encode(t, m, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, &jpeg.Options{Quality: 95}) })
	if tiff == nil {
		return j
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)
	return append(append(j[:2:2], seg...), j[2:]...)
}

// A rotated copy decodes to the source as pix showed it, turned the way
// the user turned it: for every orientation the source may have, with or
// without an Orientation tag or Exif at all.
func TestCopiedRotationDecodes(t *testing.T) {
	pic := quadrants(32, 16)
	cases := []struct {
		name string
		tiff []byte
	}{
		{"no exif", nil},
		{"exif without orientation", exifDates("2024:07:14 18:30:05", "2024:07:14 18:30:05")},
	}
	for o := uint16(1); o <= 8; o++ {
		cases = append(cases, struct {
			name string
			tiff []byte
		}{fmt.Sprint("orientation ", o), exifTIFF(o)})
	}
	for _, c := range cases {
		data := jpegWithTIFF(t, pic, c.tiff)
		src := writeFile(t, "s.jpg", data)
		shown, err := Decode(src)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for q := 1; q <= 3; q++ {
			res := fileops.CopyAll([]string{src}, t.TempDir(), func(string) int { return q })
			if r := res[0]; r.Err != nil || !r.Rotated || r.Skipped {
				t.Fatalf("%s, %d turns: %+v", c.name, q, r)
			}
			got, err := Decode(res[0].Dst)
			if err != nil {
				t.Fatalf("%s, %d turns: %v", c.name, q, err)
			}
			if !sameNRGBA(got, turnCW(shown, q)) {
				t.Errorf("%s, %d turns: copy does not show the source turned", c.name, q)
			}
			if a, b := ReadMeta(src).Taken, ReadMeta(res[0].Dst).Taken; !a.Equal(b) {
				t.Errorf("%s, %d turns: date taken %v, source %v", c.name, q, b, a)
			}
		}
		if raw, err := os.ReadFile(src); err != nil || !bytes.Equal(raw, data) {
			t.Fatalf("%s: source changed", c.name)
		}
	}
}
