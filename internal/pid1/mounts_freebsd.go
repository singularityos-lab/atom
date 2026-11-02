package pid1

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	sysNmount = 378
	mntNosuid = 0x00000008
	mntNoexec = 0x00000004
)

var apiMounts = []apiMount{
	{"devfs", "/dev", "devfs", 0, "", 0o755, false},
	{"fdescfs", "/dev/fd", "fdescfs", 0, "", 0o555, true},
	{"tmpfs", "/run", "tmpfs", mntNosuid, "mode=755", 0o755, false},
	{"tmpfs", "/tmp", "tmpfs", mntNosuid, "mode=1777", 0o1777, false},
	{"procfs", "/proc", "procfs", mntNosuid | mntNoexec, "", 0o555, true},
}

func nmount(fstype, source, target string, flags uintptr, opts map[string]string) error {
	pairs := []string{"fstype", fstype, "fspath", target}
	if source != "" {
		pairs = append(pairs, "from", source)
	}
	for k, v := range opts {
		pairs = append(pairs, k, v)
	}
	iov := make([]syscall.Iovec, 0, len(pairs))
	for _, p := range pairs {
		b := append([]byte(p), 0)
		iov = append(iov, syscall.Iovec{Base: &b[0], Len: uint64(len(b))})
	}
	_, _, errno := syscall.Syscall(sysNmount, uintptr(unsafe.Pointer(&iov[0])), uintptr(len(iov)), flags)
	if errno != 0 {
		return errno
	}
	return nil
}

func mountOptions(data string) map[string]string {
	opts := map[string]string{}
	if data == "" {
		return opts
	}
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == ',' {
			kv := data[start:i]
			start = i + 1
			for j := 0; j < len(kv); j++ {
				if kv[j] == '=' {
					opts[kv[:j]] = kv[j+1:]
					kv = ""
					break
				}
			}
			if kv != "" {
				opts[kv] = ""
			}
		}
	}
	return opts
}

func mountProc(log func(string, ...any)) {}

func mountAPIFilesystems(log func(string, ...any)) {
	mounted := currentMounts()
	for _, m := range apiMounts {
		if mounted[m.target] {
			continue
		}
		if err := os.MkdirAll(m.target, m.mode); err != nil {
			if !m.optional {
				log("mount %s: mkdir: %v", m.target, err)
			}
			continue
		}
		if err := nmount(m.fstype, m.source, m.target, m.flags, mountOptions(m.data)); err != nil {
			if m.optional {
				continue
			}
			log("mount %s (%s): %v", m.target, m.fstype, err)
		}
	}
}

func currentMounts() map[string]bool {
	out := map[string]bool{}
	n, err := syscall.Getfsstat(nil, 2)
	if err != nil || n <= 0 {
		return out
	}
	buf := make([]syscall.Statfs_t, n)
	n, err = syscall.Getfsstat(buf, 2)
	if err != nil {
		return out
	}
	for _, st := range buf[:n] {
		var b []byte
		for _, c := range st.Mntonname {
			if c == 0 {
				break
			}
			b = append(b, byte(c))
		}
		out[string(b)] = true
	}
	return out
}

func bindMount(source, target string) error {
	return nmount("nullfs", source, target, 0, map[string]string{"target": source})
}
