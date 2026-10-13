package scan

import (
	"sync"
	"sync/atomic"
	"time"
)

// takenSource is the date source of the last Resolve or Prepare, used by
// every scan for SortDate. nil when that call had no Options.Taken.
var takenSource atomic.Pointer[takenCache]

// setTakenSource records fn (nil clears it) as the source for later scans.
func setTakenSource(fn func(string) (time.Time, bool)) {
	var c *takenCache
	if fn != nil {
		c = &takenCache{fn: fn, m: make(map[string]takenRec)}
	}
	takenSource.Store(c)
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

// taken returns the date e was taken (zero when unknown), calling fn only
// when the cache has nothing for e's path, size and mtime. A nil cache
// gives zero. Safe for concurrent use.
func (c *takenCache) taken(e Entry) time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	r, ok := c.m[e.Path]
	c.mu.Unlock()
	if ok && r.size == e.Size && r.mod.Equal(e.ModTime) {
		return r.taken
	}
	var taken time.Time
	if t, ok := c.fn(e.Path); ok {
		taken = t
	}
	c.mu.Lock()
	c.m[e.Path] = takenRec{mod: e.ModTime, size: e.Size, taken: taken}
	c.mu.Unlock()
	return taken
}

// dateKey is what SortDate orders by: the date taken, else ModTime.
func dateKey(e Entry) time.Time {
	if !e.Taken.IsZero() {
		return e.Taken
	}
	return e.ModTime
}
