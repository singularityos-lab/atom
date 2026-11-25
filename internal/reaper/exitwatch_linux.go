package reaper

import (
	"sync"
	"syscall"
)

const sysPidfdOpen = 434

type exitWatcher struct {
	once sync.Once
	ep   int
	err  error
	mu   sync.Mutex
	fds  map[int32]chan struct{}
}

var watcher exitWatcher

func (w *exitWatcher) start() {
	w.ep, w.err = syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if w.err != nil {
		return
	}
	w.fds = map[int32]chan struct{}{}
	go w.run()
}

func (w *exitWatcher) run() {
	events := make([]syscall.EpollEvent, 32)
	for {
		n, err := syscall.EpollWait(w.ep, events, -1)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return
		}
		w.mu.Lock()
		for _, ev := range events[:n] {
			if ch, ok := w.fds[ev.Fd]; ok {
				delete(w.fds, ev.Fd)
				_ = syscall.EpollCtl(w.ep, syscall.EPOLL_CTL_DEL, int(ev.Fd), nil)
				_ = syscall.Close(int(ev.Fd))
				close(ch)
			}
		}
		w.mu.Unlock()
	}
}

// Exited returns a channel closed once pid has terminated, whether or not it
// has been reaped yet. It returns nil when the kernel cannot watch pid exits
// (no pidfd support), in which case the caller has to poll.
func Exited(pid int) <-chan struct{} {
	watcher.once.Do(watcher.start)
	if watcher.err != nil {
		return nil
	}
	ch := make(chan struct{})
	r, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if errno == syscall.ESRCH {
		close(ch)
		return ch
	}
	if errno != 0 {
		return nil
	}
	fd := int32(r)
	syscall.CloseOnExec(int(fd))
	watcher.mu.Lock()
	watcher.fds[fd] = ch
	err := syscall.EpollCtl(watcher.ep, syscall.EPOLL_CTL_ADD, int(fd), &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: fd})
	if err != nil {
		delete(watcher.fds, fd)
		watcher.mu.Unlock()
		_ = syscall.Close(int(fd))
		return nil
	}
	watcher.mu.Unlock()
	return ch
}
