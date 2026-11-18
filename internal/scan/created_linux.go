package scan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// statCreated stats path (following symlinks) and returns its creation
// time with it, in one statx(2) call: the birth time when the filesystem
// reports one, else the modification time (sshfs, for one, has no birth
// time). Kernels without statx get os.Stat and the modification time.
func statCreated(path string) (os.FileInfo, time.Time, error) {
	var st unix.Statx_t
	var err error
	for {
		err = unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BASIC_STATS|unix.STATX_BTIME, &st)
		if err != unix.EINTR {
			break
		}
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) { // no statx, or a sandbox refusing it
		fi, err := os.Stat(path)
		if err != nil {
			return nil, time.Time{}, err
		}
		return fi, fi.ModTime(), nil
	}
	if err != nil {
		return nil, time.Time{}, &fs.PathError{Op: "statx", Path: path, Err: err}
	}
	fi := &statxInfo{name: filepath.Base(path), st: st}
	return fi, createdOf(&st), nil
}

// createdOf is st's birth time when statx filled one in, else its
// modification time.
func createdOf(st *unix.Statx_t) time.Time {
	if st.Mask&unix.STATX_BTIME != 0 && (st.Btime.Sec != 0 || st.Btime.Nsec != 0) {
		return statxTime(st.Btime)
	}
	return statxTime(st.Mtime)
}

func statxTime(ts unix.StatxTimestamp) time.Time { return time.Unix(ts.Sec, int64(ts.Nsec)) }

// statxInfo is the os.FileInfo of a statx result.
type statxInfo struct {
	name string
	st   unix.Statx_t
}

func (s *statxInfo) Name() string       { return s.name }
func (s *statxInfo) Size() int64        { return int64(s.st.Size) }
func (s *statxInfo) ModTime() time.Time { return statxTime(s.st.Mtime) }
func (s *statxInfo) IsDir() bool        { return s.Mode().IsDir() }
func (s *statxInfo) Sys() any           { return &s.st }

// Mode converts the mode bits as os.Stat does.
func (s *statxInfo) Mode() fs.FileMode {
	m := fs.FileMode(s.st.Mode & 0o777)
	switch s.st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= fs.ModeDir
	case unix.S_IFLNK:
		m |= fs.ModeSymlink
	case unix.S_IFIFO:
		m |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		m |= fs.ModeSocket
	case unix.S_IFBLK:
		m |= fs.ModeDevice
	case unix.S_IFCHR:
		m |= fs.ModeDevice | fs.ModeCharDevice
	}
	if s.st.Mode&unix.S_ISUID != 0 {
		m |= fs.ModeSetuid
	}
	if s.st.Mode&unix.S_ISGID != 0 {
		m |= fs.ModeSetgid
	}
	if s.st.Mode&unix.S_ISVTX != 0 {
		m |= fs.ModeSticky
	}
	return m
}
