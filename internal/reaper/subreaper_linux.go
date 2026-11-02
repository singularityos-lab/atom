package reaper

import "syscall"

const prSetChildSubreaper = 36

// SetSubreaper marks the current process as a child subreaper.
func SetSubreaper() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
