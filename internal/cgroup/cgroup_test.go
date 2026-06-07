package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAddProcAndRead(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	rel := "system.slice/foo.service"
	if err := m.Create(rel); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st, err := os.Stat(m.Abs(rel)); err != nil || !st.IsDir() {
		t.Fatalf("cgroup dir missing: %v", err)
	}
	// cgroup.procs does not exist until written in our fake fs; AddProc writes it.
	if err := m.AddProc(rel, 4242); err != nil {
		t.Fatalf("AddProc: %v", err)
	}
	pids, err := m.Procs(rel)
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	if len(pids) != 1 || pids[0] != 4242 {
		t.Errorf("Procs = %v, want [4242]", pids)
	}
}

func TestPopulated(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	rel := "system.slice/bar.service"
	if err := m.Create(rel); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(m.Abs(rel), "cgroup.events"), "populated 0\nfrozen 0\n")
	if pop, _ := m.Populated(rel); pop {
		t.Error("should be unpopulated")
	}
	write(t, filepath.Join(m.Abs(rel), "cgroup.events"), "populated 1\nfrozen 0\n")
	if pop, _ := m.Populated(rel); !pop {
		t.Error("should be populated")
	}
}

func TestApplyLimits(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	rel := "system.slice/lim.service"
	if err := m.Create(rel); err != nil {
		t.Fatal(err)
	}
	err := m.Apply(rel, Limits{
		MemoryMax:       "536870912",
		CPUQuotaPercent: 20,
		TasksMax:        "100",
		CPUWeight:       50,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, m.Abs(rel), "memory.max"); got != "536870912" {
		t.Errorf("memory.max = %q", got)
	}
	// 20% of a 100000us period => 20000us quota.
	if got := read(t, m.Abs(rel), "cpu.max"); got != "20000 100000" {
		t.Errorf("cpu.max = %q, want '20000 100000'", got)
	}
	if got := read(t, m.Abs(rel), "pids.max"); got != "100" {
		t.Errorf("pids.max = %q", got)
	}
	if got := read(t, m.Abs(rel), "cpu.weight"); got != "50" {
		t.Errorf("cpu.weight = %q", got)
	}
}

func TestEnableControllersAndKill(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.Create("system.slice"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableControllers("system.slice", "memory", "pids", "cpu"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, m.Abs("system.slice"), "cgroup.subtree_control"); got != "+memory +pids +cpu" {
		t.Errorf("subtree_control = %q", got)
	}
	if err := m.Create("system.slice/k.service"); err != nil {
		t.Fatal(err)
	}
	if err := m.Kill("system.slice/k.service"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, m.Abs("system.slice/k.service"), "cgroup.kill"); got != "1" {
		t.Errorf("cgroup.kill = %q", got)
	}
}

func TestRemove(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.Create("system.slice/gone.service"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("system.slice/gone.service"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(m.Abs("system.slice/gone.service")); !os.IsNotExist(err) {
		t.Error("cgroup dir should be gone")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, file string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
