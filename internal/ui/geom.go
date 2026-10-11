package ui

// Pure view geometry: no Fyne, no GL. Everything here works in device pixels
// of the viewport and pixels of the (decoded, EXIF-oriented) source image.
//
// Coordinate model. The source image is w×h. Rotating it by q quarter turns
// clockwise gives the "rotated image" of size rw×rh. The rotated image is
// drawn scaled by Scale with its top-left corner at (OX, OY) in the viewport.

import (
	"math"

	"golang.org/x/image/math/f64"
)

// Mode is the zoom mode of the viewport.
type Mode int

const (
	ModeFit  Mode = iota // scale to fit (never above 1.0), centred
	ModeOne              // 1:1, one image pixel per device pixel
	ModeFree             // any scale chosen by wheel or +/-
)

// Zoom constants.
const (
	MaxScale  = 64.0 // largest allowed magnification
	WheelStep = 1.1  // zoom factor per wheel notch
	KeyStep   = 1.25 // zoom factor per +/- key press
)

// NormQuarter maps any number of clockwise quarter turns into 0..3.
func NormQuarter(q int) int {
	q %= 4
	if q < 0 {
		q += 4
	}
	return q
}

// RotatedSize returns the size of a w×h image rotated by q quarter turns.
func RotatedSize(w, h float64, q int) (rw, rh float64) {
	if NormQuarter(q)%2 == 1 {
		return h, w
	}
	return w, h
}

// FitScale is the largest scale at which an rw×rh image fits inside vw×vh,
// capped at 1.0 (fit never upscales). Degenerate input yields 1.
func FitScale(vw, vh, rw, rh float64) float64 {
	if vw <= 0 || vh <= 0 || rw <= 0 || rh <= 0 {
		return 1
	}
	s := math.Min(vw/rw, vh/rh)
	if s > 1 {
		s = 1
	}
	return s
}

// MinScale is the smallest scale the user may zoom out to: a tenth of the
// fit scale (or of 1:1, if the image is smaller than the viewport).
func MinScale(fit float64) float64 {
	return math.Min(fit, 1) / 10
}

// ClampScale bounds s to [MinScale(fit), MaxScale].
func ClampScale(s, fit float64) float64 {
	if lo := MinScale(fit); s < lo {
		s = lo
	}
	if s > MaxScale {
		s = MaxScale
	}
	return s
}

// SnapThroughOne returns 1 when a zoom step from old to next crosses (or
// lands within rounding error of) 1.0, so that wheel and key zooming always
// stop at exact 1:1 on the way through. Otherwise it returns next.
func SnapThroughOne(old, next float64) float64 {
	if math.Abs(next-1) < 1e-9 {
		return 1
	}
	if (old < 1 && next > 1) || (old > 1 && next < 1) {
		return 1
	}
	return next
}

// WheelFactor converts a scroll delta in X11 units (25 per wheel notch; see
// NormalizeWheel) into a zoom factor; positive DY (wheel up) zooms in. The
// number of notches per event is bounded so accelerated touchpad flicks do
// not jump to the zoom limits in one event.
func WheelFactor(dy float64) float64 {
	n := dy / 25
	if n > 5 {
		n = 5
	}
	if n < -5 {
		n = -5
	}
	return math.Pow(WheelStep, n)
}

// waylandWheelUnit is GLFW's Wayland yoff per wheel notch: libinput reports
// 15 per detent and GLFW divides by 10, where X11 reports exactly 1.
const waylandWheelUnit = 1.5

// WaylandSession reports whether Fyne's GLFW driver will run on Wayland,
// decided the way Fyne does: FYNE_PLATFORM=x11|wayland overrides, otherwise
// a set WAYLAND_DISPLAY means Wayland. (Fyne also falls back to XWayland on
// compositors that force client-side decorations; Hyprland does not.)
func WaylandSession(getenv func(string) string) bool {
	switch getenv("FYNE_PLATFORM") {
	case "x11":
		return false
	case "wayland":
		return true
	}
	return getenv("WAYLAND_DISPLAY") != ""
}

// NormalizeWheel converts a Fyne scroll delta to X11 units (25 per notch),
// so a wheel notch zooms by WheelStep on both platforms.
func NormalizeWheel(dy float64, wayland bool) float64 {
	if wayland {
		return dy / waylandWheelUnit
	}
	return dy
}

// CenterOffset is the offset that centres content of length d in a viewport
// of length v.
func CenterOffset(d, v float64) float64 {
	return (v - d) / 2
}

