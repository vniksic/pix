package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
