package scan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2024, 5, 1, 12, 0, 0, 0, time.Local)

// fakeTaken returns dates by base name and counts calls per path.
type fakeTaken struct {
	dates map[string]time.Time
	delay time.Duration // per call, to make a scan slow
	mu    sync.Mutex
	calls map[string]int
}

func newFakeTaken(dates map[string]time.Time) *fakeTaken {
	return &fakeTaken{dates: dates, calls: map[string]int{}}
}

func (f *fakeTaken) taken(path string) (time.Time, bool) {
	f.mu.Lock()
	f.calls[path]++
	f.mu.Unlock()
	time.Sleep(f.delay)
	t, ok := f.dates[filepath.Base(path)]
	return t, ok
}

func (f *fakeTaken) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// writeAt writes a PNG with the given mtime.
func writeAt(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, pngBytes(t))
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// dateDir: a taken 1h, b no date (mtime 2h), c taken 5h, d no date
// (mtime 1h, ties with a), e taken 0h but newest mtime.
func dateDir(t *testing.T) (string, *fakeTaken) {
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, "a.png"), t0.Add(3*time.Hour))
	writeAt(t, filepath.Join(dir, "b.png"), t0.Add(2*time.Hour))
	writeAt(t, filepath.Join(dir, "c.png"), t0)
	writeAt(t, filepath.Join(dir, "d.png"), t0.Add(time.Hour))
	writeAt(t, filepath.Join(dir, "e.png"), t0.Add(9*time.Hour))
	return dir, newFakeTaken(map[string]time.Time{
		"a.png": t0.Add(time.Hour),
		"c.png": t0.Add(5 * time.Hour),
		"e.png": t0.Add(-time.Hour),
	})
}

func TestSortDate(t *testing.T) {
	dir, f := dateDir(t)
	opts := Options{Sort: SortDate, Taken: f.taken}
	es, start, roots, err := Resolve([]string{dir}, opts)
	if err != nil || start != 0 || !slices.Equal(roots, []string{dir}) {
		t.Fatal(es, start, roots, err)
	}
	want := []string{"e.png", "a.png", "d.png", "b.png", "c.png"}
	if got := names(es); !slices.Equal(got, want) {
		t.Errorf("SortDate = %q, want %q", got, want)
	}
	for _, e := range es {
		wantTaken := f.dates[e.Name]
		if !e.Taken.Equal(wantTaken) {
			t.Errorf("%s: Taken %v, want %v", e.Name, e.Taken, wantTaken)
		}
	}
	for i := 1; i < len(es); i++ {
		if Compare(es[i-1], es[i], SortDate) >= 0 {
			t.Errorf("Compare(%s, %s) >= 0", es[i-1].Name, es[i].Name)
		}
	}

	// List (as used for rescans) reuses the Taken of the last Resolve,
	// and its cache: nothing is read again.
	calls := f.total()
	es, err = List(dir, SortDate)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(es); !slices.Equal(got, want) {
		t.Errorf("List SortDate = %q, want %q", got, want)
	}
	if f.total() != calls {
		t.Errorf("List re-read dates: %d calls, had %d", f.total(), calls)
	}
	// A changed file is read again.
	if err := os.Chtimes(filepath.Join(dir, "b.png"), t0, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	List(dir, SortDate)
	if n := f.calls[filepath.Join(dir, "b.png")]; n != 2 {
		t.Errorf("changed file read %d times, want 2", n)
	}

	// Other sorts leave Taken zero.
	es, _ = List(dir, SortName)
	for _, e := range es {
		if !e.Taken.IsZero() {
			t.Errorf("SortName filled Taken for %s", e.Name)
		}
	}

	// Without a Taken function SortDate is SortMTime, for Resolve and for
	// the List calls after it.
	es, _, _, err = Resolve([]string{dir}, Options{Sort: SortDate})
	if err != nil {
		t.Fatal(err)
	}
	mt, _ := List(dir, SortMTime)
	if got, want := names(es), names(mt); !slices.Equal(got, want) {
		t.Errorf("SortDate without Taken = %q, want mtime order %q", got, want)
	}
	es, _ = List(dir, SortDate)
	if got, want := names(es), names(mt); !slices.Equal(got, want) {
		t.Errorf("List SortDate without Taken = %q, want %q", got, want)
	}
	for _, e := range es {
		if !e.Taken.IsZero() {
			t.Errorf("Taken filled without a Taken function: %s", e.Name)
		}
	}
}

func TestSortDateFileAndAdhoc(t *testing.T) {
	dir, f := dateDir(t)
	opts := Options{Sort: SortDate, Taken: f.taken}

	es, start, _, err := Resolve([]string{filepath.Join(dir, "d.png")}, opts)
	if err != nil || es[start].Name != "d.png" || start != 2 {
		t.Errorf("one file: start %d in %q, %v", start, names(es), err)
	}

	// Ad-hoc lists keep argument order but still carry Taken.
	es, _, _, err = Resolve([]string{filepath.Join(dir, "c.png"), filepath.Join(dir, "a.png")}, opts)
	if err != nil || !slices.Equal(names(es), []string{"c.png", "a.png"}) {
		t.Fatalf("ad-hoc: %q, %v", names(es), err)
	}
	if !es[0].Taken.Equal(f.dates["c.png"]) || !es[1].Taken.Equal(f.dates["a.png"]) {
		t.Errorf("ad-hoc Taken not filled: %+v", es)
	}
}

func TestSortDateTree(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "z.png"), t0.Add(4*time.Hour))
	writeAt(t, filepath.Join(root, "sub", "x.png"), t0.Add(9*time.Hour))
	writeAt(t, filepath.Join(root, "sub", "y.png"), t0.Add(2*time.Hour))
	writeAt(t, filepath.Join(root, "a.png"), t0.Add(3*time.Hour))
	f := newFakeTaken(map[string]time.Time{"x.png": t0.Add(time.Hour)})

	es, _, _, err := Resolve([]string{root}, Options{Sort: SortDate, Recursive: true, Taken: f.taken})
	if err != nil {
		t.Fatal(err)
	}
	// Flat over the tree: x (taken 1h), y (2h), a (3h), z (4h).
	if got, want := labels(es), []string{"sub/x.png", "sub/y.png", "a.png", "z.png"}; !slices.Equal(got, want) {
		t.Errorf("tree SortDate = %q, want %q", got, want)
	}
	es, _ = ListTree(root, SortDate)
	if got, want := labels(es), []string{"sub/x.png", "sub/y.png", "a.png", "z.png"}; !slices.Equal(got, want) {
		t.Errorf("ListTree SortDate = %q, want %q", got, want)
	}
}

