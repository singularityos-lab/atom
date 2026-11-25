//go:build !linux

package service

import (
	"os/exec"

	"github.com/singularityos-lab/atom/internal/cgroup"
)

var (
	Cgroups     *cgroup.Manager
	SliceLimits func(slice string) cgroup.Limits
)

func (s *Service) enterCgroup(cmd *exec.Cmd) func() { return func() {} }

func cgroupStartFailed(cmd *exec.Cmd, err error) {}

func ProbeCgroups(m *cgroup.Manager, bin string) error { return nil }

func (s *Service) killCgroup() bool { return false }

func (s *Service) removeCgroup() {}

func PrepareSlices(names ...string) {}
