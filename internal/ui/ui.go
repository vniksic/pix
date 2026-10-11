// Package ui is the Fyne front end of pix: window, viewport, sidebar,
// status bar, key and mouse handling.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"pix/internal/clipboard"
	"pix/internal/fileops"
	"pix/internal/hypr"
	"pix/internal/imgload"
	"pix/internal/scan"
)

// Options configures Run.
type Options struct {
	Entries    []scan.Entry
	Start      int
	Dir        string // listing root; "" for ad-hoc lists (no watching)
	Recursive  bool   // Dir is listed with scan.ListTree and watched with scan.WatchTree
	Sort       scan.Sort
	Fullscreen bool // start fullscreen
	Sidebar    bool // sidebar visible at start
	Loader     *imgload.Loader
	OutDir     string    // -out: where Enter and quitting copy marked files ("" = nowhere)
	Stdout     io.Writer // receives the marked paths when Run ends (default os.Stdout)
}

const (
	loadingDelay = 150 * time.Millisecond  // keep the old image this long before showing "loading…"
	flashTime    = 2 * time.Second         // how long transient status messages stay
	deleteWindow = 1500 * time.Millisecond // a second Delete within this trashes the file

	hyprRetries    = 5                      // Hyprland fullscreen retries while our window is not mapped yet
	hyprRetryDelay = 100 * time.Millisecond // pause between those retries
)

var (
	shortcutCopyPath = &desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl | fyne.KeyModifierShift}
	shortcutCtrlC    = &desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl}
)

type viewer struct {
	opts   Options
	app    fyne.App
	win    fyne.Window
	loader *imgload.Loader

	entries []scan.Entry
	idx     int

	vp      *viewport
	list    *sideList
	sidebar *fyne.Container
	root    *fyne.Container
	right   *fyne.Container // viewport with the status bar below it
	status  *widget.Label

	sidebarOn  bool // windowed-mode preference
	fsSidebar  bool // sidebar shown while fullscreen (reset on entering)
	fullscreen bool
	statusOff  bool // status bar hidden by an explicit 1:1 in fullscreen; cleared by Fit and by (un)fullscreening
	loadToken  uint64
	loading    bool
	loadSlow   bool // loading has taken longer than loadingDelay
	loadErr    error
	taken      time.Time // when the shown picture was taken (zero if unknown)
	flash      string
	flashToken uint64
	stopWatch  func()
	quitting   bool

	// scanSeq numbers rescans as they start (from `r` or the watcher);
	// applyEntries ignores a listing older than the last one applied.
	scanSeq     atomic.Uint64
	scanApplied uint64
	// pinned is the file named on the command line: kept in the playlist
	// while it exists even if List would leave it out (hidden, unsniffable).
	pinned string

	// copies counts clipboard hand-offs in flight; Run waits for them so
	// quitting right after Ctrl+C does not cut the data off mid-pipe.
	copies sync.WaitGroup
	// copyMu guards copyPending and copyBusy: copies run one at a time on
	// one goroutine, newest request first, so the last press wins.
	copyMu      sync.Mutex
	copyPending *copyJob
	copyBusy    bool

	// Marks. markMu guards marks and copied: the copy-out and trash
	// goroutines update them, and Run reads them after the event loop.
	markMu  sync.Mutex
	marks   map[string]bool // marked paths
	copied  map[string]bool // marked paths already copied to OutDir
	nMarked int             // marked entries in the playlist
	outBusy bool            // a copy to OutDir is running
	// bg counts copy-out and trash goroutines; Run waits for them.
	bg sync.WaitGroup
	// final is the playlist (paths, in order) when the window closed, for
	// the exit copy and the list printed to stdout.
	final []string
	// errOut receives the exit copy summary (nil: os.Stderr).
	errOut io.Writer

	// Pending Delete: a second press on the same file within deleteWindow
	// trashes it. trashing holds paths being trashed.
	delPath   string
	delAt     time.Time
	delPrompt string
	trashing  map[string]bool

	reqMu      sync.Mutex // guards reqPending and reqBusy (shared with the load goroutine)
	reqPending *loadReq
	reqBusy    bool

	// Window fullscreen (see setWindowFullscreen). u.fullscreen is the only
	// fullscreen flag pix reads; Fyne's stays false on the Hyprland path.
	hypr       bool          // fullscreen through Hyprland's IPC (Wayland under Hyprland)
	mapped     bool          // the first frame was drawn, so Hyprland can see the window
	fyneFS     bool          // the window was last put in fullscreen through Fyne
	hyprLogged bool          // an IPC failure has been logged
	fsSeq      atomic.Uint64 // numbers window fullscreen requests; stale ones give up
	fsMu       sync.Mutex    // one IPC exchange at a time
}

