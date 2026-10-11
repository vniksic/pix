package main

import (
	"os"
	"syscall"
)

// reserveStdout moves the process's stdout to a new close-on-exec
// descriptor and returns it, then makes fd 1 a copy of stderr. Writes to
// fd 1 from anywhere (including C code and child processes) then land on
// stderr, and only the returned file reaches whatever stdout was. On
// failure stdout is left alone and returned.
func reserveStdout() *os.File {
	fd, err := syscall.Dup(1)
	if err != nil {
		return os.Stdout
	}
	syscall.CloseOnExec(fd)
	if err := syscall.Dup3(2, 1, 0); err != nil {
		syscall.Close(fd)
		return os.Stdout
	}
	return os.NewFile(uintptr(fd), "/dev/stdout")
}
