//go:build !linux

package reaper

// Exited returns nil: without pidfds the caller polls for the exit.
func Exited(pid int) <-chan struct{} { return nil }
