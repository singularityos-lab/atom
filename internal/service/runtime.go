package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/notify"
)

// State is the coarse activation state of a service.
type State int

const (
	Inactive State = iota
	Activating
	Active
	Deactivating
	Failed
)

func (s State) String() string {
	switch s {
	case Inactive:
		return "inactive"
	case Activating:
		return "activating"
	case Active:
		return "active"
	case Deactivating:
		return "deactivating"
	case Failed:
		return "failed"
	default:
		return "inactive"
	}
}

// DefaultStopTimeout is how long Stop waits after SIGTERM before SIGKILL.
var DefaultStopTimeout = 5 * time.Second

// DefaultStartTimeout bounds a start that has no explicit TimeoutStartSec, so a
// service that never signals readiness cannot wedge the boot (systemd default).
var DefaultStartTimeout = 90 * time.Second

func (s *Service) startTimeout() time.Duration {
	if s.cfg.TimeoutStartSec > 0 {
		return s.cfg.TimeoutStartSec
	}
	return DefaultStartTimeout
}

// Service runs and supervises one [Service] unit.
//
// Process waiting here uses exec.Cmd.Wait per service, correct when the manager
// runs as a session manager / subreaper (the component-test mode). Under true
// PID 1 the global SIGCHLD reaper owns waiting and routes exits by PID.
type Service struct {
	cfg Config

	Stdout io.Writer
	Stderr io.Writer

	// DryRun simulates start/stop without executing any process.
	DryRun bool

	mu           sync.Mutex
	state        State
	main         *exec.Cmd
	notify       *notify.Listener
	exited       chan struct{}
	exitErr      error
	lastExit     ExitInfo
	stopping     bool
	restartTimes []time.Time
	restartCount int

	lastPing        time.Time // last WATCHDOG=1 (or spawn) time
	watchdogPending bool      // a watchdog miss killed the current instance

	cred *syscall.Credential // resolved User=/Group=, applied to spawned procs
}

// New returns an inactive service for cfg.
func New(cfg Config) *Service { return &Service{cfg: cfg, state: Inactive} }

// Name returns the unit name.
func (s *Service) Name() string { return s.cfg.Name }

// State returns the current activation state.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// RestartCount returns how many times the service has been auto-restarted.
func (s *Service) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartCount
}

func (s *Service) setState(st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

// Start activates the service and blocks until it reaches its readiness point.
func (s *Service) Start(ctx context.Context) error {
	if s.DryRun {
		return s.startDry()
	}
	if len(s.cfg.ExecStart) == 0 {
		s.setState(Failed)
		return fmt.Errorf("%s: no ExecStart", s.cfg.Name)
	}
	s.setState(Activating)

	// Resolve User=/Group= and create RuntimeDirectory/StateDirectory. A User=
	// that names a missing account fails the unit fast (systemd parity via its
	// nss userdb is intentionally not reimplemented), rather than silently
	// running as root.
	if err := s.prepare(); err != nil {
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}

	// Bound the ENTIRE start -- ExecStartPre, the readiness wait, and
	// ExecStartPost -- with TimeoutStartSec, so no wait path can wedge the boot.
	sctx, cancel := context.WithTimeout(ctx, s.startTimeout())
	defer cancel()

	for _, pre := range s.cfg.ExecStartPre {
		if err := s.runToCompletion(sctx, pre); err != nil {
			s.setState(Failed)
			return fmt.Errorf("%s: ExecStartPre: %w", s.cfg.Name, err)
		}
	}

	switch s.cfg.Type {
	case TypeOneshot:
		return s.startOneshot(sctx)
	case TypeNotify:
		return s.startNotify(sctx)
	default:
		return s.startLongRunning(sctx)
	}
}

func (s *Service) startDry() error {
	s.setState(Activating)
	if s.cfg.Type == TypeOneshot && !s.cfg.RemainAfterExit {
		s.setState(Inactive)
	} else {
		s.setState(Active)
	}
	return nil
}

func (s *Service) startOneshot(ctx context.Context) error {
	for _, cmd := range s.cfg.ExecStart {
		if err := s.runToCompletion(ctx, cmd); err != nil {
			s.setState(Failed)
			return fmt.Errorf("%s: ExecStart (oneshot): %w", s.cfg.Name, err)
		}
	}
	if s.cfg.RemainAfterExit {
		s.setState(Active)
	} else {
		s.setState(Inactive)
	}
	s.runPost(ctx)
	return nil
}

func (s *Service) startLongRunning(ctx context.Context) error {
	done, err := s.spawnMain()
	if err != nil {
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}

	if s.cfg.Type == TypeForking {
		select {
		case <-done:
		case <-ctx.Done():
			s.killMain()
			s.setState(Failed)
			return fmt.Errorf("%s: forking launcher timed out after %s", s.cfg.Name, s.startTimeout())
		}
		s.mu.Lock()
		info := s.lastExit
		s.mu.Unlock()
		if !info.Clean() {
			s.setState(Failed)
			return fmt.Errorf("%s: forking launcher failed", s.cfg.Name)
		}
		s.setState(Active)
		s.runPost(ctx)
		return nil
	}

	s.setState(Active)
	s.runPost(ctx)
	go s.supervise()
	return nil
}

func (s *Service) startNotify(ctx context.Context) error {
	sock := s.notifyPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		s.setState(Failed)
		return err
	}
	_ = os.Remove(sock)
	l, err := notify.NewListener(sock)
	if err != nil {
		s.setState(Failed)
		return err
	}
	s.notify = l

	ready := make(chan struct{})
	go s.notifyReader(l, ready)

	done, err := s.spawnMain()
	if err != nil {
		l.Close()
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}

	select {
	case <-ready:
		s.setState(Active)
		s.runPost(ctx)
		if s.cfg.WatchdogSec > 0 {
			go s.watchdog()
		}
		go s.supervise()
		return nil
	case <-done:
		s.setState(Failed)
		return fmt.Errorf("%s: exited before READY=1", s.cfg.Name)
	case <-ctx.Done():
		s.killMain()
		s.setState(Failed)
		return fmt.Errorf("%s: start timed out after %s (no READY=1)", s.cfg.Name, s.startTimeout())
	}
}

