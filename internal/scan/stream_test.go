package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// bigTree makes dirs × per images with scattered mtimes, plus files every
// scan must skip, and returns the root.
func bigTree(t *testing.T, dirs, per int) string {
	t.Helper()
	root := t.TempDir()
	p := pngBytes(t)
	for d := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%d", d))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range per {
			path := filepath.Join(dir, fmt.Sprintf("img%d.png", i))
			write(t, path, p)
			mt := t0.Add(time.Duration((d*per+i)*37%(dirs*per)) * time.Minute)
			if err := os.Chtimes(path, mt, mt); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(dir, "notes.txt"), []byte("not an image"))
		write(t, filepath.Join(dir, ".hidden.png"), p)
	}
	write(t, filepath.Join(root, "top.png"), p)
	return root
}

// record runs scan and checks the rules every scan's snapshots follow:
// done is reported exactly once, last; each snapshot holds every entry of
// the one before; and each is a subsequence of the final one, so it is in
// the final order. It returns the snapshots.
func record(t *testing.T, scan func(emit func([]Entry, bool))) [][]Entry {
	t.Helper()
	var snaps [][]Entry
	dones := 0
	scan(func(es []Entry, done bool) {
		if dones > 0 {
			t.Error("emit after done")
		}
		if done {
			dones++
		}
		snaps = append(snaps, es)
	})
	if dones != 1 {
		t.Fatalf("done reported %d times", dones)
	}
	final := snaps[len(snaps)-1]
	for k, s := range snaps {
		if !subsequence(s, final) {
			t.Errorf("snapshot %d of %d is not in the final order: %q", k, len(snaps), labels(s))
		}
		if k > 0 {
			in := make(map[string]bool)
			for _, e := range s {
				in[e.Path] = true
			}
			for _, e := range snaps[k-1] {
				if !in[e.Path] {
					t.Errorf("snapshot %d lost %s", k, e.Label)
				}
			}
		}
	}
	return snaps
}

// subsequence reports whether a's paths appear in b in the same order.
func subsequence(a, b []Entry) bool {
	j := 0
	for _, e := range a {
		for j < len(b) && b[j].Path != e.Path {
			j++
		}
		if j == len(b) {
			return false
		}
		j++
	}
	return true
}

func sameEntries(a, b []Entry) bool {
	return slices.EqualFunc(a, b, func(x, y Entry) bool {
		return x.Path == y.Path && x.Name == y.Name && x.Label == y.Label && x.Size == y.Size &&
			x.ModTime.Equal(y.ModTime) && x.Taken.Equal(y.Taken)
	})
}

func fastEmits(t *testing.T) {
	old := emitEvery
	emitEvery = time.Millisecond
	t.Cleanup(func() { emitEvery = old })
}

