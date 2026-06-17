package pid1

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

type apiMount struct {
	source, target, fstype string
	flags                  uintptr
	data                   string
	mode                   os.FileMode
}

// apiMounts are the kernel API filesystems PID 1 ensures exist. Order matters:
// /sys before /sys/fs/cgroup, /dev before /dev/pts. On Sinty the initramfs has
// already mounted most of these before switch_root; mounting is idempotent
// (an already-mounted target is skipped), so this is also safe there.
var apiMounts = []apiMount{
	{"proc", "/proc", "proc", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555},
	{"sysfs", "/sys", "sysfs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555},
	{"devtmpfs", "/dev", "devtmpfs", syscall.MS_NOSUID | syscall.MS_STRICTATIME, "mode=755", 0o755},
	{"tmpfs", "/run", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=755", 0o755},
	{"cgroup2", "/sys/fs/cgroup", "cgroup2", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555},
	{"devpts", "/dev/pts", "devpts", syscall.MS_NOSUID | syscall.MS_NOEXEC, "mode=620,ptmxmode=666", 0o755},
	{"tmpfs", "/dev/shm", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777", 0o1777},
	{"tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777", 0o1777},
}

// mountProc mounts /proc first, before anything reads /proc/cmdline or
// /proc/mounts. It is idempotent: an existing /proc (kernel- or initramfs-
// mounted) is detected and left alone.
func mountProc(log func(string, ...any)) {
	if _, err := os.Stat("/proc/self"); err == nil {
		return // already mounted
	}
	if err := os.MkdirAll("/proc", 0o555); err != nil {
		log("mkdir /proc: %v", err)
		return
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV)
	if err := syscall.Mount("proc", "/proc", "proc", flags, ""); err != nil && err != syscall.EBUSY {
		log("mount /proc: %v", err)
	}
}

// mountAPIFilesystems mounts the kernel API filesystems that are not already
// present. It is best-effort: a failure on one mount is logged, not fatal.
func mountAPIFilesystems(log func(string, ...any)) {
	mounted := currentMounts()
	for _, m := range apiMounts {
		if mounted[m.target] {
			continue
		}
		if err := os.MkdirAll(m.target, m.mode); err != nil {
			log("mount %s: mkdir: %v", m.target, err)
			continue
		}
		if err := syscall.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			log("mount %s (%s): %v", m.target, m.fstype, err)
		}
	}
}

// currentMounts returns the set of currently mounted targets from /proc/mounts.
func currentMounts() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 {
			out[fields[1]] = true
		}
	}
	return out
}
