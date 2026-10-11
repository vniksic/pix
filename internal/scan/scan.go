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
	Label   string // display path: relative to the listing root, or see Resolve
	ModTime time.Time
	Size    int64
}

// Sort selects the playlist order.
type Sort int

const (
	SortName  Sort = iota // natural sort on Name, case-insensitive ("img2" < "img10")
	SortMTime             // oldest first; ties broken by natural name
)

// Options configures Resolve.
type Options struct {
	Sort      Sort
	Recursive bool // expand directories with ListTree instead of List
}

// Resolve turns CLI args into a playlist.
//
//	no args      -> cwd listing, start=0, roots=[cwd]
//	one dir      -> that dir's listing, start=0, roots=[dir]
//	one file     -> its parent dir's listing, start=index of that file,
//	                roots=[parent] (the file is included even if IsImage
//	                says no)
//	several args -> ad-hoc list of exactly those paths that exist
//	                (dirs expanded, non-images dropped), roots=nil
//
// Directories are listed with List, or with ListTree when opts.Recursive.
// For the single-root cases each Label is relative to the root. Ad-hoc lists
// keep the argument order (each expanded directory is sorted internally),
// drop duplicates, and label each file relative to the working directory
// when it is under it, else by its absolute path with $HOME shortened to
// "~". Returns an error only when nothing is viewable.
func Resolve(args []string, opts Options) (entries []Entry, start int, roots []string, err error) {
	switch len(args) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return nil, 0, nil, err
		}
		return resolveDir(cwd, opts)
	case 1:
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return nil, 0, nil, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return nil, 0, nil, err
		}
		if fi.IsDir() {
			return resolveDir(abs, opts)
		}
		return resolveFile(abs, fi, opts)
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	seen := make(map[string]bool)
	add := func(e Entry) {
		if !seen[e.Path] {
			seen[e.Path] = true
			e.Label = adhocLabel(e.Path, cwd, home)
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
			list, err := listDir(abs, opts)
			if err != nil {
				continue
			}
			for _, e := range list {
				add(e)
			}
		} else if fi.Mode().IsRegular() && IsImage(abs) {
			add(entryOf(abs, "", fi))
		}
	}
	if len(entries) == 0 {
		return nil, 0, nil, errors.New("no viewable images among the given paths")
	}
	return entries, 0, nil, nil
}

func listDir(dir string, opts Options) ([]Entry, error) {
	if opts.Recursive {
		return ListTree(dir, opts.Sort)
	}
	return List(dir, opts.Sort)
}

func resolveDir(dir string, opts Options) ([]Entry, int, []string, error) {
	entries, err := listDir(dir, opts)
	if err != nil {
		return nil, 0, nil, err
	}
	if len(entries) == 0 {
		if opts.Recursive {
			return nil, 0, nil, fmt.Errorf("no images in %s or below it", dir)
		}
		return nil, 0, nil, fmt.Errorf("no images in %s", dir)
	}
	return entries, 0, []string{dir}, nil
}

func resolveFile(path string, fi os.FileInfo, opts Options) ([]Entry, int, []string, error) {
	dir := filepath.Dir(path)
	roots := []string{dir}
	self := entryOf(path, filepath.Base(path), fi)
	entries, err := listDir(dir, opts)
	if err != nil {
		// Unreadable parent: still show the one file we were given.
		return []Entry{self}, 0, roots, nil
	}
	if i := indexOf(entries, path); i >= 0 {
		return entries, i, roots, nil
	}
	entries = append(entries, self)
	sortEntries(entries, opts.Sort)
	return entries, indexOf(entries, path), roots, nil
}

func indexOf(entries []Entry, path string) int {
	return slices.IndexFunc(entries, func(e Entry) bool { return e.Path == path })
}

// List returns the readable images directly inside dir (no recursion),
// hidden files (leading dot) excluded, sorted. Symlinks followed. Each
// Label is the file's Name.
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
		if e, ok := imageEntry(dir, "", de); ok {
			entries = append(entries, e)
		}
	}
	sortEntries(entries, sort)
	return entries, nil
}