func (s *Service) killMain() {
	s.mu.Lock()
	main := s.main
	s.mu.Unlock()
	if main != nil && main.Process != nil {
		_ = main.Process.Kill()
	}
}

// spawnMain starts ExecStart[0], records the process, and launches a waiter
// that captures the exit and closes the per-run done channel. For Type=notify
// it always passes NOTIFY_SOCKET, so a restarted instance can signal readiness.
func (s *Service) spawnMain() (chan struct{}, error) {
	var extra []string
	if s.cfg.Type == TypeNotify {
		extra = append(extra, notify.Env(s.notifyPath()))
		if s.cfg.WatchdogSec > 0 {
			extra = append(extra, fmt.Sprintf("WATCHDOG_USEC=%d", s.cfg.WatchdogSec.Microseconds()))
		}
	}
	cmd := s.command(context.Background(), s.cfg.ExecStart[0], extra)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	s.mu.Lock()
	s.main = cmd
	s.exited = done
	s.lastPing = time.Now()
	s.mu.Unlock()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.exitErr = err
		info := exitInfoFrom(cmd)
		if s.watchdogPending {
			info.Watchdog = true
			s.watchdogPending = false
		}
		s.lastExit = info
		s.mu.Unlock()
		close(done)
	}()
	return done, nil
}

// notifyReader consumes the sd_notify stream for the service's lifetime: it
// closes ready on the first READY=1 and refreshes the watchdog ping on each
// WATCHDOG=1. It returns when the listener is closed.
func (s *Service) notifyReader(l *notify.Listener, ready chan struct{}) {
	readyClosed := false
	for {
		r, err := l.Receive()
		if err != nil {
			return
		}
		if r.Msg.Ready && !readyClosed {
			close(ready)
			readyClosed = true
		}
		if r.Msg.Watchdog {
			s.mu.Lock()
			s.lastPing = time.Now()
			s.mu.Unlock()
		}
	}
}

// watchdog kills the current instance if it misses its WATCHDOG=1 deadline; the
// resulting exit carries Watchdog=true so supervise can restart it. One
// watchdog goroutine runs for the service's lifetime.
func (s *Service) watchdog() {
	iv := s.cfg.WatchdogSec
	t := time.NewTicker(iv / 2)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		miss := time.Since(s.lastPing) > iv
		main := s.main
		if miss {
			s.watchdogPending = true
			s.lastPing = time.Now() // give the next instance a fresh interval
		}
		s.mu.Unlock()
		if miss && main != nil && main.Process != nil {
			_ = main.Process.Kill()
		}
	}
}

