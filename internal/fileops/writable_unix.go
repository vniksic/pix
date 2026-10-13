//go:build unix

package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckWritable reports whether copies could be made into dir: it must be
// a directory the user may write to and enter, or not exist yet below one
// (CopyAll then creates it). It asks the kernel with access(2), so ACLs
// and group membership count and nothing is written. Network mounts may
// answer optimistically; a copy can still fail later.
func CheckWritable(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Walk up to the deepest path that exists.
	p := dir
	for {
		fi, err := os.Stat(p)
		if err == nil {
			if !fi.IsDir() {
				if p == dir {
					return fmt.Errorf("%s exists and is not a directory", dir)
				}
				return fmt.Errorf("%s is not a directory", p)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return err
		}
		p = parent
	}
	if err := syscall.Access(p, 0x2|0x1); err != nil { // W_OK | X_OK
		if p == dir {
			return fmt.Errorf("cannot write to %s: %w", dir, err)
		}
		return fmt.Errorf("cannot create %s: %s: %w", dir, p, err)
	}
	return nil
}
