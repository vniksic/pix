// Package fileops copies, trashes, deletes and externally opens image files.
package fileops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CopyResult is the outcome of copying one file.
type CopyResult struct {
	Src, Dst string // Dst is "" when Err is set
	Skipped  bool   // an identical file was already at Dst
	Rotated  bool   // the rotation asked for is stored in Dst's EXIF orientation
	RotLost  string // why a rotation asked for could not be stored ("png", "jpeg: ..."); Dst is then a plain copy
	Err      error
}

// maxSuffix bounds the name-2, name-3, ... search.
const maxSuffix = 10000

// CopyAll copies each src into dir (created with 0755 if missing), never
// overwriting: the destination name is the base name; if a file of that
// name exists and is byte-identical (size, then content), the copy is
// Skipped with Dst the existing file; otherwise name-2.ext, name-3.ext, ...
// are tried until one is free or identical. Two srcs with the same base
// name therefore get distinct names unless their contents are equal.
//
// Each copy is written to a temporary file in dir, synced, given the
// source's mtime and only then linked (or renamed) to its final name, so a
// crash never leaves a partial file under that name; on error the
// temporary file is removed. Results are in the order of srcs.
//
// rot, if not nil, gives the clockwise quarter turns a source was viewed
// with. A JPEG copy then gets the EXIF orientation that shows it turned
// that way (see orientJPEG; the image data is copied byte for byte);
// other formats are copied unchanged with RotLost set. The identical-file
// check compares against the bytes the copy would hold.
func CopyAll(srcs []string, dir string, rot func(src string) int) []CopyResult {
	results := make([]CopyResult, len(srcs))
	dirErr := os.MkdirAll(dir, 0o755)
	if dirErr == nil {
		if fi, err := os.Stat(dir); err != nil {
			dirErr = err
		} else if !fi.IsDir() {
			dirErr = fmt.Errorf("%s is not a directory", dir)
		}
	}
	for i, src := range srcs {
		results[i].Src = src
		if dirErr != nil {
			results[i].Err = dirErr
			continue
		}
		q := 0
		if rot != nil {
			q = rot(src)
		}
		r, err := copyOne(src, dir, q)
		if err != nil {
			results[i].Err = fmt.Errorf("copy %s: %w", src, err)
			continue
		}
		results[i] = r
	}
	return results
}

// content is what a copy of a file holds: head, then the file's bytes
// from offset rest on. A plain copy has no head; a rotated JPEG gets its
// rewritten header there.
type content struct {
	f          *os.File
	head       []byte
	rest, size int64 // size: of the file f
}

// length is the length of the copy.
func (c content) length() int64 { return int64(len(c.head)) + c.size - c.rest }

// reader returns the copy's bytes from the start (it reads f with ReadAt,
// so it never moves f's offset).
func (c content) reader() io.Reader {
	return io.MultiReader(bytes.NewReader(c.head), io.NewSectionReader(c.f, c.rest, c.size-c.rest))
}

// withRotation plans the copy of f (of the given size) turned by q
// clockwise quarter turns: a JPEG with its EXIF orientation set, anything
// else (or a JPEG pix cannot safely change) unchanged with the reason in
// lost. f is only read.
func withRotation(f *os.File, size int64, q int) (c content, rotated bool, lost string) {
	c = content{f: f, size: size}
	if q%4 == 0 {
		return c, false, ""
	}
	head := make([]byte, min(size, jpegHeadMax))
	n, err := f.ReadAt(head, 0)
	if n < len(head) && err != nil {
		return c, false, "read error"
	}
	if formatName(head) != "jpeg" {
		return c, false, formatName(head)
	}
	out, cut, err := orientJPEG(head, q)
	if err != nil {
		return c, false, err.Error()
	}
	c.head, c.rest = out, int64(cut)
	return c, true, ""
}

// copyOne copies src, turned by q quarter turns (see CopyAll), into dir.
func copyOne(src, dir string, q int) (r CopyResult, err error) {
	r.Src = src
	in, err := os.Open(src)
	if err != nil {
		return r, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return r, err
	}
	if !fi.Mode().IsRegular() {
		return r, errors.New("not a regular file")
	}
	base := filepath.Base(src)
	c, rotated, lost := withRotation(in, fi.Size(), q)
	r.Rotated, r.RotLost = rotated, lost

	// Find the first name that is free or already holds this content.
	n, err := nextName(c, dir, base, 1)
	if err != nil {
		return r, err
	}
	if n < 0 {
		r.Dst, r.Skipped = filepath.Join(dir, suffixed(base, -n)), true
		return r, nil
	}

	tmp, err := os.CreateTemp(dir, "."+base+".*.part")
	if err != nil {
		return r, err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmp != nil {
			tmp.Close()
		}
		os.Remove(tmpName) // after a successful link, or on error
	}()
	if _, err := io.Copy(tmp, c.reader()); err != nil {
		return r, err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return r, err
	}
	if err := tmp.Sync(); err != nil {
		return r, err
	}
	err = tmp.Close()
	tmp = nil
	if err != nil {
		return r, err
	}
	if err := os.Chtimes(tmpName, time.Time{}, fi.ModTime()); err != nil {
		return r, err
	}

	for {
		dst := filepath.Join(dir, suffixed(base, n))
		err := place(tmpName, dst)
		if err == nil {
			r.Dst = dst
			return r, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return r, err
		}
		// Someone created dst meanwhile: look further.
		if n, err = nextName(c, dir, base, n+1); err != nil {
			return r, err
		}
		if n < 0 {
			r.Dst, r.Skipped = filepath.Join(dir, suffixed(base, -n)), true
			return r, nil
		}
	}
}

// nextName returns the first suffix number >= from whose name in dir is
// free, or minus the number of one holding a copy identical to c.
func nextName(c content, dir, base string, from int) (int, error) {
	for n := from; n <= maxSuffix; n++ {
		path := filepath.Join(dir, suffixed(base, n))
		dfi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if dfi.Mode().IsRegular() && dfi.Size() == c.length() && sameContent(c.reader(), path) {
			return -n, nil
		}
	}
	return 0, fmt.Errorf("no free name for %s in %s", base, dir)
}

// place gives tmp the name dst without ever replacing an existing dst:
// a hard link (atomic, fails if dst exists), or on filesystems without
// hard links a rename after checking dst is free.
func place(tmp, dst string) error {
	err := os.Link(tmp, dst)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, serr := os.Lstat(dst); serr == nil {
		return fs.ErrExist
	} else if !errors.Is(serr, fs.ErrNotExist) {
		return serr
	}
	return os.Rename(tmp, dst)
}

// sameContent reports whether in and the file at path have the same
// bytes. Read errors count as different.
func sameContent(in io.Reader, path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	a, b := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(in, a)
		nb, eb := io.ReadFull(f, b)
		if na != nb || !bytes.Equal(a[:na], b[:nb]) {
			return false
		}
		aEnd := ea == io.EOF || ea == io.ErrUnexpectedEOF
		bEnd := eb == io.EOF || eb == io.ErrUnexpectedEOF
		if (ea != nil && !aEnd) || (eb != nil && !bEnd) {
			return false
		}
		if aEnd || bEnd {
			return aEnd && bEnd
		}
	}
}

// suffixed returns base for n <= 1, else base with "-n" inserted before
// the extension ("a.jpg" -> "a-2.jpg"; ".hidden" -> ".hidden-2").
func suffixed(base string, n int) string {
	if n <= 1 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	return stem + "-" + strconv.Itoa(n) + ext
}
