package reaper

import "syscall"

const (
	sysProcctl      = 544
	pPID            = 0
	procReapAcquire = 2
)

// SetSubreaper marks the current process as a child subreaper.
func SetSubreaper() error {
	if syscall.Getpid() == 1 {
		return nil
	}
	if _, _, errno := syscall.Syscall6(sysProcctl, pPID, uintptr(syscall.Getpid()), procReapAcquire, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
