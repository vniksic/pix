package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sync/atomic"
	"testing"
	"time"

	xdraw "golang.org/x/image/draw"
)

func testImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 10), G: uint8(y * 10), B: uint8(x + y), A: 255})
		}
	}
	return img
}

func TestRotateNRGBA(t *testing.T) {
	src := testImage(3, 2)
	if RotateNRGBA(src, 0) != src {
		t.Errorf("q=0 should return src")
	}
	cw := RotateNRGBA(src, 1)
	if b := cw.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
		t.Fatalf("q=1 size %v", b)
	}
	// Clockwise: the source bottom-left becomes the top-left.
	if cw.NRGBAAt(0, 0) != src.NRGBAAt(0, 1) || cw.NRGBAAt(1, 0) != src.NRGBAAt(0, 0) {
		t.Errorf("q=1 top row wrong")
	}
	if got := RotateNRGBA(RotateNRGBA(src, 1), 3); !samePixels(got, src) {
		t.Errorf("CW then CCW is not identity")
	}
	if got := RotateNRGBA(RotateNRGBA(src, 2), 2); !samePixels(got, src) {
		t.Errorf("180 twice is not identity")
	}
	// Non-zero origin sub-image.
	big := testImage(6, 5)
	sub := big.SubImage(image.Rect(2, 1, 5, 3)).(*image.NRGBA)
	if got := RotateNRGBA(sub, 2); got.NRGBAAt(0, 0) != big.NRGBAAt(4, 2) {
		t.Errorf("sub-image rotation used wrong origin")
	}
}

func samePixels(a, b *image.NRGBA) bool {
	if a.Bounds().Size() != b.Bounds().Size() {
		return false
	}
	ab, bb := a.Bounds(), b.Bounds()
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			if a.NRGBAAt(ab.Min.X+x, ab.Min.Y+y) != b.NRGBAAt(bb.Min.X+x, bb.Min.Y+y) {
				return false
			}
		}
	}
	return true
}

// At 1:1 the transform rendering of every rotation must agree pixel for
// pixel with RotateNRGBA: this ties the affine matrix, the renderer and
// the clipboard rotation together.
func TestRenderMatchesRotateAtOne(t *testing.T) {
	src := testImage(7, 4)
	for q := 0; q < 4; q++ {
		v := &View{}
		v.SetViewport(20, 20)
		v.SetImage(7, 4)
		v.SetOne()
		v.Rotate(q)
		dst := image.NewRGBA(image.Rect(0, 0, 20, 20))
		if !renderFrame(dst, src, v.Matrix(), xdraw.ApproxBiLinear, true, nil) {
			t.Fatal("render cancelled")
		}
		want := RotateNRGBA(src, q)
		wb := want.Bounds()
		// The rotated image's top-left in dst is the (rounded) view offset.
		x0, y0 := int(math.Round(v.OX)), int(math.Round(v.OY))
		for y := 0; y < wb.Dy(); y++ {
			for x := 0; x < wb.Dx(); x++ {
				w := want.NRGBAAt(x, y)
				g := dst.RGBAAt(x0+x, y0+y)
				if g.R != w.R || g.G != w.G || g.B != w.B || g.A != 255 {
					t.Fatalf("q=%d pixel (%d,%d): got %v want %v", q, x, y, g, w)
				}
			}
		}
		if dst.RGBAAt(0, 0) != Background {
			t.Errorf("q=%d: background not filled", q)
		}
	}
}

func TestRenderTranslationAndOver(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 0}) // fully transparent
	dst := image.NewRGBA(image.Rect(0, 0, 5, 3))
	renderFrame(dst, src, Translate(2, 1), xdraw.ApproxBiLinear, false, nil)
	if got := dst.RGBAAt(2, 1); got != (color.RGBA{R: 255, A: 255}) {
		t.Errorf("copied pixel %v", got)
	}
	if got := dst.RGBAAt(3, 1); got != Background {
		t.Errorf("transparent pixel should show background, got %v", got)
	}
	if got := dst.RGBAAt(0, 0); got != Background {
		t.Errorf("background %v", got)
	}
}

func TestRenderScaledCoversExpectedArea(t *testing.T) {
	src := testImage(40, 20)
	v := &View{}
	v.SetViewport(100, 100)
	v.SetImage(40, 20)
	v.ZoomBy(2, 50, 50) // 80x40, centred: (10,30)-(90,70)
	dst := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for _, interp := range []xdraw.Interpolator{xdraw.ApproxBiLinear, xdraw.CatmullRom} {
		renderFrame(dst, src, v.Matrix(), interp, true, nil)
		if dst.RGBAAt(5, 50) != Background || dst.RGBAAt(50, 25) != Background {
			t.Errorf("outside the image should be background")
		}
		if dst.RGBAAt(50, 50) == Background || dst.RGBAAt(11, 31) == Background || dst.RGBAAt(88, 68) == Background {
			t.Errorf("inside the image should be drawn")
		}
	}
}

