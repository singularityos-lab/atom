package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Type is the service start-up/readiness model (systemd Type=).
type Type int

const (
	TypeSimple Type = iota
	TypeExec
	TypeOneshot
	TypeForking
	TypeNotify
	TypeDbus
	TypeIdle
)

func (t Type) String() string {
	switch t {
	case TypeSimple:
		return "simple"
	case TypeExec:
		return "exec"
	case TypeOneshot:
		return "oneshot"
	case TypeForking:
		return "forking"
	case TypeNotify:
		return "notify"
	case TypeDbus:
		return "dbus"
	case TypeIdle:
		return "idle"
	default:
		return "simple"
	}
}

func parseType(s string) Type {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "exec":
		return TypeExec
	case "oneshot":
		return TypeOneshot
	case "forking":
		return TypeForking
	case "notify":
		return TypeNotify
	case "dbus":
		return TypeDbus
	case "idle":
		return TypeIdle
	default:
		return TypeSimple
	}
}

// Config is the runtime projection of a [Service] unit.
type Config struct {
	Name string
	Type Type

	ConditionPathExists []string

	ExecStartPre  []ExecCommand
	ExecStart     []ExecCommand
	ExecStartPost []ExecCommand
	ExecStop      []ExecCommand
	ExecStopPost  []ExecCommand

	Environment      []string
	EnvironmentFiles []string
	WorkingDir       string
	RemainAfterExit  bool
	NotifyAccess     string
	PIDFile          string
	BusName          string // Type=dbus readiness: the well-known name to wait for

	// Credentials and managed directories.
	User             string
	Group            string
	RuntimeDirectory []string // created under /run, chown to User
	StateDirectory   []string // created under /var/lib, chown to User

	// Restart policy and rate limiting.
	RestartMode        RestartMode
	RestartSec         time.Duration
	StartLimitInterval time.Duration
	StartLimitBurst    int

	// WatchdogSec: if > 0, a Type=notify service must send WATCHDOG=1 within
	// this interval or it is considered hung and killed (then restarted per
	// Restart=on-watchdog/on-failure).
	WatchdogSec time.Duration

	// TimeoutStartSec bounds how long a start may take to reach readiness
	// (oneshot completion, notify READY=1, forking launcher exit) before the
	// unit is killed and marked failed. 0 means use DefaultStartTimeout.
	TimeoutStartSec time.Duration

	// RuntimeDir is the base for the per-service notify socket (default
	// /run/atom). Overridable so the runtime is testable in a temp dir.
	RuntimeDir string

	// Sched holds the CPU and I/O priorities every process of the unit
	// starts with.
	Sched Sched
}

// Sched mirrors Nice=, CPUSchedulingPolicy=, CPUSchedulingPriority=,
// IOSchedulingClass= and IOSchedulingPriority=. Negative class and policy
// values mean unset.
type Sched struct {
	Nice       *int
	Policy     int
	Priority   int
	IOClass    int
	IOPriority int
}

func (s Sched) set() bool { return s.Nice != nil || s.Policy >= 0 || s.IOClass >= 0 }

