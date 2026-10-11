package scan

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// takenSource is the date source of the last Resolve, used by List and
// ListTree for SortDate. nil when that Resolve had no Options.Taken.
var takenSource atomic.Pointer[takenCache]

// setTakenSource records fn (nil clears it) as the source for List and
// ListTree and returns its cache.
func setTakenSource(fn func(string) (time.Time, bool)) *takenCache {
	var c *takenCache
	if fn != nil {
		c = &takenCache{fn: fn, m: make(map[string]takenRec)}
	}
	takenSource.Store(c)
	return c
}

// takenCache memoizes a Taken function by path, size and mtime, so the
// rescans of a watched directory do not re-read every file.
type takenCache struct {
	fn func(string) (time.Time, bool)

	mu sync.Mutex
	m  map[string]takenRec
}

type takenRec struct {
	mod   time.Time
	size  int64
	taken time.Time
}

// fill sets Taken on entries (left zero when unknown), calling fn
// concurrently for the ones not cached. A nil cache does nothing.
func (c *takenCache) fill(entries []Entry) {
	if c == nil {
		return
	}
	var todo []int
	c.mu.Lock()
	for i := range entries {
		e := &entries[i]
		if r, ok := c.m[e.Path]; ok && r.size == e.Size && r.mod.Equal(e.ModTime) {
			e.Taken = r.taken
		} else {
			todo = append(todo, i)
		}
	}
	c.mu.Unlock()
	if len(todo) == 0 {
		return
	}

	// Reading EXIF is mostly waiting on the disk: use a few more
	// goroutines than CPUs.
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(len(todo), max(4, 2*runtime.GOMAXPROCS(0))) {
		wg.Go(func() {
			for {
				k := int(next.Add(1) - 1)
				if k >= len(todo) {
					return
				}
				e := &entries[todo[k]]
				if t, ok := c.fn(e.Path); ok {
					e.Taken = t
				}
			}
		})
	}
	wg.Wait()

	c.mu.Lock()
	for _, i := range todo {
		e := &entries[i]
		c.m[e.Path] = takenRec{mod: e.ModTime, size: e.Size, taken: e.Taken}
	}
	c.mu.Unlock()
}

// dateKey is what SortDate orders by: the date taken, else ModTime.
func dateKey(e Entry) time.Time {
	if !e.Taken.IsZero() {
		return e.Taken
	}
	return e.ModTime
}
