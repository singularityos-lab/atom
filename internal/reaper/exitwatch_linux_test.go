package reaper

import (
	"os/exec"
	"testing"
	"time"
)

func TestExitedFiresOnTermination(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ch := Exited(cmd.Process.Pid)
	if ch == nil {
		t.Skip("pidfd unsupported")
	}
	select {
	case <-ch:
		t.Fatal("fired before the process exited")
	case <-time.After(100 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("exit not observed")
	}
	_ = cmd.Wait()
}

func TestExitedOnGonePid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	ch := Exited(cmd.Process.Pid)
	if ch == nil {
		t.Skip("pidfd unsupported")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("gone pid not reported")
	}
}
