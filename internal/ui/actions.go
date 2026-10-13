package ui

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"pix/internal/fileops"
	"pix/internal/scan"
)

// ---- marks ----

func (u *viewer) isMarked(path string) bool {
	u.markMu.Lock()
	defer u.markMu.Unlock()
	return u.marks[path]
}

// isMarkedAt reports whether playlist entry i is marked (for the sidebar).
func (u *viewer) isMarkedAt(i int) bool {
	return i >= 0 && i < len(u.entries) && u.isMarked(u.entries[i].Path)
}

// recountMarks recomputes nMarked after the playlist changed.
func (u *viewer) recountMarks() {
	u.markMu.Lock()
	defer u.markMu.Unlock()
	n := 0
	if len(u.marks) > 0 {
		for _, e := range u.entries {
			if u.marks[e.Path] {
				n++
			}
		}
	}
	u.nMarked = n
}

// toggleMark marks or unmarks the current image.
func (u *viewer) toggleMark() {
	if len(u.entries) == 0 {
		return
	}
	p := u.entries[u.idx].Path
	u.markMu.Lock()
	on := !u.marks[p]
	if on {
		u.marks[p] = true
	} else {
		delete(u.marks, p)
	}
	u.markMu.Unlock()
	if on {
		u.nMarked++
		// Marking says "this one, as shown": keep the rotation it is shown
		// with (a locked view may have carried it over), so the copy gets it.
		u.rememberRot()
	} else {
		u.nMarked--
	}
	u.list.RefreshItem(u.idx)
	u.updateStatus()
}

// copyMarked copies the marked files not copied yet to OutDir, in playlist
// order, off the main goroutine.
func (u *viewer) copyMarked() {
	dir := u.opts.OutDir
	switch {
	case dir == "":
		u.flashMsg("no -out directory")
		return
	case u.outBusy:
		u.flashMsg("still copying…")
		return
	}
	var pending []string
	u.markMu.Lock()
	for _, e := range u.entries {
		if u.marks[e.Path] && !u.copied[e.Path] {
			pending = append(pending, e.Path)
		}
	}
	u.markMu.Unlock()
	if len(pending) == 0 {
		if u.nMarked == 0 {
			u.flashMsg("nothing marked")
		} else {
			u.flashMsg("nothing new to copy")
		}
		return
	}
	rots := u.copyRots()
	u.outBusy = true
	u.flashMsg(fmt.Sprintf("copying %d to %s …", len(pending), dir))
	u.bg.Add(1)
	go func() {
		defer u.bg.Done()
		res := fileops.CopyAll(pending, dir, func(p string) int { return rots[p] })
		u.markMu.Lock()
		for i, r := range res {
			if r.Err == nil && i < len(pending) {
				u.copied[pending[i]] = true
			}
		}
		u.markMu.Unlock()
		msg, _ := copySummary(res, dir)
		fyne.Do(func() {
			u.outBusy = false
			if !u.quitting {
				u.flashMsg(msg)
			}
		})
	}()
}

// copyRots returns the rotation each photo is copied out with: the one
// remembered for it and, for the image on screen, the one it is shown
// with, which a locked view may have carried over without remembering it.
// Main goroutine only; the copy gets its own map.
func (u *viewer) copyRots() map[string]int {
	rots := maps.Clone(u.rots)
	if len(u.entries) > 0 && u.vp != nil && u.vp.img != nil && !u.loading && u.loadErr == nil {
		if rots == nil {
			rots = make(map[string]int)
		}
		rots[u.entries[u.idx].Path] = u.vp.view.Rot
	}
	return rots
}

// ---- trash ----

// isModifierKey reports whether k is a modifier on its own, which must not
// cancel a pending Delete (it may be pressed on the way to another key).
func isModifierKey(k fyne.KeyName) bool {
	switch k {
	case desktop.KeyShiftLeft, desktop.KeyShiftRight,
		desktop.KeyControlLeft, desktop.KeyControlRight,
		desktop.KeyAltLeft, desktop.KeyAltRight,
		desktop.KeySuperLeft, desktop.KeySuperRight,
		desktop.KeyCapsLock:
		return true
	}
	return false
}

// keyDown sees each physical key press once (auto-repeat goes only to
// typedKey). It comes before the press's typedKey, typedShortcut and
// typedRune.
//
// A Delete press only sets delDown here; its typedKey runs deleteKey. The
// event carries no modifiers, and with Shift the driver sends the press to
// typedShortcut (as a secondary Cut) instead of typedKey, so only there is
// a plain Delete told apart from Shift+Delete. Auto-repeat reaches typedKey
// without coming here, so it never counts as a second press.
func (u *viewer) keyDown(e *fyne.KeyEvent) {
	if u.confirm != nil {
		if e.Name == fyne.KeyTab {
			// Fyne moves focus into the dialog on Tab, after this; a focused
			// button would keep Enter and Escape from reaching us. Take the
			// focus back once the press has been handled.
			fyne.Do(func() {
				if u.confirm != nil {
					u.win.Canvas().Unfocus()
				}
			})
		}
		return
	}
	if !isModifierKey(e.Name) {
		u.confirmClosed = false
		u.helpPress()
		u.delDown = e.Name == fyne.KeyDelete
	}
}

