//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/cgroup"
	"github.com/singularityos-lab/atom/internal/unit"
)

func TestParseLimits(t *testing.T) {
	f, err := unit.Parse("bg.service", strings.NewReader("[Service]\nSlice=background.slice\nCPUWeight=20\nIOWeight=10\nCPUQuota=50%\nMemoryMax=512M\nMemoryHigh=infinity\nTasksMax=64\nExecStart=/bin/true\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ConfigFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	want := cgroup.Limits{CPUWeight: 20, IOWeight: 10, CPUQuotaPercent: 50, MemoryMax: strconv.Itoa(512 << 20), MemoryHigh: "max", TasksMax: "64"}
	if c.Slice != "background.slice" || c.Limits != want {
		t.Fatalf("slice %q limits %+v, want background.slice %+v", c.Slice, c.Limits, want)
	}
	for _, bad := range []string{"Slice=../x.slice", "Slice=system", "CPUWeight=0", "IOWeight=20000", "MemoryMax=lots", "TasksMax=-1"} {
		f, _ := unit.Parse("bad.service", strings.NewReader("[Service]\n"+bad+"\nExecStart=/bin/true\n"))
		if _, err := ConfigFromFile(f); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	f, _ = unit.Parse("plain.service", strings.NewReader("[Service]\nExecStart=/bin/true\n"))
	if c, _ := ConfigFromFile(f); c.Slice != DefaultSlice || c.Limits != (cgroup.Limits{}) {
		t.Fatalf("defaults: slice %q limits %+v", c.Slice, c.Limits)
	}
}

func TestWaitInactiveReturnsWhenTheServiceEnds(t *testing.T) {
	requireBins(t, "/bin/sleep")
	s := New(Config{Name: "short.service", RuntimeDir: t.TempDir(), ExecStart: []ExecCommand{cmd(t, "/bin/sleep 0.3")}})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.WaitInactive(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("WaitInactive still blocked in state %v", s.State())
	}
}

func delegatedCgroup(t *testing.T) *cgroup.Manager {
	t.Helper()
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	own := strings.TrimSpace(strings.TrimPrefix(string(data), "0::"))
	root := filepath.Join("/sys/fs/cgroup", own, fmt.Sprintf("atom-test-%d", os.Getpid()))
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Skipf("no writable cgroup2 here: %v", err)
	}
	m := &cgroup.Manager{Root: root}
	t.Cleanup(func() {
		for _, d := range []string{"test.slice/kids.service", "test.slice", "system.slice"} {
			_ = m.Kill(d)
			for i := 0; i < 100; i++ {
				if os.Remove(m.Abs(d)) == nil || os.IsNotExist(os.Remove(m.Abs(d))) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		_ = os.Remove(root)
	})
	return m
}

func useCgroups(t *testing.T, m *cgroup.Manager) {
	t.Helper()
	old := Cgroups
	Cgroups = m
	slicesMu.Lock()
	slices = map[string]bool{}
	slicesMu.Unlock()
	t.Cleanup(func() { Cgroups = old })
}

func childPid(t *testing.T, path string) int {
	t.Helper()
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the child never wrote its pid")
	return 0
}

func gone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err != nil || strings.Contains(string(b), ") Z ") {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestServiceStartsInItsCgroupAndStopTakesItAll(t *testing.T) {
	requireBins(t, "/bin/sh", "/bin/sleep")
	m := delegatedCgroup(t)
	useCgroups(t, m)
	pidFile := filepath.Join(t.TempDir(), "kid")
	s := New(Config{
		Name:       "kids.service",
		Slice:      "test.slice",
		RuntimeDir: t.TempDir(),
		ExecStart:  []ExecCommand{cmd(t, "/bin/sh -c \"(trap '' TERM; exec /bin/sleep 60) & echo $! > "+pidFile+"; exec /bin/sleep 60\"")},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	kid := childPid(t, pidFile)
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", kid))
	if !strings.HasSuffix(strings.TrimSpace(string(b)), "/test.slice/kids.service") {
		t.Fatalf("child cgroup %q, want .../test.slice/kids.service", strings.TrimSpace(string(b)))
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gone(kid, 3*time.Second) {
		syscall.Kill(kid, syscall.SIGKILL)
		t.Fatal("a child that ignores SIGTERM outlived the stop")
	}
	if _, err := os.Stat(m.Abs("test.slice/kids.service")); !os.IsNotExist(err) {
		t.Errorf("the service cgroup was left behind: %v", err)
	}
}

func TestRestartClearsWhatTheLastInstanceLeft(t *testing.T) {
	requireBins(t, "/bin/sh", "/bin/sleep")
	m := delegatedCgroup(t)
	useCgroups(t, m)
	dir := t.TempDir()
	s := New(Config{
		Name:               "kids.service",
		Slice:              "test.slice",
		RuntimeDir:         t.TempDir(),
		ExecStart:          []ExecCommand{cmd(t, "/bin/sh -c \"/bin/sleep 60 & echo $! > "+dir+"/kid.$$; exec /bin/sleep 60\"")},
		RestartMode:        RestartAlways,
		RestartSec:         20 * time.Millisecond,
		StartLimitInterval: 10 * time.Second,
		StartLimitBurst:    100,
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	var first string
	for i := 0; i < 200 && first == ""; i++ {
		matches, _ := filepath.Glob(filepath.Join(dir, "kid.*"))
		if len(matches) > 0 {
			first = matches[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	kid := childPid(t, first)
	s.mu.Lock()
	main := s.main.Process.Pid
	s.mu.Unlock()
	syscall.Kill(main, syscall.SIGKILL)
	if !gone(kid, 3*time.Second) {
		syscall.Kill(kid, syscall.SIGKILL)
		t.Fatal("the restart left the previous instance's child running")
	}
}

func TestProbeCgroups(t *testing.T) {
	requireBins(t, "/bin/true")
	if err := ProbeCgroups(&cgroup.Manager{Root: t.TempDir()}, "/bin/true"); err == nil {
		t.Error("the probe passed on a directory that is not a cgroup")
	}
	if err := ProbeCgroups(delegatedCgroup(t), "/bin/true"); err != nil {
		t.Errorf("the probe failed on a real cgroup: %v", err)
	}
}

func TestUnusableCgroupFallsBackToAPlainStart(t *testing.T) {
	requireBins(t, "/bin/true")
	useCgroups(t, &cgroup.Manager{Root: t.TempDir()})
	s := New(Config{Name: "plain.service", RuntimeDir: t.TempDir(), ExecStart: []ExecCommand{cmd(t, "/bin/true")}, Type: TypeOneshot})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start with an unusable cgroup tree: %v", err)
	}
	if Cgroups != nil {
		t.Fatal("cgroups stayed on after a failed start into one")
	}
}
