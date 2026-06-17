package timer

import (
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func TestNextCalendarKeywords(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	if got := NextCalendar("daily", now); got != time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC) {
		t.Errorf("daily = %v", got)
	}
	if got := NextCalendar("hourly", now); got != time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC) {
		t.Errorf("hourly = %v", got)
	}
	wk := NextCalendar("weekly", now)
	if wk.Weekday() != time.Monday || wk.Hour() != 0 || !wk.After(now) {
		t.Errorf("weekly = %v (want a future Monday 00:00)", wk)
	}
}

func TestNextCalendarExplicit(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if got := NextCalendar("*-*-* 04:00:00", now); got != time.Date(2026, 7, 2, 4, 0, 0, 0, time.UTC) {
		t.Errorf("04:00 = %v, want tomorrow", got)
	}
	if got := NextCalendar("*-*-* 14:30:00", now); got != time.Date(2026, 7, 1, 14, 30, 0, 0, time.UTC) {
		t.Errorf("14:30 = %v, want today", got)
	}
	if got := NextCalendar("nonsense 99:99", now); !got.IsZero() {
		t.Errorf("nonsense should be zero, got %v", got)
	}
}

func TestNextElapseMonotonic(t *testing.T) {
	f, _ := unit.Parse("clean.timer", strings.NewReader("[Timer]\nOnBootSec=15min\nOnUnitActiveSec=1d\n"))
	tm := FromFile(f)
	if tm.Unit != "clean.service" {
		t.Errorf("unit = %q, want clean.service", tm.Unit)
	}
	boot := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	// First elapse, never activated: boot + OnBootSec.
	if got := tm.NextElapse(boot, boot, time.Time{}); got != boot.Add(15*time.Minute) {
		t.Errorf("first elapse = %v, want boot+15min", got)
	}
	// After an activation: lastActivated + OnUnitActiveSec.
	la := boot.Add(20 * time.Minute)
	if got := tm.NextElapse(la, boot, la); got != la.Add(24*time.Hour) {
		t.Errorf("recurring elapse = %v, want lastActivated+1d", got)
	}
}

func TestNextElapseCalendarWins(t *testing.T) {
	f, _ := unit.Parse("fstrim.timer", strings.NewReader("[Timer]\nOnCalendar=weekly\n"))
	tm := FromFile(f)
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	got := tm.NextElapse(now, now, time.Time{})
	if got.Weekday() != time.Monday {
		t.Errorf("fstrim weekly next = %v, want a Monday", got)
	}
}
