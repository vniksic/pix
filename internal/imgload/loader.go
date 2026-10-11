package imgload

import (
	"container/list"
	"image"
	"sync"
)

// Loader is an async LRU cache in front of Decode.
//
// It owns no long-lived goroutines: prefetch workers are started on demand
// and exit as soon as the queue is empty, so there is nothing to close.
type Loader struct {
	capacity int
	workers  int
	decode   func(string) (*image.NRGBA, error) // tests only: replaces decodeWithMeta (Meta then zero)

	mu      sync.Mutex
	entries map[string]*entry // queued, decoding and finished
	lru     *list.List        // finished entries, most recently used first
	queue   []*entry          // prefetches not yet started, highest priority first
	running int               // prefetch worker goroutines
	budget  int64             // max total pixel bytes cached (see SetByteBudget)
	bytes   int64             // pixel bytes of the entries in lru
	current *entry            // last entry asked for by Get; never evicted
}

// DefaultByteBudget is the pixel memory a new Loader may keep cached: 1.5 GiB.
const DefaultByteBudget = 3 << 29

type entryState int

const (
	queued entryState = iota
	decoding
	finished
)

type entry struct {
	path  string
	state entryState
	done  chan struct{} // closed when finished
	img   *image.NRGBA
	meta  Meta
	err   error
	elem  *list.Element // position in lru once finished and cached
}

// size is the cache cost of a finished entry: its pixel bytes.
func (e *entry) size() int64 {
	if e.img == nil {
		return 0
	}
	return int64(len(e.img.Pix))
}

// NewLoader keeps at most capacity decoded images (recommend 6), and at
// most DefaultByteBudget bytes of pixels, and runs decodes on up to workers
// goroutines (recommend 2).
func NewLoader(capacity, workers int) *Loader {
	return &Loader{
		capacity: max(capacity, 1),
		workers:  max(workers, 1),
		entries:  make(map[string]*entry),
		lru:      list.New(),
		budget:   DefaultByteBudget,
	}
}

// SetByteBudget limits the cached pixel memory (len(img.Pix) summed over
// cached images) to n bytes, evicting least recently used images beyond it.
// The image most recently asked for by Get is always kept, even if it alone
// exceeds the budget. n <= 0 means no byte limit (count cap only).
func (l *Loader) SetByteBudget(n int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.budget = n
	l.evict()
}

// Get blocks until path is decoded. Errors are cached too (so a broken
// file does not re-decode on every visit) but evicted like images.
//
// If path is already being decoded by a prefetch, Get waits for that
// decode. If it is only queued, Get takes it over and decodes it on the
// calling goroutine instead of waiting behind other prefetches.
func (l *Loader) Get(path string) (*image.NRGBA, error) {
	img, _, err := l.GetMeta(path)
	return img, err
}

// GetMeta is Get that also returns the image's Meta. Meta is extracted
// from the same file read as the pixels and cached with the image, so it
// costs nothing extra.
func (l *Loader) GetMeta(path string) (*image.NRGBA, Meta, error) {
	l.mu.Lock()
	e := l.entries[path]
	run := false
	switch {
	case e == nil:
		e = &entry{path: path, state: decoding, done: make(chan struct{})}
		l.entries[path] = e
		run = true
	case e.state == queued:
		e.state = decoding // the worker will skip it
		run = true
	case e.elem != nil:
		l.lru.MoveToFront(e.elem)
	}
	l.current = e
	l.mu.Unlock()
	if run {
		l.run(e)
	} else {
		<-e.done
	}
	return e.img, e.meta, e.err
}

// Prefetch schedules decodes without blocking; already-cached paths are noop
// (apart from counting as recently used). Paths are decoded in the order
// given, ahead of anything queued by earlier calls; stale queued paths
// beyond the cache capacity are dropped.
func (l *Loader) Prefetch(paths ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var fresh []*entry
	for _, p := range paths {
		if e := l.entries[p]; e != nil {
			if e.elem != nil {
				l.lru.MoveToFront(e.elem)
			}
			continue
		}
		e := &entry{path: p, state: queued, done: make(chan struct{})}
		l.entries[p] = e
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 {
		return
	}
	// Remove entries a Get has taken over, then put the new ones first.
	queue := fresh
	for _, e := range l.queue {
		if e.state == queued {
			queue = append(queue, e)
		}
	}
	for _, e := range queue[min(len(queue), l.capacity):] {
		delete(l.entries, e.path) // nobody waits on a queued entry
	}
	l.queue = queue[:min(len(queue), l.capacity)]
	for l.running < l.workers && l.running < len(l.queue) {
		l.running++
		go l.worker()
	}
}

// Invalidate drops path from the cache (used after dir rescans when mtime changed).
// A decode already in flight still completes for its waiters but is not cached.
func (l *Loader) Invalidate(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[path]
	if e == nil {
		return
	}
	delete(l.entries, path)
	if e.elem != nil {
		l.lru.Remove(e.elem)
		l.bytes -= e.size()
		e.elem = nil
	}
	if l.current == e {
		l.current = nil
	}
	if e.state == queued {
		e.state = decoding // makes the worker skip it; nobody waits on it
	}
}

func (l *Loader) worker() {
	for {
		l.mu.Lock()
		var e *entry
		for len(l.queue) > 0 && e == nil {
			e, l.queue = l.queue[0], l.queue[1:]
			if e.state != queued {
				e = nil
			}
		}
		if e == nil {
			l.running--
			l.queue = nil
			l.mu.Unlock()
			return
		}
		e.state = decoding
		l.mu.Unlock()
		l.run(e)
	}
}

// run decodes e (whose state is already decoding) and publishes the result.
func (l *Loader) run(e *entry) {
	var (
		img  *image.NRGBA
		meta Meta
		err  error
	)
	if l.decode != nil {
		img, err = l.decode(e.path)
	} else {
		img, meta, err = decodeWithMeta(e.path)
	}

	l.mu.Lock()
	e.img, e.meta, e.err, e.state = img, meta, err, finished
	if l.entries[e.path] == e { // not invalidated meanwhile
		e.elem = l.lru.PushFront(e)
		l.bytes += e.size()
		l.evict()
	}
	l.mu.Unlock()
	close(e.done)
}

// evict drops least recently used entries while the cache is over its count
// cap or byte budget, sparing the current (last Get) entry. Called with l.mu held.
func (l *Loader) evict() {
	over := func() bool {
		return l.lru.Len() > l.capacity || (l.budget > 0 && l.bytes > l.budget)
	}
	for el := l.lru.Back(); el != nil && over(); {
		old := el.Value.(*entry)
		el = el.Prev()
		if old == l.current {
			continue
		}
		l.lru.Remove(old.elem)
		l.bytes -= old.size()
		old.elem = nil
		delete(l.entries, old.path)
	}
}
