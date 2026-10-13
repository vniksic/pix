package ui

import (
	"image"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"pix/internal/imgload"
	"pix/internal/scan"
)

func TestWithPinned(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n0000000000000000")
	base := time.Now().Add(-time.Hour)
	write := func(name string, data []byte, age int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(age) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("img2.png", png, 3)
	write("img10.png", png, 1)
	write("img1.png", png, 2)
	pinned := write("img3.dat", []byte("not an image"), 0) // List leaves it out

	names := func(es []scan.Entry) (out []string) {
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}
	for _, c := range []struct {
		sort scan.Sort
		want []string
	}{
		{scan.SortName, []string{"img1.png", "img2.png", "img3.dat", "img10.png"}},
		{scan.SortMTime, []string{"img3.dat", "img10.png", "img1.png", "img2.png"}},
	} {
		listed, err := scan.List(dir, c.sort)
		if err != nil || len(listed) != 3 {
			t.Fatalf("List: %v %v", names(listed), err)
		}
		got := names(withPinned(listed, pinned, dir, c.sort))
		if len(got) != len(c.want) {
			t.Fatalf("sort %d: got %v, want %v", c.sort, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("sort %d: got %v, want %v", c.sort, got, c.want)
			}
		}
		// Already listed: unchanged.
		if again := withPinned(listed, listed[0].Path, dir, c.sort); len(again) != 3 {
			t.Errorf("listed pin duplicated: %v", names(again))
		}
	}

	if err := os.Remove(pinned); err != nil {
		t.Fatal(err)
	}
	listed, _ := scan.List(dir, scan.SortName)
	if got := withPinned(listed, pinned, dir, scan.SortName); len(got) != 3 {
		t.Errorf("deleted pin re-inserted: %v", names(got))
	}
}

func TestWithPinnedTree(t *testing.T) {
	root := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n0000000000000000")
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"b.png", png},
		{"sub/a.png", png},
		{"sub/z.png", png},
		{"notes.dat", []byte("not an image")},
		{"sub/m.dat", []byte("not an image")},
	} {
		p := filepath.Join(root, f.name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := scan.ListTree(root, scan.SortName)
	if err != nil {
		t.Fatal(err)
	}
	labels := func(es []scan.Entry) (out []string) {
		for _, e := range es {
			out = append(out, e.Label)
		}
		return out
	}
	for _, c := range []struct {
		pinned string
		want   []string
	}{
		// Among the root's own files, before the subdirectory.
		{"notes.dat", []string{"b.png", "notes.dat", "sub/a.png", "sub/z.png"}},
		// Inside its subdirectory, with a root-relative label.
		{"sub/m.dat", []string{"b.png", "sub/a.png", "sub/m.dat", "sub/z.png"}},
	} {
		got := labels(withPinned(slices.Clone(listed), filepath.Join(root, c.pinned), root, scan.SortName))
		if !slices.Equal(got, c.want) {
			t.Errorf("pin %s: got %q, want %q", c.pinned, got, c.want)
		}
	}
}

func TestSplitLabel(t *testing.T) {
	for _, c := range []struct{ label, name, dir string }{
		{"photo.jpg", "photo.jpg", ""},
		{"sub/photo.jpg", "photo.jpg", "sub"},
		{"sub/dir/photo.jpg", "photo.jpg", "sub/dir"},
		{"~/Pictures/a.jpg", "a.jpg", "~/Pictures"},
		{"/mnt/x/a.jpg", "a.jpg", "/mnt/x"},
		{"/a.jpg", "a.jpg", "/"},
		{"", "", ""},
	} {
		name, dir := splitLabel(c.label)
		if name != c.name || dir != c.dir {
			t.Errorf("splitLabel(%q) = %q, %q; want %q, %q", c.label, name, dir, c.name, c.dir)
		}
	}

	labels := []string{"a.jpg", "b.jpg"}
	at := func(i int) string { return labels[i] }
	if hasDirParts(len(labels), at) {
		t.Error("hasDirParts: true for plain names")
	}
	labels = append(labels, "sub/c.jpg")
	if !hasDirParts(len(labels), at) {
		t.Error("hasDirParts: false with a directory part")
	}
}

// finish (the exit path after the event loop) copies what is marked and not
// yet copied, then prints the marked paths in playlist order.
func TestFinishCopiesAndPrintsMarked(t *testing.T) {
	src, out := t.TempDir(), filepath.Join(t.TempDir(), "keep")
	var paths []string
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"} {
		p := filepath.Join(src, n)
		if err := os.WriteFile(p, []byte("data "+n), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	var stdout, stderr strings.Builder
	u := &viewer{
		opts:   Options{OutDir: out, Stdout: &stdout},
		marks:  map[string]bool{paths[3]: true, paths[0]: true, paths[2]: true, "/gone.jpg": true},
		copied: map[string]bool{paths[2]: true}, // copied earlier with Enter
		final:  paths,
		// Not images, so the rotation cannot be stored, but it is passed on.
		finalRots: map[string]int{paths[0]: 1},
		errOut:    &stderr,
	}
	if err := u.finish(); err != nil {
		t.Fatal(err)
	}
	if want := "pix: copied 2 to " + out + ", rotation not stored for 1 (unknown format)\n"; stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
	if want := paths[0] + "\n" + paths[2] + "\n" + paths[3] + "\n"; stdout.String() != want {
		t.Errorf("stdout %q, want %q", stdout.String(), want)
	}
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"a.jpg", "d.jpg"}) {
		t.Errorf("copied %v, want [a.jpg d.jpg] (c.jpg was already copied)", names)
	}

	// Nothing marked: nothing printed, nothing copied, no -out needed.
	stdout.Reset()
	u = &viewer{opts: Options{Stdout: &stdout}, marks: map[string]bool{}, copied: map[string]bool{}, final: paths}
	if err := u.finish(); err != nil || stdout.Len() != 0 {
		t.Errorf("unmarked: err %v, stdout %q", err, stdout.String())
	}
}

