package logkeep

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(path); strings.Contains(string(b), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	t.Fatalf("%s: want %q, have %q", path, want, b)
}

func TestCopiesBothSourcesAndRotates(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	run := filepath.Join(root, "run", "singularity")
	kmsg := filepath.Join(root, "kmsg")
	if err := syscall.Mkfifo(kmsg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "kernel.0.log"), []byte("previous boot\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(kmsg, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cfg := Config{Dir: keep, Keep: 3, Cap: 1 << 20, Mark: filepath.Join(root, "mark"), Kmsg: kmsg, Compositor: filepath.Join(run, "labwc.log")}
	go func() { _ = Run(cfg) }()

	if _, err := w.WriteString("6,1,0,-;hello kernel\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, filepath.Join(keep, "kernel.0.log"), "hello kernel")
	waitFor(t, filepath.Join(keep, "kernel.1.log"), "previous boot")

	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f, err := os.OpenFile(cfg.Compositor, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString("first line\n")
	waitFor(t, filepath.Join(keep, "labwc.0.log"), "first line")
	_, _ = f.WriteString("second line\n")
	waitFor(t, filepath.Join(keep, "labwc.0.log"), "second line")
	if b, _ := os.ReadFile(filepath.Join(keep, "labwc.0.log")); strings.Count(string(b), "first line") != 1 {
		t.Fatalf("duplicated copy: %q", b)
	}
}

func TestCapStopsCopying(t *testing.T) {
	root := t.TempDir()
	kmsg := filepath.Join(root, "kmsg")
	if err := syscall.Mkfifo(kmsg, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(kmsg, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cfg := Config{Dir: filepath.Join(root, "keep"), Keep: 2, Cap: 8, Mark: filepath.Join(root, "mark"), Kmsg: kmsg, Compositor: filepath.Join(root, "labwc.log")}
	go func() { _ = Run(cfg) }()
	_, _ = w.WriteString("0123456789abcdef\n")
	waitFor(t, filepath.Join(cfg.Dir, "kernel.0.log"), "01234567")
	time.Sleep(100 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(cfg.Dir, "kernel.0.log")); len(b) != 8 {
		t.Fatalf("cap not honoured: %q", b)
	}
}
