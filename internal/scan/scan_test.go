package scan

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func testImage() image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.NRGBA{255, 0, 0, 255})
	return img
}

func encoded(t *testing.T, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, testImage()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pngBytes(t *testing.T) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })
}

func TestIsImage(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		data []byte
		want bool
	}{
		"png":  {pngBytes(t), true},
		"jpeg": {encoded(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }), true},
		"gif":  {encoded(t, func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) }), true},
		"bmp":  {encoded(t, func(b *bytes.Buffer, m image.Image) error { return bmp.Encode(b, m) }), true},
		"tiff": {encoded(t, func(b *bytes.Buffer, m image.Image) error { return tiff.Encode(b, m, nil) }), true},

		// Formats without a Go encoder: hand-written headers.
		"webp":       {[]byte("RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00"), true},
		"tiff-be":    {[]byte("MM\x00*\x00\x00\x00\x08"), true},
		"heic":       {[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), true},
		"heix":       {[]byte("\x00\x00\x00\x18ftypheix\x00\x00\x00\x00mif1heix"), true},
		"hevc":       {[]byte("\x00\x00\x00\x18ftyphevc\x00\x00\x00\x00msf1hevc"), true},
		"mif1":       {[]byte("\x00\x00\x00\x18ftypmif1\x00\x00\x00\x00mif1miaf"), true},
		"msf1":       {[]byte("\x00\x00\x00\x18ftypmsf1\x00\x00\x00\x00msf1iso8"), true},
		"avif":       {[]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00mif1avifmiaf"), true},
		"avis":       {[]byte("\x00\x00\x00\x1cftypavis\x00\x00\x00\x00msf1avismiaf"), true},
		"jxl-code":   {[]byte("\xff\x0a\xfa\x7f"), true},
		"jxl-box":    {[]byte("\x00\x00\x00\x0cJXL \r\n\x87\n\x00\x00\x00\x14ftypjxl "), true},
		"mp4":        {[]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), false},
		"riff-wav":   {[]byte("RIFF\x24\x00\x00\x00WAVEfmt "), false},
		"bm-text":    {[]byte("BMW is a car maker, not a bitmap."), false},
		"text":       {[]byte("hello world\n"), false},
		"empty":      {nil, false},
		"short-jpeg": {[]byte("\xff\xd8"), false},
	}
	for name, c := range cases {
		path := filepath.Join(dir, name+".png") // extension must not matter
		write(t, path, c.data)
		if got := IsImage(path); got != c.want {
			t.Errorf("IsImage(%s) = %v, want %v", name, got, c.want)
		}
	}
	if IsImage(filepath.Join(dir, "missing")) {
		t.Error("IsImage(missing) = true")
	}
	if IsImage(dir) {
		t.Error("IsImage(dir) = true")
	}
}

func TestNaturalLess(t *testing.T) {
	want := []string{
		"a", "A1", "a2", "img2", "IMG3.jpg", "img10", "img010b", "img10c",
		"img99999999999999999999", "img100000000000000000000", "x", "Z",
	}
	got := slices.Clone(want)
	slices.Reverse(got)
	slices.SortFunc(got, func(a, b string) int {
		if NaturalLess(a, b) {
			return -1
		}
		if NaturalLess(b, a) {
			return 1
		}
		return 0
	})
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %q\nwant     %q", got, want)
	}
	for _, s := range want {
		if NaturalLess(s, s) {
			t.Errorf("NaturalLess(%q, %q) = true", s, s)
		}
	}
	// Equal under natural rules but different bytes: still strictly ordered.
	if NaturalLess("img1", "IMG1") == NaturalLess("IMG1", "img1") {
		t.Error("img1/IMG1 not strictly ordered")
	}
	if NaturalLess("a01", "a1") == NaturalLess("a1", "a01") {
		t.Error("a01/a1 not strictly ordered")
	}
}

