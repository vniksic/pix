package fileops

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string, mtime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

func TestCopyAll(t *testing.T) {
	src := t.TempDir()
	out := filepath.Join(t.TempDir(), "out", "sub") // created by CopyAll
	mt := time.Date(2023, 3, 4, 5, 6, 7, 0, time.Local)

	x := writeFile(t, filepath.Join(src, "a", "x.jpg"), "content A", mt)
	res := CopyAll([]string{x}, out, nil)
	if len(res) != 1 || res[0].Err != nil || res[0].Skipped || res[0].Src != x || res[0].Dst != filepath.Join(out, "x.jpg") {
		t.Fatalf("first copy: %+v", res)
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("dir not created 0755: %v %v", fi, err)
	}
	if got := readFile(t, res[0].Dst); got != "content A" {
		t.Errorf("content %q", got)
	}
	fi, err := os.Stat(res[0].Dst)
	if err != nil || !fi.ModTime().Equal(mt) {
		t.Errorf("mtime %v, want %v (%v)", fi.ModTime(), mt, err)
	}

	// Identical file already there: skipped.
	res = CopyAll([]string{x}, out, nil)
	if res[0].Err != nil || !res[0].Skipped || res[0].Dst != filepath.Join(out, "x.jpg") {
		t.Errorf("identical: %+v", res)
	}

	// Same name, different content (also same size): -2.
	x2 := writeFile(t, filepath.Join(src, "b", "x.jpg"), "content B", time.Time{})
	res = CopyAll([]string{x2}, out, nil)
	if res[0].Err != nil || res[0].Skipped || res[0].Dst != filepath.Join(out, "x-2.jpg") {
		t.Errorf("collision: %+v", res)
	}
	if got := readFile(t, filepath.Join(out, "x.jpg")); got != "content A" {
		t.Errorf("x.jpg overwritten: %q", got)
	}
	// ...and copying it again finds its identical -2 copy.
	res = CopyAll([]string{x2}, out, nil)
	if !res[0].Skipped || res[0].Dst != filepath.Join(out, "x-2.jpg") {
		t.Errorf("identical -2: %+v", res)
	}

	// Pre-existing name and name-2 both taken: -3.
	writeFile(t, filepath.Join(out, "y.jpg"), "other 1", time.Time{})
	writeFile(t, filepath.Join(out, "y-2.jpg"), "other two", time.Time{})
	y := writeFile(t, filepath.Join(src, "y.jpg"), "mine", time.Time{})
	res = CopyAll([]string{y}, out, nil)
	if res[0].Err != nil || res[0].Dst != filepath.Join(out, "y-3.jpg") || readFile(t, res[0].Dst) != "mine" {
		t.Errorf("-3: %+v", res)
	}

	// Two inputs with the same base name in one call, plus an error in
	// between: distinct names, results in input order.
	z1 := writeFile(t, filepath.Join(src, "c", "z.png"), "zzz one", time.Time{})
	z2 := writeFile(t, filepath.Join(src, "d", "z.png"), "zzz two", time.Time{})
	missing := filepath.Join(src, "missing.png")
	noext := writeFile(t, filepath.Join(src, "e", "README"), "r", time.Time{})
	noext2 := writeFile(t, filepath.Join(src, "f", "README"), "s", time.Time{})
	res = CopyAll([]string{z1, missing, z2, filepath.Join(src, "c"), noext, noext2}, out, nil)
	want := []struct {
		src, dst string
		err      bool
	}{
		{z1, "z.png", false}, {missing, "", true}, {z2, "z-2.png", false},
		{filepath.Join(src, "c"), "", true}, {noext, "README", false}, {noext2, "README-2", false},
	}
	for i, w := range want {
		r := res[i]
		wantDst := ""
		if w.dst != "" {
			wantDst = filepath.Join(out, w.dst)
		}
		if r.Src != w.src || r.Dst != wantDst || (r.Err != nil) != w.err || r.Skipped {
			t.Errorf("result %d: %+v, want src %s dst %s err %v", i, r, w.src, wantDst, w.err)
		}
	}
	if readFile(t, filepath.Join(out, "z.png")) != "zzz one" || readFile(t, filepath.Join(out, "z-2.png")) != "zzz two" {
		t.Error("z copies have the wrong content")
	}

	// No temporary files left behind.
	for _, n := range dirNames(t, out) {
		if strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".part") {
			t.Errorf("leftover %s", n)
		}
	}
}

