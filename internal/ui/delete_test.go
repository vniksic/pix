package ui

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"pix/internal/imgload"
	"pix/internal/scan"
)

// loopDriver stands in for the GLFW event loop: fyne.Do queues the
// callback, and pump runs the queue on the test goroutine. (The test
// driver runs callbacks at once on whatever goroutine calls fyne.Do, so
// the load and trash goroutines would touch the widgets concurrently with
// the test.)
type loopDriver struct {
	fyne.Driver
	mu sync.Mutex
	q  []func()
}

func (d *loopDriver) DoFromGoroutine(f func(), _ bool) {
	d.mu.Lock()
	d.q = append(d.q, f)
	d.mu.Unlock()
}

type loopApp struct {
	fyne.App
	d *loopDriver
}

func (a *loopApp) Driver() fyne.Driver { return a.d }

// pump waits for the viewer's trash, delete and load goroutines and runs
// the callbacks they queue, until nothing is left to do.
func pump(u *viewer) {
	d := u.app.(*loopApp).d
	for {
		u.bg.Wait()
		for {
			u.reqMu.Lock()
			busy := u.reqBusy
			u.reqMu.Unlock()
			if !busy {
				break
			}
			time.Sleep(time.Millisecond)
		}
		d.mu.Lock()
		q := d.q
		d.q = nil
		d.mu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

// deleteViewer builds a viewer over real (non-image) files a.png, b.png and
// c.png in a temp directory, showing the first. The trash goes to the temp
// directory too.
func deleteViewer(t *testing.T) (*viewer, string) {
	t.Helper()
	ta := test.NewTempApp(t)
	a := &loopApp{App: ta, d: &loopDriver{Driver: ta.Driver()}}
	fyne.SetCurrentApp(a) // NewTempApp's cleanup replaces it
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	dir := filepath.Join(tmp, "pics")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var entries []scan.Entry
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, scan.Entry{Path: p, Name: name, Label: name})
	}
	u := &viewer{
		opts:     Options{Target: scan.Target{Root: dir}},
		app:      a,
		win:      a.NewWindow("pix"),
		loader:   imgload.NewLoader(2, 1),
		entries:  entries,
		marks:    map[string]bool{},
		copied:   map[string]bool{},
		trashing: map[string]bool{},
		rots:     map[string]int{},
		gone:     map[string]bool{},
	}
	u.build()
	u.show(0)
	pump(u)
	return u, dir
}

// press is one physical key press: KeyDown, then TypedKey.
func press(u *viewer, k fyne.KeyName) {
	u.keyDown(&fyne.KeyEvent{Name: k})
	u.typedKey(&fyne.KeyEvent{Name: k})
}

