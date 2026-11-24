package pid1

import (
	"hash/fnv"
	"sync"
	"syscall"

	"github.com/mirkobrombin/atomloops/sealedlog"

	"github.com/singularityos-lab/atom/internal/service"
)

const sealedQueue = 64

type sealedEvent struct {
	name string
	args []uint64
}

type sealedRecorder struct {
	mu      sync.Mutex
	open    func() (*sealedlog.Writer, error)
	w       *sealedlog.Writer
	pending []sealedEvent
}

func newSealedRecorder(open func() (*sealedlog.Writer, error)) *sealedRecorder {
	return &sealedRecorder{open: open}
}

func (r *sealedRecorder) log(name string, args ...uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == sealedQueue {
		r.pending = r.pending[1:]
	}
	r.pending = append(r.pending, sealedEvent{name, args})
	r.flushLocked()
}

func (r *sealedRecorder) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushLocked()
}

func (r *sealedRecorder) flushLocked() {
	if r.w == nil {
		w, err := r.open()
		if err != nil {
			return
		}
		r.w = w
	}
	for len(r.pending) > 0 {
		e := r.pending[0]
		if err := r.w.Log(e.name, e.args...); err != nil {
			return
		}
		r.pending = r.pending[1:]
	}
}

func unitHash(unit string) uint64 {
	h := fnv.New32a()
	h.Write([]byte(unit))
	return uint64(h.Sum32())
}

func (r *sealedRecorder) UnitFailed(unit string, exit service.ExitInfo) {
	code := uint64(uint32(int32(exit.Code)))
	sig := uint64(0)
	if exit.Signaled {
		sig = uint64(exit.Signal)
	}
	if exit.Watchdog {
		sig = uint64(syscall.SIGABRT) | 1<<16
	}
	r.log("unit-fail", unitHash(unit), code, sig)
}

func (r *sealedRecorder) UnitRestarted(unit string, count int) {
	r.log("unit-restart", unitHash(unit), uint64(count))
}