// Run builds the Fyne app and blocks until the window closes.
func Run(opts Options) error {
	if opts.Loader == nil {
		return fmt.Errorf("ui: no loader")
	}
	a := app.NewWithID("pix.viewer")
	a.Settings().SetTheme(darkTheme{theme.DefaultTheme()})

	u := &viewer{
		opts:      opts,
		app:       a,
		win:       a.NewWindow("pix"),
		loader:    opts.Loader,
		entries:   opts.Entries,
		sidebarOn: opts.Sidebar,
		marks:     make(map[string]bool),
		copied:    make(map[string]bool),
		trashing:  make(map[string]bool),
	}
	if opts.Dir != "" && opts.Start >= 0 && opts.Start < len(opts.Entries) {
		u.pinned = opts.Entries[opts.Start].Path
	}
	u.build()
	u.hypr = onWayland && hypr.Available()
	u.vp.onFirstFrame = func() {
		u.mapped = true
		if u.fullscreen && u.hypr && !u.quitting {
			u.setWindowFullscreen(true) // -fullscreen: deferred until now
		}
	}

	u.win.Resize(fyne.NewSize(1200, 800))
	if opts.Fullscreen {
		u.setFullscreen(true)
	}
	if len(u.entries) > 0 {
		u.show(opts.Start)
	} else {
		u.vp.SetMessage("no images")
		u.updateStatus()
	}

	if opts.Dir != "" {
		watch := scan.Watch
		if opts.Recursive {
			watch = scan.WatchTree
		}
		stop, err := watch(opts.Dir, func() {
			seq := u.scanSeq.Add(1)
			entries, err := u.listRoot()
			fyne.Do(func() { u.applyEntries(seq, entries, err) })
		})
		if err != nil {
			log.Printf("pix: watching %s: %v", opts.Dir, err)
		} else {
			u.stopWatch = stop
		}
	}
	u.win.SetOnClosed(func() {
		// Every way out (q, Escape, closing the window) comes here, on the
		// main goroutine, while the loop still runs. From now on callbacks
		// leave the viewer state alone (they check quitting), so Run can
		// read it once the loop is over.
		u.quitting = true
		u.final = make([]string, len(u.entries))
		for i, e := range u.entries {
			u.final[i] = e.Path
		}
		if u.stopWatch != nil {
			u.stopWatch()
		}
	})

	u.win.ShowAndRun()
	u.copies.Wait()
	u.bg.Wait()
	return u.finish()
}

// finish runs after the event loop: it copies marked files not yet copied
// to OutDir (if set; one summary line on stderr), then prints every marked
// path, in playlist order, to Stdout. Nothing else in pix writes there, so
// the output can be piped. A failed copy is returned as the error.
func (u *viewer) finish() error {
	u.markMu.Lock()
	var marked, pending []string
	for _, p := range u.final {
		if u.marks[p] {
			marked = append(marked, p)
			if !u.copied[p] {
				pending = append(pending, p)
			}
		}
	}
	u.markMu.Unlock()

	var err error
	if dir := u.opts.OutDir; dir != "" && len(pending) > 0 {
		msg, failed := copySummary(fileops.CopyAll(pending, dir), dir)
		if failed {
			err = errors.New(msg)
		} else {
			errOut := u.errOut
			if errOut == nil {
				errOut = os.Stderr
			}
			fmt.Fprintln(errOut, "pix: "+msg)
		}
	}

	out := u.opts.Stdout
	if out == nil {
		out = os.Stdout
	}
	w := bufio.NewWriter(out)
	for _, p := range marked {
		fmt.Fprintln(w, p)
	}
	if ferr := w.Flush(); ferr != nil && err == nil {
		err = fmt.Errorf("printing marked files: %w", ferr)
	}
	return err
}