// supervise watches the main process and applies the Restart= policy with
// RestartSec backoff and the start-limit burst guard.
func (s *Service) supervise() {
	for {
		s.mu.Lock()
		done := s.exited
		s.mu.Unlock()
		<-done

		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		info := s.lastExit
		mode := s.cfg.RestartMode
		s.mu.Unlock()

		if !mode.ShouldRestart(info) {
			if info.Clean() {
				s.setState(Inactive)
			} else {
				s.setState(Failed)
			}
			return
		}
		if !s.allowRestart() {
			s.setState(Failed) // start-limit hit
			return
		}

		s.setState(Activating)
		time.Sleep(s.cfg.RestartSec)

		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if stopping {
			return
		}

		if _, err := s.spawnMain(); err != nil {
			s.setState(Failed)
			return
		}
		s.mu.Lock()
		s.restartCount++
		s.mu.Unlock()
		s.setState(Active)
	}
}

// allowRestart enforces StartLimitBurst within StartLimitInterval.
func (s *Service) allowRestart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-s.cfg.StartLimitInterval)
	kept := s.restartTimes[:0]
	for _, t := range s.restartTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.restartTimes = kept
	if s.cfg.StartLimitBurst > 0 && len(s.restartTimes) >= s.cfg.StartLimitBurst {
		return false
	}
	s.restartTimes = append(s.restartTimes, now)
	return true
}

func exitInfoFrom(cmd *exec.Cmd) ExitInfo {
	ps := cmd.ProcessState
	if ps == nil {
		return ExitInfo{Code: -1}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return ExitInfo{Signaled: true, Signal: ws.Signal()}
		}
		return ExitInfo{Code: ws.ExitStatus()}
	}
	return ExitInfo{Code: ps.ExitCode()}
}

// Stop deactivates the service: ExecStop, then SIGTERM, then SIGKILL on timeout.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	main := s.main
	exited := s.exited
	s.mu.Unlock()

	s.setState(Deactivating)
	for _, ec := range s.cfg.ExecStop {
		_ = s.runToCompletion(ctx, ec)
	}

	if main != nil && main.Process != nil && exited != nil {
		_ = main.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(DefaultStopTimeout):
			_ = main.Process.Kill()
			<-exited
		}
	}
	if s.notify != nil {
		s.notify.Close()
	}
	s.setState(Inactive)
	return nil
}

func (s *Service) runPost(ctx context.Context) {
	for _, ec := range s.cfg.ExecStartPost {
		_ = s.runToCompletion(ctx, ec)
	}
}

func (s *Service) runToCompletion(ctx context.Context, ec ExecCommand) error {
	cmd := s.command(ctx, ec, nil)
	err := cmd.Run()
	if err != nil && !ec.IgnoreFailure {
		return err
	}
	return nil
}

func (s *Service) command(ctx context.Context, ec ExecCommand, extraEnv []string) *exec.Cmd {
	var args []string
	if len(ec.Argv) > 1 {
		args = ec.Argv[1:]
	}
	cmd := exec.CommandContext(ctx, ec.Path(), args...)
	cmd.Env = append([]string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}, s.cfg.Environment...)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Dir = s.cfg.WorkingDir
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if s.cred != nil {
		cmd.SysProcAttr.Credential = s.cred
	}
	return cmd
}

// prepare resolves credentials and creates managed directories. It is called
// once at the start of Start; a failure (e.g. an unresolvable User=) fails the
// unit fast.
func (s *Service) prepare() error {
	if s.cfg.User != "" {
		u, err := user.Lookup(s.cfg.User)
		if err != nil {
			return fmt.Errorf("User=%s: %w", s.cfg.User, err)
		}
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		s.cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	if s.cfg.Group != "" {
		g, err := user.LookupGroup(s.cfg.Group)
		if err != nil {
			return fmt.Errorf("Group=%s: %w", s.cfg.Group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if s.cred == nil {
			s.cred = &syscall.Credential{Uid: 0}
		}
		s.cred.Gid = uint32(gid)
	}
	for _, d := range s.cfg.RuntimeDirectory {
		s.makeManagedDir(filepath.Join("/run", d))
	}
	for _, d := range s.cfg.StateDirectory {
		s.makeManagedDir(filepath.Join("/var/lib", d))
	}
	return nil
}

func (s *Service) makeManagedDir(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return
	}
	if s.cred != nil {
		_ = os.Chown(path, int(s.cred.Uid), int(s.cred.Gid))
	}
}

func (s *Service) notifyPath() string {
	return filepath.Join(s.cfg.RuntimeDir, "notify", s.cfg.Name+".sock")
}
