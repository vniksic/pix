// Package scan turns command-line arguments and directories into an ordered
// list of image files, and watches directories for changes.
package scan

import (
	"bufio"
	"context"
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

// Options configures Resolve and Prepare.
type Options struct {
	Sort      Sort
	Recursive bool // expand directories with ListTree instead of List
	// Taken returns the date a file was taken (e.g. imgload.Taken) for
	// SortDate. It may be called concurrently. nil makes SortDate order
	// like SortMTime.
	Taken func(path string) (time.Time, bool)
}

// Target is a command line resolved without listing anything (see
// Prepare); Scan lists it.
type Target struct {
	Root      string   // listing root in the single-root cases; "" for an ad-hoc list
	File      string   // the file named on its own (Root is its parent); "" otherwise
	Paths     []string // the ad-hoc list's paths, absolute, in order (Root == "")
	Sort      Sort
	Recursive bool // directories are listed with ListTree
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
//
// Resolve is Prepare followed by a Scan to the end.
func Resolve(args []string, opts Options) (entries []Entry, start int, roots []string, err error) {
	t, err := Prepare(args, opts)
	if err != nil {
		return nil, 0, nil, err
	}
	return resolve(t)
}

// ResolveReader builds an ad-hoc list (as Resolve does for several
// arguments) from newline-separated paths read from r: CRLF line endings
// are tolerated, blank lines skipped, relative paths taken against the
// working directory, directories expanded and missing paths skipped. Even
// a single path makes an ad-hoc list. roots is always nil. It is what
// Resolve uses for the argument "-".
func ResolveReader(r io.Reader, opts Options) (entries []Entry, start int, roots []string, err error) {
	t, err := PrepareReader(r, opts)
	if err != nil {
		return nil, 0, nil, err
	}
	return resolve(t)
}

// resolve scans t to the end and works out the rest of Resolve's results.
func resolve(t Target) (entries []Entry, start int, roots []string, err error) {
	entries = final(func(emit func([]Entry, bool)) { Scan(context.Background(), t, emit) })
	switch {
	case len(entries) > 0:
	case t.Root == "":
		return nil, 0, nil, errors.New("no viewable images among the given paths")
	case t.Recursive:
		return nil, 0, nil, fmt.Errorf("no images in %s or below it", t.Root)
	default:
		return nil, 0, nil, fmt.Errorf("no images in %s", t.Root)
	}
	if t.Root != "" {
		roots = []string{t.Root}
	}
	if t.File != "" {
		start = max(0, indexOf(entries, t.File))
	}
	return entries, start, roots, nil
}

// Prepare is the part of Resolve that comes before any listing, cheap
// enough to run before a window opens: it stats a single argument, checks
// that a single directory (or the working directory) can be opened, and
// reads the paths from standard input for "-". The errors it returns are
// the ones Resolve gives up front; that nothing turns out to be viewable is
// only known once Scan is done.
//
// Like Resolve, Prepare remembers opts.Taken (nil clears it) for Scan, List
// and ListTree.
func Prepare(args []string, opts Options) (Target, error) {
	if len(args) == 1 && args[0] == "-" {
		return PrepareReader(os.Stdin, opts)
	}
	setTakenSource(opts.Taken)
	t := Target{Sort: opts.Sort, Recursive: opts.Recursive}
	switch len(args) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return Target{}, err
		}
		t.Root = cwd
		return t, openable(cwd)
	case 1:
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return Target{}, err
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return Target{}, err
		}
		if fi.IsDir() {
			t.Root = abs
			return t, openable(abs)
		}
		// An unreadable parent is no error: the file alone is still shown.
		t.Root, t.File = filepath.Dir(abs), abs
		return t, nil
	}
	t.Paths = absPaths(args)
	return t, nil
}

// PrepareReader is Prepare for the path list of "-", read from r (see
// ResolveReader).
func PrepareReader(r io.Reader, opts Options) (Target, error) {
	setTakenSource(opts.Taken)
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
		return Target{}, fmt.Errorf("reading path list: %w", err)
	}
	return Target{Paths: absPaths(paths), Sort: opts.Sort, Recursive: opts.Recursive}, nil
}

// absPaths makes paths absolute, dropping the ones that cannot be.
func absPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, abs)
		}
	}
	return out
}

// openable reports why dir cannot be listed, without reading its entries.
func openable(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return f.Close()
}

func indexOf(entries []Entry, path string) int {
	return slices.IndexFunc(entries, func(e Entry) bool { return e.Path == path })
}

// List returns the readable images directly inside dir (no recursion),
// hidden files (leading dot) excluded, sorted. Symlinks followed. Each
// Label is the file's Name. SortDate uses the Taken function of the last
// Resolve or Prepare (see there). It is ScanList run to the end.
func List(dir string, sort Sort) ([]Entry, error) {
	var err error
	entries := final(func(emit func([]Entry, bool)) { err = ScanList(context.Background(), dir, sort, emit) })
	return entries, err
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
// the last Resolve or Prepare) falling back to ModTime. It is ScanTree run
// to the end.
func ListTree(root string, sort Sort) ([]Entry, error) {
	var err error
	entries := final(func(emit func([]Entry, bool)) { err = ScanTree(context.Background(), root, sort, emit) })
	return entries, err
}

// final runs a scan to the end and returns its last snapshot.
func final(scan func(emit func([]Entry, bool))) []Entry {
	var out []Entry
	scan(func(entries []Entry, done bool) {
		if done {
			out = entries
		}
	})
	return out
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