func TestCompareSortDate(t *testing.T) {
	e := func(label string, mod, taken time.Time) Entry {
		return Entry{Path: "/r/" + label, Name: label, Label: label, ModTime: mod, Taken: taken}
	}
	cases := []struct {
		a, b Entry
		want int
	}{
		{e("a", t0, t0.Add(time.Hour)), e("b", t0.Add(30*time.Minute), time.Time{}), 1}, // taken vs mtime
		{e("a", t0.Add(time.Hour), time.Time{}), e("b", t0, t0.Add(time.Hour)), -1},     // tie -> label
		{e("b", t0.Add(time.Hour), time.Time{}), e("a", t0, t0.Add(time.Hour)), 1},      // tie -> label
		{e("a", t0.Add(time.Hour), t0), e("b", t0, t0.Add(time.Minute)), -1},            // mtime ignored when taken
		{e("x", t0, time.Time{}), e("x", t0, time.Time{}), 0},
	}
	for i, c := range cases {
		if got := Compare(c.a, c.b, SortDate); got != c.want {
			t.Errorf("case %d: Compare = %d, want %d", i, got, c.want)
		}
	}
}

func TestResolveReader(t *testing.T) {
	root := t.TempDir()
	writeAt(t, filepath.Join(root, "one.png"), t0)
	writeAt(t, filepath.Join(root, "two.png"), t0)
	writeAt(t, filepath.Join(root, "dir", "b.png"), t0)
	writeAt(t, filepath.Join(root, "dir", "a.png"), t0)
	write(t, filepath.Join(root, "notes.txt"), []byte("not an image"))
	other := t.TempDir()
	writeAt(t, filepath.Join(other, "far.png"), t0)
	t.Chdir(root)

	input := strings.Join([]string{
		"two.png",
		"",
		"   ",
		"dir",
		"missing.png",
		"notes.txt",
		filepath.Join(other, "far.png"),
		"./one.png",
		"two.png", // duplicate
	}, "\r\n") + "\r\n\n"
	es, start, roots, err := ResolveReader(strings.NewReader(input), Options{})
	if err != nil || start != 0 || roots != nil {
		t.Fatal(es, start, roots, err)
	}
	want := []string{"two.png", "dir/a.png", "dir/b.png", filepath.Join(other, "far.png"), "one.png"}
	if got := labels(es); !slices.Equal(got, want) {
		t.Errorf("labels %q\nwant   %q", got, want)
	}
	for _, e := range es {
		if !filepath.IsAbs(e.Path) || strings.ContainsRune(e.Path, '\r') {
			t.Errorf("bad path %q", e.Path)
		}
	}

	// One path is still an ad-hoc list, not its directory's listing.
	es, _, roots, err = ResolveReader(strings.NewReader("one.png\n"), Options{})
	if err != nil || len(es) != 1 || roots != nil {
		t.Errorf("single path: %q, %v, %v", labels(es), roots, err)
	}

	for _, in := range []string{"", "\n\r\n", "missing.png\nnotes.txt\n"} {
		if _, _, _, err := ResolveReader(strings.NewReader(in), Options{}); err == nil {
			t.Errorf("%q: no error", in)
		}
	}

	// Resolve reads stdin for exactly ["-"].
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	stdin.WriteString("one.png\ndir\n")
	stdin.Seek(0, 0)
	old := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = old }()
	es, _, roots, err = Resolve([]string{"-"}, Options{})
	if got, want := labels(es), []string{"one.png", "dir/a.png", "dir/b.png"}; err != nil || roots != nil || !slices.Equal(got, want) {
		t.Errorf(`Resolve("-") = %q, %v, %v`, got, roots, err)
	}
	// "-" among other arguments is just a (missing) path.
	es, _, _, err = Resolve([]string{"-", "two.png"}, Options{})
	if err != nil || !slices.Equal(labels(es), []string{"two.png"}) {
		t.Errorf(`Resolve("-", "two.png") = %q, %v`, labels(es), err)
	}
}
