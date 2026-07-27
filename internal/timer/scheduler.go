package timer

import (
	"context"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Scheduler arms enabled .timer units: it computes each timer's next elapse and, when
// one comes due, activates its Unit. It runs until ctx is cancelled; an activation
// error is logged and the timer is rescheduled, so one bad unit never stops PID 1.
type Scheduler struct {
	timers   []*Timer
	activate func(name string) error
	logf     func(format string, a ...any)
	bootTime time.Time
	now      func() time.Time
	last     map[string]time.Time
}

// NewScheduler projects the given .timer unit files into a scheduler that fires each
// timer's Unit via activate. bootTime anchors OnBootSec.
func NewScheduler(files []*unit.File, activate func(string) error, logf func(string, ...any), bootTime time.Time) *Scheduler {
	s := &Scheduler{activate: activate, logf: logf, bootTime: bootTime, now: time.Now, last: map[string]time.Time{}}
	for _, f := range files {
		s.timers = append(s.timers, FromFile(f))
	}
	return s
}

// plan returns the timers due at or before now and the earliest wake time strictly
// after now (zero if nothing else is scheduled). It is pure given now and s.last.
func (s *Scheduler) plan(now time.Time) (due []*Timer, next time.Time) {
	for _, t := range s.timers {
		el := t.NextElapse(now, s.bootTime, s.last[t.Name])
		if el.IsZero() {
			continue
		}
		if !el.After(now) {
			due = append(due, t)
			continue
		}
		if next.IsZero() || el.Before(next) {
			next = el
		}
	}
	return due, next
}

// Run arms the timers and blocks until ctx is cancelled, firing each timer's Unit as
// it comes due. It recovers from panics so a scheduling bug can never take down PID 1.
func (s *Scheduler) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil && s.logf != nil {
			s.logf("timer scheduler panic: %v", r)
		}
	}()
	for len(s.timers) > 0 {
		now := s.now()
		due, next := s.plan(now)
		for _, t := range due {
			if err := s.activate(t.Unit); err != nil && s.logf != nil {
				s.logf("timer %s: activate %s: %v", t.Name, t.Unit, err)
			}
			s.last[t.Name] = now
		}
		if len(due) > 0 {
			continue // firing changed s.last; re-plan before sleeping
		}
		if next.IsZero() {
			return // nothing left to fire
		}
		tm := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			tm.Stop()
			return
		case <-tm.C:
		}
	}
}
