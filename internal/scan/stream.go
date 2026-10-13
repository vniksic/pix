package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// workers is how many files are stat'ed, sniffed and dated at once. That
// work is mostly waiting on the disk, or on the network on a remote mount,
// where every file costs a few round trips.
const workers = 16

// emitEvery is the shortest time between two snapshots of a scan, after
// the first. A variable so tests can lower it.
var emitEvery = 100 * time.Millisecond

// firstSnapshot is how many entries the first snapshot waits for: it goes
// out as soon as they are in, so the first pictures show without waiting
// for the throttle.
const firstSnapshot = 8

// Scan lists t as Resolve would, delivering the growing playlist to emit as
// it is discovered. Every call to emit gets a fresh slice (never touched by
// Scan again) holding exactly the entries processed so far (stat'ed,
// sniffed and, for SortDate, dated), in their final order and labelled as
// Resolve labels them. The first snapshot comes as soon as 8 entries are
// in, later ones at most every 100ms, and the last call has done=true. Calls to emit are sequential,
// on the caller's goroutine. When ctx is cancelled, Scan stops starting new
// file work and returns at once, and emit is not called again.
//
// A file named on its own (t.File) is in every snapshot. An unreadable
// listing root gives an empty listing (but for t.File).
func Scan(ctx context.Context, t Target, emit func(entries []Entry, done bool)) {
	if t.Root == "" {
		scanAdhoc(ctx, t, emit)
		return
	}
	if scanRoot(ctx, t.Root, t.File, t.Sort, t.Recursive, emit) != nil && ctx.Err() == nil {
		emit(nil, true)
	}
}

// ScanList is List delivering snapshots as Scan does. It returns an error,
// without calling emit, when dir cannot be read.
func ScanList(ctx context.Context, dir string, sort Sort, emit func(entries []Entry, done bool)) error {
	return scanRoot(ctx, dir, "", sort, false, emit)
}

// ScanTree is ListTree delivering snapshots as Scan does. It returns an
// error, without calling emit, when root cannot be read.
func ScanTree(ctx context.Context, root string, sort Sort, emit func(entries []Entry, done bool)) error {
	return scanRoot(ctx, root, "", sort, true, emit)
}

// scanRoot lists root (recursively when recursive), plus file, when set,
// which is kept even if the listing would leave it out. Without file an
// unreadable root is returned as the error before anything is emitted.
func scanRoot(ctx context.Context, root, file string, sort Sort, recursive bool, emit func([]Entry, bool)) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	s := newScanner(sort, 1)
	if file != "" {
		if e, ok := s.process(job{path: file, label: filepath.Base(file), force: true}); ok {
			s.add(0, e)
		}
	}
	des, err := os.ReadDir(root)
	if err != nil && file == "" {
		return err
	}
	s.run(ctx, emit, func(send func(job) bool) {
		skipFile := func(j job) bool { return j.path == file || send(j) }
		lister{ctx: ctx, send: skipFile, recursive: recursive}.dir(root, "", des)
	})
	return nil
}

// scanAdhoc lists t.Paths in order: each path is a group of its own, a
// directory expanded and sorted within its group, and a path already met
// is skipped.
func scanAdhoc(ctx context.Context, t Target, emit func([]Entry, bool)) {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	s := newScanner(t.Sort, len(t.Paths))
	// Sorted with the labels of each directory's own listing, as List
	// gives them; relabelled for the snapshot.
	s.relabel = func(path string) string { return adhocLabel(path, cwd, home) }
	s.run(ctx, emit, func(send func(job) bool) {
		seen := make(map[string]bool)
		once := func(j job) bool {
			if seen[j.path] {
				return true
			}
			seen[j.path] = true
			return send(j)
		}
		stats := statAll(ctx, t.Paths)
		for i, path := range t.Paths {
			var fi os.FileInfo
			select {
			case fi = <-stats[i]:
			case <-ctx.Done():
				return
			}
			switch {
			case fi == nil: // missing
			case fi.IsDir():
				des, err := os.ReadDir(path)
				if err == nil && !(lister{ctx: ctx, send: once, group: i, recursive: t.Recursive}).dir(path, "", des) {
					return
				}
			default:
				if !once(job{group: i, path: path, label: filepath.Base(path), fi: fi}) {
					return
				}
			}
		}
	})
}

// statAll stats paths concurrently, a pool's worth at a time, ahead of
// whoever reads the results: paths[i]'s FileInfo (nil when it cannot be
// stat'ed) arrives on the i-th channel. Ad-hoc lists read from stdin can be
// long, and the directory arguments among them must be told apart from the
// files before the list can be put in order.
func statAll(ctx context.Context, paths []string) []chan os.FileInfo {
	out := make([]chan os.FileInfo, len(paths))
	for i := range out {
		out[i] = make(chan os.FileInfo, 1)
	}
	go func() {
		sem := make(chan struct{}, workers)
		for i, p := range paths {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				defer func() { <-sem }()
				fi, err := os.Stat(p)
				if err != nil {
					fi = nil
				}
				out[i] <- fi
			}()
		}
	}()
	return out
}

