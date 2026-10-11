package imgload

import "image"

// orient applies EXIF orientation o (1..8) so the result displays upright.
// Any other value returns m unchanged.
func orient(m *image.NRGBA, o int) *image.NRGBA {
	if o < 2 || o > 8 {
		return m
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	dw, dh := w, h
	if o >= 5 { // 5..8 swap axes
		dw, dh = h, w
	}
	// src maps a destination pixel to its source pixel.
	var src func(x, y int) (int, int)
	switch o {
	case 2: // mirror horizontally
		src = func(x, y int) (int, int) { return w - 1 - x, y }
	case 3: // rotate 180
		src = func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case 4: // mirror vertically
		src = func(x, y int) (int, int) { return x, h - 1 - y }
	case 5: // transpose
		src = func(x, y int) (int, int) { return y, x }
	case 6: // rotate 90 clockwise
		src = func(x, y int) (int, int) { return y, h - 1 - x }
	case 7: // transverse
		src = func(x, y int) (int, int) { return w - 1 - y, h - 1 - x }
	case 8: // rotate 90 counter-clockwise
		src = func(x, y int) (int, int) { return w - 1 - y, x }
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		row := dst.Pix[y*dst.Stride:]
		for x := range dw {
			sx, sy := src(x, y)
			copy(row[4*x:4*x+4], m.Pix[m.PixOffset(sx+m.Rect.Min.X, sy+m.Rect.Min.Y):])
		}
	}
	return dst
}
