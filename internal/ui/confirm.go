package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// confirmWidth is the width of the confirmation box; a long file label is
// cut short with an ellipsis rather than widening it past the window.
const confirmWidth = 440

// confirmBox is a modal yes/no question: a title, a message, the subject
// (a file label) and Cancel / confirm buttons. It is fyne's confirm dialog
// built from widgets: the dialog package would pull a module into the
// build that pix does not otherwise need. Like that dialog it has no key
// handling of its own (the viewer feeds it Enter and Escape, see
// confirmKey), and done runs once, after the box is gone: true from
// Confirm, false from Hide.
type confirmBox struct {
	pop    *widget.PopUp
	done   func(bool)
	closed bool
}

func newConfirmBox(title, subject, msg, confirm string, done func(bool), c fyne.Canvas) *confirmBox {
	b := &confirmBox{done: done}
	head := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	subj := widget.NewLabel(subject)
	subj.Truncation = fyne.TextTruncateEllipsis
	cancel := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), b.Hide)
	ok := widget.NewButtonWithIcon(confirm, theme.DeleteIcon(), b.Confirm)
	ok.Importance = widget.DangerImportance
	body := container.NewVBox(head, subj, widget.NewLabel(msg), container.NewGridWithColumns(2, cancel, ok))
	b.pop = widget.NewModalPopUp(container.New(fixedWidth(confirmWidth), body), c)
	return b
}

func (b *confirmBox) Show() { b.pop.Show() }

// Confirm closes the box with the answer yes.
func (b *confirmBox) Confirm() { b.close(true) }

// Hide closes the box with the answer no.
func (b *confirmBox) Hide() { b.close(false) }

func (b *confirmBox) close(ok bool) {
	if b.closed {
		return
	}
	b.closed = true
	b.pop.Hide()
	b.done(ok)
}