// The copy-out takes the remembered rotations, and for the image on
// screen the one it is shown with (a locked view carries it over without
// remembering it), unless that image is still loading or failed.
func TestCopyRots(t *testing.T) {
	test.NewTempApp(t)
	a, b := scan.Entry{Path: "/p/a.jpg"}, scan.Entry{Path: "/p/b.jpg"}
	u := &viewer{
		entries: []scan.Entry{a, b},
		idx:     1,
		rots:    map[string]int{a.Path: 2},
		vp:      newViewport(),
	}
	u.vp.SetImage(image.NewNRGBA(image.Rect(0, 0, 4, 2)), true)
	u.vp.SetRot(3)
	if got := u.copyRots(); !maps.Equal(got, map[string]int{a.Path: 2, b.Path: 3}) {
		t.Errorf("shown: %v", got)
	}
	if len(u.rots) != 1 {
		t.Errorf("copyRots changed rots: %v", u.rots)
	}
	u.loading = true
	if got := u.copyRots(); !maps.Equal(got, map[string]int{a.Path: 2}) {
		t.Errorf("loading: %v", got)
	}
	u.entries = nil
	if got := u.copyRots(); !maps.Equal(got, map[string]int{a.Path: 2}) {
		t.Errorf("no entries: %v", got)
	}
}

// Snapshots of the scan in progress all apply; those of a scan a newer one
// replaced are dropped; until a scan is done, entries it has not reached
// stay; its last snapshot drops the files that are gone.
func TestApplyEntriesSnapshots(t *testing.T) {
	a := test.NewTempApp(t)
	ent := func(name string) scan.Entry {
		return scan.Entry{Path: "/pics/" + name, Name: name, Label: name}
	}
	b := ent("b.png")
	u := &viewer{
		opts:     Options{Target: scan.Target{Root: "/pics"}},
		app:      a,
		win:      a.NewWindow("pix"),
		loader:   imgload.NewLoader(2, 1),
		entries:  []scan.Entry{b}, // the pinned file, already shown
		marks:    map[string]bool{},
		copied:   map[string]bool{},
		trashing: map[string]bool{},
		rots:     map[string]int{},
		gone:     map[string]bool{},
		scanning: true,
		scanSeq:  1,
	}
	u.build()
	paths := func() (out []string) {
		for _, e := range u.entries {
			out = append(out, e.Name)
		}
		return out
	}
	check := func(step string, want []string, scanning bool) {
		t.Helper()
		if got := paths(); !slices.Equal(got, want) {
			t.Errorf("%s: playlist %q, want %q", step, got, want)
		}
		if u.scanning != scanning || strings.Contains(u.status.Text, "scanning…") != scanning {
			t.Errorf("%s: scanning %v, status %q", step, u.scanning, u.status.Text)
		}
		if len(u.entries) > 0 && u.entries[u.idx].Path != b.Path {
			t.Errorf("%s: current is %s, want b.png", step, u.entries[u.idx].Name)
		}
	}

	u.applyEntries(1, []scan.Entry{ent("a.png"), b}, false, nil)
	check("first snapshot", []string{"a.png", "b.png"}, true)
	u.applyEntries(1, []scan.Entry{ent("a.png"), b, ent("c.png"), ent("d.png")}, false, nil)
	check("same scan, later snapshot", []string{"a.png", "b.png", "c.png", "d.png"}, true)

	u.scanSeq = 2 // a rescan replaced scan 1
	u.applyEntries(1, []scan.Entry{ent("a.png"), b, ent("c.png"), ent("d.png"), ent("e.png")}, true, nil)
	check("older scan dropped", []string{"a.png", "b.png", "c.png", "d.png"}, true)
	u.applyEntries(2, []scan.Entry{b, ent("e.png")}, false, nil)
	check("rescan in progress keeps unreached", []string{"a.png", "b.png", "c.png", "d.png", "e.png"}, true)
	u.applyEntries(2, []scan.Entry{b, ent("c.png"), ent("e.png")}, true, nil)
	check("rescan done", []string{"b.png", "c.png", "e.png"}, false)
	if !strings.HasPrefix(u.status.Text, "b.png   1/3") {
		t.Errorf("status %q", u.status.Text)
	}

	u.scanSeq = 3
	u.scanning = true
	u.applyEntries(3, nil, true, nil)
	check("empty", nil, false)
	if u.vp.msg.Text != "no images" || !strings.HasPrefix(u.status.Text, "no images") {
		t.Errorf("empty: message %q, status %q", u.vp.msg.Text, u.status.Text)
	}
}

func TestMergeListing(t *testing.T) {
	ent := func(name string) scan.Entry { return scan.Entry{Path: "/p/" + name, Name: name, Label: name} }
	cur := []scan.Entry{ent("a"), ent("c"), ent("e")}
	partial := []scan.Entry{ent("b"), ent("c"), ent("f")}
	var got []string
	for _, e := range mergeListing(cur, partial, scan.SortName) {
		got = append(got, e.Name)
	}
	if want := []string{"a", "b", "c", "e", "f"}; !slices.Equal(got, want) {
		t.Errorf("merged %q, want %q", got, want)
	}
}
