package scan

import (
	"errors"
	"os"
	"time"
)

// statBorn is how StatSince gets a file's FileInfo and creation time in
// one call (statCreated); a variable so tests can make up creation times,
// which cannot be set on a real file.
var statBorn = statCreated

// errOld is StatSince's error for a file created before the cutoff.
var errOld = errors.New("created before the cutoff")

// StatSince is os.Stat for a listing limited to files created at or after
// since (Target.Since). A zero since makes it os.Stat. Otherwise a file
// created before since is an error, but a directory never is (it is still
// to be descended). A file's creation time is its birth time where the OS
// and filesystem report one (Linux statx), else its modification time,
// decided per file and had in the same single stat call.
func StatSince(path string, since time.Time) (os.FileInfo, error) {
	if since.IsZero() {
		return os.Stat(path)
	}
	fi, created, err := statBorn(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() && created.Before(since) {
		return nil, &os.PathError{Op: "stat", Path: path, Err: errOld}
	}
	return fi, nil
}
