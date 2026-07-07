package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/reaper"
)

// TestNoDoubleStart fires many concurrent Start calls at one service (a boot
// layer racing a control `start`, or two `start` requests) and asserts the
// process is spawned exactly once. Before the fix the start had no atomic
// claim, so every caller spawned its own copy and all but the last were left
// orphaned and unsupervised.
func TestNoDoubleStart(t *testing.T) {
	requireBins(t, "/bin/sh")
	reg := reaper.NewRegistry()
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()

	old := ReaperWait
	ReaperWait = reg.Wait
	defer func() { ReaperWait = old }()

	marker := filepath.Join(t.TempDir(), "spawns")
	s := New(Config{
		Name:      "once.service",
		ExecStart: []ExecCommand{cmd(t, "/bin/sh -c 'echo x >> "+marker+"; sleep 3'")},
	})
	w := &sinkWriter{}
	s.Stdout = w
	s.Stderr = w

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Start(context.Background()) }()
	}
	wg.Wait()

	time.Sleep(300 * time.Millisecond) // let the single spawn write its marker
	data, _ := os.ReadFile(marker)
	n := strings.Count(string(data), "x")
	_ = s.Stop(context.Background())

	if n != 1 {
		t.Fatalf("service spawned %d times under 20 concurrent Start calls, want 1 (double-start race)", n)
	}
}
