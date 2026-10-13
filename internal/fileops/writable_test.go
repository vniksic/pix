//go:build unix

package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckWritable(t *testing.T) {
	root := t.TempDir()
	if err := CheckWritable(root); err != nil {
		t.Errorf("writable dir: %v", err)
	}
	if err := CheckWritable(filepath.Join(root, "new", "deeper")); err != nil {
		t.Errorf("missing dir below a writable one: %v", err)
	}
	file := filepath.Join(root, "file")
	os.WriteFile(file, nil, 0o644)
	if err := CheckWritable(file); err == nil {
		t.Error("a file passed")
	}
	if err := CheckWritable(filepath.Join(file, "sub")); err == nil {
		t.Error("a path below a file passed")
	}
	if os.Getuid() == 0 {
		t.Skip("root may write anywhere")
	}
	ro := filepath.Join(root, "ro")
	os.Mkdir(ro, 0o555)
	if err := CheckWritable(ro); err == nil {
		t.Error("read-only dir passed")
	}
	if err := CheckWritable(filepath.Join(ro, "new")); err == nil {
		t.Error("missing dir below a read-only one passed")
	}
}
