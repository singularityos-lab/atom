package pid1

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func fakeProc(t *testing.T, root string, pid, sid int, comm, cgroup string, anonKB, swapKB int, adj string) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) S 1 %d %d 0 -1 4194560 0 0 0 0", pid, comm, sid, sid)
	files := map[string]string{
		"stat":          stat,
		"status":        fmt.Sprintf("Name:\t%s\nRssAnon:\t%d kB\nVmSwap:\t%d kB\n", comm, anonKB, swapKB),
		"cgroup":        "0::" + cgroup + "\n",
		"oom_score_adj": adj + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPickVictimTakesTheRunawaySessionNotTheDesktop(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()

	apps := "/user.slice/user-1001.slice/app-dev.sinty.leafs.scope/init"
	fakeProc(t, root, 1484, 824, "labwc", "/system.slice/singularity-compositor.service", 900000, 0, "0")
	fakeProc(t, root, 2111, 2087, "singularity-desktop", "/user.slice/user-1001.slice/session-2.scope", 700000, 0, "0")
	fakeProc(t, root, 3170, 2733, "claude", apps, 450000, 0, "0")
	fakeProc(t, root, 4311, 4311, "bash", apps, 4000, 0, "0")
	for i := range 280 {
		fakeProc(t, root, 5000+i, 4311, "raven-bootstrap", apps, 90000, 10000, "0")
	}
	fakeProc(t, root, 900, 900, "sealedlogd", "/system.slice/sealedlogd.service", 5000000, 0, "-1000")

	v, ok := pickVictim(scanProcs())
	if !ok {
		t.Fatal("no victim")
	}
	if v.sid != 4311 || v.leader != "bash" || len(v.pids) != 281 {
		t.Fatalf("victim = session %d leader %q with %d processes", v.sid, v.leader, len(v.pids))
	}
}

func TestPickVictimFallsBackToGuardedWhenNothingElse(t *testing.T) {
	root := t.TempDir()
	old := procRoot
	procRoot = root
	defer func() { procRoot = old }()

	fakeProc(t, root, 700, 700, "leaky", "/system.slice/leaky.service", 8000000, 0, "0")
	fakeProc(t, root, 1484, 824, "labwc", "/system.slice/singularity-compositor.service", 90000, 0, "0")
	fakeProc(t, root, 900, 900, "keeper", "/system.slice/keeper.service", 9000000, 0, "-1000")

	v, ok := pickVictim(scanProcs())
	if !ok || v.leader != "leaky" || len(v.pids) != 1 || v.pids[0] != 700 {
		t.Fatalf("victim = %+v ok=%v", v, ok)
	}
}

func TestMemInfoExhausted(t *testing.T) {
	gb := uint64(1) << 30
	cases := []struct {
		m     memInfo
		stall float64
		want  bool
	}{
		{memInfo{total: 31 * gb, available: 136 << 20, swapTotal: 8 * gb, swapFree: 0}, 0, true},
		{memInfo{total: 31 * gb, available: 136 << 20, swapTotal: 8 * gb, swapFree: 4 * gb}, 0, false},
		{memInfo{total: 31 * gb, available: 136 << 20, swapTotal: 8 * gb, swapFree: 4 * gb}, 0.4, true},
		{memInfo{total: 31 * gb, available: 20 * gb, swapTotal: 8 * gb, swapFree: 0}, 0.9, false},
		{memInfo{total: 4 * gb, available: 200 << 20, swapTotal: 0}, 0, true},
		{memInfo{total: 4 * gb, available: 300 << 20, swapTotal: 0}, 0, false},
	}
	for i, c := range cases {
		if got := c.m.exhausted(c.stall); got != c.want {
			t.Errorf("case %d: exhausted = %v, want %v", i, got, c.want)
		}
	}
}

func TestReadMemInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	body := "MemTotal:       32543740 kB\nMemFree:          139204 kB\nMemAvailable:     150000 kB\nSwapTotal:       8388604 kB\nSwapFree:              0 kB\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readMemInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.total != 32543740<<10 || m.available != 150000<<10 || m.swapTotal != 8388604<<10 || m.swapFree != 0 || !m.exhausted(0) {
		t.Fatalf("meminfo = %+v", m)
	}
}

func TestMemoryGuardLive(t *testing.T) {
	if os.Getenv("MEMGUARD_HOG") == "1" {
		var keep [][]byte
		for {
			b := make([]byte, 8<<20)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1
			}
			keep = append(keep, b)
		}
	}
	if os.Getenv("MEMGUARD_LIVE") != "1" {
		t.Skip("set MEMGUARD_LIVE=1 inside a disposable VM")
	}
	f, err := os.OpenFile(pressureFile, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(pressureTrigger + "\x00")); err != nil {
		t.Fatal(err)
	}
	if err := lockResident(); err != nil {
		t.Fatal(err)
	}
	go runMemoryGuard(f)
	exe, _ := os.Executable()
	var hogs []*exec.Cmd
	for range 4 {
		c := exec.Command(exe, "-test.run", "TestMemoryGuardLive")
		c.Env = append(os.Environ(), "MEMGUARD_HOG=1", "MEMGUARD_LIVE=")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		hogs = append(hogs, c)
	}
	if err := os.WriteFile("/proc/self/oom_score_adj", []byte("-1000"), 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, len(hogs))
	for _, c := range hogs {
		go func() { done <- c.Wait() }()
	}
	deadline := time.After(120 * time.Second)
	for range hogs {
		select {
		case err := <-done:
			t.Logf("hog ended: %v", err)
		case <-deadline:
			t.Fatal("hogs still running after 120s")
		}
	}
}

func TestReadFullStall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory")
	body := "some avg10=1.00 avg60=0.50 avg300=0.10 total=900\nfull avg10=0.50 avg60=0.20 avg300=0.05 total=420\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := readFullStall(path); err != nil || v != 420 {
		t.Fatalf("full stall = %d, %v", v, err)
	}
}
