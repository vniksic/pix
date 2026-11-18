//go:build !linux

package scan

import (
	"os"
	"time"
)

// statCreated stats path (following symlinks) and returns its creation
// time with it: here always the modification time (see the Linux version).
func statCreated(path string) (os.FileInfo, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	return fi, fi.ModTime(), nil
}
