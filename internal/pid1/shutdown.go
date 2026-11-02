package pid1

import (
	"context"
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