func TestRenderCancel(t *testing.T) {
	src := testImage(40, 40)
	dst := image.NewRGBA(image.Rect(0, 0, 300, 300))
	if renderFrame(dst, src, ScaleM(3.3), xdraw.CatmullRom, true, func() bool { return true }) {
		t.Errorf("cancelled render reported success")
	}
}

func TestMipLevelFor(t *testing.T) {
	cases := []struct {
		w, h int
		s    float64
		want int
	}{
		{6000, 4000, 1, 0},
		{6000, 4000, 4, 0},      // zoom-in reads the original
		{6000, 4000, 0.6, 0},    // between 0.5 and 1: original
		{6000, 4000, 0.5, 1},    // exactly half: level 1, no remaining downscale
		{6000, 4000, 0.3, 1},    // 0.5 >= 0.3 > 0.25
		{6000, 4000, 0.25, 2},   // exactly a quarter
		{6000, 4000, 0.2, 2},    // 0.25 >= 0.2 > 0.125
		{30000, 2000, 0.064, 3}, // 1/8 >= 0.064 > 1/16
		{5999, 4000, 0.5, 0},    // floor halving gives a factor just under 0.5
		{3, 1, 0.01, 1},         // stops when halving no longer shrinks
		{0, 0, 0.1, 0},
	}
	for _, c := range cases {
		if got := mipLevelFor(c.w, c.h, c.s); got != c.want {
			t.Errorf("mipLevelFor(%d, %d, %v) = %d, want %d", c.w, c.h, c.s, got, c.want)
		}
	}
}

func TestHalveNRGBA(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 5, 3)) // odd: last row/column dropped
	px := func(x, y int, c color.NRGBA) { src.SetNRGBA(x, y, c) }
	px(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	px(1, 0, color.NRGBA{R: 20, G: 20, B: 30, A: 255})
	px(0, 1, color.NRGBA{R: 30, G: 20, B: 30, A: 255})
	px(1, 1, color.NRGBA{R: 41, G: 20, B: 30, A: 255})
	// Half-transparent block: colour weighted by alpha.
	px(2, 0, color.NRGBA{R: 200, A: 255})
	px(3, 0, color.NRGBA{R: 0, G: 255, A: 0})
	px(2, 1, color.NRGBA{R: 100, A: 85})
	px(3, 1, color.NRGBA{R: 0, A: 0})
	got := halveNRGBA(src, nil)
	if b := got.Bounds(); b != image.Rect(0, 0, 2, 1) {
		t.Fatalf("size %v", b)
	}
	if c := got.NRGBAAt(0, 0); c != (color.NRGBA{R: 25, G: 20, B: 30, A: 255}) {
		t.Errorf("opaque block %v", c)
	}
	// alpha (255+85)/4 = 85; red (200*255 + 100*85) / 340 = 175.
	if c := got.NRGBAAt(1, 0); c != (color.NRGBA{R: 175, A: 85}) {
		t.Errorf("transparent block %v", c)
	}

	// A one-pixel axis is kept, not halved to zero; sub-image origins honoured.
	big := testImage(10, 6)
	sub := big.SubImage(image.Rect(2, 3, 6, 4)).(*image.NRGBA) // 4×1
	h := halveNRGBA(sub, nil)
	if b := h.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("1-row size %v", b)
	}
	a, b := big.NRGBAAt(2, 3), big.NRGBAAt(3, 3)
	if c := h.NRGBAAt(0, 0); c.R != uint8((uint32(a.R)+uint32(b.R)+1)/2) || c.G != a.G {
		t.Errorf("1-row pixel %v from %v %v", c, a, b)
	}

	var stop atomic.Bool
	stop.Store(true)
	if halveNRGBA(testImage(64, 64), &stop) != nil {
		t.Errorf("stopped halving returned an image")
	}
}

func TestMipChain(t *testing.T) {
	src := testImage(64, 32)
	done := make(chan struct{}, 4)
	c := newMipChain(src, func() { done <- struct{}{} })
	if img, l := c.best(3); img != src || l != 0 {
		t.Fatalf("before building: level %d", l)
	}
	c.request(3)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mip chain never finished")
	}
	img, l := c.best(5)
	if l != 3 || img.Bounds() != image.Rect(0, 0, 8, 4) {
		t.Fatalf("best(5) = level %d %v, want level 3 8×4", l, img.Bounds())
	}
	if _, l := c.best(1); l != 1 {
		t.Errorf("best(1) = %d", l)
	}
	c.close()
	c.request(5) // closed: nothing more is built
	if _, l := c.best(5); l != 3 {
		t.Errorf("closed chain grew to level %d", l)
	}
}

