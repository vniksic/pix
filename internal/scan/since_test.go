package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mtimeBorn makes the files' modification times their creation times for
// the test (birth times cannot be set), and counts the lookups.
func mtimeBorn(t *testing.T) *atomic.Int64 {
	t.Helper()
	calls := new(atomic.Int64)
	statBorn = func(path string) (os.FileInfo, time.Time, error) {
		calls.Add(1)
		fi, err := os.Stat(path)
		if err != nil {
			return nil, time.Time{}, err
		}
		return fi, fi.ModTime(), nil
	}
	t.Cleanup(func() { statBorn = statCreated })
	return calls
}

// sinceTree: old files (t0) and new ones (now) at three levels, the
// directories themselves dated t0, and a new non-image.
func sinceTree(t *testing.T) string {
	root := t.TempDir()
	now := time.Now()
	writeAt(t, filepath.Join(root, "old.png"), t0)
	writeAt(t, filepath.Join(root, "new.png"), now)
	write(t, filepath.Join(root, "notes.txt"), []byte("not an image"))
	writeAt(t, filepath.Join(root, "sub", "old2.png"), t0)
	writeAt(t, filepath.Join(root, "sub", "new2.png"), now)
	writeAt(t, filepath.Join(root, "sub", "deeper", "new3.png"), now)
	for _, d := range []string{"sub/deeper", "sub"} {
		if err := os.Chtimes(filepath.Join(root, d), t0, t0); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSinceScans(t *testing.T) {
	mtimeBorn(t)
	fastEmits(t)
	root := sinceTree(t)
	cutoff := time.Now().Add(-time.Hour)
	opts := Options{Recursive: true, Since: cutoff}
	newOnes := []string{"new.png", "sub/new2.png", "sub/deeper/new3.png"}

	// A directory: old files go, old directories are still descended.
	es, start, _, err := Resolve([]string{root}, opts)
	if err != nil || start != 0 || !slices.Equal(labels(es), newOnes) {
		t.Errorf("dir: %v start %d %v, want %v", labels(es), start, err, newOnes)
	}

	// SortMTime, SortShuffle: the same files.
	for _, s := range []Sort{SortMTime, SortShuffle} {
		es, _, _, _ := Resolve([]string{root}, Options{Sort: s, Recursive: true, Since: cutoff})
		got := labels(es)
		slices.Sort(got)
		if want := slices.Sorted(slices.Values(newOnes)); !slices.Equal(got, want) {
			t.Errorf("sort %d: %v, want %v", s, got, want)
		}
	}

	// A named old file is left out of its parent's listing; a new one is
	// where the listing starts.
	es, start, _, err = Resolve([]string{filepath.Join(root, "old.png")}, opts)
	if err != nil || start != 0 || !slices.Equal(labels(es), newOnes) {
		t.Errorf("old file: %v start %d %v", labels(es), start, err)
	}
	es, start, _, err = Resolve([]string{filepath.Join(root, "sub", "new2.png")}, opts)
	if err != nil || !slices.Equal(labels(es), []string{"new2.png", "deeper/new3.png"}) || es[start].Name != "new2.png" {
		t.Errorf("new file: %v start %d %v", labels(es), start, err)
	}

	// Ad-hoc list: named files and expanded directories alike.
	es, _, _, err = Resolve([]string{filepath.Join(root, "old.png"), filepath.Join(root, "new.png"), filepath.Join(root, "sub")}, opts)
	if want := []string{"new.png", "new2.png", "new3.png"}; err != nil || !slices.Equal(names(es), want) {
		t.Errorf("ad-hoc: %v %v, want %v", names(es), err, want)
	}
	es, _, _, err = ResolveReader(strings.NewReader(filepath.Join(root, "old.png")+"\n"+filepath.Join(root, "sub", "new2.png")+"\n"), opts)
	if err != nil || !slices.Equal(names(es), []string{"new2.png"}) {
		t.Errorf("stdin: %v %v", names(es), err)
	}

	// Nothing new: the error says why.
	_, _, _, err = Resolve([]string{filepath.Join(root, "old.png"), filepath.Join(root, "sub", "old2.png")}, opts)
	if err == nil || !strings.Contains(err.Error(), "created since") {
		t.Errorf("all old: %v", err)
	}
}

// A rescan (ScanTree, ScanList with the startup cutoff) keeps the filter:
// a new file shows up, an old one moved in does not, and what was listed
// stays listed.
func TestSinceRescan(t *testing.T) {
	mtimeBorn(t)
	root := sinceTree(t)
	cutoff := time.Now().Add(-time.Hour)
	tree := func() []string {
		var err error
		es := final(func(emit func([]Entry, bool)) { err = ScanTree(context.Background(), root, SortName, cutoff, emit) })
		if err != nil {
			t.Fatal(err)
		}
		return labels(es)
	}
	if got, want := tree(), []string{"new.png", "sub/new2.png", "sub/deeper/new3.png"}; !slices.Equal(got, want) {
		t.Errorf("first: %v, want %v", got, want)
	}
	writeAt(t, filepath.Join(root, "sub", "later.png"), time.Now())
	writeAt(t, filepath.Join(root, "sub", "moved-in.png"), t0)
	if got, want := tree(), []string{"new.png", "sub/later.png", "sub/new2.png", "sub/deeper/new3.png"}; !slices.Equal(got, want) {
		t.Errorf("rescan: %v, want %v", got, want)
	}

	var flat []Entry
	err := ScanList(context.Background(), root, SortName, cutoff, func(es []Entry, done bool) { flat = es })
	if err != nil || !slices.Equal(names(flat), []string{"new.png"}) {
		t.Errorf("ScanList: %v %v", names(flat), err)
	}
	// No cutoff: everything, as before.
	all, _ := ListTree(root, SortName)
	if len(all) != 7 {
		t.Errorf("ListTree without cutoff: %v", labels(all))
	}
}

// StatSince: a zero cutoff is os.Stat, old directories pass, old files do
// not, and the creation time is looked up once per file.
func TestStatSince(t *testing.T) {
	calls := mtimeBorn(t)
	root := sinceTree(t)
	cutoff := time.Now().Add(-time.Hour)
	if _, err := StatSince(filepath.Join(root, "old.png"), time.Time{}); err != nil || calls.Load() != 0 {
		t.Errorf("zero cutoff: %v, %d lookups", err, calls.Load())
	}
	if fi, err := StatSince(filepath.Join(root, "sub"), cutoff); err != nil || !fi.IsDir() {
		t.Errorf("old dir: %v", err)
	}
	if _, err := StatSince(filepath.Join(root, "old.png"), cutoff); !errors.Is(err, errOld) {
		t.Errorf("old file: %v", err)
	}
	if fi, err := StatSince(filepath.Join(root, "new.png"), cutoff); err != nil || fi.Name() != "new.png" {
		t.Errorf("new file: %v", err)
	}
	if _, err := StatSince(filepath.Join(root, "missing.png"), cutoff); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("%d lookups for 4 stats", n)
	}

	// Each file of a scan costs one lookup, old ones included.
	calls.Store(0)
	final(func(emit func([]Entry, bool)) { ScanTree(context.Background(), root, SortName, cutoff, emit) })
	if n := calls.Load(); n != 6 { // the six files; directories are not stat'ed
		t.Errorf("tree scan: %d lookups, want 6", n)
	}
}

// The real lookup on the test's filesystem: a file whose mtime is long
// past has either a birth time of about now or, where the filesystem has
// none, its mtime. Its FileInfo matches os.Stat's.
func TestStatCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	before := time.Now()
	writeAt(t, path, t0)
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, link, dir} {
		fi, created, err := statCreated(p)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.Stat(p)
		if fi.Name() != want.Name() || fi.Size() != want.Size() || fi.Mode() != want.Mode() ||
			!fi.ModTime().Equal(want.ModTime()) || fi.IsDir() != want.IsDir() {
			t.Errorf("%s: FileInfo %v %v %v %v, os.Stat %v %v %v %v", p,
				fi.Name(), fi.Size(), fi.Mode(), fi.ModTime(), want.Name(), want.Size(), want.Mode(), want.ModTime())
		}
		if p != path {
			continue
		}
		switch {
		case created.Equal(t0):
			t.Logf("%s: no birth time here, mtime used", dir)
		case !created.Before(before.Add(-time.Second)) && created.Before(time.Now().Add(time.Second)):
			t.Logf("%s: birth time %v", dir, created)
		default:
			t.Errorf("created %v: neither about now (%v) nor the mtime %v", created, before, t0)
		}
	}
	if _, _, err := statCreated(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}