func (u *viewer) build() {
	u.vp = newViewport()
	u.vp.onChange = u.viewChanged
	u.vp.onOne = u.explicitOne
	u.vp.onKey = u.typedKey
	u.vp.onKeyDown = u.keyDown
	u.vp.onRune = u.typedRune
	u.vp.onShortcut = u.typedShortcut
	u.vp.focusSelf = u.focusViewport
	u.vp.onFocusOff = func() {
		// If focus went nowhere (click on the status bar, say), take it back
		// so Tab keeps reaching us instead of moving focus.
		fyne.Do(func() {
			if u.win.Canvas().Focused() == nil && !u.quitting {
				u.focusViewport()
			}
		})
	}

	label := func(id int) string {
		if id >= 0 && id < len(u.entries) {
			return u.entries[id].Label
		}
		return ""
	}
	// Two-line rows when directory parts can show up: always in a recursive
	// listing (new subdirectories may appear), else only if the fixed
	// ad-hoc list has some.
	twoLine := (u.opts.Dir != "" && u.opts.Recursive) || hasDirParts(len(u.entries), label)
	u.list = newSideList(func() int { return len(u.entries) }, label, u.isMarkedAt, twoLine)
	u.list.onKey = u.typedKey
	u.list.onKeyDown = u.keyDown
	u.list.onRune = u.typedRune
	u.list.onShortcut = u.typedShortcut
	u.list.OnSelected = func(id int) {
		if id != u.idx {
			u.show(id)
		}
		// Give keyboard focus back to the viewport after a click.
		u.focusViewport()
	}

	u.status = widget.NewLabel("")
	u.status.SizeName = theme.SizeNameCaptionText
	u.status.Truncation = fyne.TextTruncateEllipsis

	u.sidebar = container.New(fixedWidth(sidebarWidth), u.list)
	u.right = container.NewBorder(nil, u.status, nil, nil, u.vp)
	u.root = container.NewBorder(nil, nil, u.sidebar, nil, u.right)
	u.win.SetContent(u.root)
	u.applySidebar()

	c := u.win.Canvas()
	// Canvas-level fallbacks for when nothing is focused.
	c.SetOnTypedKey(func(e *fyne.KeyEvent) { u.typedKey(e); u.focusViewport() })
	c.SetOnTypedRune(func(r rune) { u.typedRune(r); u.focusViewport() })
	if dc, ok := c.(desktop.Canvas); ok {
		dc.SetOnKeyDown(u.keyDown)
	}
	// Ctrl+C arrives as fyne.ShortcutCopy from the GLFW driver; register the
	// desktop.CustomShortcut form too in case another driver reports that.
	c.AddShortcut(&fyne.ShortcutCopy{}, u.typedShortcut)
	c.AddShortcut(shortcutCtrlC, u.typedShortcut)
	c.AddShortcut(shortcutCopyPath, u.typedShortcut)
	u.focusViewport()
}

func (u *viewer) focusViewport() {
	if u.quitting {
		return
	}
	if c := u.win.Canvas(); c.Focused() != fyne.Focusable(u.vp) {
		c.Focus(u.vp)
	}
}

// ---- navigation ----

func (u *viewer) show(i int) {
	if len(u.entries) == 0 {
		return
	}
	i = max(0, min(i, len(u.entries)-1))
	u.idx = i
	e := u.entries[i]
	u.win.SetTitle(e.Label)
	u.syncList()

	u.loadToken++
	tok := u.loadToken
	u.loading, u.loadSlow = true, false
	u.loadErr = nil
	u.updateStatus()

	var pre []string
	for _, j := range []int{i + 1, i - 1, i + 2} {
		if j >= 0 && j < len(u.entries) {
			pre = append(pre, u.entries[j].Path)
		}
	}
	u.requestLoad(&loadReq{tok: tok, entry: e, prefetch: pre})
	time.AfterFunc(loadingDelay, func() {
		fyne.Do(func() {
			if tok == u.loadToken && u.loading {
				u.loadSlow = true
				u.vp.SetMessage("loading " + e.Label + " …")
				u.updateStatus()
			}
		})
	})
}

