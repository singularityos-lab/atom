package pid1

import "syscall"

const (
	sysReboot  = 55
	rbAutoboot = 0
	rbHalt     = 0x8
	rbPoweroff = 0x4000
)

func disableCtrlAltDel() {}

func rebootSyscall(howto uintptr) error {
	if _, _, errno := syscall.Syscall(sysReboot, howto, 0, 0); errno != 0 {
		return errno
	}
	return nil
}

func powerOff() int {
	logf("powering off")
	syscall.Sync()
	if err := rebootSyscall(rbPoweroff); err != nil {
		logf("poweroff failed: %v, halting", err)
		_ = rebootSyscall(rbHalt)
	}
	return 1
}

func reboot() int {
	logf("rebooting")
	syscall.Sync()
	if err := rebootSyscall(rbAutoboot); err != nil {
		logf("reboot failed: %v", err)
	}
	return 1
}