// ClampOffset constrains the offset o of content of length d inside a
// viewport of length v: content smaller than the viewport is centred,
// larger content must cover the viewport (its edges never come inside).
func ClampOffset(o, d, v float64) float64 {
	if d <= v {
		return CenterOffset(d, v)
	}
	if o > 0 {
		return 0
	}
	if o < v-d {
		return v - d
	}
	return o
}

// ZoomAbout returns the new offset on one axis when the scale changes from
// s to ns while keeping the content point under viewport position p fixed.
func ZoomAbout(o, s, ns, p float64) float64 {
	if s == 0 {
		return o
	}
	return p - (p-o)/s*ns
}

// RotatePoint maps a point (x, y) of a rotated image of size rw×rh to the
// same image point after rotating the image by a further dq quarter turns
// clockwise.
func RotatePoint(x, y, rw, rh float64, dq int) (float64, float64) {
	for i := 0; i < NormQuarter(dq); i++ {
		// One clockwise quarter: (x, y) in rw×rh -> (rh-y, x) in rh×rw.
		x, y = rh-y, x
		rw, rh = rh, rw
	}
	return x, y
}

// quarter rotation matrices (clockwise on screen, y pointing down).
var rotTable = [4][4]float64{
	{1, 0, 0, 1},
	{0, -1, 1, 0},
	{-1, 0, 0, -1},
	{0, 1, -1, 0},
}

// Mul returns the affine product a·b (apply b first, then a).
func Mul(a, b f64.Aff3) f64.Aff3 {
	return f64.Aff3{
		a[0]*b[0] + a[1]*b[3], a[0]*b[1] + a[1]*b[4], a[0]*b[2] + a[1]*b[5] + a[2],
		a[3]*b[0] + a[4]*b[3], a[3]*b[1] + a[4]*b[4], a[3]*b[2] + a[4]*b[5] + a[5],
	}
}

// Translate returns a translation matrix.
func Translate(tx, ty float64) f64.Aff3 { return f64.Aff3{1, 0, tx, 0, 1, ty} }

// ScaleM returns a uniform scale matrix.
func ScaleM(s float64) f64.Aff3 { return f64.Aff3{s, 0, 0, 0, s, 0} }

// RotateM returns the matrix for q clockwise quarter turns about the origin.
func RotateM(q int) f64.Aff3 {
	r := rotTable[NormQuarter(q)]
	return f64.Aff3{r[0], r[1], 0, r[2], r[3], 0}
}

// Affine builds the source->viewport matrix for a w×h image rotated by q
// quarter turns about its centre, scaled by s and with the rotated image's
// top-left corner at (ox, oy):
//
//	T(ox,oy) · S(s) · T(rw/2,rh/2) · R(q) · T(-w/2,-h/2)
func Affine(w, h float64, q int, s, ox, oy float64) f64.Aff3 {
	rw, rh := RotatedSize(w, h, q)
	m := Translate(-w/2, -h/2)
	m = Mul(RotateM(q), m)
	m = Mul(Translate(rw/2, rh/2), m)
	m = Mul(ScaleM(s), m)
	m = Mul(Translate(ox, oy), m)
	return m
}

// Apply maps (x, y) through m.
func Apply(m f64.Aff3, x, y float64) (float64, float64) {
	return m[0]*x + m[1]*y + m[2], m[3]*x + m[4]*y + m[5]
}

// View is the complete, display-independent state of the viewport.
type View struct {
	ImgW, ImgH float64 // source image size (0 when nothing is shown)
	Rot        int     // clockwise quarter turns, 0..3
	Mode       Mode
	Scale      float64
	OX, OY     float64 // top-left of the rotated, scaled image in the viewport
	VW, VH     float64 // viewport size in device pixels
	// Locked keeps the mode, scale, pan offsets and rotation across image
	// changes (offsets clamped for the new size) instead of resetting them.
	Locked bool
}

// HasImage reports whether there is a non-empty image to show.
func (v *View) HasImage() bool { return v.ImgW > 0 && v.ImgH > 0 }

// RotSize returns the rotated image size.
func (v *View) RotSize() (float64, float64) { return RotatedSize(v.ImgW, v.ImgH, v.Rot) }

// FitScale returns the fit scale for the current image, rotation and viewport.
func (v *View) FitScale() float64 {
	rw, rh := v.RotSize()
	return FitScale(v.VW, v.VH, rw, rh)
}

func (v *View) centre() {
	rw, rh := v.RotSize()
	v.OX = CenterOffset(rw*v.Scale, v.VW)
	v.OY = CenterOffset(rh*v.Scale, v.VH)
}

func (v *View) clamp() {
	rw, rh := v.RotSize()
	v.OX = ClampOffset(v.OX, rw*v.Scale, v.VW)
	v.OY = ClampOffset(v.OY, rh*v.Scale, v.VH)
}

