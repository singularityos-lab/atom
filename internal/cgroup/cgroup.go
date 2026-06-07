package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Manager addresses cgroups relative to a unified hierarchy root.
type Manager struct {
	// Root is the cgroup2 mount point (default /sys/fs/cgroup).
	Root string
}

// New returns a manager rooted at the standard mount point.
func New() *Manager { return &Manager{Root: "/sys/fs/cgroup"} }

func (m *Manager) root() string {
	if m.Root == "" {
		return "/sys/fs/cgroup"
	}
	return m.Root
}

// Abs returns the absolute path of a cgroup given its hierarchy-relative path
// (e.g. "system.slice/foo.service").
func (m *Manager) Abs(rel string) string { return filepath.Join(m.root(), rel) }

// Create makes the cgroup directory (and any missing parents).
func (m *Manager) Create(rel string) error {
	return os.MkdirAll(m.Abs(rel), 0o755)
}

// Remove deletes the (empty) cgroup directory.
func (m *Manager) Remove(rel string) error {
	return os.Remove(m.Abs(rel))
}

// AddProc moves pid into the cgroup by writing cgroup.procs.
func (m *Manager) AddProc(rel string, pid int) error {
	return m.write(rel, "cgroup.procs", strconv.Itoa(pid))
}

// Procs returns the PIDs currently in the cgroup.
func (m *Manager) Procs(rel string) ([]int, error) {
	f, err := os.Open(filepath.Join(m.Abs(rel), "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var pids []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if n, err := strconv.Atoi(strings.TrimSpace(sc.Text())); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, sc.Err()
}

// Populated reports whether the cgroup (or a descendant) still has processes,
// read from cgroup.events ("populated 1"). This is how we learn a service's
// last process exited even if it is not our direct child.
func (m *Manager) Populated(rel string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(m.Abs(rel), "cgroup.events"))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && k == "populated" {
			return v == "1", nil
		}
	}
	return false, nil
}

// Kill terminates every process in the cgroup via the kernel's cgroup.kill
// (single atomic kill of the whole subtree).
func (m *Manager) Kill(rel string) error {
	return m.write(rel, "cgroup.kill", "1")
}

// EnableControllers delegates controllers to children by writing the parent's
// cgroup.subtree_control (e.g. "+memory +pids +cpu").
func (m *Manager) EnableControllers(parentRel string, ctrls ...string) error {
	if len(ctrls) == 0 {
		return nil
	}
	parts := make([]string, len(ctrls))
	for i, c := range ctrls {
		parts[i] = "+" + c
	}
	return m.write(parentRel, "cgroup.subtree_control", strings.Join(parts, " "))
}

// Limits are the cgroup resource controls projected from a unit's [Service] or
// [Slice] directives. Empty/zero fields are left unset.
type Limits struct {
	MemoryMax       string // bytes or "max"
	MemoryHigh      string // bytes or "max"
	CPUQuotaPercent int    // e.g. 50 => 50% of one CPU; 0 = unset
	CPUWeight       int    // 1..10000
	TasksMax        string // number or "max"
	IOWeight        int    // 1..10000
}

const cpuPeriod = 100000

// Apply writes the resource limits into the cgroup's interface files.
func (m *Manager) Apply(rel string, l Limits) error {
	if l.MemoryMax != "" {
		if err := m.write(rel, "memory.max", l.MemoryMax); err != nil {
			return err
		}
	}
	if l.MemoryHigh != "" {
		if err := m.write(rel, "memory.high", l.MemoryHigh); err != nil {
			return err
		}
	}
	if l.CPUQuotaPercent > 0 {
		quota := l.CPUQuotaPercent * cpuPeriod / 100
		if err := m.write(rel, "cpu.max", fmt.Sprintf("%d %d", quota, cpuPeriod)); err != nil {
			return err
		}
	}
	if l.CPUWeight > 0 {
		if err := m.write(rel, "cpu.weight", strconv.Itoa(l.CPUWeight)); err != nil {
			return err
		}
	}
	if l.TasksMax != "" {
		if err := m.write(rel, "pids.max", l.TasksMax); err != nil {
			return err
		}
	}
	if l.IOWeight > 0 {
		if err := m.write(rel, "io.weight", strconv.Itoa(l.IOWeight)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) write(rel, file, content string) error {
	return os.WriteFile(filepath.Join(m.Abs(rel), file), []byte(content), 0o644)
}
