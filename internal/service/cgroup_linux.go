package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/cgroup"
)

var (
	Cgroups     *cgroup.Manager
	SliceLimits func(slice string) cgroup.Limits

	slicesMu sync.Mutex
	slices   = map[string]bool{}
)

func (s *Service) cgroupPath() string {
	if Cgroups == nil || s.cfg.Name == "" {
		return ""
	}
	slice := s.cfg.Slice
	if slice == "" {
		slice = DefaultSlice
	}
	return slice + "/" + s.cfg.Name
}

func PrepareSlices(names ...string) {
	if Cgroups == nil {
		return
	}
	for _, n := range names {
		if err := ensureSlice(n); err != nil {
			fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", n, err)
		}
	}
}

func ensureSlice(slice string) error {
	slicesMu.Lock()
	defer slicesMu.Unlock()
	if slices[slice] {
		return nil
	}
	if err := Cgroups.Create(slice); err != nil {
		return err
	}
	for _, c := range []string{"cpu", "io", "memory", "pids"} {
		_ = Cgroups.EnableControllers(slice, c)
	}
	if SliceLimits != nil {
		if err := Cgroups.Apply(slice, SliceLimits(slice)); err != nil {
			fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", slice, err)
		}
	}
	slices[slice] = true
	return nil
}

func (s *Service) enterCgroup(cmd *exec.Cmd) func() {
	rel := s.cgroupPath()
	if rel == "" {
		return func() {}
	}
	if err := ensureSlice(s.cfg.Slice); err != nil {
		fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", s.cfg.Name, err)
		return func() {}
	}
	if err := Cgroups.Create(rel); err != nil {
		fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", s.cfg.Name, err)
		return func() {}
	}
	if err := Cgroups.Apply(rel, s.cfg.Limits); err != nil {
		fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", s.cfg.Name, err)
	}
	if _, err := os.Stat(filepath.Join(Cgroups.Abs(rel), "cgroup.procs")); err != nil {
		fmt.Fprintf(os.Stderr, "sinit: %s is not a cgroup, cgroups are off from now on\n", Cgroups.Abs(rel))
		Cgroups = nil
		return func() {}
	}
	fd, err := syscall.Open(Cgroups.Abs(rel), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sinit: %s: %v\n", s.cfg.Name, err)
		return func() {}
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
	return func() { syscall.Close(fd) }
}

func cgroupStartFailed(cmd *exec.Cmd, err error) {
	if err == nil || cmd.SysProcAttr == nil || !cmd.SysProcAttr.UseCgroupFD {
		return
	}
	fmt.Fprintf(os.Stderr, "sinit: starting into a cgroup failed (%v), cgroups are off from now on\n", err)
	Cgroups = nil
}

func ProbeCgroups(m *cgroup.Manager, bin string) error {
	rel := DefaultSlice + "/sinit-probe"
	if err := m.Create(rel); err != nil {
		return err
	}
	defer m.Remove(rel)
	fd, err := syscall.Open(m.Abs(rel), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	if err := cmd.Start(); err != nil {
		return err
	}
	if ReaperWait != nil {
		<-ReaperWait(cmd.Process.Pid)
		_ = cmd.Process.Release()
		return nil
	}
	return cmd.Wait()
}

func (s *Service) killCgroup() bool {
	rel := s.cgroupPath()
	if rel == "" {
		return false
	}
	populated, err := Cgroups.Populated(rel)
	if err != nil {
		return false
	}
	if !populated {
		return true
	}
	if Cgroups.Kill(rel) != nil {
		return false
	}
	deadline := time.Now().Add(DefaultStopTimeout)
	for time.Now().Before(deadline) {
		if p, err := Cgroups.Populated(rel); err != nil || !p {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (s *Service) removeCgroup() {
	if rel := s.cgroupPath(); rel != "" {
		_ = Cgroups.Remove(rel)
	}
}
