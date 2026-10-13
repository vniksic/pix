package fileops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	f := writeFile(t, filepath.Join(dir, "a.jpg"), "a", time.Time{})
	if err := Delete(f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(f); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file still there: %v", err)
	}

	// A link goes, its target stays.
	target := writeFile(t, filepath.Join(dir, "target.jpg"), "target", time.Time{})
	link := filepath.Join(dir, "link.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Delete(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("link still there: %v", err)
	}
	if got := readFile(t, target); got != "target" {
		t.Errorf("target changed: %q", got)
	}
}

func TestDeleteRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := Delete(filepath.Join(dir, "missing.jpg")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}

	// An empty directory, which os.Remove alone would remove.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Delete(sub); err == nil {
		t.Error("directory: no error")
	}
	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Errorf("directory gone or changed: %v", err)
	}
}