// loadReq asks the load goroutine to decode one entry.
type loadReq struct {
	tok      uint64
	entry    scan.Entry
	prefetch []string
}

// requestLoad hands r to the single load goroutine. Requests coalesce: when
// paging faster than images decode, only the newest pending one is decoded
// next, so holding PageDown never piles up concurrent decodes.
func (u *viewer) requestLoad(r *loadReq) {
	u.reqMu.Lock()
	u.reqPending = r
	if u.reqBusy {
		u.reqMu.Unlock()
		return
	}
	u.reqBusy = true
	u.reqMu.Unlock()
	go u.loadLoop()
}

func (u *viewer) loadLoop() {
	for {
		u.reqMu.Lock()
		r := u.reqPending
		u.reqPending = nil
		if r == nil {
			u.reqBusy = false
			u.reqMu.Unlock()
			return
		}
		u.reqMu.Unlock()

		e := r.entry
		img, meta, err := u.loader.GetMeta(e.Path)
		if err == nil && img == nil {
			err = errors.New("no image")
		}
		opaque := err == nil && img.Opaque()

		u.reqMu.Lock()
		superseded := u.reqPending != nil
		u.reqMu.Unlock()
		if !superseded {
			// Prefetch only after the current image, so it never waits behind them.
			u.loader.Prefetch(r.prefetch...)
		}
		fyne.Do(func() {
			if r.tok != u.loadToken {
				return
			}
			u.loading, u.loadSlow = false, false
			if err != nil {
				u.loadErr = err
				u.taken = time.Time{}
				u.vp.SetMessage("cannot show " + e.Label + ": " + err.Error())
			} else {
				u.taken = meta.Taken
				u.vp.SetImage(img, opaque)
			}
			u.updateStatus()
		})
	}
}

// syncList selects the current entry in the sidebar and scrolls to it.
func (u *viewer) syncList() {
	u.list.Select(u.idx)
	u.list.ScrollTo(u.idx)
}

func (u *viewer) step(d int) {
	if len(u.entries) == 0 {
		return
	}
	j := u.idx + d
	if j < 0 || j >= len(u.entries) {
		return
	}
	u.show(j)
}

// ---- rescanning ----

// rescan re-reads the directory (or re-stats an ad-hoc list) off the main
// goroutine and applies the result.
func (u *viewer) rescan() {
	seq := u.scanSeq.Add(1)
	if u.opts.Dir == "" {
		old := append([]scan.Entry(nil), u.entries...)
		go func() {
			var out []scan.Entry
			for _, e := range old {
				fi, err := os.Stat(e.Path)
				if err != nil || fi.IsDir() {
					continue
				}
				e.ModTime, e.Size = fi.ModTime(), fi.Size()
				out = append(out, e)
			}
			fyne.Do(func() { u.applyEntries(seq, out, nil) })
		}()
		return
	}
	go func() {
		entries, err := u.listRoot()
		fyne.Do(func() { u.applyEntries(seq, entries, err) })
	}()
}

// listRoot reads the listing root (u.opts.Dir must be set). It touches only
// u.opts, so it runs on any goroutine.
func (u *viewer) listRoot() ([]scan.Entry, error) {
	if u.opts.Recursive {
		return scan.ListTree(u.opts.Dir, u.opts.Sort)
	}
	return scan.List(u.opts.Dir, u.opts.Sort)
}

