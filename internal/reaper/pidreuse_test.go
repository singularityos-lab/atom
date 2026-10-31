package reaper

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func exit(code int) syscall.WaitStatus { return syscall.WaitStatus(code << 8) }

// deadPID returns the pid of a child that has exited and been collected.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// TestWaitDropsStalePIDReuse ensures a buffered status left by an earlier child
// is not handed to a new child that reused the same pid: Wait must ignore the
// stale entry, install a fresh waiter, and route the new child's real exit
//.
func TestWaitDropsStalePIDReuse(t *testing.T) {
	r := NewRegistry()
	pid := os.Getpid() // alive, standing in for the new child
	// An orphan exited earlier with this pid and its status was buffered (no waiter).
	r.Deliver(pid, exit(7))

	ch := r.Wait(pid) // the new child reused the pid and registers interest
	select {
	case ws := <-ch:
		t.Fatalf("Wait returned stale status (exit %d) for a reused pid; must wait for the new child", ws.ExitStatus())
	default:
	}

	r.Deliver(pid, exit(0)) // the new child's real exit
	select {
	case ws := <-ch:
		if ws.ExitStatus() != 0 {
			t.Errorf("routed exit %d, want 0 (the new child)", ws.ExitStatus())
		}
	default:
		t.Fatal("the new child's status did not route to the waiter")
	}
}

// TestWaitReturnsFreshBufferedStatus keeps the legitimate register-vs-exit race
// working: a status buffered just before Wait is returned immediately.
func TestWaitReturnsFreshBufferedStatus(t *testing.T) {
	r := NewRegistry()
	pid := deadPID(t)
	r.Deliver(pid, exit(3))
	select {
	case ws := <-r.Wait(pid):
		if ws.ExitStatus() != 3 {
			t.Errorf("got exit %d, want 3", ws.ExitStatus())
		}
	default:
		t.Fatal("a freshly buffered status should be returned immediately")
	}
}

// TestWaitReturnsDelayedBufferedStatus covers a caller delayed by a loaded
// machine: the child is gone, so its status is still the one to return.
func TestWaitReturnsDelayedBufferedStatus(t *testing.T) {
	r := NewRegistry()
	pid := deadPID(t)
	r.Deliver(pid, exit(3))
	r.mu.Lock()
	r.pending[pid] = pending{ws: exit(3), at: time.Now().Add(-5 * time.Second)}
	r.mu.Unlock()
	select {
	case ws := <-r.Wait(pid):
		if ws.ExitStatus() != 3 {
			t.Errorf("got exit %d, want 3", ws.ExitStatus())
		}
	default:
		t.Fatal("a delayed caller lost the status of its exited child")
	}
}