// makeDir creates img10.png, img2.png, img1.png with distinct mtimes, plus
// files that List must skip.
func makeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := pngBytes(t)
	base := time.Now().Add(-time.Hour)
	for i, name := range []string{"img10.png", "img2.png", "IMG1.png"} {
		path := filepath.Join(dir, name)
		write(t, path, p)
		mt := base.Add(time.Duration(i) * time.Minute) // img10 oldest
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dir, ".hidden.png"), p)
	write(t, filepath.Join(dir, "notes.txt"), []byte("not an image"))
	if err := os.Mkdir(filepath.Join(dir, "sub.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub.png", "deep.png"), p)
	if err := os.Symlink(filepath.Join(dir, "img2.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone.png"), filepath.Join(dir, "dangling.png")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func names(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func TestList(t *testing.T) {
	dir := makeDir(t)
	es, err := List(dir, SortName)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(es), []string{"IMG1.png", "img2.png", "img10.png", "link.png"}; !slices.Equal(got, want) {
		t.Errorf("SortName = %q, want %q", got, want)
	}
	for _, e := range es {
		if !filepath.IsAbs(e.Path) || filepath.Base(e.Path) != e.Name || e.Label != e.Name || e.Size == 0 || e.ModTime.IsZero() {
			t.Errorf("bad entry %+v", e)
		}
	}

	es, err = List(dir, SortMTime)
	if err != nil {
		t.Fatal(err)
	}
	// link.png's mtime is its target's (img2.png); ties broken by name.
	if got, want := names(es), []string{"img10.png", "img2.png", "link.png", "IMG1.png"}; !slices.Equal(got, want) {
		t.Errorf("SortMTime = %q, want %q", got, want)
	}

	if _, err := List(filepath.Join(dir, "nope"), SortName); err == nil {
		t.Error("List(missing dir) succeeded")
	}
}

func TestResolve(t *testing.T) {
	dir := makeDir(t)

	t.Run("dir", func(t *testing.T) {
		es, start, d, err := Resolve([]string{dir}, Options{})
		if err != nil || start != 0 || !slices.Equal(d, []string{dir}) || len(es) != 4 {
			t.Fatalf("got %q %d %q %v", names(es), start, d, err)
		}
	})

	t.Run("file", func(t *testing.T) {
		es, start, d, err := Resolve([]string{filepath.Join(dir, "img10.png")}, Options{})
		if err != nil || !slices.Equal(d, []string{dir}) || es[start].Name != "img10.png" || start != 2 {
			t.Fatalf("got %q %d %q %v", names(es), start, d, err)
		}
	})

	t.Run("non-image file is included", func(t *testing.T) {
		es, start, _, err := Resolve([]string{filepath.Join(dir, "notes.txt")}, Options{})
		if err != nil || len(es) != 5 || es[start].Name != "notes.txt" {
			t.Fatalf("got %q %d %v", names(es), start, err)
		}
	})

	t.Run("hidden file is included", func(t *testing.T) {
		es, start, _, err := Resolve([]string{filepath.Join(dir, ".hidden.png")}, Options{})
		if err != nil || es[start].Name != ".hidden.png" {
			t.Fatalf("got %q %d %v", names(es), start, err)
		}
	})

	t.Run("relative file", func(t *testing.T) {
		t.Chdir(dir)
		es, start, d, err := Resolve([]string{"img2.png"}, Options{})
		if err != nil || !slices.Equal(d, []string{dir}) || es[start].Path != filepath.Join(dir, "img2.png") {
			t.Fatalf("got %q %d %q %v", names(es), start, d, err)
		}
	})

	t.Run("no args uses cwd", func(t *testing.T) {
		t.Chdir(dir)
		es, start, d, err := Resolve(nil, Options{})
		if err != nil || start != 0 || !slices.Equal(d, []string{dir}) || len(es) != 4 {
			t.Fatalf("got %q %d %q %v", names(es), start, d, err)
		}
	})

	t.Run("several args", func(t *testing.T) {
		args := []string{
			filepath.Join(dir, "img2.png"),
			filepath.Join(dir, "notes.txt"), // dropped: not an image
			filepath.Join(dir, "missing.png"),
			filepath.Join(dir, "sub.png"),  // expanded one level
			filepath.Join(dir, "img2.png"), // duplicate
			filepath.Join(dir, "IMG1.png"),
		}
		es, start, d, err := Resolve(args, Options{})
		if err != nil || start != 0 || d != nil {
			t.Fatalf("got %d %q %v", start, d, err)
		}
		if got, want := names(es), []string{"img2.png", "deep.png", "IMG1.png"}; !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("errors", func(t *testing.T) {
		empty := t.TempDir()
		if _, _, _, err := Resolve([]string{empty}, Options{}); err == nil {
			t.Error("empty dir: no error")
		}
		if _, _, _, err := Resolve([]string{filepath.Join(dir, "missing")}, Options{}); err == nil {
			t.Error("missing file: no error")
		}
		if _, _, _, err := Resolve([]string{filepath.Join(dir, "notes.txt"), empty}, Options{}); err == nil {
			t.Error("nothing viewable: no error")
		}
	})
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	baseline := runtime.NumGoroutine()

	var calls atomic.Int32
	changed := make(chan struct{}, 10)
	stop, err := Watch(dir, func() {
		calls.Add(1)
		changed <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}

	// A burst of events is debounced into one call.
	for i := range 5 {
		write(t, filepath.Join(dir, "f"+string(rune('a'+i))), []byte("x"))
	}
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("onChange not called")
	}
	time.Sleep(500 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("onChange called %d times for one burst, want 1", n)
	}

	// Hidden files are ignored.
	write(t, filepath.Join(dir, ".swp"), []byte("x"))
	time.Sleep(600 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("hidden file triggered onChange (%d calls)", n)
	}

	// Removal triggers again.
	if err := os.Remove(filepath.Join(dir, "fa")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("onChange not called after remove")
	}

	stop()
	stop() // idempotent
	write(t, filepath.Join(dir, "after"), []byte("x"))
	time.Sleep(500 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Errorf("calls after stop: %d, want 2", n)
	}
	waitGoroutines(t, baseline)

	if _, err := Watch(filepath.Join(dir, "missing"), func() {}); err == nil {
		t.Error("Watch(missing) succeeded")
	}
	waitGoroutines(t, baseline)
}

func TestWatchStopFromCallback(t *testing.T) {
	dir := t.TempDir()
	baseline := runtime.NumGoroutine()
	var stop func()
	returned := make(chan struct{})
	ready := make(chan struct{})
	s, err := Watch(dir, func() {
		<-ready
		stop()
		close(returned)
	})
	if err != nil {
		t.Fatal(err)
	}
	stop = s
	close(ready)
	write(t, filepath.Join(dir, "a"), []byte("x"))
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("stop() from onChange blocked")
	}
	waitGoroutines(t, baseline)
}

func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > want {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutine leak: %d > %d\n%s", runtime.NumGoroutine(), want, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// makeTree creates a nested fixture for ListTree:
//
//	b.png a10.png a2.png link.png->a2.png notes.txt .h.png
//	a1/z.png  Sub1/f.png  sub2/c.png  sub10/d.png  sub10/deeper/e.png
//	.hidden/x.png  linkdir->sub2  empty/
//
// Every image gets a distinct mtime, oldest first in this order:
// e, d, c, f, z, b, a10, a2 (link.png shares a2's).
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	p := pngBytes(t)
	for _, d := range []string{"a1", "Sub1", "sub2", "sub10/deeper", ".hidden", "empty"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	for i, f := range []string{"sub10/deeper/e.png", "sub10/d.png", "sub2/c.png", "Sub1/f.png", "a1/z.png", "b.png", "a10.png", "a2.png"} {
		path := filepath.Join(root, f)
		write(t, path, p)
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, ".h.png"), p)
	write(t, filepath.Join(root, ".hidden", "x.png"), p)
	write(t, filepath.Join(root, "notes.txt"), []byte("not an image"))
	if err := os.Symlink(filepath.Join(root, "a2.png"), filepath.Join(root, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sub2"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	return root
}

func labels(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Label)
	}
	return out
}

func TestListTree(t *testing.T) {
	root := makeTree(t)

	es, err := ListTree(root, SortName)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a2.png", "a10.png", "b.png", "link.png", // root's own files first
		"a1/z.png", "Sub1/f.png", "sub2/c.png", // then subdirs, natural order
		"sub10/d.png", "sub10/deeper/e.png", // sub10's files before sub10/deeper
	}
	if got := labels(es); !slices.Equal(got, want) {
		t.Errorf("SortName = %q\nwant       %q", got, want)
	}
	for _, e := range es {
		if e.Path != filepath.Join(root, e.Label) || e.Name != filepath.Base(e.Path) || e.Size == 0 {
			t.Errorf("bad entry %+v", e)
		}
	}

	es, err = ListTree(root, SortMTime)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"sub10/deeper/e.png", "sub10/d.png", "sub2/c.png", "Sub1/f.png", "a1/z.png",
		"b.png", "a10.png", "a2.png", "link.png", // link.png ties with a2.png: by path
	}
	if got := labels(es); !slices.Equal(got, want) {
		t.Errorf("SortMTime = %q\nwant        %q", got, want)
	}

	// Compare agrees with the order ListTree produced.
	for i := 1; i < len(es); i++ {
		if Compare(es[i-1], es[i], SortMTime) >= 0 {
			t.Errorf("Compare(%s, %s) >= 0", es[i-1].Label, es[i].Label)
		}
	}

	if _, err := ListTree(filepath.Join(root, "nope"), SortName); err == nil {
		t.Error("ListTree(missing dir) succeeded")
	}
}

func TestTreeCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"a.png", "b.png", -1},
		{"img10.png", "img2.png", 1},
		{"z.png", "a/a.png", -1}, // own files before subdirs
		{"a/b/c.png", "a/z.png", 1},
		{"a2/x.png", "a10/x.png", -1},
		{"A/x.png", "a/x.png", -1}, // bytewise tie-break of NaturalLess
		{"same.png", "same.png", 0},
	} {
		if got := treeCompare(c.a, c.b); got != c.want {
			t.Errorf("treeCompare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLabels(t *testing.T) {
	root := makeTree(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, filepath.Join(home, "h.png"), pngBytes(t))

	// Single root, one level: Label == Name.
	es, _, _, err := Resolve([]string{root}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := labels(es), []string{"a2.png", "a10.png", "b.png", "link.png"}; !slices.Equal(got, want) {
		t.Errorf("one dir: %q, want %q", got, want)
	}

	// Single root, recursive: relative to the root.
	es, _, _, err = Resolve([]string{filepath.Join(root, "sub10")}, Options{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := labels(es), []string{"d.png", "deeper/e.png"}; !slices.Equal(got, want) {
		t.Errorf("recursive dir: %q, want %q", got, want)
	}

	// Ad-hoc: relative to cwd when under it, else absolute with ~ for $HOME.
	outside := t.TempDir()
	write(t, filepath.Join(outside, "o.png"), pngBytes(t))
	t.Chdir(filepath.Join(root, "sub10"))
	es, _, _, err = Resolve([]string{"d.png", "../sub2", filepath.Join(home, "h.png"), filepath.Join(outside, "o.png")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"d.png", filepath.Join(root, "sub2", "c.png"), "~/h.png", filepath.Join(outside, "o.png")}
	if got := labels(es); !slices.Equal(got, want) {
		t.Errorf("ad-hoc: %q\nwant     %q", got, want)
	}

	for _, c := range []struct{ path, cwd, home, want string }{
		{"/a/b/c.png", "/a", "/h", "b/c.png"},
		{"/a/c.png", "/a/b", "/h", "/a/c.png"},
		{"/h/p/c.png", "/a", "/h", "~/p/c.png"},
		{"/h/p/c.png", "/h/p", "/h", "c.png"},
		{"/hx/c.png", "/a", "/h", "/hx/c.png"},
		{"/a/c.png", "", "", "/a/c.png"},
	} {
		if got := adhocLabel(c.path, c.cwd, c.home); got != c.want {
			t.Errorf("adhocLabel(%q, %q, %q) = %q, want %q", c.path, c.cwd, c.home, got, c.want)
		}
	}
	if got := RelLabel("/r", "/r/s/x.png"); got != "s/x.png" {
		t.Errorf("RelLabel = %q", got)
	}
	if got := RelLabel("/r", "/q/x.png"); got != "/q/x.png" {
		t.Errorf("RelLabel outside = %q", got)
	}
}

func TestResolveRecursive(t *testing.T) {
	root := makeTree(t)
	all := Options{Recursive: true}
	full := []string{
		"a2.png", "a10.png", "b.png", "link.png",
		"a1/z.png", "Sub1/f.png", "sub2/c.png", "sub10/d.png", "sub10/deeper/e.png",
	}

	t.Run("no args", func(t *testing.T) {
		t.Chdir(root)
		es, start, roots, err := Resolve(nil, all)
		if err != nil || start != 0 || !slices.Equal(roots, []string{root}) || !slices.Equal(labels(es), full) {
			t.Fatalf("got %q %d %q %v", labels(es), start, roots, err)
		}
	})

	t.Run("dir", func(t *testing.T) {
		es, start, roots, err := Resolve([]string{root}, all)
		if err != nil || start != 0 || !slices.Equal(roots, []string{root}) || !slices.Equal(labels(es), full) {
			t.Fatalf("got %q %d %q %v", labels(es), start, roots, err)
		}
	})

	t.Run("file", func(t *testing.T) {
		es, start, roots, err := Resolve([]string{filepath.Join(root, "b.png")}, all)
		if err != nil || !slices.Equal(roots, []string{root}) || !slices.Equal(labels(es), full) || es[start].Label != "b.png" {
			t.Fatalf("got %q %d %q %v", labels(es), start, roots, err)
		}
		// A file in a subdirectory: its parent is the root.
		es, start, roots, err = Resolve([]string{filepath.Join(root, "sub10", "deeper", "e.png")}, all)
		if err != nil || !slices.Equal(roots, []string{filepath.Join(root, "sub10", "deeper")}) || es[start].Label != "e.png" {
			t.Fatalf("got %q %d %q %v", labels(es), start, roots, err)
		}
		// A non-image file is pinned among the root's own files.
		es, start, _, err = Resolve([]string{filepath.Join(root, "notes.txt")}, all)
		if err != nil || len(es) != len(full)+1 || es[start].Label != "notes.txt" || start != 4 {
			t.Fatalf("got %q %d %v", labels(es), start, err)
		}
	})

	t.Run("several args", func(t *testing.T) {
		t.Chdir(root)
		es, start, roots, err := Resolve([]string{"sub10", "b.png", "sub10/d.png", "linkdir"}, all)
		if err != nil || start != 0 || roots != nil {
			t.Fatalf("got %d %q %v", start, roots, err)
		}
		// linkdir given explicitly is expanded (only nested symlinks are skipped).
		want := []string{"sub10/d.png", "sub10/deeper/e.png", "b.png", "linkdir/c.png"}
		if got := labels(es); !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty tree", func(t *testing.T) {
		empty := t.TempDir()
		if err := os.MkdirAll(filepath.Join(empty, "a", "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := Resolve([]string{empty}, all); err == nil {
			t.Error("empty tree: no error")
		}
	})
}

func TestWatchTree(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"old/deep", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	baseline := runtime.NumGoroutine()
	changed := make(chan struct{}, 10)
	stop, err := WatchTree(root, func() { changed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	wait := func(what string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(3 * time.Second):
			t.Fatalf("onChange not called: %s", what)
		}
	}
	quiet := func(what string) {
		t.Helper()
		select {
		case <-changed:
			t.Fatalf("onChange called: %s", what)
		case <-time.After(600 * time.Millisecond):
		}
	}

	write(t, filepath.Join(root, "old", "deep", "a.png"), []byte("x"))
	wait("file in an existing nested dir")

	write(t, filepath.Join(root, ".hidden", "b.png"), []byte("x"))
	quiet("file in a hidden dir")

	// A new directory is watched once it appears.
	if err := os.Mkdir(filepath.Join(root, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	wait("new dir")
	write(t, filepath.Join(root, "new", "c.png"), []byte("x"))
	wait("file in the new dir")

	// So is a whole new subtree created at once (mkdir -p, then a file).
	if err := os.MkdirAll(filepath.Join(root, "n2", "x", "y"), 0o755); err != nil {
		t.Fatal(err)
	}
	wait("new subtree")
	write(t, filepath.Join(root, "n2", "x", "y", "d.png"), []byte("x"))
	wait("file deep in the new subtree")

	stop()
	waitGoroutines(t, baseline)

	if _, err := WatchTree(filepath.Join(root, "missing"), func() {}); err == nil {
		t.Error("WatchTree(missing) succeeded")
	}
}

func TestWatchTreeCap(t *testing.T) {
	old := maxWatchDirs
	maxWatchDirs = 3
	t.Cleanup(func() { maxWatchDirs = old })

	root := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tw := &treeWatch{w: w, dirs: map[string]bool{root: true}}
	tw.addBelow(root)
	if n := len(tw.dirs); n != 3 {
		t.Errorf("watched %d dirs, want the cap of 3", n)
	}
	if n := len(w.WatchList()); n != 2 { // root itself was not added to w here
		t.Errorf("watcher has %d dirs, want 2", n)
	}
}