func TestCopyAllErrors(t *testing.T) {
	src := writeFile(t, filepath.Join(t.TempDir(), "p.jpg"), "p", time.Time{})

	// dir is a file.
	file := writeFile(t, filepath.Join(t.TempDir(), "f"), "", time.Time{})
	res := CopyAll([]string{src, src}, file, nil)
	if len(res) != 2 || res[0].Err == nil || res[1].Err == nil || res[0].Src != src {
		t.Errorf("dir is a file: %+v", res)
	}

	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	// Unwritable dir: error, nothing created.
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	res = CopyAll([]string{src}, ro, nil)
	if res[0].Err == nil || res[0].Dst != "" {
		t.Errorf("read-only dir: %+v", res)
	}
	if n := dirNames(t, ro); len(n) != 0 {
		t.Errorf("read-only dir has %q", n)
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 10000) // several buffers
	other := bytes.Clone(big)
	other[len(other)-1] = 'X'
	a := writeFile(t, filepath.Join(dir, "a"), string(big), time.Time{})
	b := writeFile(t, filepath.Join(dir, "b"), string(big), time.Time{})
	c := writeFile(t, filepath.Join(dir, "c"), string(other), time.Time{})
	d := writeFile(t, filepath.Join(dir, "d"), string(big[:100]), time.Time{})
	f, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	whole := func() io.Reader { return io.NewSectionReader(f, 0, int64(len(big))) }
	if !sameContent(whole(), b) || sameContent(whole(), c) || sameContent(whole(), d) || sameContent(whole(), filepath.Join(dir, "none")) {
		t.Error("sameContent wrong")
	}
}

func TestSuffixed(t *testing.T) {
	for _, c := range []struct {
		base string
		n    int
		want string
	}{
		{"a.jpg", 1, "a.jpg"}, {"a.jpg", 2, "a-2.jpg"}, {"a.b.jpg", 3, "a.b-3.jpg"},
		{"README", 2, "README-2"}, {".hidden", 2, ".hidden-2"},
	} {
		if got := suffixed(c.base, c.n); got != c.want {
			t.Errorf("suffixed(%q, %d) = %q, want %q", c.base, c.n, got, c.want)
		}
	}
}

// trashHome points the home trash at a temp dir and returns the trash dir.
func trashHome(t *testing.T) string {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	return filepath.Join(data, "Trash")
}

func parseInfo(t *testing.T, path string) (p string, date time.Time) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "[Trash Info]" || !strings.HasPrefix(lines[1], "Path=") || !strings.HasPrefix(lines[2], "DeletionDate=") {
		t.Fatalf("bad trashinfo %q", lines)
	}
	d, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimPrefix(lines[2], "DeletionDate="), time.Local)
	if err != nil {
		t.Fatalf("DeletionDate: %v", err)
	}
	return strings.TrimPrefix(lines[1], "Path="), d
}

func TestTrash(t *testing.T) {
	trash := trashHome(t)
	src := t.TempDir()
	orig := writeFile(t, filepath.Join(src, "my photo%.jpg"), "pixels", time.Time{})

	before := time.Now().Truncate(time.Second)
	if err := Trash(orig); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(orig); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("original still there: %v", err)
	}
	if got := readFile(t, filepath.Join(trash, "files", "my photo%.jpg")); got != "pixels" {
		t.Errorf("trashed content %q", got)
	}
	p, date := parseInfo(t, filepath.Join(trash, "info", "my photo%.jpg.trashinfo"))
	realSrc, _ := filepath.EvalSymlinks(src)
	if want := escapePath(realSrc) + "/my%20photo%25.jpg"; p != want {
		t.Errorf("Path=%s, want %s", p, want)
	}
	if !strings.HasPrefix(p, "/") {
		t.Errorf("home trash Path not absolute: %s", p)
	}
	if date.Before(before) || date.After(time.Now()) {
		t.Errorf("DeletionDate %v not now", date)
	}
	for _, d := range []string{trash, filepath.Join(trash, "files"), filepath.Join(trash, "info")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want 0700 dir", d, fi, err)
		}
	}

	// Same name again, plus an orphaned .trashinfo for the -2 name.
	writeFile(t, filepath.Join(trash, "info", "my photo%-2.jpg.trashinfo"), "orphan", time.Time{})
	again := writeFile(t, filepath.Join(src, "my photo%.jpg"), "second", time.Time{})
	if err := Trash(again); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(trash, "files", "my photo%-3.jpg")); got != "second" {
		t.Errorf("collision content %q", got)
	}
	if p, _ := parseInfo(t, filepath.Join(trash, "info", "my photo%-3.jpg.trashinfo")); !strings.HasSuffix(p, "/my%20photo%25.jpg") {
		t.Errorf("collision Path=%s", p)
	}
	if got := readFile(t, filepath.Join(trash, "info", "my photo%-2.jpg.trashinfo")); got != "orphan" {
		t.Error("existing trashinfo overwritten")
	}
	if got := readFile(t, filepath.Join(trash, "files", "my photo%.jpg")); got != "pixels" {
		t.Error("first trashed file overwritten")
	}

	// Relative paths and directories work too.
	sub := filepath.Join(src, "dir")
	writeFile(t, filepath.Join(sub, "in.png"), "x", time.Time{})
	t.Chdir(src)
	if err := Trash("dir"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(trash, "files", "dir", "in.png")); got != "x" {
		t.Errorf("trashed dir content %q", got)
	}
}