// lister sends the candidate files of a directory listing to the workers.
// Directory reads stay sequential, on the discovering goroutine.
type lister struct {
	ctx       context.Context
	send      func(job) bool // false: cancelled
	group     int
	recursive bool
}

// dir sends the files of dir (rel: dir relative to the listing root, ""
// for the root itself), given its entries, and below it when recursive.
// It reports false once the scan is cancelled.
func (l lister) dir(dir, rel string, des []os.DirEntry) bool {
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if de.IsDir() { // a real directory; symlinks report their own type
			if !l.recursive {
				continue
			}
			if l.ctx.Err() != nil {
				return false
			}
			if sub, err := os.ReadDir(path); err == nil && !l.dir(path, joinRel(rel, name), sub) {
				return false
			}
			continue
		}
		if !l.send(job{group: l.group, path: path, label: joinRel(rel, name)}) {
			return false
		}
	}
	return true
}

// job is one candidate file for the workers.
type job struct {
	group int    // position of its group in the playlist (see scanner)
	path  string // absolute
	label string // Label within its own listing
	fi    os.FileInfo
	force bool // keep it even if it is not a regular image file
}

// scanner collects the entries the workers produce. The playlist is its
// groups one after the other, each sorted on its own: one group for a
// listing, one per argument for an ad-hoc list.
type scanner struct {
	sort    Sort
	c       *takenCache
	relabel func(path string) string // nil: keep the listing's labels

	mu     sync.Mutex
	groups []group
	dirty  bool          // entries were added since the last snapshot
	count  int           // entries added in all
	early  chan struct{} // closed when count reaches firstSnapshot
}

type group struct {
	sorted []Entry // as of the last snapshot
	added  []Entry // since then, unsorted
}

func newScanner(sort Sort, groups int) *scanner {
	return &scanner{sort: sort, c: takenSource.Load(), groups: make([]group, groups), early: make(chan struct{})}
}

// run processes the jobs discover sends on a pool of workers and emits
// snapshots until discover has returned and every job is done (see Scan).
// discover's send reports false once ctx is cancelled.
func (s *scanner) run(ctx context.Context, emit func([]Entry, bool), discover func(send func(job) bool)) {
	jobs := make(chan job)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				if e, ok := s.process(j); ok {
					s.add(j.group, e)
				}
			}
		})
	}
	go func() {
		defer close(jobs)
		discover(func(j job) bool {
			select {
			case jobs <- j:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	idle := make(chan struct{})
	go func() {
		wg.Wait()
		close(idle)
	}()

	tick := time.NewTicker(emitEvery)
	defer tick.Stop()
	early := s.early
	for {
		select {
		case <-ctx.Done():
			return
		case <-early:
			early = nil
			if ctx.Err() == nil {
				emit(s.snapshot(), false)
				tick.Reset(emitEvery) // the throttle counts from here
			}
		case <-idle:
			if ctx.Err() == nil {
				emit(s.snapshot(), true)
			}
			return
		case <-tick.C:
			if ctx.Err() == nil && s.changed() {
				emit(s.snapshot(), false)
			}
		}
	}
}

// process does the per-file work: stat (unless known), sniff, date.
func (s *scanner) process(j job) (Entry, bool) {
	fi := j.fi
	if fi == nil {
		var err error
		if fi, err = os.Stat(j.path); err != nil { // follows symlinks
			return Entry{}, false
		}
	}
	if !j.force && (!fi.Mode().IsRegular() || !IsImage(j.path)) {
		return Entry{}, false
	}
	e := entryOf(j.path, j.label, fi)
	if s.sort == SortDate {
		e.Taken = s.c.taken(e)
	}
	return e, true
}

func (s *scanner) add(g int, e Entry) {
	s.mu.Lock()
	s.groups[g].added = append(s.groups[g].added, e)
	s.dirty = true
	if s.count++; s.count == firstSnapshot {
		close(s.early)
	}
	s.mu.Unlock()
}

func (s *scanner) changed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// snapshot returns the playlist so far as a new slice. Only the entries
// added since the last snapshot are sorted; they are merged into the rest.
func (s *scanner) snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = false
	n := 0
	for i := range s.groups {
		g := &s.groups[i]
		if len(g.added) > 0 {
			sortEntries(g.added, s.sort)
			g.sorted = mergeSorted(g.sorted, g.added, s.sort)
			g.added = g.added[:0]
		}
		n += len(g.sorted)
	}
	out := make([]Entry, 0, n)
	for _, g := range s.groups {
		out = append(out, g.sorted...)
	}
	if s.relabel != nil {
		for i := range out {
			out[i].Label = s.relabel(out[i].Path)
		}
	}
	return out
}

// mergeSorted merges two sorted lists into a new one.
func mergeSorted(a, b []Entry, sort Sort) []Entry {
	out := make([]Entry, 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		if Compare(b[0], a[0], sort) < 0 {
			out, b = append(out, b[0]), b[1:]
		} else {
			out, a = append(out, a[0]), a[1:]
		}
	}
	return append(append(out, a...), b...)
}
