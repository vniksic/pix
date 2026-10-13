//go:build !unix

package fileops

// CheckWritable does nothing here: a copy that cannot be made fails at
// copy time.
func CheckWritable(dir string) error { return nil }
