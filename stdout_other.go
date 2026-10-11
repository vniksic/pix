//go:build !linux

package main

import "os"

// reserveStdout returns stdout unchanged (see the Linux version).
func reserveStdout() *os.File { return os.Stdout }