func TestTrashErrors(t *testing.T) {
	trash := trashHome(t)
	if err := Trash(filepath.Join(t.TempDir(), "missing.jpg")); err == nil {
		t.Error("missing file: no error")
	}
	if names, _ := os.ReadDir(filepath.Join(trash, "info")); len(names) != 0 {
		t.Errorf("info entries after failure: %v", names)
	}

	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	// The source directory is read-only: rename fails, the file stays and
	// no .trashinfo is left behind.
	src := t.TempDir()
	f := writeFile(t, filepath.Join(src, "keep.jpg"), "keep", time.Time{})
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })
	if err := Trash(f); err == nil {
		t.Error("read-only dir: no error")
	}
	if got := readFile(t, f); got != "keep" {
		t.Errorf("file changed: %q", got)
	}
	if n := dirNames(t, filepath.Join(trash, "info")); len(n) != 0 {
		t.Errorf("info entries after failure: %q", n)
	}
	if n := dirNames(t, filepath.Join(trash, "files")); len(n) != 0 {
		t.Errorf("files entries after failure: %q", n)
	}
}

func TestTopdirTrash(t *testing.T) {
	// The $topdir trash pieces, on a temp dir standing in for a mount.
	top := t.TempDir()
	uid := strconv.Itoa(os.Getuid())
	td, err := ensureTrashDir(filepath.Join(top, ".Trash-"+uid))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(td); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("trash dir %v %v", fi, err)
	}
	f := writeFile(t, filepath.Join(top, "photos", "a b.jpg"), "a", time.Time{})
	if err := trashInto(td, f, "photos/a b.jpg"); err != nil {
		t.Fatal(err)
	}
	if p, _ := parseInfo(t, filepath.Join(td, "info", "a b.jpg.trashinfo")); p != "photos/a%20b.jpg" {
		t.Errorf("relative Path=%s", p)
	}
	if got := readFile(t, filepath.Join(td, "files", "a b.jpg")); got != "a" {
		t.Errorf("content %q", got)
	}

	// A symlink is not accepted as a trash directory.
	link := filepath.Join(top, ".Trash-link")
	if err := os.Symlink(td, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTrashDir(link); err == nil {
		t.Error("symlinked trash dir accepted")
	}

	// mountTop: same device as the start, parent on another device (or /).
	mt, err := mountTop(top)
	if err != nil || !strings.HasPrefix(top, mt) {
		t.Errorf("mountTop(%s) = %s, %v", top, mt, err)
	}
	if m, _ := mountTop("/"); m != "/" {
		t.Errorf("mountTop(/) = %s", m)
	}
}

