package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/reaper"
)

// TestForkingStopKillsDaemonNotLauncher covers a Type=forking service's
// launcher fork()s the real daemon and exits, recording the daemon's pid in
// PIDFile=. Before the fix, Stop signalled the (already dead) launcher and left the
// daemon running unsupervised; now Stop reads PIDFile and stops the daemon itself.
func TestForkingStopKillsDaemonNotLauncher(t *testing.T) {
	requireBins(t, "/bin/sh", "/bin/sleep")
	reg := reaper.NewRegistry()
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()

	old := ReaperWait
	ReaperWait = reg.Wait
	defer func() { ReaperWait = old }()

	pidfile := filepath.Join(t.TempDir(), "daemon.pid")
	// The launcher backgrounds a long sleep (the "daemon"), writes its pid, and exits.
	line := "/bin/sh -c 'sleep 100 & echo $! > " + pidfile + "'"
	s := New(Config{
		Name:      "forky.service",
		Type:      TypeForking,
		PIDFile:   pidfile,
		ExecStart: []ExecCommand{cmd(t, line)},
	})
	w := &sinkWriter{}
	s.Stdout = w
	s.Stderr = w

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start (forking): %v", err)
	}
	s.mu.Lock()
	daemon := s.forkedPID
	s.mu.Unlock()
	if daemon <= 0 {
		t.Fatalf("forkedPID not learned from PIDFile (got %d)", daemon)
	}
	if !pidAlive(daemon) {
		t.Fatalf("daemon pid %d should be alive after start", daemon)
	}

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if pidAlive(daemon) {
		t.Fatalf("daemon pid %d still alive after Stop: Stop hit the dead launcher, not the daemon", daemon)
	}
}
