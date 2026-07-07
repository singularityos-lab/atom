package reaper

import (
	"syscall"
	"testing"
	"time"
)

func exit(code int) syscall.WaitStatus { return syscall.WaitStatus(code << 8) }

// TestWaitDropsStalePIDReuse ensures a buffered status left by an earlier child
// is not handed to a new child that reused the same pid: Wait must ignore the
// stale entry, install a fresh waiter, and route the new child's real exit
//.
func TestWaitDropsStalePIDReuse(t *testing.T) {
	r := NewRegistry()
	// An orphan exited earlier with pid 100 and its status was buffered (no waiter).
	r.Deliver(100, exit(7))
	// Age it so it predates the reused pid's registration.
	r.mu.Lock()
	r.pending[100] = pending{ws: exit(7), at: time.Now().Add(-5 * time.Second)}
	r.mu.Unlock()

	ch := r.Wait(100) // a new child reused pid 100 and registers interest
	select {
	case ws := <-ch:
		t.Fatalf("Wait returned stale status (exit %d) for a reused pid; must wait for the new child", ws.ExitStatus())
	default:
	}

	r.Deliver(100, exit(0)) // the new child's real exit
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
	r.Deliver(200, exit(3))
	select {
	case ws := <-r.Wait(200):
		if ws.ExitStatus() != 3 {
			t.Errorf("got exit %d, want 3", ws.ExitStatus())
		}
	default:
		t.Fatal("a freshly buffered status should be returned immediately")
	}
}
