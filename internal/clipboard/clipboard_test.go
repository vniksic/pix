package clipboard

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTools creates a PATH directory holding the named tools. Each records
// its argv (one per line) to <out>/<tool>.args and stdin to <out>/<tool>.stdin,
// then runs extra.
func fakeTools(t *testing.T, extra string, tools ...string) (out string) {
	t.Helper()
	bin := t.TempDir()
	out = t.TempDir()
	for _, tool := range tools {
		script := "#!/bin/sh\nPATH=/usr/bin:/bin\n" +
			"printf '%s\\n' \"$@\" > '" + filepath.Join(out, tool+".args") + "'\n" +
			"cat > '" + filepath.Join(out, tool+".stdin") + "'\n" + extra + "\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return out
}

func readOut(t *testing.T, out, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatalf("%s not written: %v", name, err)
	}
	return string(b)
}

func testImage() *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	m.SetNRGBA(2, 1, color.NRGBA{10, 20, 30, 255})
	return m
}

func TestCopyImageWayland(t *testing.T) {
	out := fakeTools(t, "", "wl-copy", "xclip")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if err := CopyImage(testImage()); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out, "wl-copy.args"); got != "--type\nimage/png\n" {
		t.Errorf("args %q", got)
	}
	img, err := png.Decode(strings.NewReader(readOut(t, out, "wl-copy.stdin")))
	if err != nil {
		t.Fatalf("stdin is not a PNG: %v", err)
	}
	if img.Bounds() != testImage().Rect || color.NRGBAModel.Convert(img.At(2, 1)) != (color.NRGBA{10, 20, 30, 255}) {
		t.Error("PNG content differs")
	}
	if _, err := os.Stat(filepath.Join(out, "xclip.args")); err == nil {
		t.Error("xclip also ran")
	}
}

func TestCopyImageX11(t *testing.T) {
	out := fakeTools(t, "", "wl-copy", "xclip")
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := CopyImage(testImage()); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out, "xclip.args"); got != "-selection\nclipboard\n-t\nimage/png\n-i\n" {
		t.Errorf("args %q", got)
	}
	if !bytes.HasPrefix([]byte(readOut(t, out, "xclip.stdin")), []byte("\x89PNG")) {
		t.Error("stdin is not a PNG")
	}
}

func TestWaylandWithoutWlCopyFallsBackToXclip(t *testing.T) {
	out := fakeTools(t, "", "xclip")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if err := CopyText("hi"); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out, "xclip.args"); got != "-selection\nclipboard\n" {
		t.Errorf("args %q", got)
	}
}

func TestCopyText(t *testing.T) {
	out := fakeTools(t, "", "wl-copy")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	const s = "/home/me/pics/a b ä.jpg"
	if err := CopyText(s); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out, "wl-copy.args"); got != "\n" { // printf with no args prints one empty line
		t.Errorf("args %q, want none", got)
	}
	if got := readOut(t, out, "wl-copy.stdin"); got != s {
		t.Errorf("stdin %q, want %q", got, s)
	}
}

func TestNoBackend(t *testing.T) {
	fakeTools(t, "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if err := CopyText("x"); !errors.Is(err, ErrNoBackend) {
		t.Errorf("CopyText err = %v", err)
	}
	if err := CopyImage(testImage()); !errors.Is(err, ErrNoBackend) {
		t.Errorf("CopyImage err = %v", err)
	}
}

func TestToolFailure(t *testing.T) {
	fakeTools(t, "exit 3", "xclip")
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := CopyText("x"); err == nil {
		t.Error("no error from failing tool")
	}
}

func TestLingeringToolDoesNotBlock(t *testing.T) {
	old := exitWait
	exitWait = 200 * time.Millisecond
	defer func() { exitWait = old }()

	// Like wl-copy --foreground: reads stdin, then stays alive.
	out := fakeTools(t, "exec sleep 5", "wl-copy")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	start := time.Now()
	if err := CopyText("still here"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("CopyText took %v", d)
	}
	if got := readOut(t, out, "wl-copy.stdin"); got != "still here" {
		t.Errorf("stdin %q", got)
	}
}

func TestLargePayload(t *testing.T) {
	out := fakeTools(t, "", "xclip")
	t.Setenv("WAYLAND_DISPLAY", "")
	s := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB, far above a pipe buffer
	if err := CopyText(s); err != nil {
		t.Fatal(err)
	}
	if got := readOut(t, out, "xclip.stdin"); got != s {
		t.Errorf("got %d bytes, want %d", len(got), len(s))
	}
}

// sessionOf returns field 6 (session id) of /proc/<pid>/stat.
func sessionOf(t *testing.T, stat string) string {
	t.Helper()
	i := strings.LastIndexByte(stat, ')')
	f := strings.Fields(stat[i+1:])
	if i < 0 || len(f) < 4 {
		t.Fatalf("bad stat %q", stat)
	}
	return f[3] // state ppid pgrp session
}

func TestToolRunsInOwnSession(t *testing.T) {
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc")
	}
	out := fakeTools(t, "", "xclip")
	stat := filepath.Join(out, "stat")
	// Record the tool's own /proc stat line after the default script runs.
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "xclip"), []byte(
		"#!/bin/sh\nPATH=/usr/bin:/bin\ncat >/dev/null\ncat /proc/$$/stat > '"+stat+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := CopyText("x"); err != nil {
		t.Fatal(err)
	}
	if got, ours := sessionOf(t, readOut(t, out, "stat")), sessionOf(t, string(self)); got == ours {
		t.Errorf("clipboard tool shares our session %s; a signal to our group would kill the selection owner", ours)
	}
}