func parseSched(f *unit.File) (Sched, error) {
	sc := Sched{Policy: -1, IOClass: -1}
	if v, ok := f.Get("Service", "Nice"); ok && v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < -20 || n > 19 {
			return sc, fmt.Errorf("Nice=%s: want -20..19", v)
		}
		sc.Nice = &n
	}
	if v, ok := f.Get("Service", "CPUSchedulingPolicy"); ok && v != "" {
		policies := map[string]int{"other": 0, "fifo": 1, "rr": 2, "batch": 3, "idle": 5}
		p, known := policies[strings.TrimSpace(v)]
		if !known {
			return sc, fmt.Errorf("CPUSchedulingPolicy=%s: unknown policy", v)
		}
		sc.Policy = p
		if p == 1 || p == 2 {
			prio := int(f.Int("Service", "CPUSchedulingPriority", 1))
			if prio < 1 || prio > 99 {
				return sc, fmt.Errorf("CPUSchedulingPriority=%d: want 1..99", prio)
			}
			sc.Priority = prio
		}
	}
	if v, ok := f.Get("Service", "IOSchedulingClass"); ok && v != "" {
		classes := map[string]int{"none": 0, "0": 0, "realtime": 1, "1": 1, "best-effort": 2, "2": 2, "idle": 3, "3": 3}
		c, known := classes[strings.TrimSpace(v)]
		if !known {
			return sc, fmt.Errorf("IOSchedulingClass=%s: unknown class", v)
		}
		sc.IOClass = c
		prio := int(f.Int("Service", "IOSchedulingPriority", 4))
		if prio < 0 || prio > 7 {
			return sc, fmt.Errorf("IOSchedulingPriority=%d: want 0..7", prio)
		}
		if c == 1 || c == 2 {
			sc.IOPriority = prio
		}
	} else if v, ok := f.Get("Service", "IOSchedulingPriority"); ok && v != "" {
		prio := int(f.Int("Service", "IOSchedulingPriority", 4))
		if prio < 0 || prio > 7 {
			return sc, fmt.Errorf("IOSchedulingPriority=%d: want 0..7", prio)
		}
		sc.IOClass, sc.IOPriority = 2, prio
	}
	return sc, nil
}

// ConfigFromFile projects a parsed unit file into a service Config.
func ConfigFromFile(f *unit.File) (Config, error) {
	c := Config{Name: f.Name, RuntimeDir: "/run/atom"}

	parseList := func(key string) ([]ExecCommand, error) {
		var out []ExecCommand
		for _, line := range f.List("Service", key) {
			ec, err := ParseExecCommand(line)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out = append(out, ec)
		}
		return out, nil
	}

	var err error
	if c.ExecStartPre, err = parseList("ExecStartPre"); err != nil {
		return c, err
	}
	if c.ExecStart, err = parseList("ExecStart"); err != nil {
		return c, err
	}
	if c.ExecStartPost, err = parseList("ExecStartPost"); err != nil {
		return c, err
	}
	if c.ExecStop, err = parseList("ExecStop"); err != nil {
		return c, err
	}
	if c.ExecStopPost, err = parseList("ExecStopPost"); err != nil {
		return c, err
	}

	typeStr, _ := f.Get("Service", "Type")
	c.Type = parseType(typeStr)
	c.ConditionPathExists = f.List("Unit", "ConditionPathExists")

	for _, line := range f.List("Service", "Environment") {
		c.Environment = append(c.Environment, strings.Fields(line)...)
	}
	c.EnvironmentFiles = f.List("Service", "EnvironmentFile")
	c.WorkingDir = f.GetDefault("Service", "WorkingDirectory", "")
	c.User = f.GetDefault("Service", "User", "")
	c.Group = f.GetDefault("Service", "Group", "")
	c.RuntimeDirectory = f.Strv("Service", "RuntimeDirectory")
	c.StateDirectory = f.Strv("Service", "StateDirectory")
	c.RemainAfterExit = f.Bool("Service", "RemainAfterExit", false)
	c.NotifyAccess = f.GetDefault("Service", "NotifyAccess", "main")
	c.PIDFile = f.GetDefault("Service", "PIDFile", "")
	c.BusName = f.GetDefault("Service", "BusName", "")

	c.RestartMode = parseRestart(f.GetDefault("Service", "Restart", "no"))
	c.RestartSec = f.Duration("Service", "RestartSec", 100*time.Millisecond)
	// StartLimitIntervalSec / StartLimitBurst live in [Unit].
	c.StartLimitInterval = f.Duration("Unit", "StartLimitIntervalSec", 10*time.Second)
	c.StartLimitBurst = int(f.Int("Unit", "StartLimitBurst", 5))
	c.WatchdogSec = f.Duration("Service", "WatchdogSec", 0)
	// TimeoutStartSec, falling back to TimeoutSec (which sets both start+stop).
	c.TimeoutStartSec = f.Duration("Service", "TimeoutStartSec", f.Duration("Service", "TimeoutSec", 0))
	if c.Sched, err = parseSched(f); err != nil {
		return c, err
	}
	return c, nil
}
