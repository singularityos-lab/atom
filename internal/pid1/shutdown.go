package pid1

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/core"
)

// shutdownDeadline bounds the whole stop phase. A single service whose Stop wedges (an
// unkillable D-state child, a Stop blocking on an exit notification that never arrives)
// must not trap PID 1 on the way down: past the deadline we sync and reboot anyway.
const shutdownDeadline = 30 * time.Second

// shutdown stops the target's units, then syncs filesystems. Stopping runs under a hard
// deadline in a goroutine: if it does not finish we log and proceed, because the caller
// is about to reboot or power off and a wedged unit must not force a manual power cycle.
func shutdown(m *core.Manager, target string) {
	logf("stopping %s", target)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownDeadline)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = m.StopTarget(ctx, target)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		logf("stop did not finish in %s, forcing shutdown", shutdownDeadline)
	}
	logf("sync")
	syscall.Sync()
}

// powerOff halts the machine, escalating to a SysRq power off if the ordered path
// returns (which only happens when it failed to bring the machine down).
func powerOff() int {
	logf("powering off")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF); err != nil {
		logf("poweroff failed: %v, escalating", err)
	}
	return sysrq('o')
}

// reboot restarts the machine, escalating to a SysRq reset if the ordered restart
// returns. On some firmware the kernel's default restart method fails to reset the box
// while reboot(2) still returns, leaving the machine sitting there until the user power
// cycles by hand. SysRq 'b' triggers an immediate machine_restart down a different path,
// so the reboot completes instead of hanging.
func reboot() int {
	logf("rebooting")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART); err != nil {
		logf("reboot failed: %v, escalating", err)
	}
	return sysrq('b')
}

// sysrq is the last-resort bring-down after the ordered reboot/poweroff returned instead
// of taking the machine down. It enables SysRq (the /proc trigger ignores the keyboard
// mask, but enabling is free) and alternates the SysRq action with a retry of the reboot
// syscall a few times, so a box whose primary method is ineffective still resets rather
// than stranding the user. Filesystems are already synced by the caller.
func sysrq(op byte) int {
	cmd := syscall.LINUX_REBOOT_CMD_RESTART
	if op == 'o' {
		cmd = syscall.LINUX_REBOOT_CMD_POWER_OFF
	}
	_ = os.WriteFile("/proc/sys/kernel/sysrq", []byte("1"), 0)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile("/proc/sysrq-trigger", []byte{op, '\n'}, 0); err != nil {
			logf("sysrq %c failed: %v", op, err)
		}
		time.Sleep(500 * time.Millisecond)
		_ = syscall.Reboot(cmd)
	}
	return 1
}
