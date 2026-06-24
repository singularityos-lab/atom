package reaper

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Registry routes reaped statuses to per-PID waiters.
type Registry struct {
	mu      sync.Mutex
	waiters map[int]chan syscall.WaitStatus
	pending map[int]pending
}

type pending struct {
	ws syscall.WaitStatus
	at time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		waiters: map[int]chan syscall.WaitStatus{},
		pending: map[int]pending{},
	}
}

// Wait registers interest in pid and returns a one-shot channel for its exit
// status. If the child already exited (its status was delivered before this
// call -- the tiny register-vs-exit window), the buffered status is returned
// immediately.
func (r *Registry) Wait(pid int) <-chan syscall.WaitStatus {
	ch := make(chan syscall.WaitStatus, 1)
	r.mu.Lock()
	if p, ok := r.pending[pid]; ok {
		ch <- p.ws
		delete(r.pending, pid)
	} else {
		r.waiters[pid] = ch
	}
	r.mu.Unlock()
	return ch
}

// Deliver routes a reaped status to its waiter, or buffers it for a waiter that
// has not registered yet.
func (r *Registry) Deliver(pid int, ws syscall.WaitStatus) {
	r.mu.Lock()
	if ch, ok := r.waiters[pid]; ok {
		ch <- ws
		delete(r.waiters, pid)
	} else {
		r.pending[pid] = pending{ws: ws, at: time.Now()}
		r.gcLocked()
	}
	r.mu.Unlock()
}

// gcLocked bounds the pending buffer: statuses no waiter ever claims (true
// orphans) are dropped once they age out.
func (r *Registry) gcLocked() {
	if len(r.pending) < 1024 {
		return
	}
	cutoff := time.Now().Add(-2 * time.Minute)
	for pid, p := range r.pending {
		if p.at.Before(cutoff) {
			delete(r.pending, pid)
		}
	}
}

// Reaper drains exited children on SIGCHLD and routes their statuses through a
// Registry. As PID 1 it collects every orphaned descendant too.
type Reaper struct {
	reg  *Registry
	ch   chan os.Signal
	done chan struct{}
}

// NewReaper installs a SIGCHLD handler routing to reg; call Run in a goroutine.
func NewReaper(reg *Registry) *Reaper {
	r := &Reaper{reg: reg, ch: make(chan os.Signal, 64), done: make(chan struct{})}
	signal.Notify(r.ch, syscall.SIGCHLD)
	return r
}

// Run reaps until Stop is called.
func (r *Reaper) Run() {
	for {
		select {
		case <-r.done:
			return
		case <-r.ch:
			r.drain()
		}
	}
}

func (r *Reaper) drain() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		if pid <= 0 || err != nil {
			return
		}
		r.reg.Deliver(pid, ws)
	}
}

// Stop removes the signal handler and ends Run.
func (r *Reaper) Stop() {
	signal.Stop(r.ch)
	close(r.done)
}

const prSetChildSubreaper = 36

// SetSubreaper marks the current process as a child subreaper.
func SetSubreaper() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
