package pid1

import (
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestReaperReapsChildren forks raw children (not via os/exec, so the reaper is
// the sole waiter) and asserts each is collected on SIGCHLD.
func TestReaperReapsChildren(t *testing.T) {
	bin := "/bin/true"
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no /bin/true on this host")
	}

	var mu sync.Mutex
	reaped := map[int]bool{}
	r := NewReaper(func(pid int, ws syscall.WaitStatus) {
		mu.Lock()
		reaped[pid] = true
		mu.Unlock()
	})
	go r.Run()
	defer r.Stop()

	var pids []int
	for i := 0; i < 3; i++ {
		pid, err := syscall.ForkExec(bin, []string{bin}, &syscall.ProcAttr{})
		if err != nil {
			t.Fatalf("ForkExec: %v", err)
		}
		pids = append(pids, pid)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(reaped)
		mu.Unlock()
		if n >= len(pids) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range pids {
		if !reaped[p] {
			t.Errorf("child pid %d was not reaped (reaped set: %v)", p, reaped)
		}
	}
}
