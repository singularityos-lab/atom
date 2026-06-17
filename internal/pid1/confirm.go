package pid1

import (
	"os"
	"strings"
	"time"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/deploy"
)

// bootHealthDeadline is the hard cap for the health gate before we give up
// waiting for the boot-critical units and leave the boot unconfirmed.
var bootHealthDeadline = 90 * time.Second

// confirmBoot runs the boot-success handshake: it records the attempt against
// the current boot_id (idempotent), waits for the health gate, and on success
// confirms the deployment so a future boot does not roll back. Everything is
// best-effort and logged: on a system without a writable /var it degrades to a
// no-op rather than failing the boot.
func confirmBoot(m *core.Manager) {
	path := deploy.DefaultPath
	d, err := deploy.Load(path)
	if err != nil {
		// No document yet (fresh single-slot Sinty): synthesize a minimal one.
		// The OS/update tooling owns the real slot fields; we own the boot block.
		d = deploy.NewSingleSlot("", "")
	}

	d.BeginBoot(currentBootID())

	if healthGate(m, bootHealthDeadline) {
		d.Confirm(nowRFC3339())
		logf("boot confirmed good")
	} else {
		logf("boot NOT confirmed: health gate timed out")
	}

	if err := d.Save(path); err != nil {
		logf("deployment.json: %v", err)
	}
}

// healthGate returns true once every boot-critical unit is active, or false at
// the deadline. With no annotated critical units it succeeds immediately (the
// target was reached), which is the common case until a critical set is defined.
func healthGate(m *core.Manager, deadline time.Duration) bool {
	critical := m.Critical()
	if len(critical) == 0 {
		return true
	}
	end := time.Now().Add(deadline)
	for {
		allActive := true
		for _, n := range critical {
			if m.State(n) != "active" {
				allActive = false
				break
			}
		}
		if allActive {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func currentBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
