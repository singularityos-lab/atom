package notify

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRealSystemdNotifyBarrier checks that atom satisfies libsystemd's
// sd_notify barrier: systemd-notify --ready must complete quickly and succeed,
// not hang ~5s on an unclosed barrier fd. Skipped where systemd-notify is absent.
func TestRealSystemdNotifyBarrier(t *testing.T) {
	if _, err := exec.LookPath("systemd-notify"); err != nil {
		t.Skip("no systemd-notify")
	}
	path := filepath.Join(t.TempDir(), "n.sock")
	l, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Consume every datagram (the READY message and the separate BARRIER
	// message), as notifyReader does during a real boot.
	go func() {
		for {
			if _, err := l.Receive(); err != nil {
				return
			}
		}
	}()

	c := exec.Command("systemd-notify", "--ready", "--status=up")
	c.Env = append(c.Environ(), "NOTIFY_SOCKET="+path)
	begin := time.Now()
	out, err := c.CombinedOutput()
	elapsed := time.Since(begin)

	if err != nil {
		t.Errorf("systemd-notify failed (barrier not satisfied?): %v, out=%q", err, out)
	}
	if elapsed > 3*time.Second {
		t.Errorf("systemd-notify took %v -- barrier fd still leaking", elapsed)
	}
}
