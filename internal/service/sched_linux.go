package service

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	ioprioWhoProcess = 1
	ioprioClassShift = 13
	schedReset       = 0x40000000
)

type threadSched struct {
	nice   int
	ioprio int
	policy int
	prio   int
}

func readThreadSched(tid int) (threadSched, error) {
	var t threadSched
	r, _, errno := syscall.Syscall(syscall.SYS_GETPRIORITY, syscall.PRIO_PROCESS, uintptr(tid), 0)
	if errno != 0 {
		return t, errno
	}
	t.nice = 20 - int(r)
	r, _, errno = syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(tid), 0)
	if errno != 0 {
		return t, errno
	}
	t.ioprio = int(r)
	r, _, errno = syscall.Syscall(syscall.SYS_SCHED_GETSCHEDULER, uintptr(tid), 0, 0)
	if errno != 0 {
		return t, errno
	}
	t.policy = int(r) &^ schedReset
	var p int32
	if _, _, errno = syscall.Syscall(syscall.SYS_SCHED_GETPARAM, uintptr(tid), uintptr(unsafe.Pointer(&p)), 0); errno != 0 {
		return t, errno
	}
	t.prio = int(p)
	return t, nil
}

func writeThreadSched(tid int, t threadSched) error {
	p := int32(t.prio)
	if _, _, errno := syscall.Syscall(syscall.SYS_SCHED_SETSCHEDULER, uintptr(tid), uintptr(t.policy), uintptr(unsafe.Pointer(&p))); errno != 0 {
		return errno
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, t.nice); err != nil {
		return err
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), uintptr(t.ioprio)); errno != 0 {
		return errno
	}
	return nil
}

// withSched runs start (a fork) on a thread that carries the service's CPU and
// I/O priorities, so the child inherits them from its first instruction. The
// thread gets its own values back before it returns to the scheduler.
func (s *Service) withSched(start func() error) error {
	sc := s.cfg.Sched
	if !sc.set() {
		return start()
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tid := syscall.Gettid()
	orig, err := readThreadSched(tid)
	if err != nil {
		return start()
	}
	want := orig
	if sc.Nice != nil {
		want.nice = *sc.Nice
	}
	if sc.IOClass >= 0 {
		want.ioprio = sc.IOClass<<ioprioClassShift | sc.IOPriority
	}
	if sc.Policy >= 0 {
		want.policy = sc.Policy
		want.prio = sc.Priority
		if sc.Policy == 1 || sc.Policy == 2 {
			want.policy |= schedReset
		}
	}
	if err := writeThreadSched(tid, want); err != nil {
		_ = writeThreadSched(tid, orig)
		return start()
	}
	err = start()
	if writeThreadSched(tid, orig) != nil {
		runtime.LockOSThread()
	}
	return err
}