// applyEntries installs a fresh listing from rescan number seq, keeping the
// current file selected by path and invalidating cached decodes of files
// that changed. Listings finishing after a newer one are dropped.
func (u *viewer) applyEntries(seq uint64, entries []scan.Entry, err error) {
	if u.quitting || seq <= u.scanApplied {
		return
	}
	u.scanApplied = seq
	if err != nil {
		u.flashMsg("rescan: " + err.Error())
		return
	}
	if u.opts.Dir != "" {
		entries = withPinned(entries, u.pinned, u.opts.Dir, u.opts.Sort)
	}
	curPath := ""
	if u.idx >= 0 && u.idx < len(u.entries) {
		curPath = u.entries[u.idx].Path
	}
	old := make(map[string]scan.Entry, len(u.entries))
	for _, e := range u.entries {
		old[e.Path] = e
	}
	reload := false
	newIdx := -1
	listed := make(map[string]bool, len(entries))
	for i, e := range entries {
		listed[e.Path] = true
		if o, ok := old[e.Path]; ok && (!o.ModTime.Equal(e.ModTime) || o.Size != e.Size) {
			u.loader.Invalidate(e.Path)
			if e.Path == curPath {
				reload = true
			}
		}
		if e.Path == curPath {
			newIdx = i
		}
	}
	// A path that left the listing may later return with other content;
	// drop its cached decode (or error) so that is never shown.
	for p := range old {
		if !listed[p] {
			u.loader.Invalidate(p)
		}
	}
	if newIdx < 0 {
		reload = true
		newIdx = max(0, min(u.idx, len(entries)-1))
	}
	u.entries = entries
	u.recountMarks()
	u.list.UnselectAll()
	u.list.Refresh()

	if len(entries) == 0 {
		u.showNone()
		return
	}
	if reload {
		u.show(newIdx)
		return
	}
	u.idx = newIdx
	u.syncList()
	u.updateStatus()
}

// showNone is the empty-playlist state.
func (u *viewer) showNone() {
	u.idx = 0
	u.loadToken++
	u.loading, u.loadSlow = false, false
	u.loadErr = nil
	u.win.SetTitle("pix")
	u.vp.SetMessage("no images")
	u.updateStatus()
}

// withPinned returns entries with the file at path inserted at its sorted
// position if it is missing from the listing of root but still exists. Its
// Label is recomputed relative to root, as the listing's are.
func withPinned(entries []scan.Entry, path, root string, sort scan.Sort) []scan.Entry {
	if path == "" || slices.ContainsFunc(entries, func(e scan.Entry) bool { return e.Path == path }) {
		return entries
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return entries
	}
	p := scan.Entry{
		Path:    path,
		Name:    filepath.Base(path),
		Label:   scan.RelLabel(root, path),
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
	}
	if sort == scan.SortDate {
		p.Taken, _ = imgload.Taken(path)
	}
	i := slices.IndexFunc(entries, func(e scan.Entry) bool { return scan.Compare(p, e, sort) < 0 })
	if i < 0 {
		i = len(entries)
	}
	return slices.Insert(entries, i, p)
}

// ---- status bar ----

// viewChanged runs on every viewport state change. A return to Fit (x,
// double-click, or an image change resetting free zoom) brings back a
// status bar hidden by 1:1 in fullscreen.
func (u *viewer) viewChanged() {
	if u.statusOff && u.vp.view.Mode == ModeFit {
		u.statusOff = false
		u.applyStatusBar()
	}
	u.updateStatus()
}

// explicitOne runs when 1:1 is chosen explicitly (z or double-click). In
// fullscreen it hides the status bar so the image gets the whole screen.
// Wheel and +/- zoom never come here, even when they snap to 1:1.
func (u *viewer) explicitOne() {
	if u.fullscreen && !u.statusOff {
		u.statusOff = true
		u.applyStatusBar()
	}
}

// applyStatusBar shows or hides the status bar. The border layout skips a
// hidden bottom object (and its padding), so the viewport takes the space.
func (u *viewer) applyStatusBar() {
	hide := u.fullscreen && u.statusOff
	if hide != u.status.Visible() {
		return
	}
	if hide {
		u.status.Hide()
	} else {
		u.status.Show()
	}
	u.right.Refresh() // re-run the border layout so the viewport resizes
}

func (u *viewer) updateStatus() {
	s := statusInfo{
		Empty:   len(u.entries) == 0,
		Locked:  u.vp.view.Locked,
		NMarked: u.nMarked,
		Flash:   u.flash,
	}
	if !s.Empty {
		e := u.entries[u.idx]
		s.Label, s.Index, s.Count = e.Label, u.idx, len(u.entries)
		s.Marked = u.isMarked(e.Path)
		s.Loading, s.Slow, s.Err = u.loading, u.loadSlow, u.loadErr
		if img := u.vp.img; img != nil {
			b := img.Bounds()
			s.HasImg, s.W, s.H = true, b.Dx(), b.Dy()
			s.Taken = u.taken
			s.Percent, s.Rot = u.vp.view.Percent(), u.vp.view.Rot
		}
	}
	u.setStatus(statusText(s))
}

