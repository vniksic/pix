package ui

// Frame rendering, independent of Fyne so it can be tested headless.

import (
	"image"
	"image/color"
	stddraw "image/draw"
	"runtime"
	"sync"
	"sync/atomic"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// Background is the colour behind the image (#111).
var Background = color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xff}

// stripRows is the height of the horizontal bands a frame is split into so
// several cores can resample in parallel (and a settle render can be
// abandoned early).
const stripRows = 64

// fillBackground paints dst with Background.
func fillBackground(dst *image.RGBA) {
	b := dst.Bounds()
	if b.Empty() {
		return
	}
	w := b.Dx() * 4
	row := dst.Pix[:w]
	for i := 0; i < w; i += 4 {
		row[i], row[i+1], row[i+2], row[i+3] = Background.R, Background.G, Background.B, Background.A
	}
	for y := 1; y < b.Dy(); y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+w], row)
	}
}

// isIntTranslation reports whether m is a pure translation by whole pixels.
func isIntTranslation(m f64.Aff3) (dx, dy int, ok bool) {
	if m[0] != 1 || m[1] != 0 || m[3] != 0 || m[4] != 1 {
		return 0, 0, false
	}
	dx, dy = int(m[2]), int(m[5])
	return dx, dy, float64(dx) == m[2] && float64(dy) == m[5]
}

// renderFrame clears dst to Background and draws src through m (which maps
// source coordinates, with the source's top-left at the origin, to dst
// coordinates). An integer translation is a plain copy; anything else is one
// Transform per horizontal strip, strips spread over all cores. opaque lets
// the caller skip x/image/draw's per-call full-image opacity scan. cancel,
// if non-nil, is polled between strips; when it returns true the frame is
// abandoned and renderFrame returns false.
func renderFrame(dst *image.RGBA, src *image.NRGBA, m f64.Aff3, interp xdraw.Interpolator, opaque bool, cancel func() bool) bool {
	fillBackground(dst)
	if src == nil || src.Bounds().Empty() {
		return true
	}
	sb := src.Bounds()
	op := xdraw.Over
	if opaque {
		op = xdraw.Src
	}

	if dx, dy, ok := isIntTranslation(m); ok {
		r := image.Rect(dx, dy, dx+sb.Dx(), dy+sb.Dy())
		stddraw.Draw(dst, r, src, sb.Min, op)
		return true
	}

	// Account for a source whose bounds do not start at the origin.
	m = Mul(m, Translate(-float64(sb.Min.X), -float64(sb.Min.Y)))

	db := dst.Bounds()
	nStrips := (db.Dy() + stripRows - 1) / stripRows
	workers := runtime.GOMAXPROCS(0)
	if workers > nStrips {
		workers = nStrips
	}
	var next atomic.Int64
	var cancelled atomic.Bool
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= nStrips || cancelled.Load() {
					return
				}
				if cancel != nil && cancel() {
					cancelled.Store(true)
					return
				}
				y0 := db.Min.Y + i*stripRows
				y1 := min(y0+stripRows, db.Max.Y)
				sub := dst.SubImage(image.Rect(db.Min.X, y0, db.Max.X, y1)).(*image.RGBA)
				interp.Transform(sub, m, src, sb, op, nil)
			}
		}()
	}
	wg.Wait()
	return !cancelled.Load()
}

// RotateNRGBA returns src rotated by q clockwise quarter turns as a new
// image (src itself when q is 0). Pixels are moved exactly; nothing is
// resampled. Used for "copy as displayed".
func RotateNRGBA(src *image.NRGBA, q int) *image.NRGBA {
	q = NormQuarter(q)
	if q == 0 {
		return src
	}
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	dw, dh := w, h
	if q%2 == 1 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		drow := dst.Pix[dy*dst.Stride:]
		for dx := 0; dx < dw; dx++ {
			var sx, sy int
			switch q {
			case 1: // clockwise
				sx, sy = dy, h-1-dx
			case 2:
				sx, sy = w-1-dx, h-1-dy
			case 3: // counter-clockwise
				sx, sy = w-1-dy, dx
			}
			si := src.PixOffset(sb.Min.X+sx, sb.Min.Y+sy)
			copy(drow[dx*4:dx*4+4], src.Pix[si:si+4])
		}
	}
	return dst
}

// ---- mip chain ----
//
// Resampling filters cost in proportion to the downscale factor (CatmullRom
// widens its kernel to cover every source pixel) or alias badly (bilinear
// skips pixels), so views below 0.5 read from a pre-halved copy instead:
// level i is the source box-downsampled by 2, i times. The remaining
// downscale is then between 1× and 2×.

// mipSize returns the size of the next mip level below w×h (floor halving,
// never below 1).
func mipSize(w, h int) (int, int) {
	return max(w/2, 1), max(h/2, 1)
}

// mipLevelFor returns the smallest mip level of a w×h source whose scale
// factor (relative to the source, the smaller of the two axes) is still
// >= s. Level 0 is the source itself, used for s >= ~0.5, 1:1 and zoom-in.
func mipLevelFor(w, h int, s float64) int {
	if w <= 0 || h <= 0 || s <= 0 {
		return 0
	}
	l, mw, mh := 0, w, h
	for {
		nw, nh := mipSize(mw, mh)
		if nw == mw && nh == mh {
			return l
		}
		if min(float64(nw)/float64(w), float64(nh)/float64(h)) < s {
			return l
		}
		l, mw, mh = l+1, nw, nh
	}
}

