package pid1

import (
	"context"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/core"
)

// shutdown stops the target's units in reverse order and syncs filesystems.
func shutdown(m *core.Manager, target string) {
	logf("stopping %s", target)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = m.StopTarget(ctx, target)
	logf("sync")
	syscall.Sync()
}

// powerOff halts the machine.
func powerOff() int {
	logf("powering off")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF); err != nil {
		logf("poweroff failed: %v", err)
		return 1
	}
	return 0
}

// reboot restarts the machine.
func reboot() int {
	logf("rebooting")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART); err != nil {
		logf("reboot failed: %v", err)
		return 1
	}
	return 0
}