func (u *viewer) setStatus(s string) {
	if u.status.Text != s {
		u.status.SetText(s)
	}
}

// flashMsg shows a transient message in the status bar.
func (u *viewer) flashMsg(msg string) { u.flashFor(msg, flashTime) }

// flashFor shows a transient message for d.
func (u *viewer) flashFor(msg string, d time.Duration) {
	u.flash = strings.ReplaceAll(msg, "\n", "; ") // joined errors: keep one line
	u.flashToken++
	tok := u.flashToken
	u.updateStatus()
	time.AfterFunc(d, func() {
		fyne.Do(func() {
			if tok == u.flashToken {
				u.flash = ""
				u.updateStatus()
			}
		})
	})
}

// ---- sidebar / fullscreen ----

func (u *viewer) applySidebar() {
	on := u.sidebarOn
	if u.fullscreen {
		on = u.fsSidebar
	}
	if on {
		u.sidebar.Show()
		u.list.ScrollTo(u.idx)
	} else {
		u.sidebar.Hide()
	}
	if u.root != nil {
		u.root.Refresh() // re-run the border layout so the viewport resizes
	}
}

func (u *viewer) toggleSidebar() {
	if u.fullscreen {
		u.fsSidebar = !u.fsSidebar
	} else {
		u.sidebarOn = !u.sidebarOn
	}
	u.applySidebar()
	u.focusViewport()
}

func (u *viewer) setFullscreen(on bool) {
	u.fullscreen = on
	u.fsSidebar = false
	u.statusOff = false
	u.applyStatusBar()
	u.setWindowFullscreen(on)
	if on {
		u.vp.view.SetFit()
	}
	u.applySidebar()
	u.vp.changed()
}

// setWindowFullscreen puts the window itself in or out of fullscreen.
//
// Fyne fullscreens on the monitor it thinks the window is on, which on
// Wayland is always the primary one (a Wayland window has no global
// position). Under Hyprland we ask the compositor instead, which keeps the
// window on its current monitor; GLFW sees that as an ordinary configure
// and resize. Hyprland can only act on a mapped window, so before the first
// frame this does nothing and the first frame catches up. If the IPC fails,
// Fyne does it after all.
func (u *viewer) setWindowFullscreen(on bool) {
	if !u.hypr || (!on && u.fyneFS) {
		u.fyneFullscreen(on)
		return
	}
	if !u.mapped {
		return
	}
	seq := u.fsSeq.Add(1)
	pid := os.Getpid()
	go func() {
		u.fsMu.Lock()
		defer u.fsMu.Unlock()
		var err error
		for try := 0; ; try++ {
			if u.fsSeq.Load() != seq {
				return // a newer request replaces this one
			}
			err = hypr.SetFullscreen(pid, on)
			if !errors.Is(err, hypr.ErrNoWindow) || try >= hyprRetries {
				break
			}
			time.Sleep(hyprRetryDelay) // window not mapped (or not known) yet
		}
		if err == nil {
			return
		}
		fyne.Do(func() {
			if u.fsSeq.Load() != seq || u.quitting {
				return
			}
			if !u.hyprLogged {
				u.hyprLogged = true
				log.Printf("pix: Hyprland fullscreen failed, falling back to Fyne: %v", err)
			}
			u.fyneFullscreen(on)
		})
	}()
}

func (u *viewer) fyneFullscreen(on bool) {
	u.fyneFS = on
	u.win.SetFullScreen(on)
}

func (u *viewer) quit() {
	u.quitting = true
	u.win.Close()
}

// ---- keys ----

