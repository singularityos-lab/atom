package service

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestShouldRestart(t *testing.T) {
	clean := ExitInfo{Code: 0}
	fail := ExitInfo{Code: 1}
	sig := ExitInfo{Signaled: true, Signal: syscall.SIGTERM}
	wd := ExitInfo{Watchdog: true}

	cases := []struct {
		mode RestartMode
		in   ExitInfo
		want bool
	}{
		{RestartNo, fail, false},
		{RestartAlways, clean, true},
		{RestartAlways, fail, true},
		{RestartOnSuccess, clean, true},
		{RestartOnSuccess, fail, false},
		{RestartOnFailure, fail, true},
		{RestartOnFailure, clean, false},
		{RestartOnFailure, sig, true},
		{RestartOnFailure, wd, true},
		{RestartOnAbnormal, sig, true},
		{RestartOnAbnormal, fail, false},
		{RestartOnWatchdog, wd, true},
		{RestartOnWatchdog, fail, false},
	}
	for _, c := range cases {
		if got := c.mode.ShouldRestart(c.in); got != c.want {
			t.Errorf("mode %d on %+v = %v, want %v", c.mode, c.in, got, c.want)
		}
	}
}

func waitState(t *testing.T, s *Service, want State, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state = %s, want %s within %v", s.State(), want, d)
}

func TestRestartHitsStartLimit(t *testing.T) {
	requireBins(t, "/bin/false")
	s := New(Config{
		Name:               "loop.service",
		Type:               TypeSimple,
		ExecStart:          []ExecCommand{cmd(t, "/bin/false")},
		RestartMode:        RestartOnFailure,
		RestartSec:         10 * time.Millisecond,
		StartLimitInterval: 10 * time.Second,
		StartLimitBurst:    3,
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// It restarts on each failing exit until the burst limit, then fails.
	waitState(t, s, Failed, 3*time.Second)
	if rc := s.RestartCount(); rc < 1 || rc > 3 {
		t.Errorf("restart count = %d, want within [1,3]", rc)
	}
}

func TestNoRestartOnCleanExit(t *testing.T) {
	requireBins(t, "/bin/true")
	s := New(Config{
		Name:        "clean.service",
		Type:        TypeSimple,
		ExecStart:   []ExecCommand{cmd(t, "/bin/true")},
		RestartMode: RestartOnFailure,
		RestartSec:  10 * time.Millisecond,
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, s, Inactive, 2*time.Second)
	if rc := s.RestartCount(); rc != 0 {
		t.Errorf("restart count = %d, want 0 (clean exit, on-failure)", rc)
	}
}

func TestRestartAlwaysRespawns(t *testing.T) {
	requireBins(t, "/bin/sh")
	s := New(Config{
		Name:               "always.service",
		Type:               TypeSimple,
		ExecStart:          []ExecCommand{cmd(t, "/bin/sh -c 'exit 0'")},
		RestartMode:        RestartAlways,
		RestartSec:         5 * time.Millisecond,
		StartLimitInterval: 10 * time.Second,
		StartLimitBurst:    1000,
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.RestartCount() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if s.RestartCount() < 1 {
		t.Errorf("Restart=always did not respawn a cleanly-exiting service")
	}
	_ = s.Stop(context.Background())
}
