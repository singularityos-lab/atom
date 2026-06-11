package service

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestWatchdogRestartsOnMiss(t *testing.T) {
	requireBins(t, "/bin/sleep")
	rt := t.TempDir()
	s := New(Config{
		Name:               "wd.service",
		Type:               TypeNotify,
		RuntimeDir:         rt,
		ExecStart:          []ExecCommand{cmd(t, "/bin/sleep 30")},
		WatchdogSec:        150 * time.Millisecond,
		RestartMode:        RestartOnWatchdog,
		RestartSec:         10 * time.Millisecond,
		StartLimitInterval: 10 * time.Second,
		StartLimitBurst:    1000,
	})

	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()

	// Wait for the notify socket, then send READY=1 so it goes active. After
	// that we deliberately send NO watchdog pings, so the deadline is missed.
	sock := s.notifyPath()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatalf("dial notify: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1")); err != nil {
		t.Fatalf("write READY: %v", err)
	}
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The missed watchdog must kill the instance, and Restart=on-watchdog must
	// bring it back at least once.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.RestartCount() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if s.RestartCount() < 1 {
		t.Errorf("watchdog miss did not trigger a restart (count=%d)", s.RestartCount())
	}
	_ = s.Stop(context.Background())
}
