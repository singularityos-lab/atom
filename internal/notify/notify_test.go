package notify

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMessage(t *testing.T) {
	m := ParseMessage([]byte("READY=1\nSTATUS=serving\nMAINPID=1234\nWATCHDOG=1\n"))
	if !m.Ready || m.Status != "serving" || !m.HasMainPID || m.MainPID != 1234 || !m.Watchdog {
		t.Fatalf("decoded wrong: %+v", m)
	}
	if t2 := ParseMessage([]byte("WATCHDOG=trigger")); !t2.WatchdogTrigger || t2.Watchdog {
		t.Errorf("watchdog trigger decoded wrong: %+v", t2)
	}
	if s := ParseMessage([]byte("STOPPING=1\nERRNO=19")); !s.Stopping || s.Errno != 19 {
		t.Errorf("stopping/errno wrong: %+v", s)
	}
	if r := ParseMessage([]byte("X-CUSTOM=hello")); r.Raw["X-CUSTOM"] != "hello" {
		t.Errorf("unknown key not preserved: %+v", r.Raw)
	}
}

func TestListenerSocketWorldWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.sock")
	l, err := NewListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o002 == 0 {
		t.Errorf("notify socket perms = %o, want world-writable so a privilege-dropped service (e.g. dbus) can send READY", st.Mode().Perm())
	}
}

func TestListenerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	l, err := NewListener(path)
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	defer l.Close()

	got := make(chan Received, 1)
	go func() {
		r, err := l.Receive()
		if err == nil {
			got <- r
		}
	}()

	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1\nSTATUS=up")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case r := <-got:
		if !r.Msg.Ready || r.Msg.Status != "up" {
			t.Errorf("message wrong: %+v", r.Msg)
		}
		// Credentials are best-effort; if present, the PID must be ours.
		if r.HasCred && r.PID != os.Getpid() {
			t.Errorf("peer pid = %d, want %d", r.PID, os.Getpid())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for notify message")
	}
}