func TestTrashOtherFilesystem(t *testing.T) {
	// The home trash must be on another device than the temp dir for
	// rename to fail with EXDEV; /dev/shm usually is.
	data, err := os.MkdirTemp("/dev/shm", "pix-test-")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	t.Cleanup(func() { os.RemoveAll(data) })
	top := t.TempDir() // stands in for the mount point of the file's filesystem
	if dev(t, data) == dev(t, top) {
		t.Skip("/dev/shm is on the same filesystem as the temp dir")
	}
	t.Setenv("XDG_DATA_HOME", data)
	old := findTop
	findTop = func(string) (string, error) { return top, nil }
	t.Cleanup(func() { findTop = old })
	uid := strconv.Itoa(os.Getuid())
	home := filepath.Join(data, "Trash")

	check := func(td, name, content string) {
		t.Helper()
		if got := readFile(t, filepath.Join(td, "files", name)); got != content {
			t.Errorf("%s: content %q", td, got)
		}
		if p, _ := parseInfo(t, filepath.Join(td, "info", name+".trashinfo")); p != "photos/"+name {
			t.Errorf("%s: Path=%s, want relative to the top dir", td, p)
		}
		if n := dirNames(t, filepath.Join(home, "info")); len(n) != 0 {
			t.Errorf("home trash info has %q", n)
		}
	}

	// $topdir/.Trash without the sticky bit is ignored: .Trash-$uid.
	admin := filepath.Join(top, ".Trash")
	if err := os.Mkdir(admin, 0o777); err != nil {
		t.Fatal(err)
	}
	f := writeFile(t, filepath.Join(top, "photos", "a.jpg"), "a", time.Time{})
	if err := Trash(f); err != nil {
		t.Fatal(err)
	}
	check(filepath.Join(top, ".Trash-"+uid), "a.jpg", "a")
	if fi, err := os.Lstat(filepath.Join(top, ".Trash-"+uid)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf(".Trash-uid: %v %v", fi, err)
	}

	// Sticky $topdir/.Trash: $topdir/.Trash/$uid.
	if err := os.Chmod(admin, 0o777|fs.ModeSticky); err != nil {
		t.Fatal(err)
	}
	f = writeFile(t, filepath.Join(top, "photos", "b.jpg"), "b", time.Time{})
	if err := Trash(f); err != nil {
		t.Fatal(err)
	}
	check(filepath.Join(admin, uid), "b.jpg", "b")

	// Nothing usable: error, file untouched.
	if err := os.RemoveAll(admin); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(top, ".Trash-"+uid)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(top, ".Trash-"+uid), "in the way", time.Time{})
	f = writeFile(t, filepath.Join(top, "photos", "c.jpg"), "c", time.Time{})
	if err := Trash(f); err == nil {
		t.Error("no usable trash: no error")
	}
	if got := readFile(t, f); got != "c" {
		t.Errorf("file changed: %q", got)
	}
	if n := dirNames(t, filepath.Join(home, "files")); len(n) != 0 {
		t.Errorf("home trash files has %q", n)
	}
}

func dev(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(fi.Sys().(*syscall.Stat_t).Dev)
}

func TestEscapePath(t *testing.T) {
	if got, want := escapePath("/home/u/a b/ć%#?&.jpg"), "/home/u/a%20b/%C4%87%25%23%3F%26.jpg"; got != want {
		t.Errorf("escapePath = %s, want %s", got, want)
	}
}

func TestOpenExternal(t *testing.T) {
	bin := t.TempDir()
	rec := filepath.Join(t.TempDir(), "args")
	for _, name := range []string{"fake-open", "xdg-open"} {
		// Records its name and arguments using shell builtins only.
		script := "#!/bin/sh\nprintf '%s:%s|%s\\n' " + name + " \"$#\" \"$1\" > \"$REC\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// PATH holds only the fakes: the real xdg-open can never run.
	t.Setenv("PATH", bin)
	t.Setenv("REC", rec)

	wait := func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(rec); err == nil && bytes.HasSuffix(b, []byte("\n")) {
				os.Remove(rec)
				return string(b)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("program did not run")
		return ""
	}

	t.Setenv("PIX_OPEN", "fake-open")
	if err := OpenExternal("/some/where/a b.jpg"); err != nil {
		t.Fatal(err)
	}
	if got := wait(); got != "fake-open:1|/some/where/a b.jpg\n" {
		t.Errorf("PIX_OPEN ran %q", got)
	}

	t.Setenv("PIX_OPEN", "")
	if err := OpenExternal("/x.png"); err != nil {
		t.Fatal(err)
	}
	if got := wait(); got != "xdg-open:1|/x.png\n" {
		t.Errorf("xdg-open fallback ran %q", got)
	}

	t.Setenv("PIX_OPEN", "no-such-program-pix")
	if err := OpenExternal("/x.png"); err == nil {
		t.Error("missing program: no error")
	}
}
