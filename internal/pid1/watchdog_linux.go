package pid1

import (
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

const (
	wdiocKeepalive  = 0x80045705
	wdiocSetTimeout = 0xC0045706
	watchdogDevice  = "/dev/watchdog0"
)

func watchdogTimeout() int {
	v := cmdlineValue("sinit.watchdog")
	if v == "" {
		return 60
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 60
	}
	return n
}

func startWatchdog() {
	timeout := watchdogTimeout()
	if timeout == 0 {
		logf("hardware watchdog disabled (sinit.watchdog=0)")
		return
	}
	go runWatchdog(timeout)
}

func runWatchdog(timeout int) {
	var f *os.File
	for f == nil {
		var err error
		f, err = os.OpenFile(watchdogDevice, os.O_WRONLY, 0)
		if err != nil {
			time.Sleep(30 * time.Second)
		}
	}
	t := int32(timeout)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), wdiocSetTimeout, uintptr(unsafe.Pointer(&t))); errno != 0 {
		logf("watchdog: set timeout %ds: %v", timeout, errno)
	}
	logf("hardware watchdog armed, timeout %ds", t)
	interval := time.Duration(t) * time.Second / 4
	if interval < time.Second {
		interval = time.Second
	}
	var dummy int32
	for {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), wdiocKeepalive, uintptr(unsafe.Pointer(&dummy))); errno != 0 {
			_, _ = f.Write([]byte{0})
		}
		time.Sleep(interval)
	}
}
