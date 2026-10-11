// Package clipboard copies images and text to the system clipboard by
// piping them into wl-copy (Wayland) or xclip (X11).
package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var ErrNoBackend = errors.New("clipboard: neither wl-copy nor xclip found")

// writeTimeout bounds how long we wait for the tool to read its stdin;
// exitWait bounds how long we wait for it to exit after that. Both tools
// normally fork a child that keeps serving the selection, so the process
// we started exits right away, but wl-copy may legitimately stay alive.
var (
	writeTimeout = 10 * time.Second
	exitWait     = time.Second
)

// CopyImage PNG-encodes img and hands it to the system clipboard.
// Backend selection: if $WAYLAND_DISPLAY is set and wl-copy is in PATH ->
// `wl-copy --type image/png` (stdin). Else if xclip in PATH ->
// `xclip -selection clipboard -t image/png -i`. Else ErrNoBackend.
func CopyImage(img image.Image) error {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return fmt.Errorf("clipboard: encode png: %w", err)
	}
	return copyData(buf.Bytes(),
		[]string{"--type", "image/png"},
		[]string{"-selection", "clipboard", "-t", "image/png", "-i"})
}

// CopyText puts s on the clipboard with the same backend selection
// (wl-copy / xclip -selection clipboard).
func CopyText(s string) error {
	return copyData([]byte(s), nil, []string{"-selection", "clipboard"})
}

func copyData(data []byte, wlArgs, xclipArgs []string) error {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if path, err := exec.LookPath("wl-copy"); err == nil {
			return run(path, wlArgs, data)
		}
	}
	if path, err := exec.LookPath("xclip"); err == nil {
		return run(path, xclipArgs, data)
	}
	return ErrNoBackend
}

// run starts the tool with data on stdin. It waits until stdin is fully
// written and closed, then at most exitWait for the process to exit; a
// process still running after that is left alone (and reaped in the
// background) because it may be serving the selection.
func run(path string, args []string, data []byte) error {
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = r
	// Own session: the forked selection owner must not share our process
	// group, or a terminal Ctrl+C / SIGHUP aimed at pix would kill it too
	// and the clipboard would empty.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Stdout/Stderr stay nil (/dev/null): a pipe there would be inherited
	// by the forked selection server and keep Wait from returning.
	err = cmd.Start()
	r.Close()
	if err != nil {
		w.Close()
		return fmt.Errorf("clipboard: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	w.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, werr := w.Write(data)
	w.Close()

	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("clipboard: %s: %w", path, err)
		}
	case <-time.After(exitWait):
		// Still running: assume it is serving the selection.
	}
	if werr != nil {
		return fmt.Errorf("clipboard: writing to %s: %w", path, werr)
	}
	return nil
}
