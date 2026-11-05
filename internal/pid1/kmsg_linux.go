package pid1

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	kernelLogPath  = "/run/log/kernel.log"
	kernelLogGroup = "singularity-session"
	kernelLogMax   = 8 << 20
)

func groupID(name string) int {
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 && f[0] == name {
			if id, err := strconv.Atoi(f[2]); err == nil {
				return id
			}
		}
	}
	return -1
}

func formatKmsg(rec []byte) (string, bool) {
	s := string(rec)
	semi := strings.IndexByte(s, ';')
	if semi < 0 {
		return "", false
	}
	head := strings.Split(s[:semi], ",")
	if len(head) < 3 {
		return "", false
	}
	prio, err := strconv.Atoi(head[0])
	if err != nil {
		return "", false
	}
	usec, err := strconv.ParseInt(head[2], 10, 64)
	if err != nil {
		return "", false
	}
	msg := s[semi+1:]
	if nl := strings.IndexByte(msg, '\n'); nl >= 0 {
		msg = msg[:nl]
	}
	return fmt.Sprintf("<%d>[%5d.%06d] %s\n", prio, usec/1000000, usec%1000000, msg), true
}

func openKernelLog(gid int) (*os.File, int64, error) {
	f, err := os.OpenFile(kernelLogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return nil, 0, err
	}
	if gid >= 0 {
		_ = f.Chown(0, gid)
	}
	_ = f.Chmod(0o640)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func startKernelLog() {
	if err := os.MkdirAll(filepath.Dir(kernelLogPath), 0o755); err != nil {
		logf("kernel log: %v", err)
		return
	}
	go runKernelLog()
}

func runKernelLog() {
	src, err := os.Open("/dev/kmsg")
	if err != nil {
		logf("kernel log: %v", err)
		return
	}
	defer src.Close()
	gid := groupID(kernelLogGroup)
	out, size, err := openKernelLog(gid)
	if err != nil {
		logf("kernel log: %v", err)
		return
	}
	w := bufio.NewWriter(out)
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Read(int(src.Fd()), buf)
		if err != nil {
			if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.EINTR) {
				continue
			}
			logf("kernel log: %v", err)
			w.Flush()
			out.Close()
			return
		}
		line, ok := formatKmsg(buf[:n])
		if !ok {
			continue
		}
		if size+int64(len(line)) > kernelLogMax {
			w.Flush()
			out.Close()
			_ = os.Rename(kernelLogPath, kernelLogPath+".1")
			if out, size, err = openKernelLog(gid); err != nil {
				logf("kernel log: %v", err)
				return
			}
			w = bufio.NewWriter(out)
		}
		w.WriteString(line)
		size += int64(len(line))
		w.Flush()
	}
}
