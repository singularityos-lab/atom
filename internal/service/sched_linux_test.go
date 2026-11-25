package service

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/unit"
)

func TestSchedDirectivesParse(t *testing.T) {
	f, err := unit.Parse("bg.service", strings.NewReader("[Service]\nExecStart=/bin/true\nNice=10\nCPUSchedulingPolicy=batch\nIOSchedulingClass=idle\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := ConfigFromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sched.Nice == nil || *c.Sched.Nice != 10 || c.Sched.Policy != 3 || c.Sched.IOClass != 3 {
		t.Fatalf("sched %+v", c.Sched)
	}
	for _, bad := range []string{"Nice=30", "CPUSchedulingPolicy=turbo", "IOSchedulingClass=fast"} {
		f, _ := unit.Parse("bad.service", strings.NewReader("[Service]\nExecStart=/bin/true\n"+bad+"\n"))
		if _, err := ConfigFromFile(f); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestSchedReachesTheChild(t *testing.T) {
	nice := 7
	s := New(Config{
		Name:      "bg.service",
		Type:      TypeSimple,
		ExecStart: []ExecCommand{cmd(t, "/bin/sleep 5")},
		Sched:     Sched{Nice: &nice, Policy: 3, IOClass: 3},
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(context.Background())
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		s.mu.Lock()
		if s.main != nil && s.main.Process != nil {
			pid = s.main.Process.Pid
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("no main process")
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
	if fields[16] != "7" {
		t.Fatalf("child nice %s, want 7", fields[16])
	}
	if fields[38] != "3" {
		t.Fatalf("child policy %s, want SCHED_BATCH", fields[38])
	}
	r, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(pid), 0)
	if errno != 0 || int(r)>>ioprioClassShift != 3 {
		t.Fatalf("child ioprio %#x (%v), want idle class", r, errno)
	}
	self, _ := readThreadSched(syscall.Gettid())
	if self.policy != 0 {
		t.Fatalf("parent thread policy leaked: %d", self.policy)
	}
}
