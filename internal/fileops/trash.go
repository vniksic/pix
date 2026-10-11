package fileops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Trash moves path to the freedesktop.org trash (Trash spec 1.0), in pure
// Go: the home trash ($XDG_DATA_HOME/Trash, else ~/.local/share/Trash)
// when path is on the same filesystem, else $topdir/.Trash/$uid (only if
// $topdir/.Trash is a sticky, non-symlink directory) or $topdir/.Trash-$uid
// (created 0700), $topdir being the mount point of path's filesystem.
//
// It writes info/NAME.trashinfo (created exclusively, NAME unique in
// files/) and then renames path to files/NAME. The file is only ever
// renamed, never copied and deleted; when no trash directory accepts it,
// Trash returns an error and path is left untouched.
func Trash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("trash %s: %w", path, err)
	}
	if _, err := os.Lstat(abs); err != nil {
		return fmt.Errorf("trash: %w", err)
	}
	// Resolve symlinks in the directories (not the file itself), so the
	// recorded path and the mount point are the real ones.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("trash %s: %w", path, err)
	}
	base := filepath.Base(abs)
	if base == "/" || base == "." || base == ".." {
		return fmt.Errorf("trash %s: refusing to trash this path", path)
	}
	real := filepath.Join(dir, base)

	home, err := homeTrash()
	if err == nil {
		err = trashInto(home, real, real)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("trash %s: %w", path, err)
		}
	}
	homeErr := err

	top, err := findTop(dir)
	if err != nil {
		return fmt.Errorf("trash %s: %w", path, err)
	}
	rel, err := filepath.Rel(top, real)
	if err != nil {
		return fmt.Errorf("trash %s: %w", path, err)
	}
	uid := strconv.Itoa(os.Getuid())
	candidates := []func() (string, error){
		func() (string, error) {
			admin := filepath.Join(top, ".Trash")
			fi, err := os.Lstat(admin)
			if err != nil {
				return "", err
			}
			if !fi.IsDir() || fi.Mode()&fs.ModeSticky == 0 {
				return "", fmt.Errorf("%s is not a sticky directory", admin)
			}
			return ensureTrashDir(filepath.Join(admin, uid))
		},
		func() (string, error) {
			return ensureTrashDir(filepath.Join(top, ".Trash-"+uid))
		},
	}
	errs := []error{fmt.Errorf("home trash: %w", homeErr)}
	for _, c := range candidates {
		td, err := c()
		if err == nil {
			if err = trashInto(td, real, rel); err == nil {
				return nil
			}
		}
		errs = append(errs, err)
	}
	return fmt.Errorf("trash %s: no usable trash directory: %w", path, errors.Join(errs...))
}

// homeTrash returns the home trash directory, creating it (and its files/
// and info/ subdirectories) if needed.
func homeTrash() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" || !filepath.IsAbs(data) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	trash := filepath.Join(data, "Trash")
	for _, sub := range []string{"files", "info"} {
		if err := os.MkdirAll(filepath.Join(trash, sub), 0o700); err != nil {
			return "", err
		}
	}
	return trash, nil
}

// ensureTrashDir creates dir (0700) if missing, checks it is a real
// directory, and creates its files/ and info/ subdirectories.
func ensureTrashDir(dir string) (string, error) {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() { // includes symlinks, which Lstat does not follow
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	for _, sub := range []string{"files", "info"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return dir, nil
}

// trashInto moves real into the trash directory td, recording infoPath
// (absolute, or relative to the trash's top directory) as its origin. On
// failure nothing is left behind and real is untouched.
func trashInto(td, real, infoPath string) error {
	files, info := filepath.Join(td, "files"), filepath.Join(td, "info")
	content := "[Trash Info]\nPath=" + escapePath(infoPath) +
		"\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n"
	base := filepath.Base(real)
	for n := 1; n <= maxSuffix; n++ {
		name := suffixed(base, n)
		target := filepath.Join(files, name)
		if taken, err := exists(target); err != nil {
			return err
		} else if taken {
			continue
		}
		infoFile := filepath.Join(info, name+".trashinfo")
		f, err := os.OpenFile(infoFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(infoFile)
			return werr
		}
		// The .trashinfo reserves the name; still, do not clobber a file
		// that appeared in files/ meanwhile.
		if taken, err := exists(target); err != nil || taken {
			os.Remove(infoFile)
			if err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(real, target); err != nil {
			os.Remove(infoFile)
			return err
		}
		return nil
	}
	return fmt.Errorf("no free name for %s in %s", base, files)
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// findTop is mountTop; tests replace it to fake a mount point.
var findTop = mountTop

// mountTop returns the top directory of the filesystem dir (a real path,
// symlinks resolved) is on: the highest ancestor with the same device.
func mountTop(dir string) (string, error) {
	dev := func(p string) (uint64, error) {
		fi, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, errors.New("no device information")
		}
		return uint64(st.Dev), nil
	}
	d, err := dev(dir)
	if err != nil {
		return "", err
	}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir, nil
		}
		if pd, err := dev(parent); err != nil || pd != d {
			return dir, nil
		}
		dir = parent
	}
}

// escapePath percent-encodes a path for a .trashinfo Path= line (as a URI
// path: every byte except unreserved characters and "/").
func escapePath(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
