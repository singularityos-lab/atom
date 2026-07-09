package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startTestBus launches a private dbus-daemon on a short socket path and points
// DBUS_SYSTEM_BUS_ADDRESS at it. Skips if dbus-daemon is not installed.
func startTestBus(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not available")
	}
	sock := filepath.Join("/tmp", "atom-svc-dbus-"+itoa(os.Getpid())+".sock")
	os.Remove(sock)
	conf := filepath.Join(t.TempDir(), "bus.conf")
	os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>unix:path=`+sock+`</listen>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>`), 0o644)
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); os.Remove(sock) })
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+sock)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("dbus-daemon socket never appeared")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A Type=dbus service whose BusName is already owned on the bus becomes active
// (the bus itself always owns org.freedesktop.DBus, so no name-owner helper is
// needed to exercise the "name present" path).
func TestDbusReadinessBecomesActive(t *testing.T) {
	requireBins(t, "/bin/sleep")
	startTestBus(t)
	s := New(Config{
		Name:      "owned.service",
		Type:      TypeDbus,
		BusName:   "org.freedesktop.DBus",
		ExecStart: []ExecCommand{cmd(t, "/bin/sleep 30")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active", s.State())
	}
	_ = s.Stop(context.Background())
}

// A Type=dbus service whose BusName never appears must NOT be killed on timeout
// (sinit has no D-Bus activation to relaunch it; killing a still-running daemon
// like wpa_supplicant would leave the machine worse off). It proceeds to Active
// with the process still alive.
func TestDbusReadinessTimesOutKeepsProcessAlive(t *testing.T) {
	requireBins(t, "/bin/sleep")
	startTestBus(t)
	s := New(Config{
		Name:            "absent.service",
		Type:            TypeDbus,
		BusName:         "com.sinty.definitely.absent",
		TimeoutStartSec: 800 * time.Millisecond,
		ExecStart:       []ExecCommand{cmd(t, "/bin/sleep 30")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v; want nil (proceed, do not fail/kill)", err)
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active (process kept alive on timeout)", s.State())
	}
	// The daemon must still be running, not killed.
	if s.State() == Failed {
		t.Error("service was failed/killed on timeout; must stay alive")
	}
	_ = s.Stop(context.Background())
}

// Type=dbus without a BusName= degrades to simple readiness (active on spawn),
// so nothing regresses for units that set Type=dbus but declare no name.
func TestDbusReadinessNoBusNameDegrades(t *testing.T) {
	requireBins(t, "/bin/sleep")
	s := New(Config{
		Name:      "noname.service",
		Type:      TypeDbus,
		ExecStart: []ExecCommand{cmd(t, "/bin/sleep 30")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active", s.State())
	}
	_ = s.Stop(context.Background())
}
