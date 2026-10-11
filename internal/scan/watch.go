package scan

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounce = 300 * time.Millisecond

// maxWatchDirs caps how many directories WatchTree watches; deeper trees
// are only picked up by explicit rescans. A variable so tests can lower it.
var maxWatchDirs = 2000

// Watch calls onChange (debounced ~300ms, from a goroutine) whenever files
// appear/disappear/change in dir. stop() releases the watcher; it is
// idempotent, safe to call from onChange, and does not block on onChange.
// Changes to hidden files and pure permission changes are ignored.
func Watch(dir string, onChange func()) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, err
	}
	return startWatch(w, onChange, nil), nil
}

// WatchTree is Watch for root and every non-hidden subdirectory below it
// (symlinked directories are not followed), the tree ListTree lists.
// Directories created later are watched as they appear. At most 2000
// directories are watched; beyond that, changes deeper in the tree are not
// noticed.
func WatchTree(root string, onChange func()) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(root); err != nil {
		w.Close()
		return nil, err
	}
	t := &treeWatch{w: w, dirs: map[string]bool{root: true}}
	t.addBelow(root)
	return startWatch(w, onChange, t.event), nil
}

// treeWatch tracks the directories a WatchTree watcher has added. Its
// methods run on one goroutine at a time (setup, then the watch loop).
type treeWatch struct {
	w    *fsnotify.Watcher
	dirs map[string]bool
}

// addBelow adds the non-hidden real subdirectories of dir, recursively,
// until the cap is reached.
func (t *treeWatch) addBelow(dir string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range des {
		if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		if !t.add(filepath.Join(dir, de.Name())) {
			return
		}
	}
}

// add watches dir and its subtree; it reports false once the cap is hit.
func (t *treeWatch) add(dir string) bool {
	if t.dirs[dir] {
		return true
	}
	if len(t.dirs) >= maxWatchDirs {
		return false
	}
	if err := t.w.Add(dir); err != nil {
		return true // vanished or unreadable: skip it, keep going
	}
	t.dirs[dir] = true
	t.addBelow(dir)
	return len(t.dirs) < maxWatchDirs
}

func (t *treeWatch) event(ev fsnotify.Event) {
	switch {
	case ev.Has(fsnotify.Create):
		if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
			t.add(ev.Name)
		}
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		// The kernel drops the watch itself; forget it and anything below.
		if t.dirs[ev.Name] {
			prefix := ev.Name + string(filepath.Separator)
			for d := range t.dirs {
				if d == ev.Name || strings.HasPrefix(d, prefix) {
					_ = t.w.Remove(d) // usually already gone
					delete(t.dirs, d)
				}
			}
		}
	}
}

// startWatch runs the debounce loop on w and returns its stop function.
// onEvent, if set, sees every non-hidden event first, on the loop goroutine.
func startWatch(w *fsnotify.Watcher, onChange func(), onEvent func(fsnotify.Event)) (stop func()) {
	done := make(chan struct{})
	go watchLoop(w, done, onChange, onEvent)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			w.Close()
		})
	}
}

func watchLoop(w *fsnotify.Watcher, done <-chan struct{}, onChange func(), onEvent func(fsnotify.Event)) {
	timer := time.NewTimer(watchDebounce)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod || isHidden(ev.Name) {
				continue
			}
			if onEvent != nil {
				onEvent(ev)
			}
			timer.Reset(watchDebounce)
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			// An overflow or similar: something changed, we don't know what.
			timer.Reset(watchDebounce)
		case <-timer.C:
			select {
			case <-done:
				return
			default:
				onChange()
			}
		}
	}
}

func isHidden(path string) bool {
	return strings.HasPrefix(filepath.Base(path), ".")
}
