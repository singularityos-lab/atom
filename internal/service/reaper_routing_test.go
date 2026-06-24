package service

import (
	"context"
	"testing"

	"github.com/singularityos-lab/atom/internal/reaper"
)

// TestServiceUsesReaperRouting runs a oneshot with a global reaper active and the
// ReaperWait hook set, proving the start path gets the exit status via routing
// instead of racing the reaper (the "waitid: no child processes" bug).
func TestServiceUsesReaperRouting(t *testing.T) {
	requireBins(t, "/bin/true")
	reg := reaper.NewRegistry()
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()

	old := ReaperWait
	ReaperWait = reg.Wait
	defer func() { ReaperWait = old }()

	s := New(Config{
		Name:            "routed.service",
		Type:            TypeOneshot,
		RemainAfterExit: true,
		ExecStart:       []ExecCommand{cmd(t, "/bin/true")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start under reaper routing: %v", err)
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active", s.State())
	}
}