// The final snapshot of each streaming scan is what its synchronous
// counterpart returns, for every order.
func TestScanMatchesSync(t *testing.T) {
	fastEmits(t)
	root := bigTree(t, 4, 30)
	sub := filepath.Join(root, "d1")
	f := newFakeTaken(map[string]time.Time{"img3.png": t0.Add(-time.Hour), "img7.png": t0.Add(time.Hour)})
	bg := context.Background()
	for _, sort := range []Sort{SortName, SortMTime, SortDate} {
		opts := Options{Sort: sort, Taken: f.taken}
		for _, recursive := range []bool{false, true} {
			opts.Recursive = recursive
			name := fmt.Sprintf("sort %d recursive %v", sort, recursive)
			check := func(what string, want, got []Entry) {
				t.Helper()
				if !sameEntries(got, want) {
					t.Errorf("%s %s:\n got %q\nwant %q", name, what, labels(got), labels(want))
				}
			}
			for _, args := range [][]string{
				{root},
				{filepath.Join(sub, "img5.png")},
				{filepath.Join(sub, "img5.png"), root, filepath.Join(root, "missing"), sub},
			} {
				want, start, _, err := Resolve(args, opts)
				if err != nil {
					t.Fatal(err)
				}
				tg, err := Prepare(args, opts)
				if err != nil {
					t.Fatal(err)
				}
				snaps := record(t, func(emit func([]Entry, bool)) { Scan(bg, tg, emit) })
				check(fmt.Sprintf("Scan %d args", len(args)), want, snaps[len(snaps)-1])
				if tg.File != "" {
					for k, s := range snaps {
						if indexOf(s, tg.File) < 0 {
							t.Errorf("%s: the named file is missing from snapshot %d", name, k)
						}
					}
					if want[start].Path != tg.File {
						t.Errorf("%s: start %d is not the named file", name, start)
					}
				}
			}

			list, listScan := List, ScanList
			if recursive {
				list, listScan = ListTree, ScanTree
			}
			want, err := list(root, sort)
			if err != nil {
				t.Fatal(err)
			}
			snaps := record(t, func(emit func([]Entry, bool)) {
				if err := listScan(bg, root, sort, emit); err != nil {
					t.Fatal(err)
				}
			})
			check("listing", want, snaps[len(snaps)-1])
		}
	}

	calls := 0
	err := ScanTree(bg, filepath.Join(root, "missing"), SortName, func([]Entry, bool) { calls++ })
	if err == nil || calls != 0 {
		t.Errorf("ScanTree(missing) = %v after %d emits", err, calls)
	}
}

// A slow scan (dates read at 20ms a file) delivers a first snapshot as soon
// as a few entries are in, then partial ones at most one per emitEvery, and
// ends in the full SortDate order.
func TestScanSnapshots(t *testing.T) {
	root := bigTree(t, 3, 40)
	f := newFakeTaken(map[string]time.Time{"img1.png": t0.Add(-time.Hour), "img2.png": t0.Add(-2 * time.Hour)})
	f.delay = 20 * time.Millisecond
	tg, err := Prepare([]string{root}, Options{Sort: SortDate, Recursive: true, Taken: f.taken})
	if err != nil {
		t.Fatal(err)
	}
	var at []time.Time
	start := time.Now()
	snaps := record(t, func(emit func([]Entry, bool)) {
		Scan(context.Background(), tg, func(es []Entry, done bool) {
			at = append(at, time.Now())
			emit(es, done)
		})
	})
	if len(snaps) < 3 {
		t.Fatalf("%d snapshots, want partial ones too", len(snaps))
	}
	if n, after := len(snaps[0]), at[0].Sub(start); n < firstSnapshot || after >= emitEvery {
		t.Errorf("first snapshot: %d entries after %v, want %d+ before %v", n, after, firstSnapshot, emitEvery)
	}
	for k := 1; k < len(at)-1; k++ {
		if gap := at[k].Sub(at[k-1]); gap < emitEvery/2 {
			t.Errorf("snapshots %d and %d only %v apart", k-1, k, gap)
		}
	}
	final := snaps[len(snaps)-1]
	if len(final) != 3*40+1 {
		t.Errorf("final has %d entries, want %d", len(final), 3*40+1)
	}
	for i := 1; i < len(final); i++ {
		if Compare(final[i-1], final[i], SortDate) >= 0 {
			t.Errorf("not in SortDate order: %s before %s", final[i-1].Label, final[i].Label)
		}
	}
	for _, e := range final {
		if !e.Taken.Equal(f.dates[e.Name]) {
			t.Errorf("%s: Taken %v, want %v", e.Label, e.Taken, f.dates[e.Name])
		}
	}
	if final[0].Name != "img2.png" || final[3].Name != "img1.png" {
		t.Errorf("dated files not first: %q", labels(final[:6]))
	}
	want, _ := ListTree(root, SortDate) // dates are cached now: quick
	if !sameEntries(final, want) {
		t.Errorf("final differs from ListTree")
	}
}

