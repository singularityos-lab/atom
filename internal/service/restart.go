package service

import "syscall"

// RestartMode is the systemd Restart= policy.
type RestartMode int

const (
	RestartNo RestartMode = iota
	RestartOnSuccess
	RestartOnFailure
	RestartOnAbnormal
	RestartOnAbort
	RestartOnWatchdog
	RestartAlways
)

func parseRestart(s string) RestartMode {
	switch s {
	case "on-success":
		return RestartOnSuccess
	case "on-failure":
		return RestartOnFailure
	case "on-abnormal":
		return RestartOnAbnormal
	case "on-abort":
		return RestartOnAbort
	case "on-watchdog":
		return RestartOnWatchdog
	case "always":
		return RestartAlways
	default:
		return RestartNo
	}
}

// ExitInfo describes how a service's main process ended.
type ExitInfo struct {
	Code     int
	Signaled bool
	Signal   syscall.Signal
	Watchdog bool // killed because it missed its WatchdogSec deadline
}

// Clean reports a graceful exit (status 0, not signaled).
func (e ExitInfo) Clean() bool { return !e.Signaled && e.Code == 0 }

// ShouldRestart applies the Restart= policy to an exit.
func (m RestartMode) ShouldRestart(e ExitInfo) bool {
	switch m {
	case RestartAlways:
		return true
	case RestartOnSuccess:
		return e.Clean()
	case RestartOnFailure:
		// non-zero exit, signal, or watchdog timeout
		return !e.Clean() || e.Watchdog
	case RestartOnAbnormal:
		return e.Signaled || e.Watchdog
	case RestartOnAbort:
		return e.Signaled && e.Signal == syscall.SIGABRT
	case RestartOnWatchdog:
		return e.Watchdog
	default:
		return false
	}
}
