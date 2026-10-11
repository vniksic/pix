// Package scan turns command-line arguments and directories into an ordered
// list of image files, and watches directories for changes.
package scan

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
	Taken   time.Time // date taken (Options.Taken); filled only for SortDate, zero otherwise or when unknown
}

// Sort selects the playlist order.
type Sort int

const (
	SortName  Sort = iota // natural sort on Name, case-insensitive ("img2" < "img10")
	SortMTime             // oldest first; ties broken by natural name
	SortDate              // by date taken, oldest first; files without one use ModTime; ties as SortMTime
)

// Options configures Resolve.
type Options struct {
	Sort      Sort
	Recursive bool // expand directories with ListTree instead of List
	// Taken returns the date a file was taken (e.g. imgload.Taken) for
	// SortDate. It may be called concurrently. nil makes SortDate order
	// like SortMTime.
	Taken func(path string) (time.Time, bool)
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
//
// The single argument "-" reads the list of paths from standard input
// (see ResolveReader).
//
// Resolve also remembers opts.Taken (nil clears it): List and ListTree use
// it for SortDate, so rescans keep the same order. Dates are cached by
// path, size and modification time.
func Resolve(args []string, opts Options) (entries []Entry, start int, roots []string, err error) {
	if len(args) == 1 && args[0] == "-" {
		return ResolveReader(os.Stdin, opts)
	}
	c := setTakenSource(opts.Taken)
	switch len(args) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return nil, 0, nil, err
		}
		return resolveDir(cwd, opts, c)
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
			return resolveDir(abs, opts, c)
		}
		return resolveFile(abs, fi, opts, c)
	}
	return resolveAdhoc(args, opts, c)
}

// ResolveReader builds an ad-hoc list (as Resolve does for several
// arguments) from newline-separated paths read from r: CRLF line endings
// are tolerated, blank lines skipped, relative paths taken against the
// working directory, directories expanded and missing paths skipped. Even
// a single path makes an ad-hoc list. roots is always nil. It is what
// Resolve uses for the argument "-".
func ResolveReader(r io.Reader, opts Options) (entries []Entry, start int, roots []string, err error) {
	c := setTakenSource(opts.Taken)
	var paths []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		paths = append(paths, line)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("reading path list: %w", err)
	}
	return resolveAdhoc(paths, opts, c)
}

// resolveAdhoc implements the ad-hoc list case of Resolve.
func resolveAdhoc(args []string, opts Options, c *takenCache) (entries []Entry, start int, roots []string, err error) {
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
			list, err := listDir(abs, opts, c)
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
	if opts.Sort == SortDate {
		c.fill(entries) // the files given directly; listed ones are cached
	}
	return entries, 0, nil, nil
}

func listDir(dir string, opts Options, c *takenCache) ([]Entry, error) {
	if opts.Recursive {
		return listTree(dir, opts.Sort, c)
	}
	return list(dir, opts.Sort, c)
}

func resolveDir(dir string, opts Options, c *takenCache) ([]Entry, int, []string, error) {
	entries, err := listDir(dir, opts, c)
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

func resolveFile(path string, fi os.FileInfo, opts Options, c *takenCache) ([]Entry, int, []string, error) {
	dir := filepath.Dir(path)
	roots := []string{dir}
	self := entryOf(path, filepath.Base(path), fi)
	if opts.Sort == SortDate {
		one := []Entry{self}
		c.fill(one)
		self = one[0]
	}
	entries, err := listDir(dir, opts, c)
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
// Label is the file's Name. SortDate uses the Taken function of the last
// Resolve (see there).
func List(dir string, sort Sort) ([]Entry, error) {
	return list(dir, sort, takenSource.Load())
}

func list(dir string, sort Sort, c *takenCache) ([]Entry, error) {
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
	if sort == SortDate {
		c.fill(entries)
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
// whole tree, ties broken by path; so is SortDate, by date taken (Taken of
// the last Resolve) falling back to ModTime.
func ListTree(root string, sort Sort) ([]Entry, error) {
	return listTree(root, sort, takenSource.Load())
}

func listTree(root string, sort Sort, c *takenCache) ([]Entry, error) {
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
	if sort == SortDate {
		c.fill(entries)
	}
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
// modification time first for SortMTime, by date taken (Taken, or ModTime
// when Taken is zero) first for SortDate, then in tree order on Label (a
// directory's own files before its subdirectories, both in natural order),
// then by Path.
func Compare(a, b Entry, sort Sort) int {
	switch sort {
	case SortMTime:
		if c := a.ModTime.Compare(b.ModTime); c != 0 {
			return c
		}
	case SortDate:
		if c := dateKey(a).Compare(dateKey(b)); c != 0 {
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
