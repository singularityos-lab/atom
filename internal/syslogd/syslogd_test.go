package syslogd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

func TestServeWritesDecodedRecord(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "log.sock")
	out := filepath.Join(dir, "messages")
	s, err := New(Config{SocketPath: sock, OutputPath: out, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// PRI 30 = facility 3 (daemon), severity 6 (info).
	if _, err := c.Write([]byte("<30>hello from a service")); err != nil {
		t.Fatal(err)
	}

	var data []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(out)
		if len(data) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := string(data)
	if !strings.Contains(got, "3.6 hello from a service") {
		t.Errorf("record = %q, want decoded priority + message", got)
	}
	if !strings.Contains(got, "2026-07-01T12:00:00Z") {
		t.Errorf("record = %q, want the timestamp", got)
	}
}

func TestFormat(t *testing.T) {
	// A raw message with no <PRI> is preserved.
	if l := format(fixedNow(), []byte("plain message\n")); !strings.HasSuffix(l, "plain message\n") {
		t.Errorf("plain = %q", l)
	}
	// Priority decodes to facility.severity.
	if l := format(fixedNow(), []byte("<13>user notice")); !strings.Contains(l, "1.5 user notice") {
		t.Errorf("pri = %q", l)
	}
}

func TestBoundedFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "m")
	s, err := New(Config{SocketPath: filepath.Join(dir, "s"), OutputPath: out, MaxBytes: 200, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 50; i++ {
		if err := s.write("some log line that is a few dozen bytes long\n"); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := os.Stat(out)
	if st.Size() > 400 {
		t.Errorf("file grew unbounded: %d bytes, cap 200", st.Size())
	}
}

func TestServeTeesToKmsg(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "log.sock")
	out := filepath.Join(dir, "messages")
	kmsg := filepath.Join(dir, "kmsg") // stand-in for /dev/kmsg
	s, err := New(Config{SocketPath: sock, OutputPath: out, KmsgPath: kmsg, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go s.Serve()

	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("<30>service to kmsg")); err != nil {
		t.Fatal(err)
	}

	var kdata []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		kdata, _ = os.ReadFile(kmsg)
		if len(kdata) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(string(kdata), "service to kmsg") {
		t.Errorf("kmsg tee = %q, want the record teed to the ring buffer", string(kdata))
	}
}
