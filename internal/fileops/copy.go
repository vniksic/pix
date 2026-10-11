// Package fileops copies, trashes and externally opens image files.
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
func CopyAll(srcs []string, dir string) []CopyResult {
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
		dst, skipped, err := copyOne(src, dir)
		if err != nil {
			results[i].Err = fmt.Errorf("copy %s: %w", src, err)
			continue
		}
		results[i].Dst, results[i].Skipped = dst, skipped
	}
	return results
}

// copyOne copies src into dir and returns the destination path.
func copyOne(src, dir string) (dst string, skipped bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", false, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, errors.New("not a regular file")
	}
	base := filepath.Base(src)

	// Find the first name that is free or already holds this content.
	n, err := nextName(in, fi, dir, base, 1)
	if err != nil {
		return "", false, err
	}
	if n < 0 {
		return filepath.Join(dir, suffixed(base, -n)), true, nil
	}

	tmp, err := os.CreateTemp(dir, "."+base+".*.part")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmp != nil {
			tmp.Close()
		}
		os.Remove(tmpName) // after a successful link, or on error
	}()
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return "", false, err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		return "", false, err
	}
	err = tmp.Close()
	tmp = nil
	if err != nil {
		return "", false, err
	}
	if err := os.Chtimes(tmpName, time.Time{}, fi.ModTime()); err != nil {
		return "", false, err
	}

	for {
		dst = filepath.Join(dir, suffixed(base, n))
		err := place(tmpName, dst)
		if err == nil {
			return dst, false, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", false, err
		}
		// Someone created dst meanwhile: look further.
		if n, err = nextName(in, fi, dir, base, n+1); err != nil {
			return "", false, err
		}
		if n < 0 {
			return filepath.Join(dir, suffixed(base, -n)), true, nil
		}
	}
}

// nextName returns the first suffix number >= from whose name in dir is
// free, or minus the number of one holding a copy identical to in.
func nextName(in *os.File, fi os.FileInfo, dir, base string, from int) (int, error) {
	for n := from; n <= maxSuffix; n++ {
		path := filepath.Join(dir, suffixed(base, n))
		dfi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if dfi.Mode().IsRegular() && dfi.Size() == fi.Size() && sameContent(in, path) {
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

// sameContent reports whether in (from its start) and the file at path
// have the same bytes. Read errors count as different.
func sameContent(in *os.File, path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return false
	}
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
