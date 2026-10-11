package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// sidebarWidth is the fixed sidebar width in points.
const sidebarWidth = 280

// sideList is a widget.List that never swallows keys: when a click leaves
// it focused, every key, rune and shortcut is forwarded to the viewer
// (including Tab, which it claims via AcceptsTab).
type sideList struct {
	widget.List

	onKey      func(*fyne.KeyEvent)
	onRune     func(rune)
	onShortcut func(fyne.Shortcut)
}

var (
	_ fyne.Focusable    = (*sideList)(nil)
	_ fyne.Tabbable     = (*sideList)(nil)
	_ fyne.Shortcutable = (*sideList)(nil)
)

// newSideList lists label(i) for each entry. With twoLine, every row has
// room for a second, smaller and dimmer line showing the label's directory
// part under the file name (rows keep one fixed height; a label without a
// directory part gets its name centred). Without it, rows are one line and
// show only the file name.
func newSideList(length func() int, label func(int) string, twoLine bool) *sideList {
	l := &sideList{}
	l.Length = length
	l.CreateItem = func() fyne.CanvasObject {
		name := widget.NewLabel("template-name.jpg")
		name.Truncation = fyne.TextTruncateEllipsis
		if !twoLine {
			return name
		}
		dir := widget.NewLabel("template/dir")
		dir.Truncation = fyne.TextTruncateEllipsis
		dir.SizeName = theme.SizeNameCaptionText
		dir.Importance = widget.LowImportance
		return container.New(twoLines{}, name, dir)
	}
	l.UpdateItem = func(id widget.ListItemID, o fyne.CanvasObject) {
		name, dir := splitLabel(label(id))
		c, ok := o.(*fyne.Container)
		if !ok {
			o.(*widget.Label).SetText(name)
			return
		}
		c.Objects[0].(*widget.Label).SetText(name)
		d := c.Objects[1].(*widget.Label)
		d.SetText(dir)
		if (dir != "") != d.Visible() {
			if dir != "" {
				d.Show()
			} else {
				d.Hide()
			}
			c.Refresh() // re-run the layout to (un)centre the name
		}
	}
	l.ExtendBaseWidget(l)
	return l
}

// splitLabel splits a display label into the file name and its directory
// part ("" when there is none): "sub/dir/a.jpg" -> "a.jpg", "sub/dir".
func splitLabel(label string) (name, dir string) {
	i := strings.LastIndexByte(label, '/')
	switch {
	case i < 0:
		return label, ""
	case i == 0:
		return label[1:], "/"
	}
	return label[i+1:], label[:i]
}

// hasDirParts reports whether any label has a directory part.
func hasDirParts(n int, label func(int) string) bool {
	for i := range n {
		if _, dir := splitLabel(label(i)); dir != "" {
			return true
		}
	}
	return false
}

// twoLines stacks a name label and a smaller directory label, overlapping
// their inner padding so the texts sit a line spacing apart.
// Its height is the same whether or not the second label is visible, so
// list rows stay one fixed height; a lone first label is centred.
type twoLines struct{}

func (twoLines) MinSize(objs []fyne.CanvasObject) fyne.Size {
	a, b := objs[0].MinSize(), objs[1].MinSize()
	return fyne.NewSize(max(a.Width, b.Width), a.Height+b.Height-twoLinesOverlap())
}

// twoLinesOverlap is how far the second label moves up over the first: the
// two labels' inner padding between the texts, less one line spacing.
func twoLinesOverlap() float32 {
	return max(0, 2*theme.InnerPadding()-theme.LineSpacing())
}

func (twoLines) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	a, b := objs[0].MinSize().Height, objs[1].MinSize().Height
	if !objs[1].Visible() {
		objs[0].Move(fyne.NewPos(0, (size.Height-a)/2))
		objs[0].Resize(fyne.NewSize(size.Width, a))
		return
	}
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(fyne.NewSize(size.Width, a))
	objs[1].Move(fyne.NewPos(0, a-twoLinesOverlap()))
	objs[1].Resize(fyne.NewSize(size.Width, b))
}

func (l *sideList) TypedKey(e *fyne.KeyEvent) {
	if l.onKey != nil {
		l.onKey(e)
	}
}

func (l *sideList) TypedRune(r rune) {
	if l.onRune != nil {
		l.onRune(r)
	}
}

func (l *sideList) TypedShortcut(s fyne.Shortcut) {
	if l.onShortcut != nil {
		l.onShortcut(s)
	}
}

func (l *sideList) AcceptsTab() bool { return true }

// fixedWidth is a layout that gives its objects a fixed minimum width and
// stretches them over the full area.
type fixedWidth float32

func (f fixedWidth) MinSize(objs []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objs {
		if o.Visible() {
			h = max(h, o.MinSize().Height)
		}
	}
	return fyne.NewSize(float32(f), h)
}

func (f fixedWidth) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}
