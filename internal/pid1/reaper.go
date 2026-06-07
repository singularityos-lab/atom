package pid1

import (
	"os"
	"os/signal"
	"syscall"
)

// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER; orphaned descendants reparent
// to a process that sets it (PID 1 reaps regardless, but this lets the reaper
// be exercised outside PID 1).
const prSetChildSubreaper = 36

// ReapFunc is called for each child collected by the reaper.
type ReapFunc func(pid int, status syscall.WaitStatus)

// Reaper drains exited children on SIGCHLD via Wait4(-1). As PID 1 it collects
// every orphaned descendant reparented to it -- the duty pure shell does badly.
type Reaper struct {
	ch     chan os.Signal
	onReap ReapFunc
	done   chan struct{}
}

// NewReaper installs a SIGCHLD handler and returns a reaper; call Run in a
// goroutine.
func NewReaper(onReap ReapFunc) *Reaper {
	r := &Reaper{
		ch:     make(chan os.Signal, 32),
		onReap: onReap,
		done:   make(chan struct{}),
	}
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

// drain collects every currently-exited child without blocking.
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
		if r.onReap != nil {
			r.onReap(pid, ws)
		}
	}
}

// Stop removes the signal handler and ends Run.
func (r *Reaper) Stop() {
	signal.Stop(r.ch)
	close(r.done)
}

// SetSubreaper marks the current process as a child subreaper.
func SetSubreaper() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
