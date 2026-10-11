package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// sidebarWidth is the fixed sidebar width in points.
const sidebarWidth = 220

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

func newSideList(length func() int, name func(int) string) *sideList {
	l := &sideList{}
	l.Length = length
	l.CreateItem = func() fyne.CanvasObject {
		lb := widget.NewLabel("template-name.jpg")
		lb.Truncation = fyne.TextTruncateEllipsis
		return lb
	}
	l.UpdateItem = func(id widget.ListItemID, o fyne.CanvasObject) {
		o.(*widget.Label).SetText(name(id))
	}
	l.ExtendBaseWidget(l)
	return l
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
