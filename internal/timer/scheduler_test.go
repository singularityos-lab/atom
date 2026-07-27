package timer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func mkTimer(t *testing.T, name, body string) *unit.File {
	t.Helper()
	f, err := unit.Parse(name, strings.NewReader("[Timer]\n"+body+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSchedulerPlan(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := NewScheduler([]*unit.File{mkTimer(t, "x.timer", "OnBootSec=3min")}, nil, nil, t0)

	if due, next := s.plan(t0.Add(time.Minute)); len(due) != 0 || !next.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("before elapse: due=%d next=%v want due=0 next=%v", len(due), next, t0.Add(3*time.Minute))
	}
	if due, _ := s.plan(t0.Add(3 * time.Minute)); len(due) != 1 {
		t.Fatalf("at elapse: due=%d want 1", len(due))
	}
}

func TestSchedulerRunFiresOnceForOnBoot(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var fired []string
	s := NewScheduler(
		[]*unit.File{mkTimer(t, "updated-check.timer", "OnBootSec=1ms")},
		func(n string) error { fired = append(fired, n); return nil },
		nil, t0,
	)
	// now is well past boot+OnBootSec, so the timer is due immediately; with only
	// OnBootSec it has no reschedule, so Run fires once and returns.
	s.now = func() time.Time { return t0.Add(time.Hour) }
	s.Run(context.Background())

	if len(fired) != 1 || fired[0] != "updated-check.service" {
		t.Fatalf("fired=%v want [updated-check.service]", fired)
	}
}

func TestSchedulerRunReschedulesRecurring(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var fired int
	s := NewScheduler(
		[]*unit.File{mkTimer(t, "poll.timer", "OnBootSec=1ms\nOnUnitActiveSec=6h")},
		func(string) error { fired++; return nil }, nil, t0,
	)
	// A clock fixed past boot fires OnBootSec once; the next elapse is now+6h (future),
	// so Run must stop firing and wait. Cancel the wait via ctx to keep the test fast.
	s.now = func() time.Time { return t0.Add(time.Hour) }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	s.Run(ctx)

	if fired != 1 {
		t.Fatalf("recurring timer fired %d times, want exactly 1 (then wait, not busy-loop)", fired)
	}
}
