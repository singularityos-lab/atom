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
	optional               bool // a kernel fs that may be absent; mount best-effort, quiet if unsupported
}

// apiMounts are the kernel API filesystems PID 1 ensures exist. Order matters:
// /sys before its children, /dev before /dev/pts. On Sinty the initramfs has
// already mounted most of these before switch_root; mounting is idempotent
// (an already-mounted target is skipped), so this is also safe there. The
// optional entries mirror systemd's secondary set: mounted only when the kernel
// provides the filesystem, and quiet when it does not.
var apiMounts = []apiMount{
	{"proc", "/proc", "proc", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555, false},
	{"sysfs", "/sys", "sysfs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555, false},
	{"devtmpfs", "/dev", "devtmpfs", syscall.MS_NOSUID | syscall.MS_STRICTATIME, "mode=755", 0o755, false},
	{"tmpfs", "/run", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=755", 0o755, false},
	{"cgroup2", "/sys/fs/cgroup", "cgroup2", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o555, false},
	{"devpts", "/dev/pts", "devpts", syscall.MS_NOSUID | syscall.MS_NOEXEC, "mode=620,ptmxmode=666", 0o755, false},
	{"tmpfs", "/dev/shm", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777", 0o1777, false},
	{"tmpfs", "/tmp", "tmpfs", syscall.MS_NOSUID | syscall.MS_NODEV, "mode=1777", 0o1777, false},

	// Secondary kernel filesystems: present only when compiled in. fusectl at
	// /sys/fs/fuse/connections is what `fusermount -u` needs; mqueue and hugepages
	// are needed by some services; the /sys/kernel/* set matches systemd.
	{"mqueue", "/dev/mqueue", "mqueue", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o755, true},
	{"hugetlbfs", "/dev/hugepages", "hugetlbfs", syscall.MS_NOSUID | syscall.MS_NODEV, "", 0o755, true},
	{"securityfs", "/sys/kernel/security", "securityfs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o755, true},
	{"debugfs", "/sys/kernel/debug", "debugfs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o700, true},
	{"tracefs", "/sys/kernel/tracing", "tracefs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o700, true},
	{"configfs", "/sys/kernel/config", "configfs", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o755, true},
	{"pstore", "/sys/fs/pstore", "pstore", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o755, true},
	{"bpf", "/sys/fs/bpf", "bpf", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "mode=700", 0o700, true},
	{"fusectl", "/sys/fs/fuse/connections", "fusectl", syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV, "", 0o755, true},
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
			if m.optional {
				continue // mountpoint absent (kernel-created dir missing) -> fs not present
			}
			log("mount %s: mkdir: %v", m.target, err)
			continue
		}
		if err := syscall.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			if m.optional && (err == syscall.ENODEV || err == syscall.ENOENT || err == syscall.EBUSY || err == syscall.EACCES) {
				continue // filesystem not compiled in / already mounted -> quiet
			}
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
