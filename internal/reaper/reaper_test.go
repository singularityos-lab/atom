package reaper

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestRegistryWaitThenDeliver(t *testing.T) {
	r := NewRegistry()
	ch := r.Wait(1234)
	r.Deliver(1234, syscall.WaitStatus(0))
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("status not delivered to a registered waiter")
	}
}

func TestRegistryBuffersEarlyDelivery(t *testing.T) {
	r := NewRegistry()
	// Status arrives BEFORE the waiter registers (the register-vs-exit race).
	r.Deliver(5678, syscall.WaitStatus(0))
	ch := r.Wait(5678)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("buffered (early) status not returned to a late waiter")
	}
}

func TestReaperRoutesRealChild(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	r := NewRegistry()
	rp := NewReaper(r)
	go rp.Run()
	defer rp.Stop()

	pid, err := syscall.ForkExec("/bin/true", []string{"/bin/true"}, &syscall.ProcAttr{})
	if err != nil {
		t.Fatalf("ForkExec: %v", err)
	}
	ch := r.Wait(pid)
	select {
	case ws := <-ch:
		if ws.Signaled() || ws.ExitStatus() != 0 {
			t.Errorf("routed status = %+v, want clean exit 0", ws)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("child %d exit status was not routed", pid)
	}
}
