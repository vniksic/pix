// Package scan turns command-line arguments and directories into an ordered
// list of image files, and watches directories for changes.
package scan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Entry is one file in a playlist.
type Entry struct {
	Path    string // absolute path
	Name    string // base name
	ModTime time.Time
	Size    int64
}

// Sort selects the playlist order.
type Sort int

const (
	SortName  Sort = iota // natural sort on Name, case-insensitive ("img2" < "img10")
	SortMTime             // oldest first; ties broken by natural name
)

// Resolve turns CLI args into a playlist.
//
//	no args      -> cwd listing, start=0
//	one dir      -> that dir's listing, start=0
//	one file     -> its parent dir's listing, start=index of that file
//	                (the file is included even if IsImage says no)
//	several args -> ad-hoc list of exactly those paths that exist
//	                (dirs expanded one level, non-images dropped), dir=""
//
// Ad-hoc lists keep the argument order (each expanded directory is sorted
// internally) and drop duplicates. Returns an error only when nothing is
// viewable.
func Resolve(args []string, sort Sort) (entries []Entry, start int, dir string, err error) {
	switch len(args) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return nil, 0, "", err
		}
		return resolveDir(cwd, sort)
	case 1:
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return nil, 0, "", err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, 0, "", err
		}
		if fi.IsDir() {
			return resolveDir(abs, sort)
		}
		return resolveFile(abs, fi, sort)
	}

	seen := make(map[string]bool)
	add := func(e Entry) {
		if !seen[e.Path] {
			seen[e.Path] = true
			entries = append(entries, e)
		}
	}
	for _, arg := range args {
		abs, err := filepath.Abs(arg)
		if err != nil {
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			list, err := List(abs, sort)
			if err != nil {
				continue
			}
			for _, e := range list {
				add(e)
			}
		} else if fi.Mode().IsRegular() && IsImage(abs) {
			add(entryOf(abs, fi))
		}
	}
	if len(entries) == 0 {
		return nil, 0, "", errors.New("no viewable images among the given paths")
	}
	return entries, 0, "", nil
}

func resolveDir(dir string, sort Sort) ([]Entry, int, string, error) {
	entries, err := List(dir, sort)
	if err != nil {
		return nil, 0, "", err
	}
	if len(entries) == 0 {
		return nil, 0, "", fmt.Errorf("no images in %s", dir)
	}
	return entries, 0, dir, nil
}

func resolveFile(path string, fi os.FileInfo, sort Sort) ([]Entry, int, string, error) {
	dir := filepath.Dir(path)
	entries, err := List(dir, sort)
	if err != nil {
		// Unreadable parent: still show the one file we were given.
		return []Entry{entryOf(path, fi)}, 0, dir, nil
	}
	if i := indexOf(entries, path); i >= 0 {
		return entries, i, dir, nil
	}
	entries = append(entries, entryOf(path, fi))
	sortEntries(entries, sort)
	return entries, indexOf(entries, path), dir, nil
}

func indexOf(entries []Entry, path string) int {
	return slices.IndexFunc(entries, func(e Entry) bool { return e.Path == path })
}

// List returns the readable images directly inside dir (no recursion),
// hidden files (leading dot) excluded, sorted. Symlinks followed.
func List(dir string, sort Sort) ([]Entry, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(des))
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		fi, err := os.Stat(path) // follows symlinks
		if err != nil || !fi.Mode().IsRegular() || !IsImage(path) {
			continue
		}
		entries = append(entries, entryOf(path, fi))
	}
	sortEntries(entries, sort)
	return entries, nil
}

func entryOf(path string, fi os.FileInfo) Entry {
	return Entry{Path: path, Name: filepath.Base(path), ModTime: fi.ModTime(), Size: fi.Size()}
}

func sortEntries(entries []Entry, sort Sort) {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		if sort == SortMTime {
			if c := a.ModTime.Compare(b.ModTime); c != 0 {
				return c
			}
		}
		switch {
		case NaturalLess(a.Name, b.Name):
			return -1
		case NaturalLess(b.Name, a.Name):
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
}
