package fileops

import (
	"fmt"
	"os"
)

// Delete removes path for good, without the trash. It is the only place pix
// deletes a file outright, so it refuses anything but a regular file or a
// symbolic link: a directory, device, FIFO or socket is left alone. A
// symbolic link is removed as the link only, never what it points to, the
// way Trash moves the link and rm removes it.
func Delete(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if m := fi.Mode(); !m.IsRegular() && m&os.ModeSymlink == 0 {
		return fmt.Errorf("delete %s: not a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}