func (u *viewer) typedKey(e *fyne.KeyEvent) {
	if e.Name != fyne.KeyDelete && !isModifierKey(e.Name) {
		u.cancelDelete()
	}
	switch e.Name {
	case fyne.KeyPageDown, fyne.KeySpace, fyne.KeyRight:
		u.step(1)
	case fyne.KeyPageUp, fyne.KeyBackspace, fyne.KeyLeft:
		u.step(-1)
	case fyne.KeyHome:
		u.show(0)
	case fyne.KeyEnd:
		u.show(len(u.entries) - 1)
	case fyne.KeyTab:
		u.toggleSidebar()
	case fyne.KeyReturn, fyne.KeyEnter:
		u.copyMarked()
	case fyne.KeyEscape:
		if u.fullscreen {
			u.setFullscreen(false)
		} else {
			u.quit()
		}
	}
}

func (u *viewer) typedRune(r rune) {
	u.cancelDelete()
	v := &u.vp.view
	switch unicode.ToLower(r) {
	case 'n':
		u.step(1)
	case 'p':
		u.step(-1)
	case 'f':
		u.setFullscreen(!u.fullscreen)
	case 'z':
		v.SetOne()
		u.explicitOne()
		u.vp.changed()
	case 'x':
		v.SetFit()
		u.vp.changed()
	case '+', '=':
		v.ZoomBy(KeyStep, v.VW/2, v.VH/2)
		u.vp.changed()
	case '-':
		v.ZoomBy(1/KeyStep, v.VW/2, v.VH/2)
		u.vp.changed()
	case '[':
		v.Rotate(-1)
		u.vp.changed()
	case ']':
		v.Rotate(1)
		u.vp.changed()
	case 'b':
		u.toggleSidebar()
	case 'r':
		u.rescan()
	case 'm':
		u.toggleMark()
	case 'l':
		v.Locked = !v.Locked
		u.updateStatus()
	case 'o':
		u.openExternal()
	case 'q':
		u.quit()
	}
}

func (u *viewer) typedShortcut(s fyne.Shortcut) {
	u.cancelDelete()
	switch s.ShortcutName() {
	case (&fyne.ShortcutCopy{}).ShortcutName(), shortcutCtrlC.ShortcutName():
		u.copyImage()
	case shortcutCopyPath.ShortcutName():
		u.copyPath()
	}
}

// ---- clipboard ----

// copyJob is one clipboard hand-off; ok is flashed when it succeeds.
type copyJob struct {
	run func() error
	ok  string
}

func (u *viewer) copyImage() {
	img, rot := u.vp.img, u.vp.view.Rot
	if img == nil {
		u.flashMsg("nothing to copy")
		return
	}
	u.requestCopy(&copyJob{run: func() error { return clipboard.CopyImage(RotateNRGBA(img, rot)) }, ok: "copied"})
}

func (u *viewer) copyPath() {
	if len(u.entries) == 0 {
		return
	}
	p := u.entries[u.idx].Path
	u.requestCopy(&copyJob{run: func() error { return clipboard.CopyText(p) }, ok: "copied path"})
}

// requestCopy queues j on the single copy goroutine. A request still
// waiting when a newer one arrives is dropped: the last press wins, and only
// its outcome is flashed.
func (u *viewer) requestCopy(j *copyJob) {
	u.copies.Add(1)
	u.copyMu.Lock()
	if u.copyPending != nil {
		u.copies.Done() // superseded before it started
	}
	u.copyPending = j
	if u.copyBusy {
		u.copyMu.Unlock()
		return
	}
	u.copyBusy = true
	u.copyMu.Unlock()
	go u.copyLoop()
}

func (u *viewer) copyLoop() {
	for {
		u.copyMu.Lock()
		j := u.copyPending
		u.copyPending = nil
		if j == nil {
			u.copyBusy = false
			u.copyMu.Unlock()
			return
		}
		u.copyMu.Unlock()

		err := j.run()
		u.copyMu.Lock()
		superseded := u.copyPending != nil
		u.copyMu.Unlock()
		u.copies.Done()
		if superseded {
			continue // a newer copy replaces this one on the clipboard
		}
		fyne.Do(func() {
			if u.quitting {
				return
			}
			if err != nil {
				u.flashMsg("copy failed: " + err.Error())
			} else {
				u.flashMsg(j.ok)
			}
		})
	}
}

// darkTheme forces the dark variant of the default theme.
type darkTheme struct{ fyne.Theme }

func (t darkTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.Theme.Color(n, theme.VariantDark)
}
