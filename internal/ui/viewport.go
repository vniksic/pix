package ui

import (
	"image"
	"image/color"
	"math"
	"os"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// onWayland says whether scroll deltas arrive in GLFW's Wayland units.
var onWayland = WaylandSession(os.Getenv)

// settleDelay is how long the view must be idle before the high-quality
// (CatmullRom) frame is rendered.
const settleDelay = 120 * time.Millisecond

// frameKey identifies everything a rendered frame depends on.
type frameKey struct {
	img    *image.NRGBA
	w, h   int
	rot    int
	scale  float64
	ox, oy float64
	mip    int // mip level rendered from (0 = the original)
}

type frame struct {
	key     frameKey
	img     *image.RGBA
	settled bool // final quality; nothing more to do for this key
}

// viewport is the image display widget: a canvas.Raster whose generator
// renders the visible part of the image at device resolution.
//
// All fields are owned by the Fyne main goroutine, except that the settle
// goroutine reads the (immutable) source image or mip level, and mips
// synchronises with its own builder goroutine.
type viewport struct {
	widget.BaseWidget

	view   View
	img    *image.NRGBA
	opaque bool
	mips   *mipChain // downscaled copies of img, built on demand

	raster *canvas.Raster
	msg    *canvas.Text

	cur         frame
	fastBuf     *image.RGBA
	settleTimer *time.Timer
	settleGen   atomic.Uint64 // bumped on every state change; read by settle goroutines

	pxRatio float64 // device pixels per point, learned from the generator

	dragOK   bool
	dragMult float64
	dragging bool

	// Callbacks into the viewer.
	onChange   func()               // view state changed (status bar)
	onOne      func()               // double-click chose 1:1
	onKey      func(*fyne.KeyEvent) // routed key presses (repeats included)
	onKeyDown  func(*fyne.KeyEvent) // routed physical key-downs (no repeats)
	onRune     func(rune)           // routed characters
	onShortcut func(fyne.Shortcut)  // routed shortcuts
	onFocusOff func()               // focus lost
	focusSelf  func()               // ask the canvas to focus us
	// onFirstFrame runs once, via fyne.Do, after the first frame has been
	// generated, i.e. once the window is about to be mapped.
	onFirstFrame func()
}

var (
	_ fyne.Widget         = (*viewport)(nil)
	_ fyne.Focusable      = (*viewport)(nil)
	_ fyne.Tabbable       = (*viewport)(nil)
	_ fyne.Shortcutable   = (*viewport)(nil)
	_ fyne.Draggable      = (*viewport)(nil)
	_ fyne.Scrollable     = (*viewport)(nil)
	_ fyne.DoubleTappable = (*viewport)(nil)
	_ desktop.Mouseable   = (*viewport)(nil)
	_ desktop.Cursorable  = (*viewport)(nil)
	_ desktop.Keyable     = (*viewport)(nil)
)

func newViewport() *viewport {
	v := &viewport{dragMult: 1}
	v.raster = canvas.NewRaster(v.generate)
	v.raster.ScaleMode = canvas.ImageScalePixels
	v.msg = canvas.NewText("", color.Gray{Y: 0xc0})
	v.msg.Alignment = fyne.TextAlignCenter
	v.msg.Hide()
	v.ExtendBaseWidget(v)
	return v
}

func (v *viewport) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(container.NewStack(v.raster, container.NewCenter(v.msg)))
}

func (v *viewport) MinSize() fyne.Size { return fyne.NewSize(64, 64) }

// Resize keeps the View's viewport size (device pixels) in step with the
// widget size. The raster generator re-syncs with the exact texture size.
func (v *viewport) Resize(s fyne.Size) {
	v.BaseWidget.Resize(s)
	r := v.pxRatio
	if r <= 0 {
		r = 1
		if c := fyne.CurrentApp().Driver().CanvasForObject(v); c != nil {
			r = float64(c.Scale())
		}
	}
	w := math.Round(float64(s.Width) * r)
	h := math.Round(float64(s.Height) * r)
	if w != v.view.VW || h != v.view.VH {
		v.view.SetViewport(w, h)
		v.changed()
	}
}

