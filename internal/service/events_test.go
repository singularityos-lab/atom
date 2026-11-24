package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) UnitFailed(unit string, exit ExitInfo) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf("fail %s %d", unit, exit.Code))
}

func (l *eventLog) UnitRestarted(unit string, count int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf("restart %s %d", unit, count))
}

func (l *eventLog) first(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) < n {
		return nil
	}
	return append([]string(nil), l.events[:n]...)
}

func respawnEvents(t *testing.T, name, script string, n int) []string {
	requireBins(t, "/bin/sh")
	log := &eventLog{}
	Events = log
	t.Cleanup(func() { Events = nil })
	s := New(Config{
		Name:               name,
		Type:               TypeSimple,
		ExecStart:          []ExecCommand{cmd(t, "/bin/sh -c '"+script+"'")},
		RestartMode:        RestartAlways,
		RestartSec:         5 * time.Millisecond,
		StartLimitInterval: 10 * time.Second,
		StartLimitBurst:    1000,
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := log.first(n); got != nil {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("events %v", log.events)
	return nil
}

func TestRestartRecordsWhyTheServiceExited(t *testing.T) {
	got := respawnEvents(t, "crash.service", "exit 3", 4)
	want := []string{"fail crash.service 3", "restart crash.service 1", "fail crash.service 3", "restart crash.service 2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

func TestCleanRestartRecordsNoFailure(t *testing.T) {
	got := respawnEvents(t, "done.service", "exit 0", 2)
	if got[0] != "restart done.service 1" || got[1] != "restart done.service 2" {
		t.Fatalf("events %v", got)
	}
}