// deleteKey asks for confirmation on the first press and trashes the
// current file on a second press within deleteWindow.
func (u *viewer) deleteKey() {
	if len(u.entries) == 0 {
		return
	}
	e := u.entries[u.idx]
	if u.trashing[e.Path] {
		return
	}
	if u.delPath == e.Path && time.Since(u.delAt) <= deleteWindow {
		u.delPath, u.delPrompt = "", ""
		u.trash(e)
		return
	}
	u.delPath, u.delAt = e.Path, time.Now()
	u.delPrompt = "Delete again to trash " + e.Name
	u.flashFor(u.delPrompt, deleteWindow) // gone when the second press would no longer count
}

// helpPress closes the help on any key press (modifiers alone excepted);
// the key is then handled as usual. Every press passes through keyDown
// before anything else sees it, so this is the one place that decides. ? and
// F1 open the help and Escape leaves fullscreen or quits, but not on the
// press that closed it: helpClosed tells their handlers, and stays set
// through that key's auto-repeat, so holding Escape does not quit either.
// The ? rune comes after its key, so closing cannot happen in typedRune.
func (u *viewer) helpPress() {
	u.helpClosed = u.help.Visible()
	if u.helpClosed {
		u.setHelp(false)
	}
}

// cancelDelete forgets a pending Delete and its prompt.
func (u *viewer) cancelDelete() {
	if u.delPath == "" {
		return
	}
	u.delPath = ""
	if u.flash == u.delPrompt {
		u.flash = ""
		u.flashToken++
		u.updateStatus()
	}
	u.delPrompt = ""
}

// ---- permanent delete ----

// confirmDelete opens the dialog that asks before deleting the current file
// for good (Shift+Delete). Confirming deletes the file the dialog names,
// even if the playlist changed meanwhile.
func (u *viewer) confirmDelete() {
	if len(u.entries) == 0 || u.confirm != nil {
		return
	}
	e := u.entries[u.idx]
	if u.trashing[e.Path] {
		return
	}
	u.confirm = newConfirmBox("Delete permanently?", e.Label,
		"will be deleted for good. It will not go to the trash.", "Delete",
		func(ok bool) {
			// Cancel and Escape (Hide) come here too, with ok false.
			u.confirm = nil
			if ok && !u.quitting {
				u.deleteFile(e)
			}
			u.focusViewport()
		}, u.win.Canvas())
	u.confirm.Show()
}

// confirmKey handles a key while the dialog is open: Enter deletes, Escape
// cancels, and nothing else does anything, so a key meant for the dialog
// never pages, quits or leaves fullscreen behind it.
func (u *viewer) confirmKey(k fyne.KeyName) {
	switch k {
	case fyne.KeyReturn, fyne.KeyEnter:
		u.confirmClosed = true
		u.confirm.Confirm()
	case fyne.KeyEscape:
		u.confirmClosed = true
		u.confirm.Hide() // runs the callback with false
	}
}

// trash moves e to the trash; deleteFile removes it for good.
func (u *viewer) trash(e scan.Entry)      { u.remove(e, fileops.Trash, "trashed") }
func (u *viewer) deleteFile(e scan.Entry) { u.remove(e, fileops.Delete, "deleted") }

// remove runs op (fileops.Trash or fileops.Delete) on e off the main
// goroutine, then drops it from the playlist (see removed). trashing keeps
// a second operation off a file while one is in flight.
func (u *viewer) remove(e scan.Entry, op func(string) error, done string) {
	if u.trashing[e.Path] {
		return
	}
	u.trashing[e.Path] = true
	u.bg.Add(1)
	go func() {
		defer u.bg.Done()
		err := op(e.Path)
		if err == nil {
			// Here rather than in removed, which does not run once quitting:
			// the exit report must not list a file that is gone.
			u.markMu.Lock()
			delete(u.marks, e.Path)
			delete(u.copied, e.Path)
			u.markMu.Unlock()
		}
		fyne.Do(func() { u.removed(e, err, done) })
	}()
}

// removed finishes a trash or delete: on success the entry leaves the
// playlist, the next image (or the previous, if it was the last) is shown
// and "<done> <name>" flashes.
func (u *viewer) removed(e scan.Entry, err error, done string) {
	delete(u.trashing, e.Path)
	if u.quitting {
		return
	}
	if err != nil {
		u.flashMsg(err.Error())
		return
	}
	u.loader.Invalidate(e.Path)
	delete(u.rots, e.Path)
	u.gone[e.Path] = true // a scan in progress may have listed it already
	if u.pinned == e.Path {
		u.pinned = ""
	}
	if i := slices.IndexFunc(u.entries, func(x scan.Entry) bool { return x.Path == e.Path }); i >= 0 {
		u.entries = slices.Delete(u.entries, i, i+1)
		u.recountMarks()
		u.list.UnselectAll()
		u.list.Refresh()
		switch {
		case len(u.entries) == 0:
			u.showNone()
		case i == u.idx:
			u.jump(min(i, len(u.entries)-1)) // a pending step was relative to the removed file
		case i < u.idx:
			u.idx--
			u.syncList()
		default:
			u.syncList()
		}
	} else {
		u.recountMarks()
	}
	u.flashMsg(done + " " + e.Name)
}

// ---- open ----

// openExternal hands the current file to $PIX_OPEN or xdg-open.
func (u *viewer) openExternal() {
	if len(u.entries) == 0 {
		return
	}
	p := u.entries[u.idx].Path
	go func() {
		if err := fileops.OpenExternal(p); err != nil {
			fyne.Do(func() {
				if !u.quitting {
					u.flashMsg("open: " + err.Error())
				}
			})
		}
	}()
}
