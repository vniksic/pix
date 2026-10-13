package ui

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// helpRow is one key (or set of keys) and what it does.
type helpRow struct{ keys, action string }

// helpGroups is the key list shown by ? and F1, in groups separated by a
// blank line. README.md's "Keys" table is the source of truth;
// TestHelpMatchesReadme keeps the two in step.
var helpGroups = [][]helpRow{
	{
		{"PageDown, Space, Right, Down, n", "next image"},
		{"PageUp, Backspace, Left, Up, p", "previous image"},
		{"Home / End", "first / last image"},
	},
	{
		{"f", "toggle fullscreen (enters in fit mode)"},
		{"z", "1:1 pixels (in fullscreen also hides the status bar until x)"},
		{"x", "fit to window"},
		{"+ / = / -", "zoom in / out"},
		{"[ / ]", "rotate 90° left / right (view only; remembered per photo for the session)"},
		{"l", "lock the view: zoom, position and rotation stay when changing image"},
	},
	{
		{"m", "mark / unmark the image"},
		{"Enter", "copy the marked images to the --out directory"},
		{"Delete", "press twice (within 1.5 s) to move the file to the trash"},
		{"Shift+Delete", "delete the file permanently (asks first)"},
		{"o", "open the file in another program ($PIX_OPEN, else xdg-open)"},
	},
	{
		{"Ctrl+C", "copy image to clipboard (as displayed)"},
		{"Ctrl+Shift+C", "copy file path to clipboard"},
	},
	{
		{"Tab, b", "toggle the file list"},
		{"r", "rescan the directory"},
	},
	{
		{"?, F1", "show / hide this help"},
		{"Escape", "leave fullscreen, or quit"},
		{"q", "quit"},
	},
}

// Help text layout, in characters (the overlay uses a monospace font).
const (
	helpKeyWidth = 18 // key column; longer key lists wrap at spaces
	helpGap      = 2  // between the columns
	helpWidth    = 60 // whole line; actions wrap to fit
)

// helpText lays out helpGroups in two aligned columns, keys left and action
// right, both wrapped at spaces.
func helpText() string {
	var b strings.Builder
	for i, g := range helpGroups {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, r := range g {
			keys := wrapWords(r.keys, helpKeyWidth)
			act := wrapWords(r.action, helpWidth-helpKeyWidth-helpGap)
			for j := range max(len(keys), len(act)) {
				var k, a string
				if j < len(keys) {
					k = keys[j]
				}
				if j < len(act) {
					a = act[j]
				}
				pad := strings.Repeat(" ", max(0, helpKeyWidth+helpGap-utf8.RuneCountInString(k)))
				b.WriteString(strings.TrimRight(k+pad+a, " ") + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// wrapWords breaks s at spaces into lines of at most width runes (a single
// longer word gets a line of its own).
func wrapWords(s string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// helpOverlay shows the help text over the viewport: a translucent dark
// rectangle with the lines centred on it, cut off at the bottom when the
// viewport is too short. It is deliberately not Focusable, so the viewport
// keeps the keyboard and every key still reaches the viewer's handlers.
// Its MinSize is zero so it never makes the window's minimum size bigger.
type helpOverlay struct {
	widget.BaseWidget
	text   string
	onDown func() // a mouse button went down on it
}

var (
	_ desktop.Mouseable = (*helpOverlay)(nil)
	_ fyne.Scrollable   = (*helpOverlay)(nil)
)

func newHelpOverlay(text string) *helpOverlay {
	h := &helpOverlay{text: text}
	h.ExtendBaseWidget(h)
	h.Hide()
	return h
}

func (h *helpOverlay) CreateRenderer() fyne.WidgetRenderer {
	r := &helpRenderer{bg: canvas.NewRectangle(color.NRGBA{R: 0x08, G: 0x08, B: 0x08, A: 0xe0})}
	r.objs = []fyne.CanvasObject{r.bg}
	for _, l := range strings.Split(h.text, "\n") {
		t := canvas.NewText(l, color.Gray{Y: 0xe8})
		t.TextStyle = fyne.TextStyle{Monospace: true}
		t.TextSize = theme.TextSize()
		r.lines = append(r.lines, t)
		r.objs = append(r.objs, t)
	}
	return r
}

func (h *helpOverlay) MouseDown(*desktop.MouseEvent) {
	if h.onDown != nil {
		h.onDown()
	}
}

func (h *helpOverlay) MouseUp(*desktop.MouseEvent) {}

// Scrolled swallows the wheel, so the image under the help is not zoomed
// unseen.
func (h *helpOverlay) Scrolled(*fyne.ScrollEvent) {}

type helpRenderer struct {
	bg    *canvas.Rectangle
	lines []*canvas.Text
	objs  []fyne.CanvasObject
}

func (r *helpRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)
	if len(r.lines) == 0 {
		return
	}
	style := r.lines[0].TextStyle
	ts := r.lines[0].TextSize
	lineH := fyne.MeasureText("M", ts, style).Height
	var blockW float32
	for _, t := range r.lines {
		blockW = max(blockW, fyne.MeasureText(t.Text, ts, style).Width)
	}
	pad := theme.Padding() * 2
	// Centred when it fits; otherwise from the top (or left), cut off.
	x := max(pad, (size.Width-blockW)/2)
	y := max(pad, (size.Height-lineH*float32(len(r.lines)))/2)
	for i, t := range r.lines {
		ty := y + lineH*float32(i)
		if ty+lineH > size.Height {
			t.Hide() // no clipping in Fyne: lines that do not fit are not drawn
			continue
		}
		t.Show()
		t.Move(fyne.NewPos(x, ty))
		t.Resize(fyne.NewSize(blockW, lineH))
	}
}

func (r *helpRenderer) MinSize() fyne.Size { return fyne.NewSize(0, 0) }

func (r *helpRenderer) Refresh() {
	for _, o := range r.objs {
		o.Refresh()
	}
}

func (r *helpRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *helpRenderer) Destroy()                     {}
