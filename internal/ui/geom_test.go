package ui

import (
	"math"
	"testing"

	"golang.org/x/image/math/f64"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestNormQuarter(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 3: 3, 4: 0, 5: 1, -1: 3, -4: 0, -5: 3}
	for in, want := range cases {
		if got := NormQuarter(in); got != want {
			t.Errorf("NormQuarter(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRotatedSize(t *testing.T) {
	for q, want := range [][2]float64{{400, 300}, {300, 400}, {400, 300}, {300, 400}} {
		rw, rh := RotatedSize(400, 300, q)
		if rw != want[0] || rh != want[1] {
			t.Errorf("q=%d: got %vx%v want %v", q, rw, rh, want)
		}
	}
	if rw, rh := RotatedSize(400, 300, -1); rw != 300 || rh != 400 {
		t.Errorf("q=-1: got %vx%v", rw, rh)
	}
}

func TestFitScale(t *testing.T) {
	cases := []struct {
		vw, vh, rw, rh, want float64
	}{
		{1000, 800, 4000, 3000, 0.25},    // width-limited
		{1000, 500, 4000, 3000, 1.0 / 6}, // height-limited
		{1000, 800, 500, 400, 1},         // smaller: never upscale
		{1000, 800, 1000, 800, 1},        // exact
		{1000, 800, 3000, 4000, 0.2},     // rotated dims (portrait)
		{0, 800, 100, 100, 1},            // degenerate viewport
		{1000, 800, 0, 100, 1},           // degenerate image
	}
	for _, c := range cases {
		if got := FitScale(c.vw, c.vh, c.rw, c.rh); !near(got, c.want) {
			t.Errorf("FitScale(%v,%v,%v,%v) = %v, want %v", c.vw, c.vh, c.rw, c.rh, got, c.want)
		}
	}
}

func TestClampScale(t *testing.T) {
	if got := ClampScale(1000, 0.5); got != MaxScale {
		t.Errorf("upper clamp: %v", got)
	}
	if got := ClampScale(0.0001, 0.5); !near(got, 0.05) {
		t.Errorf("lower clamp with fit 0.5: %v", got)
	}
	if got := ClampScale(0.0001, 1); !near(got, 0.1) {
		t.Errorf("lower clamp with fit 1: %v", got)
	}
	if got := ClampScale(2, 0.5); got != 2 {
		t.Errorf("in range: %v", got)
	}
}

func TestSnapThroughOne(t *testing.T) {
	cases := []struct{ old, next, want float64 }{
		{0.95, 1.045, 1},
		{1.1, 0.9, 1},
		{1, 1.1, 1.1},
		{1, 0.9, 0.9},
		{0.5, 0.55, 0.55},
		{2, 2.2, 2.2},
		{1.25, 1.25 * 0.8, 1},
	}
	for _, c := range cases {
		if got := SnapThroughOne(c.old, c.next); got != c.want {
			t.Errorf("SnapThroughOne(%v,%v) = %v, want %v", c.old, c.next, got, c.want)
		}
	}
}

func TestWheelFactor(t *testing.T) {
	if got := WheelFactor(25); !near(got, 1.1) {
		t.Errorf("one notch up: %v", got)
	}
	if got := WheelFactor(-25); !near(got, 1/1.1) {
		t.Errorf("one notch down: %v", got)
	}
	if got := WheelFactor(0); got != 1 {
		t.Errorf("zero: %v", got)
	}
	if got := WheelFactor(25 * 125); !near(got, math.Pow(1.1, 5)) {
		t.Errorf("accelerated flick not bounded: %v", got)
	}
}

func TestWaylandSession(t *testing.T) {
	cases := []struct {
		platform, display string
		want              bool
	}{
		{"", "", false},
		{"", "wayland-1", true},
		{"x11", "wayland-1", false},
		{"wayland", "", true},
		{"bogus", "wayland-1", true}, // unknown override ignored
		{"bogus", "", false},
	}
	for _, c := range cases {
		env := map[string]string{"FYNE_PLATFORM": c.platform, "WAYLAND_DISPLAY": c.display}
		if got := WaylandSession(func(k string) string { return env[k] }); got != c.want {
			t.Errorf("FYNE_PLATFORM=%q WAYLAND_DISPLAY=%q: got %v", c.platform, c.display, got)
		}
	}
}

func TestNormalizeWheel(t *testing.T) {
	// Fyne: 25 × GLFW yoff; GLFW yoff is 1 per notch on X11, 1.5 on Wayland.
	if got := WheelFactor(NormalizeWheel(25*1.5, true)); !near(got, 1.1) {
		t.Errorf("Wayland notch: %v", got)
	}
	if got := WheelFactor(NormalizeWheel(-25*1.5, true)); !near(got, 1/1.1) {
		t.Errorf("Wayland notch down: %v", got)
	}
	if got := WheelFactor(NormalizeWheel(25, false)); !near(got, 1.1) {
		t.Errorf("X11 notch: %v", got)
	}
	// The clamp still applies after normalising an accelerated flick.
	if got := WheelFactor(NormalizeWheel(25*1.5*125, true)); !near(got, math.Pow(1.1, 5)) {
		t.Errorf("Wayland flick not bounded: %v", got)
	}
}

func TestClampOffset(t *testing.T) {
	cases := []struct{ o, d, v, want float64 }{
		{-50, 100, 300, 100},   // smaller: centred regardless of o
		{500, 100, 300, 100},   // smaller: centred
		{0, 300, 300, 0},       // equal: centred (0)
		{50, 500, 300, 0},      // larger, gap on the left -> 0
		{-300, 500, 300, -200}, // larger, gap on the right -> v-d
		{-100, 500, 300, -100}, // larger, in range
	}
	for _, c := range cases {
		if got := ClampOffset(c.o, c.d, c.v); got != c.want {
			t.Errorf("ClampOffset(%v,%v,%v) = %v, want %v", c.o, c.d, c.v, got, c.want)
		}
	}
}

func TestZoomAboutKeepsPoint(t *testing.T) {
	o, s, ns, p := -120.0, 0.5, 1.7, 333.0
	img := (p - o) / s // image coordinate under p before
	no := ZoomAbout(o, s, ns, p)
	if got := (p - no) / ns; !near(got, img) {
		t.Errorf("image point under cursor moved: %v -> %v", img, got)
	}
}

func TestMulAndPrimitives(t *testing.T) {
	id := f64.Aff3{1, 0, 0, 0, 1, 0}
	m := f64.Aff3{2, 3, 4, 5, 6, 7}
	if Mul(id, m) != m || Mul(m, id) != m {
		t.Errorf("identity product failed")
	}
	// Mul applies the right operand first.
	x, y := Apply(Mul(Translate(10, 20), ScaleM(2)), 3, 4)
	if x != 16 || y != 28 {
		t.Errorf("T·S applied to (3,4) = (%v,%v), want (16,28)", x, y)
	}
	x, y = Apply(RotateM(1), 1, 0)
	if x != 0 || y != 1 {
		t.Errorf("clockwise quarter of +x should point down (+y), got (%v,%v)", x, y)
	}
	if Mul(RotateM(1), RotateM(3)) != id {
		t.Errorf("R(1)·R(3) != I")
	}
}

func TestAffineHandDerived(t *testing.T) {
	w, h, s, ox, oy := 40.0, 30.0, 2.0, 5.0, 7.0
	want := []f64.Aff3{
		{s, 0, ox, 0, s, oy},
		{0, -s, ox + s*h, s, 0, oy},
		{-s, 0, ox + s*w, 0, -s, oy + s*h},
		{0, s, ox, -s, 0, oy + s*w},
	}
	for q := 0; q < 4; q++ {
		got := Affine(w, h, q, s, ox, oy)
		for i := range got {
			if !near(got[i], want[q][i]) {
				t.Errorf("q=%d: got %v want %v", q, got, want[q])
				break
			}
		}
	}
}

func TestAffineCornersFillRotatedRect(t *testing.T) {
	w, h, s, ox, oy := 40.0, 30.0, 0.5, -3.0, 11.0
	for q := 0; q < 4; q++ {
		rw, rh := RotatedSize(w, h, q)
		m := Affine(w, h, q, s, ox, oy)
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range [][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}} {
			x, y := Apply(m, c[0], c[1])
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
		if !near(minX, ox) || !near(minY, oy) || !near(maxX, ox+s*rw) || !near(maxY, oy+s*rh) {
			t.Errorf("q=%d: bbox (%v,%v)-(%v,%v), want (%v,%v)-(%v,%v)",
				q, minX, minY, maxX, maxY, ox, oy, ox+s*rw, oy+s*rh)
		}
		// The image centre lands on the centre of the rotated rectangle.
		cx, cy := Apply(m, w/2, h/2)
		if !near(cx, ox+s*rw/2) || !near(cy, oy+s*rh/2) {
			t.Errorf("q=%d: centre at (%v,%v)", q, cx, cy)
		}
	}
	// Clockwise: the source top-left corner ends up top-right after q=1.
	x, y := Apply(Affine(w, h, 1, 1, 0, 0), 0, 0)
	if x != h || y != 0 {
		t.Errorf("q=1 top-left -> (%v,%v), want (%v,0)", x, y, h)
	}
}

func TestRotatePointMatchesAffine(t *testing.T) {
	w, h := 40.0, 30.0
	pts := [][2]float64{{0, 0}, {3, 7}, {40, 30}, {12.5, 29}}
	for q := 0; q < 4; q++ {
		rw, rh := RotatedSize(w, h, q)
		for dq := -3; dq <= 3; dq++ {
			a := Affine(w, h, q, 1, 0, 0)
			b := Affine(w, h, q+dq, 1, 0, 0)
			for _, p := range pts {
				x, y := Apply(a, p[0], p[1])
				gx, gy := RotatePoint(x, y, rw, rh, dq)
				wx, wy := Apply(b, p[0], p[1])
				if !near(gx, wx) || !near(gy, wy) {
					t.Errorf("q=%d dq=%d p=%v: got (%v,%v) want (%v,%v)", q, dq, p, gx, gy, wx, wy)
				}
			}
		}
	}
}

func newView(vw, vh, iw, ih float64) *View {
	v := &View{}
	v.SetViewport(vw, vh)
	v.SetImage(iw, ih)
	return v
}

func TestViewFitCentres(t *testing.T) {
	v := newView(1000, 800, 4000, 2000)
	if v.Mode != ModeFit || !near(v.Scale, 0.25) {
		t.Fatalf("mode %v scale %v", v.Mode, v.Scale)
	}
	if !near(v.OX, 0) || !near(v.OY, 150) {
		t.Errorf("offset (%v,%v), want (0,150)", v.OX, v.OY)
	}
	// Small images are not upscaled and are centred.
	v = newView(1000, 800, 200, 100)
	if v.Scale != 1 || v.OX != 400 || v.OY != 350 {
		t.Errorf("small image: scale %v off (%v,%v)", v.Scale, v.OX, v.OY)
	}
}

func TestViewResizeRefitsInFit(t *testing.T) {
	v := newView(1000, 800, 4000, 2000)
	v.SetViewport(2000, 800)
	if !near(v.Scale, 0.4) || !near(v.OX, 200) || !near(v.OY, 0) {
		t.Errorf("after resize: scale %v off (%v,%v)", v.Scale, v.OX, v.OY)
	}
}

func TestViewResizeKeepsCentreInOne(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	v.SetOne()
	cx := (v.VW/2 - v.OX) / v.Scale
	cy := (v.VH/2 - v.OY) / v.Scale
	v.SetViewport(1200, 600)
	if gx, gy := (v.VW/2-v.OX)/v.Scale, (v.VH/2-v.OY)/v.Scale; !near(gx, cx) || !near(gy, cy) {
		t.Errorf("centre point moved from (%v,%v) to (%v,%v)", cx, cy, gx, gy)
	}
}

func TestViewOneCentred(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	v.SetOne()
	if v.Mode != ModeOne || v.Scale != 1 || v.OX != -1500 || v.OY != -1100 {
		t.Errorf("1:1: mode %v scale %v off (%v,%v)", v.Mode, v.Scale, v.OX, v.OY)
	}
}

func TestViewZoomKeepsCursorPoint(t *testing.T) {
	v := newView(1000, 800, 4000, 3000) // fit 0.25, image 1000x750 at (0,25)
	px, py := 300.0, 400.0
	ix, iy := (px-v.OX)/v.Scale, (py-v.OY)/v.Scale
	v.ZoomBy(WheelStep, px, py)
	if v.Mode != ModeFree || !near(v.Scale, 0.275) {
		t.Fatalf("mode %v scale %v", v.Mode, v.Scale)
	}
	if gx, gy := (px-v.OX)/v.Scale, (py-v.OY)/v.Scale; !near(gx, ix) || !near(gy, iy) {
		t.Errorf("point under cursor moved (%v,%v) -> (%v,%v)", ix, iy, gx, gy)
	}
}

func TestViewZoomSnapsToOne(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	for i := 0; i < 100 && v.Scale < 1; i++ {
		v.ZoomBy(KeyStep, 500, 400)
	}
	if v.Scale != 1 || v.Mode != ModeOne {
		t.Errorf("zooming in from fit should stop at exact 1:1, got scale %v mode %v", v.Scale, v.Mode)
	}
	v.ZoomBy(KeyStep, 500, 400)
	v.ZoomBy(1/KeyStep, 500, 400)
	if v.Scale != 1 || v.Mode != ModeOne {
		t.Errorf("in and out again should land on 1:1, got %v", v.Scale)
	}
}

func TestViewZoomLimits(t *testing.T) {
	v := newView(1000, 800, 400, 300)
	for i := 0; i < 200; i++ {
		v.ZoomBy(KeyStep, 0, 0)
	}
	if v.Scale != MaxScale {
		t.Errorf("max: %v", v.Scale)
	}
	for i := 0; i < 400; i++ {
		v.ZoomBy(1/KeyStep, 0, 0)
	}
	if !near(v.Scale, MinScale(1)) {
		t.Errorf("min: %v", v.Scale)
	}
}

func TestViewZoomWithoutImageIsNoop(t *testing.T) {
	v := &View{}
	v.SetViewport(100, 100)
	v.ZoomBy(2, 50, 50)
	if v.Mode != ModeFit {
		t.Errorf("mode changed without image: %v", v.Mode)
	}
}

func TestViewPanClamp(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	v.SetOne() // image 4000x3000 at (-1500,-1100)
	v.Pan(10000, 10000)
	if v.OX != 0 || v.OY != 0 {
		t.Errorf("pan past top-left: (%v,%v)", v.OX, v.OY)
	}
	v.Pan(-1e6, -1e6)
	if v.OX != -3000 || v.OY != -2200 {
		t.Errorf("pan past bottom-right: (%v,%v)", v.OX, v.OY)
	}
	v.Pan(100, 50)
	if v.OX != -2900 || v.OY != -2150 {
		t.Errorf("free pan: (%v,%v)", v.OX, v.OY)
	}
	// A narrow image stays centred on the axis where it is smaller.
	v = newView(1000, 800, 500, 3000)
	v.SetOne()
	v.Pan(300, 300)
	if v.OX != 250 || v.OY != -800 {
		t.Errorf("narrow: (%v,%v)", v.OX, v.OY)
	}
}

func TestViewSetImageKeepsMode(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	v.Rotate(1)
	v.SetImage(2000, 1000)
	if v.Mode != ModeFit || v.Rot != 0 || !near(v.Scale, 0.5) {
		t.Errorf("fit -> fit: mode %v rot %v scale %v", v.Mode, v.Rot, v.Scale)
	}

	v.SetOne()
	v.Pan(-300, -300)
	v.SetImage(3000, 2000)
	if v.Mode != ModeOne || v.Scale != 1 || v.OX != -1000 || v.OY != -600 {
		t.Errorf("1:1 -> 1:1 recentred: mode %v scale %v off (%v,%v)", v.Mode, v.Scale, v.OX, v.OY)
	}

	v.ZoomBy(1.1, 10, 10)
	if v.Mode != ModeFree {
		t.Fatalf("expected free mode")
	}
	v.SetImage(4000, 3000)
	if v.Mode != ModeFit || !near(v.Scale, 0.25) {
		t.Errorf("free -> fit: mode %v scale %v", v.Mode, v.Scale)
	}
}

func TestViewRotateFit(t *testing.T) {
	v := newView(1000, 800, 4000, 2000)
	v.Rotate(1)
	// rotated dims 2000x4000 -> fit 0.2, displayed 400x800 centred
	if v.Rot != 1 || !near(v.Scale, 0.2) || !near(v.OX, 300) || !near(v.OY, 0) {
		t.Errorf("rot %v scale %v off (%v,%v)", v.Rot, v.Scale, v.OX, v.OY)
	}
	v.Rotate(-2)
	if v.Rot != 3 {
		t.Errorf("rot %v, want 3", v.Rot)
	}
	v.Rotate(1)
	if v.Rot != 0 || !near(v.Scale, 0.25) {
		t.Errorf("back to 0: rot %v scale %v", v.Rot, v.Scale)
	}
}

func TestViewRotateOneKeepsCentrePoint(t *testing.T) {
	v := newView(1000, 800, 4000, 3000)
	v.SetOne()
	v.Pan(-200, 100)
	// Source point under the viewport centre before rotating.
	inv := func(v *View) (float64, float64) {
		m := v.Matrix()
		// invert the (orthogonal * scale) matrix numerically
		det := m[0]*m[4] - m[1]*m[3]
		x, y := v.VW/2-m[2], v.VH/2-m[5]
		return (m[4]*x - m[1]*y) / det, (-m[3]*x + m[0]*y) / det
	}
	sx, sy := inv(v)
	v.Rotate(1)
	if v.Mode != ModeOne || v.Rot != 1 {
		t.Fatalf("mode %v rot %v", v.Mode, v.Rot)
	}
	gx, gy := inv(v)
	if math.Abs(gx-sx) > 1 || math.Abs(gy-sy) > 1 {
		t.Errorf("centre source point moved (%v,%v) -> (%v,%v)", sx, sy, gx, gy)
	}
}

func TestViewMatrixExactAtOne(t *testing.T) {
	v := newView(1001, 801, 300, 200) // centring gives .5 offsets
	if v.Scale != 1 || !v.Exact() {
		t.Fatalf("expected 1:1 fit")
	}
	m := v.Matrix()
	if m[2] != math.Round(m[2]) || m[5] != math.Round(m[5]) {
		t.Errorf("1:1 matrix not on whole pixels: %v", m)
	}
	v.Rotate(1)
	m = v.Matrix()
	if m[2] != math.Round(m[2]) || m[5] != math.Round(m[5]) {
		t.Errorf("rotated 1:1 matrix not on whole pixels: %v", m)
	}
}

func TestViewPercent(t *testing.T) {
	v := newView(1000, 800, 3000, 3000)
	if v.Percent() != 27 {
		t.Errorf("percent %v", v.Percent())
	}
}

// srcAt inverts the view matrix: the source-image point shown at viewport
// position (px, py). Used to check that View offsets and Matrix agree.
func srcAt(v *View, px, py float64) (float64, float64) {
	m := v.Matrix()
	det := m[0]*m[4] - m[1]*m[3]
	x, y := px-m[2], py-m[5]
	return (m[4]*x - m[1]*y) / det, (-m[3]*x + m[0]*y) / det
}

func TestViewZoomKeepsSourcePointUnderCursorRotated(t *testing.T) {
	for _, q := range []int{1, 2, 3} {
		v := newView(1000, 800, 4000, 3000)
		v.Rotate(q)
		v.ZoomBy(3, 500, 400) // well above fit: no clamping near the cursor
		px, py := 310.0, 270.0
		sx, sy := srcAt(v, px, py)
		v.ZoomBy(WheelStep, px, py)
		gx, gy := srcAt(v, px, py)
		if !near(gx, sx) || !near(gy, sy) {
			t.Errorf("q=%d: source point under cursor moved (%v,%v) -> (%v,%v)", q, sx, sy, gx, gy)
		}
	}
}

func TestViewRotatedPanClampSwapsAxes(t *testing.T) {
	// 4000x500 rotated a quarter turn is 500x4000: narrower than the
	// 1000x800 viewport horizontally (centred), taller vertically (clamped).
	for _, q := range []int{1, 3} {
		v := newView(1000, 800, 4000, 500)
		v.SetOne()
		v.Rotate(q)
		if v.OX != 250 {
			t.Errorf("q=%d: OX %v, want centred 250", q, v.OX)
		}
		v.Pan(300, 1e6)
		if v.OX != 250 || v.OY != 0 {
			t.Errorf("q=%d: after pan down-right (%v,%v), want (250,0)", q, v.OX, v.OY)
		}
		v.Pan(-300, -1e6)
		if v.OX != 250 || v.OY != 800-4000 {
			t.Errorf("q=%d: after pan up-left (%v,%v), want (250,%v)", q, v.OX, v.OY, 800-4000)
		}
	}
}

func TestViewPanoramaFitAndOne(t *testing.T) {
	v := newView(1000, 800, 30000, 2000)
	if !near(v.Scale, 1.0/30) || !near(v.OX, 0) || !near(v.OY, (800-2000.0/30)/2) {
		t.Errorf("fit: scale %v off (%v,%v)", v.Scale, v.OX, v.OY)
	}
	v.SetOne()
	if v.OX != -14500 || v.OY != -600 {
		t.Errorf("1:1: off (%v,%v)", v.OX, v.OY)
	}
	v.Pan(-1e9, 1e9)
	if v.OX != 1000-30000 || v.OY != 0 {
		t.Errorf("1:1 clamped: off (%v,%v)", v.OX, v.OY)
	}
	if _, _, ok := isIntTranslation(v.Matrix()); !ok {
		t.Error("1:1 panorama should take the plain-copy path")
	}
}

func TestViewFitNeverUpscalesRotated(t *testing.T) {
	v := newView(1000, 800, 300, 900) // fits after a quarter turn at 900x300
	v.Rotate(1)
	if v.Scale != 1 || v.OX != 50 || v.OY != 250 {
		t.Errorf("scale %v off (%v,%v), want 1 at (50,250)", v.Scale, v.OX, v.OY)
	}
	v = newView(1000, 800, 1, 1)
	if v.Scale != 1 || !v.Exact() {
		t.Errorf("1x1: scale %v", v.Scale)
	}
}