// Ad-hoc lists keep argument order in every snapshot, even while later
// arguments finish first.
func TestScanAdhocOrder(t *testing.T) {
	root := bigTree(t, 2, 50)
	a, b := filepath.Join(root, "d0"), filepath.Join(root, "d1")
	one := filepath.Join(a, "img7.png")
	f := newFakeTaken(nil)
	f.delay = 20 * time.Millisecond
	args := []string{b, filepath.Join(root, "top.png"), a, one, filepath.Join(root, "nope.png")}
	tg, err := Prepare(args, Options{Sort: SortDate, Taken: f.taken})
	if err != nil {
		t.Fatal(err)
	}
	snaps := record(t, func(emit func([]Entry, bool)) { Scan(context.Background(), tg, emit) })
	if len(snaps) < 2 {
		t.Fatalf("%d snapshots, want partial ones too", len(snaps))
	}
	final := snaps[len(snaps)-1]
	want, _, _, err := Resolve(args, Options{Sort: SortDate, Taken: f.taken})
	if err != nil || !sameEntries(final, want) {
		t.Errorf("final %q\nwant  %q (%v)", labels(final), labels(want), err)
	}
	// d1's images, then top.png, then d0's (img7.png only once).
	if len(final) != 101 || final[50].Name != "top.png" || filepath.Dir(final[0].Path) != b || filepath.Dir(final[51].Path) != a {
		t.Errorf("not in argument order: %q", labels(final))
	}
}

// Cancelling a scan stops new file work at once and returns Scan without
// another snapshot.
func TestScanCancel(t *testing.T) {
	root := bigTree(t, 4, 100)
	f := newFakeTaken(nil)
	f.delay = 20 * time.Millisecond
	if _, err := Prepare([]string{root}, Options{Sort: SortDate, Taken: f.taken}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var emits, atCancel atomic.Int32
	returned := make(chan error)
	go func() {
		returned <- ScanTree(ctx, root, SortDate, func([]Entry, bool) {
			if emits.Add(1) == 1 {
				atCancel.Store(int32(f.total()))
				cancel()
			}
		})
	}()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ScanTree did not return after cancel")
	}
	time.Sleep(300 * time.Millisecond) // let work in flight finish
	if n := emits.Load(); n != 1 {
		t.Errorf("%d emits, want 1 (the one that cancelled)", n)
	}
	if got, limit := f.total(), int(atCancel.Load())+workers; got > limit {
		t.Errorf("%d files dated after cancel at %d, want at most %d", got, atCancel.Load(), limit)
	}
	if f.total() >= 4*100 {
		t.Error("every file was dated despite the cancel")
	}

	// Cancelled before it starts: nothing at all.
	n := 0
	Scan(ctx, Target{Root: root}, func([]Entry, bool) { n++ })
	if n != 0 {
		t.Errorf("cancelled Scan emitted %d times", n)
	}
}

// Prepare reports the errors that are cheap to find, and only those.
func TestPrepare(t *testing.T) {
	dir := makeDir(t)
	if _, err := Prepare([]string{filepath.Join(dir, "missing")}, Options{}); err == nil {
		t.Error("missing path: no error")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o300); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := Prepare([]string{locked}, Options{}); err == nil {
			t.Error("unreadable directory: no error")
		}
	}
	empty := t.TempDir()
	tg, err := Prepare([]string{empty}, Options{Recursive: true})
	if err != nil || tg.Root != empty || !tg.Recursive {
		t.Errorf("empty dir: %+v %v", tg, err)
	}
	file := filepath.Join(dir, "img2.png")
	if tg, err := Prepare([]string{file}, Options{}); err != nil || tg.Root != dir || tg.File != file {
		t.Errorf("file: %+v %v", tg, err)
	}
	t.Chdir(dir)
	tg, err = Prepare([]string{"img2.png", "missing.png"}, Options{Sort: SortMTime})
	if err != nil || tg.Root != "" || tg.Sort != SortMTime || !slices.Equal(tg.Paths, []string{file, filepath.Join(dir, "missing.png")}) {
		t.Errorf("ad-hoc: %+v %v", tg, err)
	}
}
