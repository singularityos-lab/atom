package service

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func requireBins(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := os.Stat(b); err != nil {
			t.Skipf("missing %s", b)
		}
	}
}

func cmd(t *testing.T, line string) ExecCommand {
	t.Helper()
	ec, err := ParseExecCommand(line)
	if err != nil {
		t.Fatal(err)
	}
	return ec
}

func TestSimpleServiceBecomesActive(t *testing.T) {
	requireBins(t, "/bin/sleep")
	s := New(Config{
		Name:      "sleep.service",
		Type:      TypeSimple,
		ExecStart: []ExecCommand{cmd(t, "/bin/sleep 30")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active", s.State())
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if s.State() != Inactive {
		t.Errorf("state after stop = %s, want inactive", s.State())
	}
}

func TestOneshotInactiveAndRemain(t *testing.T) {
	requireBins(t, "/bin/true")
	plain := New(Config{
		Name: "o.service", Type: TypeOneshot,
		ExecStart: []ExecCommand{cmd(t, "/bin/true")},
	})
	if err := plain.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if plain.State() != Inactive {
		t.Errorf("oneshot without RemainAfterExit = %s, want inactive", plain.State())
	}

	remain := New(Config{
		Name: "o2.service", Type: TypeOneshot, RemainAfterExit: true,
		ExecStart: []ExecCommand{cmd(t, "/bin/true")},
	})
	if err := remain.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if remain.State() != Active {
		t.Errorf("oneshot RemainAfterExit = %s, want active", remain.State())
	}
}

func TestOneshotFailure(t *testing.T) {
	requireBins(t, "/bin/false")
	s := New(Config{
		Name: "f.service", Type: TypeOneshot,
		ExecStart: []ExecCommand{cmd(t, "/bin/false")},
	})
	if err := s.Start(context.Background()); err == nil {
		t.Error("expected error from failing oneshot")
	}
	if s.State() != Failed {
		t.Errorf("state = %s, want failed", s.State())
	}
}

func TestExecStartPreAborts(t *testing.T) {
	requireBins(t, "/bin/false", "/bin/true")
	s := New(Config{
		Name: "pre.service", Type: TypeOneshot,
		ExecStartPre: []ExecCommand{cmd(t, "/bin/false")},
		ExecStart:    []ExecCommand{cmd(t, "/bin/true")},
	})
	if err := s.Start(context.Background()); err == nil {
		t.Error("ExecStartPre failure should abort start")
	}
	if s.State() != Failed {
		t.Errorf("state = %s, want failed", s.State())
	}
}

func TestNotifyReadinessGate(t *testing.T) {
	requireBins(t, "/bin/sleep")
	rt := t.TempDir()
	s := New(Config{
		Name:       "n.service",
		Type:       TypeNotify,
		RuntimeDir: rt,
		ExecStart:  []ExecCommand{cmd(t, "/bin/sleep 30")},
	})

	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()

	// Wait for the notify socket to appear, then act as the service and send
	// READY=1, which must unblock Start and flip the service to active.
	sock := s.notifyPath()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("notify socket never created: %v", err)
	}

	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatalf("dial notify: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1")); err != nil {
		t.Fatalf("write READY: %v", err)
	}

	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start returned error after READY=1: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after READY=1")
	}
	if s.State() != Active {
		t.Errorf("state = %s, want active", s.State())
	}
	_ = s.Stop(context.Background())
}

func TestStartTimeoutOnStuckNotify(t *testing.T) {
	requireBins(t, "/bin/sleep")
	rt := t.TempDir()
	s := New(Config{
		Name:            "stuck.service",
		Type:            TypeNotify,
		RuntimeDir:      rt,
		ExecStart:       []ExecCommand{cmd(t, "/bin/sleep 30")},
		TimeoutStartSec: 200 * time.Millisecond,
	})
	begin := time.Now()
	err := s.Start(context.Background())
	if err == nil {
		t.Fatal("expected a start-timeout error from a notify service that never sends READY")
	}
	if s.State() != Failed {
		t.Errorf("state = %s, want failed", s.State())
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("start took %v, timeout should have fired near 200ms", d)
	}
}

func TestExecStartPreTimeout(t *testing.T) {
	requireBins(t, "/bin/sleep", "/bin/true")
	// A hanging ExecStartPre must be bounded by TimeoutStartSec -- this is the
	// wait path that previously ran on an unbounded context and wedged the boot.
	s := New(Config{
		Name:            "prehang.service",
		Type:            TypeOneshot,
		ExecStartPre:    []ExecCommand{cmd(t, "/bin/sleep 30")},
		ExecStart:       []ExecCommand{cmd(t, "/bin/true")},
		TimeoutStartSec: 200 * time.Millisecond,
	})
	begin := time.Now()
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("a hanging ExecStartPre must time out, not block forever")
	}
	if s.State() != Failed {
		t.Errorf("state = %s, want failed", s.State())
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Errorf("ExecStartPre timeout took %v, expected ~200ms", d)
	}
}

func TestUserMissingFailsFast(t *testing.T) {
	requireBins(t, "/bin/true")
	// A User= naming an account that does not exist must fail the unit fast
	// (systemd resolves its own users via nss-systemd; atom does not, so an
	// unresolvable User= is an error, not a silent run-as-root).
	s := New(Config{
		Name:      "u.service",
		Type:      TypeOneshot,
		ExecStart: []ExecCommand{cmd(t, "/bin/true")},
		User:      "atom-no-such-user-zzz",
	})
	begin := time.Now()
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("a missing User= must fail the unit")
	}
	if s.State() != Failed {
		t.Errorf("state = %s, want failed", s.State())
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("missing User= took %v, must fail fast", d)
	}
}

func TestConfigFromFile(t *testing.T) {
	src := `[Service]
Type=notify
ExecStartPre=/bin/mkdir -p /run/demo
ExecStart=/usr/bin/demo --serve
Environment=FOO=bar BAZ=qux
RemainAfterExit=no
`
	f, err := unit.Parse("demo.service", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ConfigFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != TypeNotify {
		t.Errorf("type = %s, want notify", c.Type)
	}
	if len(c.ExecStart) != 1 || c.ExecStart[0].Path() != "/usr/bin/demo" {
		t.Errorf("ExecStart = %+v", c.ExecStart)
	}
	if len(c.ExecStartPre) != 1 || c.ExecStartPre[0].Path() != "/bin/mkdir" {
		t.Errorf("ExecStartPre = %+v", c.ExecStartPre)
	}
	if strings.Join(c.Environment, ",") != "FOO=bar,BAZ=qux" {
		t.Errorf("Environment = %v", c.Environment)
	}
}
