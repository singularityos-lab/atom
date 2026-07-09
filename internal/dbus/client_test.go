package dbus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startTestBus launches a private session dbus-daemon on a short socket path and
// returns its address. It skips the test if dbus-daemon is not installed.
func startTestBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not available")
	}
	// Keep the socket path short (AF_UNIX sun_path is ~108 bytes); /tmp, not t.TempDir().
	sock := filepath.Join("/tmp", "atom-dbus-test-"+randSuffix()+".sock")
	conf := filepath.Join(t.TempDir(), "bus.conf")
	os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>unix:path=`+sock+`</listen>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>`), 0o644)

	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); os.Remove(sock) })

	// Wait for the socket to appear.
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			return "unix:path=" + sock
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("dbus-daemon socket never appeared")
	return ""
}

func randSuffix() string {
	// pid-based, no Math.random; unique enough for a single test process.
	return filepathBase(os.Args[0]) + "-" + itoa(os.Getpid())
}
func filepathBase(p string) string { return filepath.Base(p) }
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

func TestNameHasOwner(t *testing.T) {
	addr := startTestBus(t)
	path := parseUnixPath(addr)

	c, err := DialPath(path)
	if err != nil {
		t.Fatalf("DialPath: %v", err)
	}
	defer c.Close()

	// The bus daemon itself always owns org.freedesktop.DBus.
	if ok, err := c.NameHasOwner("org.freedesktop.DBus"); err != nil || !ok {
		t.Errorf("NameHasOwner(org.freedesktop.DBus) = %v, %v; want true, nil", ok, err)
	}
	// A name nobody has claimed.
	if ok, err := c.NameHasOwner("com.sinty.definitely.absent"); err != nil || ok {
		t.Errorf("NameHasOwner(absent) = %v, %v; want false, nil", ok, err)
	}
}

func TestWaitForName(t *testing.T) {
	addr := startTestBus(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)

	// An owned name returns promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitForName(ctx, "org.freedesktop.DBus", 50*time.Millisecond); err != nil {
		t.Errorf("WaitForName(owned) = %v; want nil", err)
	}

	// An unowned name blocks until the deadline.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel2()
	if err := WaitForName(ctx2, "com.sinty.absent", 50*time.Millisecond); err == nil {
		t.Error("WaitForName(absent) = nil; want deadline error")
	}
}
