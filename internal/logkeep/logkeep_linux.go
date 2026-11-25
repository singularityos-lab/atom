// Package logkeep copies the kernel ring buffer and the compositor log of the
// running boot to disk, keeping the last few boots. It only wakes when one of
// the sources has new data and flushes each burst to stable storage, so the
// tail of a boot that ends in a freeze is still on disk.
package logkeep

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"
)

// Config names the sources and the destination. The zero value is not usable;
// Default returns the system configuration.
type Config struct {
	Dir        string
	Keep       int
	Cap        int64
	Mark       string
	Kmsg       string
	Compositor string
	Group      string
}

// Default is the configuration of the system service.
func Default() Config {
	return Config{
		Dir:        "/var/log/keep",
		Keep:       5,
		Cap:        32 << 20,
		Mark:       "/run/sinty-logkeep.rotated",
		Kmsg:       "/dev/kmsg",
		Compositor: "/run/singularity/labwc.log",
		Group:      "singularity-session",
	}
}

type sink struct {
	f    *os.File
	size int64
	cap  int64
}

func (s *sink) write(b []byte) {
	left := s.cap - s.size
	if left <= 0 {
		return
	}
	if int64(len(b)) > left {
		b = b[:left]
	}
	n, _ := s.f.Write(b)
	s.size += int64(n)
}

func (s *sink) full() bool { return s.size >= s.cap }

type follower struct {
	path    string
	dir     string
	f       *os.File
	off     int64
	fromEnd bool
}

// Run copies until SIGTERM or SIGINT.
func Run(cfg Config) error {
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return err
	}
	gid := -1
	if cfg.Group != "" {
		if g, err := user.LookupGroup(cfg.Group); err == nil {
			gid, _ = strconv.Atoi(g.Gid)
			_ = os.Chown(cfg.Dir, -1, gid)
		}
	}
	fresh := false
	if _, err := os.Stat(cfg.Mark); errors.Is(err, os.ErrNotExist) {
		rotate(cfg)
		fresh = true
	}
	kernel, err := openSink(filepath.Join(cfg.Dir, "kernel.0.log"), cfg.Cap, gid, fresh)
	if err != nil {
		return err
	}
	comp, err := openSink(filepath.Join(cfg.Dir, "labwc.0.log"), cfg.Cap, gid, fresh)
	if err != nil {
		return err
	}
	if fresh {
		if f, err := os.Create(cfg.Mark); err == nil {
			f.Close()
		}
	}

	ep, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return err
	}
	kfd, err := syscall.Open(cfg.Kmsg, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.Kmsg, err)
	}
	if !fresh {
		_, _ = syscall.Seek(kfd, 0, 2)
	}
	if err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, kfd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(kfd)}); err != nil {
		return err
	}
	ifd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return err
	}
	if err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, ifd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(ifd)}); err != nil {
		return err
	}
	fol := &follower{path: cfg.Compositor, dir: filepath.Dir(cfg.Compositor), fromEnd: !fresh}
	watchDir(ifd, fol.dir)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		sync(kernel, comp)
		os.Exit(0)
	}()

	buf := make([]byte, 64<<10)
	drainKernel(kfd, buf, kernel)
	fol.drain(buf, comp)
	sync(kernel, comp)

	events := make([]syscall.EpollEvent, 4)
	for {
		n, err := syscall.EpollWait(ep, events, -1)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			sync(kernel, comp)
			return err
		}
		for _, ev := range events[:n] {
			switch int(ev.Fd) {
			case kfd:
				drainKernel(kfd, buf, kernel)
				if kernel.full() {
					_ = syscall.EpollCtl(ep, syscall.EPOLL_CTL_DEL, kfd, nil)
				}
			case ifd:
				if readInotify(ifd, buf, fol) {
					watchDir(ifd, fol.dir)
				}
				fol.drain(buf, comp)
			}
		}
		sync(kernel, comp)
	}
}

func openSink(path string, cap int64, gid int, truncate bool) (*sink, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if truncate {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if err != nil {
		return nil, err
	}
	if gid >= 0 {
		_ = f.Chown(-1, gid)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return &sink{f: f, size: st.Size(), cap: cap}, nil
}

func rotate(cfg Config) {
	for i := cfg.Keep - 1; i > 0; i-- {
		for _, name := range []string{"kernel", "labwc"} {
			from := filepath.Join(cfg.Dir, fmt.Sprintf("%s.%d.log", name, i-1))
			if _, err := os.Stat(from); err == nil {
				_ = os.Rename(from, filepath.Join(cfg.Dir, fmt.Sprintf("%s.%d.log", name, i)))
			}
		}
	}
}

func sync(sinks ...*sink) {
	for _, s := range sinks {
		_ = syscall.Fdatasync(int(s.f.Fd()))
	}
}

func drainKernel(fd int, buf []byte, out *sink) {
	for !out.full() {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EPIPE || err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return
		}
		out.write(buf[:n])
	}
}

func watchDir(ifd int, dir string) {
	if _, err := syscall.InotifyAddWatch(ifd, dir, syscall.IN_CREATE|syscall.IN_MODIFY|syscall.IN_MOVED_TO|syscall.IN_DELETE_SELF); err == nil {
		return
	}
	_, _ = syscall.InotifyAddWatch(ifd, filepath.Dir(dir), syscall.IN_CREATE|syscall.IN_MOVED_TO)
}

func readInotify(ifd int, buf []byte, fol *follower) (rewatch bool) {
	for {
		n, err := syscall.Read(ifd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return rewatch
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
				for i, c := range raw {
					if c == 0 {
						raw = raw[:i]
						break
					}
				}
				name = string(raw)
			}
			if name == filepath.Base(fol.dir) && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
				rewatch = true
			}
			if ev.Mask&syscall.IN_DELETE_SELF != 0 {
				rewatch = true
				fol.close()
			}
			if name == filepath.Base(fol.path) && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
				fol.close()
			}
			off += syscall.SizeofInotifyEvent + int(ev.Len)
		}
	}
}

func (fl *follower) close() {
	if fl.f != nil {
		fl.f.Close()
		fl.f = nil
	}
	fl.off = 0
	fl.fromEnd = false
}

func (fl *follower) drain(buf []byte, out *sink) {
	if fl.f == nil {
		f, err := os.Open(fl.path)
		if err != nil {
			return
		}
		fl.f = f
		fl.off = 0
		if fl.fromEnd {
			if st, err := f.Stat(); err == nil {
				fl.off = st.Size()
			}
			fl.fromEnd = false
		}
	}
	if st, err := fl.f.Stat(); err == nil && st.Size() < fl.off {
		fl.off = 0
	}
	for !out.full() {
		n, err := fl.f.ReadAt(buf, fl.off)
		if n > 0 {
			out.write(buf[:n])
			fl.off += int64(n)
		}
		if err != nil || n == 0 {
			return
		}
	}
}