func (v *View) applyFit() {
	v.Scale = v.FitScale()
	v.centre()
}

// SetViewport updates the viewport size. Fit re-fits; other modes keep the
// image point at the viewport centre where it was, then clamp.
func (v *View) SetViewport(w, h float64) {
	if w == v.VW && h == v.VH {
		return
	}
	dw, dh := w-v.VW, h-v.VH
	v.VW, v.VH = w, h
	if v.Mode == ModeFit || v.Scale == 0 {
		v.applyFit()
		return
	}
	v.OX += dw / 2
	v.OY += dh / 2
	v.clamp()
}

// SetImage switches to a new w×h image. Rotation resets to 0. Fit stays fit,
// 1:1 stays 1:1 (recentred), free zoom resets to fit. When Locked, the
// mode, scale, rotation and pan offsets are kept instead (Fit re-fits; the
// offsets are clamped for the new size).
func (v *View) SetImage(w, h float64) {
	v.ImgW, v.ImgH = w, h
	if v.Locked && v.Scale > 0 {
		if v.Mode == ModeFit {
			v.applyFit()
		} else {
			v.clamp()
		}
		return
	}
	v.Rot = 0
	switch v.Mode {
	case ModeOne:
		v.Scale = 1
		v.centre()
	default:
		v.Mode = ModeFit
		v.applyFit()
	}
}

// SetFit switches to Fit mode.
func (v *View) SetFit() {
	v.Mode = ModeFit
	v.applyFit()
}

// SetOne switches to 1:1, centred.
func (v *View) SetOne() {
	v.Mode = ModeOne
	v.Scale = 1
	v.centre()
}

// SetOneAt switches to 1:1 keeping the image point under (px, py) fixed.
func (v *View) SetOneAt(px, py float64) {
	v.zoomTo(1, px, py)
	v.Mode = ModeOne
}

func (v *View) zoomTo(ns, px, py float64) {
	v.OX = ZoomAbout(v.OX, v.Scale, ns, px)
	v.OY = ZoomAbout(v.OY, v.Scale, ns, py)
	v.Scale = ns
	v.clamp()
}

// ZoomBy multiplies the scale by f about viewport point (px, py). Crossing
// 1.0 snaps to exactly 1:1 (mode One); otherwise the mode becomes Free.
func (v *View) ZoomBy(f, px, py float64) {
	if !v.HasImage() {
		return
	}
	ns := SnapThroughOne(v.Scale, ClampScale(v.Scale*f, v.FitScale()))
	v.zoomTo(ns, px, py)
	if ns == 1 {
		v.Mode = ModeOne
	} else {
		v.Mode = ModeFree
	}
}

// Pan moves the image by (dx, dy) device pixels, clamped.
func (v *View) Pan(dx, dy float64) {
	v.OX += dx
	v.OY += dy
	v.clamp()
}

// Rotate turns the view by dq quarter turns clockwise (negative = CCW).
// In Fit mode the image is re-fitted; otherwise the image point at the
// viewport centre stays at the centre.
func (v *View) Rotate(dq int) {
	if v.Mode == ModeFit || !v.HasImage() {
		v.Rot = NormQuarter(v.Rot + dq)
		v.applyFit()
		return
	}
	rw, rh := v.RotSize()
	cx, cy := v.VW/2, v.VH/2
	x, y := (cx-v.OX)/v.Scale, (cy-v.OY)/v.Scale
	x, y = RotatePoint(x, y, rw, rh, dq)
	v.Rot = NormQuarter(v.Rot + dq)
	v.OX = cx - x*v.Scale
	v.OY = cy - y*v.Scale
	v.clamp()
}

// SetRot turns the view to rotation q (any number of quarter turns), the
// way Rotate does: Fit re-fits, other modes keep the image point at the
// viewport centre. A no-op when already there.
func (v *View) SetRot(q int) {
	if d := NormQuarter(q) - v.Rot; d != 0 {
		v.Rotate(d)
	}
}

// Matrix returns the source->viewport matrix. At exactly 1:1 the offsets
// are rounded so the result is a pixel-exact copy.
func (v *View) Matrix() f64.Aff3 {
	ox, oy := v.OX, v.OY
	if v.Scale == 1 {
		ox, oy = math.Round(ox), math.Round(oy)
	}
	return Affine(v.ImgW, v.ImgH, v.Rot, v.Scale, ox, oy)
}

// Exact reports whether rendering is a pixel-exact copy (no resampling), in
// which case no high-quality settle pass is needed.
func (v *View) Exact() bool { return v.Scale == 1 }

// Percent is the zoom as a rounded percentage.
func (v *View) Percent() int { return int(math.Round(v.Scale * 100)) }
