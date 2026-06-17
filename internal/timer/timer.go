package timer

import (
	"strconv"
	"strings"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Timer is the schedule-relevant projection of a .timer unit.
type Timer struct {
	Name string
	// Unit is the unit activated when the timer elapses (default: prefix.service).
	Unit string

	OnBootSec         time.Duration
	OnActiveSec       time.Duration
	OnUnitActiveSec   time.Duration
	OnUnitInactiveSec time.Duration
	OnCalendar        []string

	Persistent      bool
	RandomizedDelay time.Duration
}

// FromFile projects a parsed .timer unit.
func FromFile(f *unit.File) *Timer {
	return &Timer{
		Name:              f.Name,
		Unit:              f.GetDefault("Timer", "Unit", f.Prefix+".service"),
		OnBootSec:         f.Duration("Timer", "OnBootSec", 0),
		OnActiveSec:       f.Duration("Timer", "OnActiveSec", 0),
		OnUnitActiveSec:   f.Duration("Timer", "OnUnitActiveSec", 0),
		OnUnitInactiveSec: f.Duration("Timer", "OnUnitInactiveSec", 0),
		OnCalendar:        f.List("Timer", "OnCalendar"),
		Persistent:        f.Bool("Timer", "Persistent", false),
		RandomizedDelay:   f.Duration("Timer", "RandomizedDelaySec", 0),
	}
}

// NextElapse returns the earliest time the timer should fire after now, given
// the boot time and the last time the timer's own unit was activated (zero if
// never). It returns the zero time if the timer has no schedule. Jitter
// (RandomizedDelaySec) is applied by the live scheduler, not here, so this stays
// deterministic.
func (t *Timer) NextElapse(now, bootTime, lastActivated time.Time) time.Time {
	var best time.Time
	consider := func(c time.Time) {
		if c.IsZero() {
			return
		}
		if best.IsZero() || c.Before(best) {
			best = c
		}
	}

	if lastActivated.IsZero() {
		// First elapse, relative to boot.
		if t.OnBootSec > 0 {
			consider(bootTime.Add(t.OnBootSec))
		}
		if t.OnActiveSec > 0 {
			consider(bootTime.Add(t.OnActiveSec))
		}
	} else {
		if t.OnUnitActiveSec > 0 {
			consider(lastActivated.Add(t.OnUnitActiveSec))
		}
	}

	for _, spec := range t.OnCalendar {
		consider(NextCalendar(spec, now))
	}
	return best
}

// NextCalendar returns the next time at or after now matching an OnCalendar
// expression. It supports the named shorthands (minutely/hourly/daily/weekly/
// monthly/yearly) and the "[DOW] *-*-* HH:MM:SS" form. Unsupported expressions
// return the zero time.
func NextCalendar(spec string, now time.Time) time.Time {
	spec = strings.TrimSpace(spec)
	switch strings.ToLower(spec) {
	case "minutely":
		return now.Truncate(time.Minute).Add(time.Minute)
	case "hourly":
		return atClock(now, now.Hour()+1, 0, 0)
	case "daily", "midnight":
		return nextDaily(now, 0, 0, 0)
	case "weekly":
		return nextWeekday(now, time.Monday, 0, 0, 0)
	case "monthly":
		return nextMonthly(now, 1, 0, 0, 0)
	case "yearly", "annually":
		return nextYearly(now)
	}

	// "[DOW] *-*-* HH:MM:SS" -- only the all-dates pattern "*-*-*" is supported.
	fields := strings.Fields(spec)
	var dow = -1
	var clock string
	switch len(fields) {
	case 2: // "*-*-* HH:MM:SS"
		if fields[0] != "*-*-*" {
			return time.Time{}
		}
		clock = fields[1]
	case 3: // "Mon *-*-* HH:MM:SS"
		wd, ok := weekday(fields[0])
		if !ok || fields[1] != "*-*-*" {
			return time.Time{}
		}
		dow = int(wd)
		clock = fields[2]
	default:
		return time.Time{}
	}
	h, m, s, ok := parseClock(clock)
	if !ok {
		return time.Time{}
	}
	if dow >= 0 {
		return nextWeekday(now, time.Weekday(dow), h, m, s)
	}
	return nextDaily(now, h, m, s)
}

func atClock(now time.Time, h, m, s int) time.Time {
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
}

func nextDaily(now time.Time, h, m, s int) time.Time {
	c := atClock(now, h, m, s)
	if !c.After(now) {
		c = c.AddDate(0, 0, 1)
	}
	return c
}

func nextWeekday(now time.Time, wd time.Weekday, h, m, s int) time.Time {
	c := atClock(now, h, m, s)
	days := (int(wd) - int(now.Weekday()) + 7) % 7
	c = c.AddDate(0, 0, days)
	if !c.After(now) {
		c = c.AddDate(0, 0, 7)
	}
	return c
}

func nextMonthly(now time.Time, day, h, m, s int) time.Time {
	c := time.Date(now.Year(), now.Month(), day, h, m, s, 0, now.Location())
	if !c.After(now) {
		c = c.AddDate(0, 1, 0)
	}
	return c
}

func nextYearly(now time.Time) time.Time {
	c := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, now.Location())
	if !c.After(now) {
		c = c.AddDate(1, 0, 0)
	}
	return c
}

func parseClock(s string) (h, m, sec int, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	var err error
	if h, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, 0, false
	}
	if m, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, 0, false
	}
	if len(parts) == 3 {
		if sec, err = strconv.Atoi(parts[2]); err != nil {
			return 0, 0, 0, false
		}
	}
	if h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return 0, 0, 0, false
	}
	return h, m, sec, true
}

func weekday(s string) (time.Weekday, bool) {
	switch strings.ToLower(s) {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	}
	return 0, false
}