// mipMatrix adapts m, which maps a w×h source to the viewport, to read from
// an mw×mh mip of that source instead. The mip covers exactly the source's
// extent, so the image edges land where they would from the original.
func mipMatrix(m f64.Aff3, w, h, mw, mh int) f64.Aff3 {
	if mw == w && mh == h {
		return m
	}
	return Mul(m, f64.Aff3{float64(w) / float64(mw), 0, 0, 0, float64(h) / float64(mh), 0})
}

// halveRows is how many destination rows one mip worker takes at a time.
const halveRows = 16

// halveNRGBA box-downsamples src by 2 (each output pixel is the
// alpha-weighted mean of a 2×2 block; an odd last row or column is
// dropped, a 1-pixel axis is kept). Rows are spread over all cores. If stop
// becomes true it gives up and returns nil.
func halveNRGBA(src *image.NRGBA, stop *atomic.Bool) *image.NRGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := mipSize(sw, sh)
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	xStep := 4 // byte offset from a block's left column to its right one
	if sw == 1 {
		xStep = 0
	}
	yStep := src.Stride
	if sh == 1 {
		yStep = 0
	}

	nBands := (dh + halveRows - 1) / halveRows
	workers := min(runtime.GOMAXPROCS(0), nBands)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				band := int(next.Add(1) - 1)
				if band >= nBands || (stop != nil && stop.Load()) {
					return
				}
				for y := band * halveRows; y < min((band+1)*halveRows, dh); y++ {
					s0 := src.PixOffset(sb.Min.X, sb.Min.Y+2*y)
					drow := dst.Pix[y*dst.Stride : y*dst.Stride+dw*4]
					for x := 0; x < dw; x++ {
						p := s0 + x*8
						q := p + yStep
						halvePixel(drow[x*4:x*4+4:x*4+4],
							src.Pix[p:p+4:p+4], src.Pix[p+xStep:p+xStep+4:p+xStep+4],
							src.Pix[q:q+4:q+4], src.Pix[q+xStep:q+xStep+4:q+xStep+4])
					}
				}
			}
		}()
	}
	wg.Wait()
	if stop != nil && stop.Load() {
		return nil
	}
	return dst
}

// halvePixel writes the mean of four non-premultiplied pixels to d,
// weighting colour by alpha so transparent pixels do not darken edges.
func halvePixel(d, a, b, c, e []uint8) {
	aa, ab, ac, ae := uint32(a[3]), uint32(b[3]), uint32(c[3]), uint32(e[3])
	sa := aa + ab + ac + ae
	switch sa {
	case 4 * 0xff:
		d[0] = uint8((uint32(a[0]) + uint32(b[0]) + uint32(c[0]) + uint32(e[0]) + 2) >> 2)
		d[1] = uint8((uint32(a[1]) + uint32(b[1]) + uint32(c[1]) + uint32(e[1]) + 2) >> 2)
		d[2] = uint8((uint32(a[2]) + uint32(b[2]) + uint32(c[2]) + uint32(e[2]) + 2) >> 2)
		d[3] = 0xff
	case 0:
		d[0], d[1], d[2], d[3] = 0, 0, 0, 0
	default:
		for i := range 3 {
			sum := uint32(a[i])*aa + uint32(b[i])*ab + uint32(c[i])*ac + uint32(e[i])*ae
			d[i] = uint8((sum + sa/2) / sa)
		}
		d[3] = uint8((sa + 2) >> 2)
	}
}

// mipChain lazily builds and caches the mip levels of one source image.
// Levels are built in order on one background goroutine (each level
// parallel over rows); readers use the best level that exists so far.
type mipChain struct {
	stop    atomic.Bool
	onBuilt func() // called from the builder once it has caught up with requests

	mu       sync.Mutex
	levels   []*image.NRGBA // levels[0] is the source; never modified once added
	want     int            // highest level requested
	building bool
}

func newMipChain(src *image.NRGBA, onBuilt func()) *mipChain {
	return &mipChain{levels: []*image.NRGBA{src}, onBuilt: onBuilt}
}

// best returns the highest level built so far that is <= want, and its index.
func (c *mipChain) best(want int) (*image.NRGBA, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := max(0, min(want, len(c.levels)-1))
	return c.levels[i], i
}

// request makes sure levels up to want get built in the background.
func (c *mipChain) request(want int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.want = max(c.want, want)
	if c.building || len(c.levels) > c.want || c.stop.Load() {
		return
	}
	c.building = true
	go c.build()
}

func (c *mipChain) build() {
	for {
		c.mu.Lock()
		if c.stop.Load() || len(c.levels) > c.want {
			c.building = false
			notify := !c.stop.Load()
			c.mu.Unlock()
			if notify && c.onBuilt != nil {
				c.onBuilt()
			}
			return
		}
		prev := c.levels[len(c.levels)-1]
		c.mu.Unlock()
		if next := halveNRGBA(prev, &c.stop); next != nil {
			c.mu.Lock()
			c.levels = append(c.levels, next)
			c.mu.Unlock()
		}
	}
}

// close abandons any building in progress (the image is no longer shown).
func (c *mipChain) close() { c.stop.Store(true) }