// smoothImage has slow gradients in every channel (different along x and y,
// so a wrong rotation shows), where box and CatmullRom filtering agree.
func smoothImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(255 * fx),
				G: uint8(255 * fy),
				B: uint8(127 + 120*math.Sin(3*fx+2*fy)),
				A: 255,
			})
		}
	}
	return img
}

// A Fit render from a mip must agree with a CatmullRom render from the
// original (within filtering differences) for every rotation: the mip
// matrix composes correctly with rotation, scale and offset.
func TestRenderFromMipMatchesOriginal(t *testing.T) {
	const W, H, VW, VH = 1203, 801, 160, 120
	src := smoothImage(W, H)
	c := newMipChain(src, nil)
	for q := 0; q < 4; q++ {
		v := &View{}
		v.SetViewport(VW, VH)
		v.SetImage(W, H)
		v.Rotate(q)
		lvl := mipLevelFor(W, H, v.Scale)
		if lvl < 2 {
			t.Fatalf("q=%d scale %v: expected a mip level >= 2, got %d", q, v.Scale, lvl)
		}
		for {
			c.request(lvl)
			if _, got := c.best(lvl); got == lvl {
				break
			}
			time.Sleep(time.Millisecond)
		}
		mip, _ := c.best(lvl)
		mb := mip.Bounds()

		ref := image.NewRGBA(image.Rect(0, 0, VW, VH))
		renderFrame(ref, src, v.Matrix(), xdraw.CatmullRom, true, nil)
		for _, interp := range []xdraw.Interpolator{xdraw.CatmullRom, xdraw.ApproxBiLinear} {
			got := image.NewRGBA(image.Rect(0, 0, VW, VH))
			renderFrame(got, mip, mipMatrix(v.Matrix(), W, H, mb.Dx(), mb.Dy()), interp, true, nil)
			rw, rh := v.RotSize()
			x0, y0 := v.OX, v.OY
			x1, y1 := x0+rw*v.Scale, y0+rh*v.Scale
			for _, f := range [][2]float64{{0.1, 0.1}, {0.5, 0.5}, {0.9, 0.2}, {0.2, 0.85}, {0.75, 0.6}} {
				x, y := int(x0+f[0]*(x1-x0)), int(y0+f[1]*(y1-y0))
				g, r := got.RGBAAt(x, y), ref.RGBAAt(x, y)
				if absDiff(g.R, r.R) > 3 || absDiff(g.G, r.G) > 3 || absDiff(g.B, r.B) > 3 {
					t.Errorf("q=%d level %d (%d,%d): got %v want %v", q, lvl, x, y, g, r)
				}
			}
			// The image covers the same area: background just outside, image just inside.
			if x0 > 1 && got.RGBAAt(int(x0)-1, VH/2) != Background {
				t.Errorf("q=%d: left of the image not background", q)
			}
			if got.RGBAAt(int(math.Ceil(x0)), int(y0+2)) == Background {
				t.Errorf("q=%d: image top-left corner not drawn", q)
			}
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func benchSrc(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = uint8(x^y), uint8(x*7+y), uint8(y*3), 255
		}
	}
	return img
}

// BenchmarkSettle times a Fit render into a 1920×1080 viewport of large
// images: "orig" samples the full-size source (the old path), "mip" the
// mip level the viewport now picks; "build" is the one-off cost of making
// that level (off the main goroutine). Run with
//
//	go test ./internal/ui -run XXX -bench Settle -benchtime 5x
func BenchmarkSettle(b *testing.B) {
	for _, sz := range [][2]int{{6000, 4000}, {30000, 2000}} {
		w, h := sz[0], sz[1]
		src := benchSrc(w, h)
		v := &View{}
		v.SetViewport(1920, 1080)
		v.SetImage(float64(w), float64(h))
		dst := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
		lvl := mipLevelFor(w, h, v.Scale)
		build := func() *image.NRGBA {
			m := src
			for range lvl {
				m = halveNRGBA(m, nil)
			}
			return m
		}
		mip := build()
		mm := mipMatrix(v.Matrix(), w, h, mip.Bounds().Dx(), mip.Bounds().Dy())
		name := fmt.Sprintf("%dx%d", w, h)
		for _, interp := range []struct {
			name string
			xdraw.Interpolator
		}{{"catmullrom", xdraw.CatmullRom}, {"bilinear", xdraw.ApproxBiLinear}} {
			b.Run(name+"/"+interp.name+"/orig", func(b *testing.B) {
				for b.Loop() {
					renderFrame(dst, src, v.Matrix(), interp, true, nil)
				}
			})
			b.Run(fmt.Sprintf("%s/%s/mip%d", name, interp.name, lvl), func(b *testing.B) {
				for b.Loop() {
					renderFrame(dst, mip, mm, interp, true, nil)
				}
			})
		}
		b.Run(fmt.Sprintf("%s/build-mip%d", name, lvl), func(b *testing.B) {
			for b.Loop() {
				build()
			}
		})
	}
}