// shiftDelete is Shift+Delete as the GLFW driver delivers it: KeyDown,
// then a secondary Cut shortcut instead of TypedKey.
func shiftDelete(u *viewer) {
	u.keyDown(&fyne.KeyEvent{Name: fyne.KeyDelete})
	u.typedShortcut(&fyne.ShortcutCut{Secondary: true})
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func names(u *viewer) (out []string) {
	for _, e := range u.entries {
		out = append(out, e.Name)
	}
	return out
}

func TestShiftDeleteCancel(t *testing.T) {
	u, dir := deleteViewer(t)
	a := filepath.Join(dir, "a.png")
	shiftDelete(u)
	if u.confirm == nil || u.win.Canvas().Overlays().Top() == nil {
		t.Fatal("no dialog")
	}
	if !exists(t, a) {
		t.Fatal("file gone before confirming")
	}
	// The overlay has its own focus, with nothing focused, so keys come
	// through the canvas fallbacks, as Escape does here.
	c := u.win.Canvas()
	if c.Focused() != nil {
		t.Fatalf("focused with the dialog open: %T", c.Focused())
	}
	u.keyDown(&fyne.KeyEvent{Name: fyne.KeyEscape})
	c.OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if u.confirm != nil || u.win.Canvas().Overlays().Top() != nil {
		t.Error("dialog still open after Escape")
	}
	// Auto-repeat of that Escape: no keyDown. It must not quit either.
	u.typedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if u.quitting {
		t.Error("Escape quit")
	}
	if !exists(t, a) || len(u.entries) != 3 {
		t.Errorf("cancelled, yet: file exists %v, playlist %q", exists(t, a), names(u))
	}
	if u.win.Canvas().Focused() != fyne.Focusable(u.vp) {
		t.Error("viewport not focused after the dialog")
	}
}

func TestShiftDeleteConfirm(t *testing.T) {
	u, dir := deleteViewer(t)
	b := filepath.Join(dir, "b.png")
	u.jump(1)
	pump(u)
	u.toggleMark()
	u.copied[b] = true
	shiftDelete(u)
	press(u, fyne.KeyEnter)
	pump(u)
	if u.confirm != nil || u.win.Canvas().Overlays().Top() != nil {
		t.Error("dialog still open after Enter")
	}
	if exists(t, b) {
		t.Error("file still there")
	}
	if got := names(u); !slices.Equal(got, []string{"a.png", "c.png"}) {
		t.Errorf("playlist %q", got)
	}
	if u.entries[u.idx].Name != "c.png" {
		t.Errorf("current is %s, want the next one", u.entries[u.idx].Name)
	}
	if u.isMarked(b) || u.copied[b] || u.nMarked != 0 || len(u.trashing) != 0 {
		t.Errorf("marks %v, copied %v, nMarked %d, trashing %v", u.marks, u.copied, u.nMarked, u.trashing)
	}
	if u.flash != "deleted b.png" {
		t.Errorf("flash %q", u.flash)
	}
	// Auto-repeat of that Enter does not copy out (which would flash).
	u.typedKey(&fyne.KeyEvent{Name: fyne.KeyEnter})
	if u.flash != "deleted b.png" {
		t.Errorf("Enter repeat acted: flash %q", u.flash)
	}
	if n, _ := os.ReadDir(filepath.Join(filepath.Dir(dir), "data", "Trash", "files")); len(n) != 0 {
		t.Errorf("deleted file went to the trash: %v", n)
	}
}

// While the dialog is open, keys only feed it.
func TestShiftDeleteDialogTakesKeys(t *testing.T) {
	u, dir := deleteViewer(t)
	shiftDelete(u)
	d := u.confirm
	press(u, fyne.KeyPageDown)
	if u.idx != 0 || u.pendStep != 0 {
		t.Errorf("paged: idx %d, pending %d", u.idx, u.pendStep)
	}
	u.typedRune('q')
	u.typedRune('f')
	if u.quitting || u.fullscreen {
		t.Errorf("runes acted: quitting %v, fullscreen %v", u.quitting, u.fullscreen)
	}
	press(u, fyne.KeyDelete)
	press(u, fyne.KeyDelete)
	if u.delPath != "" || len(u.trashing) != 0 {
		t.Errorf("Delete acted: pending %q, trashing %v", u.delPath, u.trashing)
	}
	shiftDelete(u)
	u.typedShortcut(&fyne.ShortcutCopy{})
	if u.confirm != d || u.flash != "" {
		t.Errorf("shortcuts acted: dialog replaced %v, flash %q", u.confirm != d, u.flash)
	}
	// Tab moves focus to a dialog button, which would swallow Enter and
	// Escape; the viewer takes it back once the press is handled.
	c := u.win.Canvas()
	u.keyDown(&fyne.KeyEvent{Name: fyne.KeyTab})
	c.FocusNext() // what the driver does for Tab, after KeyDown
	if c.Focused() == nil {
		t.Fatal("Tab focused nothing in the dialog")
	}
	pump(u)
	if c.Focused() != nil {
		t.Errorf("dialog button still focused: %T", c.Focused())
	}
	press(u, fyne.KeyEscape)
	if u.quitting || u.confirm != nil || !exists(t, filepath.Join(dir, "a.png")) {
		t.Errorf("after Escape: quitting %v, dialog %v", u.quitting, u.confirm != nil)
	}
}

func TestShiftDeleteNotOpened(t *testing.T) {
	u, _ := deleteViewer(t)
	u.trashing[u.entries[0].Path] = true // being trashed already
	shiftDelete(u)
	if u.confirm != nil {
		t.Error("dialog for a file being trashed")
	}
	u.entries = nil
	shiftDelete(u)
	if u.confirm != nil {
		t.Error("dialog with an empty playlist")
	}
}

func TestDeleteTwiceTrashes(t *testing.T) {
	u, dir := deleteViewer(t)
	a := filepath.Join(dir, "a.png")
	press(u, fyne.KeyDelete)
	if u.flash != "Delete again to trash a.png" {
		t.Errorf("first press: flash %q", u.flash)
	}
	// Auto-repeat comes without a keyDown and does not count.
	u.typedKey(&fyne.KeyEvent{Name: fyne.KeyDelete})
	u.typedKey(&fyne.KeyEvent{Name: fyne.KeyDelete})
	if len(u.trashing) != 0 || !exists(t, a) || u.delPath != a {
		t.Fatalf("auto-repeat counted: trashing %v", u.trashing)
	}
	press(u, fyne.KeyDelete)
	pump(u)
	if exists(t, a) {
		t.Error("file still there")
	}
	if !exists(t, filepath.Join(filepath.Dir(dir), "data", "Trash", "files", "a.png")) {
		t.Error("file not in the trash")
	}
	if got := names(u); !slices.Equal(got, []string{"b.png", "c.png"}) || u.flash != "trashed a.png" {
		t.Errorf("playlist %q, flash %q", got, u.flash)
	}
}

func TestCtrlXDoesNothing(t *testing.T) {
	u, dir := deleteViewer(t)
	u.keyDown(&fyne.KeyEvent{Name: fyne.KeyX})
	u.typedShortcut(&fyne.ShortcutCut{})
	if u.confirm != nil || u.flash != "" || len(u.trashing) != 0 || !exists(t, filepath.Join(dir, "a.png")) {
		t.Errorf("Ctrl+X acted: dialog %v, flash %q", u.confirm != nil, u.flash)
	}
}

// Shift+Delete right after a Delete prompt opens the dialog; its KeyDown
// is not a second Delete press.
func TestShiftDeleteAfterDeletePrompt(t *testing.T) {
	u, dir := deleteViewer(t)
	a := filepath.Join(dir, "a.png")
	press(u, fyne.KeyDelete)
	shiftDelete(u)
	if len(u.trashing) != 0 || !exists(t, a) {
		t.Fatal("trashed")
	}
	if u.confirm == nil {
		t.Fatal("no dialog")
	}
	if u.delPath != "" || u.flash == "Delete again to trash a.png" {
		t.Errorf("prompt not cancelled: pending %q, flash %q", u.delPath, u.flash)
	}
	press(u, fyne.KeyEscape)
	press(u, fyne.KeyDelete) // a first press again: prompts, does not trash
	if len(u.trashing) != 0 || !exists(t, a) || u.flash != "Delete again to trash a.png" {
		t.Errorf("after cancelling: trashing %v, flash %q", u.trashing, u.flash)
	}
}
