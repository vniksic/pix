package scan

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounce = 300 * time.Millisecond

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
	done := make(chan struct{})
	go watchLoop(w, done, onChange)

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			w.Close()
		})
	}, nil
}

func watchLoop(w *fsnotify.Watcher, done <-chan struct{}, onChange func()) {
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
