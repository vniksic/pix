package fileops

import (
	"os"
	"os/exec"
	"syscall"
)

// OpenExternal launches $PIX_OPEN (a program name, run with path as its
// only argument) or else xdg-open with path, detached in its own session
// with stdin, stdout and stderr on /dev/null. It returns once the program
// has started; the process is reaped in the background.
func OpenExternal(path string) error {
	prog := os.Getenv("PIX_OPEN")
	if prog == "" {
		prog = "xdg-open"
	}
	cmd := exec.Command(prog, path) // nil Stdin/Stdout/Stderr: /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
