package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExitBeforeReadinessFollowsRestartPolicy(t *testing.T) {
	requireBins(t, "/bin/sh")
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+t.TempDir()+"/absent")
	for _, tc := range []struct {
		name    string
		typ     Type
		mode    RestartMode
		restart bool
	}{
		{"dbus on-failure", TypeDbus, RestartOnFailure, true},
		{"notify on-failure", TypeNotify, RestartOnFailure, true},
		{"dbus no", TypeDbus, RestartNo, false},
		{"notify no", TypeNotify, RestartNo, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := os.MkdirTemp("", "rt")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(rt)
			s := New(Config{
				Name:               "early.service",
				Type:               tc.typ,
				BusName:            "com.sinty.early.absent",
				RuntimeDir:         rt,
				ExecStart:          []ExecCommand{cmd(t, "/bin/sh -c 'exit 1'")},
				RestartMode:        tc.mode,
				RestartSec:         10 * time.Millisecond,
				StartLimitInterval: 10 * time.Second,
				StartLimitBurst:    3,
			})
			if err := s.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "exited before") {
				t.Fatalf("Start = %v, want an exit before readiness", err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && (s.RestartCount() < 3 || s.State() != Failed) {
				time.Sleep(10 * time.Millisecond)
			}
			want := 0
			if tc.restart {
				want = 3
			}
			if got := s.RestartCount(); got != want || s.State() != Failed {
				t.Errorf("restarts = %d state = %s, want %d and failed", got, s.State(), want)
			}
		})
	}
}