// ListTree returns the readable images inside root and all its
// subdirectories, each Label relative to root ("sub/dir/photo.jpg").
// Hidden files and directories (leading dot) are skipped, symlinked files
// are followed and symlinked directories are not (no cycles). Unreadable
// subdirectories are skipped; only an unreadable root is an error.
//
// SortName lists each directory's own images first, in natural order, then
// its subdirectories in natural order, each the same way, so a directory's
// pictures stay together. SortMTime is one flat oldest-first order over the
// whole tree, ties broken by path.
func ListTree(root string, sort Sort) ([]Entry, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var walk func(dir, rel string, des []os.DirEntry)
	walk = func(dir, rel string, des []os.DirEntry) {
		for _, de := range des {
			name := de.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if de.IsDir() { // a real directory; symlinks report their own type
				sub := filepath.Join(dir, name)
				if subDes, err := os.ReadDir(sub); err == nil {
					walk(sub, joinRel(rel, name), subDes)
				}
				continue
			}
			if e, ok := imageEntry(dir, rel, de); ok {
				entries = append(entries, e)
			}
		}
	}
	walk(root, "", des)
	sortEntries(entries, sort)
	return entries, nil
}

// imageEntry returns the entry for de inside dir when it is a visible,
// readable image file (following symlinks). rel is dir relative to the
// listing root ("" for the root itself) and prefixes the Label.
func imageEntry(dir, rel string, de os.DirEntry) (Entry, bool) {
	if strings.HasPrefix(de.Name(), ".") {
		return Entry{}, false
	}
	path := filepath.Join(dir, de.Name())
	fi, err := os.Stat(path) // follows symlinks
	if err != nil || !fi.Mode().IsRegular() || !IsImage(path) {
		return Entry{}, false
	}
	return entryOf(path, joinRel(rel, de.Name()), fi), true
}

func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func entryOf(path, label string, fi os.FileInfo) Entry {
	return Entry{Path: path, Name: filepath.Base(path), Label: label, ModTime: fi.ModTime(), Size: fi.Size()}
}

// RelLabel is the Label of path in a listing rooted at root: the path
// relative to root ("sub/photo.jpg"), or path itself when it is not under
// root.
func RelLabel(root, path string) string {
	if rel, ok := under(root, path); ok {
		return rel
	}
	return path
}

// adhocLabel labels a file of an ad-hoc list: relative to cwd when under
// it, else absolute with home shortened to "~".
func adhocLabel(path, cwd, home string) string {
	if rel, ok := under(cwd, path); ok {
		return rel
	}
	if rel, ok := under(home, path); ok {
		return "~/" + rel
	}
	return path
}

// under returns path relative to root when it lies strictly inside root.
func under(root, path string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Compare orders entries the way List and ListTree sort them: by
// modification time first for SortMTime, then in tree order on Label (a
// directory's own files before its subdirectories, both in natural order),
// then by Path.
func Compare(a, b Entry, sort Sort) int {
	if sort == SortMTime {
		if c := a.ModTime.Compare(b.ModTime); c != 0 {
			return c
		}
	}
	if c := treeCompare(a.Label, b.Label); c != 0 {
		return c
	}
	return strings.Compare(a.Path, b.Path)
}

// treeCompare orders slash-separated relative paths so that the files of a
// directory come before its subdirectories, and siblings (files or
// directories) are in natural order.
func treeCompare(a, b string) int {
	for {
		ia, ib := strings.IndexByte(a, '/'), strings.IndexByte(b, '/')
		switch {
		case ia < 0 && ib < 0: // two files in the same directory
			return naturalCmp(a, b)
		case ia < 0: // a is a file here, b is further down
			return -1
		case ib < 0:
			return 1
		}
		if c := naturalCmp(a[:ia], b[:ib]); c != 0 {
			return c
		}
		a, b = a[ia+1:], b[ib+1:]
	}
}

func naturalCmp(a, b string) int {
	switch {
	case NaturalLess(a, b):
		return -1
	case NaturalLess(b, a):
		return 1
	}
	return 0
}

func sortEntries(entries []Entry, sort Sort) {
	slices.SortStableFunc(entries, func(a, b Entry) int { return Compare(a, b, sort) })
}
