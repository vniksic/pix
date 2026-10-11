package ui

import (
	"os"
	"path/filepath"
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
		got := names(withPinned(listed, pinned, c.sort))
		if len(got) != len(c.want) {
			t.Fatalf("sort %d: got %v, want %v", c.sort, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("sort %d: got %v, want %v", c.sort, got, c.want)
			}
		}
		// Already listed: unchanged.
		if again := withPinned(listed, listed[0].Path, c.sort); len(again) != 3 {
			t.Errorf("listed pin duplicated: %v", names(again))
		}
	}

	if err := os.Remove(pinned); err != nil {
		t.Fatal(err)
	}
	listed, _ := scan.List(dir, scan.SortName)
	if got := withPinned(listed, pinned, scan.SortName); len(got) != 3 {
		t.Errorf("deleted pin re-inserted: %v", names(got))
	}
}