// pxPerPoint converts Fyne points (event coordinates) to device pixels.
func (v *viewport) pxPerPoint() float64 {
	if w := v.Size().Width; w > 0 && v.view.VW > 0 {
		return v.view.VW / float64(w)
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(v); c != nil {
		return float64(c.Scale())
	}
	return 1
}

// changed redraws and notifies the viewer that the view state changed.
func (v *viewport) changed() {
	v.raster.Refresh()
	if v.onChange != nil {
		v.onChange()
	}
}

// SetImage shows img (opaque says whether it has no transparent pixels).
func (v *viewport) SetImage(img *image.NRGBA, opaque bool) {
	v.dropMips()
	if img != nil {
		var c *mipChain
		c = newMipChain(img, func() {
			fyne.Do(func() {
				if v.mips == c {
					v.raster.Refresh() // the frame key now names a better level
				}
			})
		})
		v.mips = c
	}
	v.img = img
	v.opaque = opaque
	b := img.Bounds()
	v.view.SetImage(float64(b.Dx()), float64(b.Dy()))
	v.msg.Hide()
	v.changed()
}

// SetMessage clears the image and shows a plain text message instead.
func (v *viewport) SetMessage(msg string) {
	v.dropMips()
	v.img = nil
	v.opaque = false
	v.view.ImgW, v.view.ImgH = 0, 0
	if !v.view.Locked {
		v.view.Rot = 0
	}
	v.msg.Text = msg
	v.msg.Show()
	v.msg.Refresh()
	v.changed()
}

func (v *viewport) dropMips() {
	if v.mips != nil {
		v.mips.close()
		v.mips = nil
	}
}

// mipWant is the mip level the current scale calls for.
func (v *viewport) mipWant() int {
	b := v.img.Bounds()
	return mipLevelFor(b.Dx(), b.Dy(), v.view.Scale)
}

func (v *viewport) key(w, h int) frameKey {
	k := frameKey{img: v.img, w: w, h: h}
	if v.img != nil {
		k.rot, k.scale, k.ox, k.oy = v.view.Rot, v.view.Scale, v.view.OX, v.view.OY
		if v.mips != nil {
			_, k.mip = v.mips.best(v.mipWant())
		}
	}
	return k
}

// source returns the image to render the current view from (mip level lvl,
// or the original) and the matrix that maps it to the viewport.
func (v *viewport) source(lvl int) (*image.NRGBA, f64.Aff3) {
	m := v.view.Matrix()
	if v.mips == nil || lvl == 0 {
		return v.img, m
	}
	src, _ := v.mips.best(lvl)
	b, sb := v.img.Bounds(), src.Bounds()
	return src, mipMatrix(m, b.Dx(), b.Dy(), sb.Dx(), sb.Dy())
}

// generate is the raster generator: w, h are device pixels. It runs on the
// main goroutine during painting.
func (v *viewport) generate(w, h int) image.Image {
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if f := v.onFirstFrame; f != nil {
		v.onFirstFrame = nil
		fyne.Do(f) // not during paint
	}
	if sw := v.Size().Width; sw > 0 {
		v.pxRatio = float64(w) / float64(sw)
	}
	if float64(w) != v.view.VW || float64(h) != v.view.VH {
		v.view.SetViewport(float64(w), float64(h))
		if v.onChange != nil {
			fyne.Do(v.onChange) // not during paint
		}
	}
	if v.img != nil && v.mips != nil {
		v.mips.request(v.mipWant()) // background; until built, key() picks a lower level
	}
	k := v.key(w, h)
	if v.cur.img != nil && v.cur.key == k {
		return v.cur.img
	}
	if v.fastBuf == nil || v.fastBuf.Bounds().Dx() != w || v.fastBuf.Bounds().Dy() != h {
		v.fastBuf = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	buf := v.fastBuf
	if v.img == nil || !v.view.HasImage() {
		fillBackground(buf)
		v.cur = frame{key: k, img: buf, settled: true}
		return buf
	}
	src, m := v.source(k.mip)
	renderFrame(buf, src, m, xdraw.ApproxBiLinear, v.opaque, nil)
	exact := v.view.Exact()
	v.cur = frame{key: k, img: buf, settled: exact}
	if !exact {
		v.scheduleSettle()
	} else {
		v.cancelSettle()
	}
	return buf
}

func (v *viewport) cancelSettle() {
	v.settleGen.Add(1)
	if v.settleTimer != nil {
		v.settleTimer.Stop()
		v.settleTimer = nil
	}
}

// scheduleSettle (re)starts the idle timer for the high-quality render.
func (v *viewport) scheduleSettle() {
	v.cancelSettle()
	gen := v.settleGen.Load()
	v.settleTimer = time.AfterFunc(settleDelay, func() {
		fyne.Do(func() { v.startSettle(gen) })
	})
}

// startSettle snapshots the state on the main goroutine and renders the
// CatmullRom frame in the background. The result is used only if the state
// is still the same when it finishes; a newer interaction (settleGen bump)
// abandons the render between strips.
func (v *viewport) startSettle(gen uint64) {
	if gen != v.settleGen.Load() || v.cur.settled || v.img == nil {
		return
	}
	if v.dragging {
		v.scheduleSettle() // mouse still held: wait for DragEnd or more idle time
		return
	}
	k := v.cur.key
	if k != v.key(k.w, k.h) {
		return // state moved on; the next generate call reschedules
	}
	src, m := v.source(k.mip)
	opaque := v.opaque
	stale := func() bool { return v.settleGen.Load() != gen }
	go func() {
		dst := image.NewRGBA(image.Rect(0, 0, k.w, k.h))
		if !renderFrame(dst, src, m, xdraw.CatmullRom, opaque, stale) {
			return
		}
		fyne.Do(func() {
			if stale() || v.key(k.w, k.h) != k {
				return
			}
			v.cur = frame{key: k, img: dst, settled: true}
			v.raster.Refresh()
		})
	}()
}

// ---- input ----

func (v *viewport) MouseDown(e *desktop.MouseEvent) {
	if v.focusSelf != nil {
		v.focusSelf()
	}
	v.dragOK = e.Button == desktop.MouseButtonPrimary
	v.dragMult = 1
	if e.Modifier&fyne.KeyModifierShift != 0 {
		v.dragMult = 4
	}
}

func (v *viewport) MouseUp(*desktop.MouseEvent) {}

func (v *viewport) Dragged(e *fyne.DragEvent) {
	if !v.dragOK || !v.view.HasImage() {
		return
	}
	v.dragging = true
	r := v.pxPerPoint() * v.dragMult
	v.view.Pan(float64(e.Dragged.DX)*r, float64(e.Dragged.DY)*r)
	v.changed()
}

func (v *viewport) DragEnd() {
	v.dragging = false
	v.dragOK = false
	if !v.cur.settled {
		v.scheduleSettle()
	}
}

func (v *viewport) Scrolled(e *fyne.ScrollEvent) {
	if !v.view.HasImage() || e.Scrolled.DY == 0 {
		return
	}
	r := v.pxPerPoint()
	v.view.ZoomBy(WheelFactor(NormalizeWheel(float64(e.Scrolled.DY), onWayland)), float64(e.Position.X)*r, float64(e.Position.Y)*r)
	v.changed()
}

func (v *viewport) DoubleTapped(e *fyne.PointEvent) {
	if !v.view.HasImage() {
		return
	}
	if v.view.Mode == ModeFit {
		r := v.pxPerPoint()
		v.view.SetOneAt(float64(e.Position.X)*r, float64(e.Position.Y)*r)
		if v.onOne != nil {
			// onOne may hide the status bar and grow the viewport downwards;
			// pan back so the clicked point stays under the cursor.
			ox, oy := v.view.OX, v.view.OY
			v.onOne()
			v.view.Pan(ox-v.view.OX, oy-v.view.OY)
		}
	} else {
		v.view.SetFit()
	}
	v.changed()
}

func (v *viewport) Cursor() desktop.Cursor {
	if v.dragging {
		return desktop.PointerCursor // the closest thing to a grab hand Fyne offers
	}
	return desktop.DefaultCursor
}

// Focus and key routing: the viewport is normally the focused widget so it
// receives every key (including Tab, via AcceptsTab) and forwards them.

func (v *viewport) FocusGained() {}

func (v *viewport) FocusLost() {
	if v.onFocusOff != nil {
		v.onFocusOff()
	}
}

func (v *viewport) TypedRune(r rune) {
	if v.onRune != nil {
		v.onRune(r)
	}
}

func (v *viewport) TypedKey(e *fyne.KeyEvent) {
	if v.onKey != nil {
		v.onKey(e)
	}
}

// KeyDown sees each physical press once; auto-repeat only reaches TypedKey.
func (v *viewport) KeyDown(e *fyne.KeyEvent) {
	if v.onKeyDown != nil {
		v.onKeyDown(e)
	}
}

func (v *viewport) KeyUp(*fyne.KeyEvent) {}

func (v *viewport) TypedShortcut(s fyne.Shortcut) {
	if v.onShortcut != nil {
		v.onShortcut(s)
	}
}

func (v *viewport) AcceptsTab() bool { return true }
