package ui

import (
	"fmt"
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
	u.outBusy = true
	u.flashMsg(fmt.Sprintf("copying %d to %s …", len(pending), dir))
	u.bg.Add(1)
	go func() {
		defer u.bg.Done()
		res := fileops.CopyAll(pending, dir)
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
// typedKey), so holding Delete never counts as pressing it twice.
func (u *viewer) keyDown(e *fyne.KeyEvent) {
	if e.Name == fyne.KeyDelete {
		u.deleteKey()
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

// trash moves e to the trash off the main goroutine, then drops it from the
// playlist.
func (u *viewer) trash(e scan.Entry) {
	u.trashing[e.Path] = true
	u.bg.Add(1)
	go func() {
		defer u.bg.Done()
		err := fileops.Trash(e.Path)
		if err == nil {
			// Here rather than in trashed, which does not run once quitting:
			// the exit report must not list a file that is gone.
			u.markMu.Lock()
			delete(u.marks, e.Path)
			delete(u.copied, e.Path)
			u.markMu.Unlock()
		}
		fyne.Do(func() { u.trashed(e, err) })
	}()
}

// trashed finishes a trash: on success the entry leaves the playlist and
// the next image (or the previous, if it was the last) is shown.
func (u *viewer) trashed(e scan.Entry, err error) {
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
			u.jump(min(i, len(u.entries)-1)) // a pending step was relative to the trashed file
		case i < u.idx:
			u.idx--
			u.syncList()
		default:
			u.syncList()
		}
	} else {
		u.recountMarks()
	}
	u.flashMsg("trashed " + e.Name)
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
