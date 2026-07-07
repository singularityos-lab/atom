package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/reaper"
)

// sinkWriter is a non-*os.File io.Writer, like the logd ring the manager wires in.
// os/exec creates a real OS pipe for such a writer, so it exercises the pipe path.
type sinkWriter struct{ n int }

func (w *sinkWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }

func openFDCount(t *testing.T) int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot read /proc/self/fd: %v", err)
	}
	return len(ents)
}

// TestNoFDLeakUnderReaper spawns many short-lived processes through the reaper
// path (ReaperWait set, so Cmd.Wait is never called) with a non-*os.File log
// writer and asserts the process fd table does not grow. Before the fix,
// os/exec's stdout/stderr pipes were closed only by Cmd.Wait, so every
// spawn leaked two fds until a GC finalizer ran -- a Restart=always crash-loop
// eventually hit EMFILE.
func TestNoFDLeakUnderReaper(t *testing.T) {
	requireBins(t, "/bin/true")
	reg := reaper.NewRegistry()
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()

	old := ReaperWait
	ReaperWait = reg.Wait
	defer func() { ReaperWait = old }()

	w := &sinkWriter{}
	before := openFDCount(t)
	const spawns = 300
	for i := 0; i < spawns; i++ {
		s := New(Config{
			Name:      "leaky.service",
			Type:      TypeOneshot,
			ExecStart: []ExecCommand{cmd(t, "/bin/true")},
		})
		s.Stdout = w
		s.Stderr = w
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("iter %d: Start: %v", i, err)
		}
	}
	// Let the pipe-copier goroutines drain and close their read ends. We do NOT
	// force a GC here: the pre-fix leak is reclaimed only by os.File finalizers, so
	// a GC would mask it -- a plain settle lets the fix's deterministic Close win
	// while leaving a real leak visible.
	time.Sleep(500 * time.Millisecond)

	after := openFDCount(t)
	if after-before > 50 {
		t.Fatalf("fd table grew by %d over %d spawns (before=%d after=%d): stdout/stderr pipes leaked",
			after-before, spawns, before, after)
	}
}
