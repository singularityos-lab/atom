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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/dbus"

	"github.com/singularityos-lab/atom/internal/notify"
	"github.com/singularityos-lab/atom/internal/socketact"
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

	// forkedPID is the daemon a Type=forking launcher fork()ed, learned from
	// PIDFile= after the launcher exits. Stop/kill target this, not the dead
	// launcher.
	forkedPID int

	// Socket activation: if set, the main process is spawned via the sd-exec
	// trampoline with these listening fds handed over as LISTEN_FDS.
	Listeners []*socketact.Listener
	FDName    string
}

// SelfExe is the atom binary path re-exec'd for the sd-exec socket-activation
// trampoline. Overridable in tests.
var SelfExe = "/proc/self/exe"

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

// beginStart atomically claims the start. It returns false when the service is
// already active or activating, so a duplicate or concurrent start -- a boot
// layer racing a control `start`, or two `start` requests -- cannot spawn a
// second, unsupervised process that orphans the first. On success the
// state is Activating and any prior stop flag is cleared.
func (s *Service) beginStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Active || s.state == Activating {
		return false
	}
	s.state = Activating
	s.stopping = false
	return true
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
	if !s.beginStart() {
		return nil // already active or activating: a duplicate start is a no-op
	}

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
	case TypeDbus:
		return s.startDbus(sctx)
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
		// The launcher has exited; the real daemon is whatever it fork()ed and wrote
		// to PIDFile=. Learn that pid so Stop/kill hit the daemon, not the dead
		// launcher. No PIDFile -> we cannot track it (best effort).
		if pid := s.readPIDFile(ctx); pid > 0 {
			s.mu.Lock()
			s.forkedPID = pid
			s.mu.Unlock()
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

// startDbus implements Type=dbus readiness: spawn the daemon, then consider it
// started only once its well-known BusName is owned on the system bus (bounded
// by the start timeout, failed if the process dies first). This is what lets an
// ordering like NetworkManager After=wpa_supplicant actually wait for wpa's
// fi.w1.wpa_supplicant1 name -- the gap that broke wifi at boot.
func (s *Service) startDbus(ctx context.Context) error {
	done, err := s.spawnMain()
	if err != nil {
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}

	// No BusName declared: degrade to simple (ready on spawn), matching the old
	// behaviour so nothing regresses.
	if s.cfg.BusName == "" {
		s.setState(Active)
		s.runPost(ctx)
		go s.supervise()
		return nil
	}

	ready := make(chan error, 1)
	go func() { ready <- dbus.WaitForName(ctx, s.cfg.BusName, 0) }()

	select {
	case werr := <-ready:
		if werr != nil {
			// The bus name did not appear within the start timeout, but the
			// process is still ALIVE. Unlike systemd (which fails+kills a
			// Type=dbus unit on timeout), sinit has no D-Bus activation to
			// re-launch it, so killing here would leave the machine with the
			// daemon dead -- e.g. NO wpa_supplicant at all, worse than never
			// waiting. Keep it running and go Active: dependents may briefly
			// race, but the daemon is up and its name will appear shortly.
			fmt.Fprintf(os.Stderr, "sinit: %s: bus name %s not acquired within %s; proceeding without kill\n",
				s.cfg.Name, s.cfg.BusName, s.startTimeout())
			s.setState(Active)
			s.runPost(ctx)
			go s.supervise()
			return nil
		}
		s.setState(Active)
		s.runPost(ctx)
		go s.supervise()
		return nil
	case <-done:
		s.setState(Failed)
		return fmt.Errorf("%s: exited before acquiring bus name %s", s.cfg.Name, s.cfg.BusName)
	}
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
	forked := s.forkedPID
	s.mu.Unlock()
	if forked > 0 {
		_ = syscall.Kill(forked, syscall.SIGKILL) // Type=forking: the daemon, not the launcher
		return
	}
	if main != nil && main.Process != nil {
		_ = main.Process.Kill()
	}
}

// readPIDFile reads PIDFile= for a Type=forking service. The daemon may write it a
// moment after the launcher exits, so we retry briefly (bounded by ctx).
func (s *Service) readPIDFile(ctx context.Context) int {
	if s.cfg.PIDFile == "" {
		return 0
	}
	for i := 0; i < 20; i++ {
		if b, err := os.ReadFile(s.cfg.PIDFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(50 * time.Millisecond):
		}
	}
	return 0
}

// pidAlive reports whether pid still exists (signal 0 probes without delivering).
func pidAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// waitPIDGone polls until pid is reaped or the timeout elapses; returns true if gone.
func waitPIDGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !pidAlive(pid)
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
	var cmd *exec.Cmd
	if len(s.Listeners) > 0 {
		cmd = s.commandActivated(s.cfg.ExecStart[0], extra)
	} else {
		cmd = s.command(context.Background(), s.cfg.ExecStart[0], extra)
	}
	if err := s.startCmd(cmd); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	exitCh := s.registerWait(cmd) // register BEFORE the child can be reaped
	s.mu.Lock()
	s.main = cmd
	s.exited = done
	s.lastPing = time.Now()
	s.mu.Unlock()
	go func() {
		info := <-exitCh
		s.mu.Lock()
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
	defer func() { _ = recover() }() // sd_notify parsing must not panic PID 1
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
	defer func() { _ = recover() }() // never let the watchdog panic kill PID 1
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
	// a panic in the supervisor goroutine must never take down PID 1.
	defer func() { _ = recover() }()
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
		return exitInfoFromStatus(ws)
	}
	return ExitInfo{Code: ps.ExitCode()}
}

func exitInfoFromStatus(ws syscall.WaitStatus) ExitInfo {
	if ws.Signaled() {
		return ExitInfo{Signaled: true, Signal: ws.Signal()}
	}
	return ExitInfo{Code: ws.ExitStatus()}
}

// ReaperWait, when set (by PID 1), is the sole source of child exit statuses:
// it registers interest in a pid and returns a channel for its WaitStatus. With
// it set, services do NOT call exec.Cmd.Wait, so the global SIGCHLD reaper does
// not race them for the status. When nil (tests, session-manager mode), services
// fall back to exec.Cmd.Wait.
var ReaperWait func(pid int) <-chan syscall.WaitStatus

// registerWait records interest in the child's exit BEFORE returning (closing
// the register-vs-exit race) and returns a one-shot channel for its ExitInfo.
func (s *Service) registerWait(cmd *exec.Cmd) <-chan ExitInfo {
	out := make(chan ExitInfo, 1)
	if ReaperWait != nil {
		ws := ReaperWait(cmd.Process.Pid) // registers synchronously here
		go func() {
			status := <-ws
			// The global reaper already wait4()'d this pid, so os/exec's Wait() is
			// never called -- release the Process ourselves to close the pidfd os/exec
			// opened for it, otherwise every spawn leaks one fd.
			_ = cmd.Process.Release()
			out <- exitInfoFromStatus(status)
		}()
	} else {
		go func() {
			_ = cmd.Wait()
			out <- exitInfoFrom(cmd)
		}()
	}
	return out
}

// Stop deactivates the service: ExecStop, then SIGTERM, then SIGKILL on timeout.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	main := s.main
	exited := s.exited
	forked := s.forkedPID
	s.mu.Unlock()

	s.setState(Deactivating)
	for _, ec := range s.cfg.ExecStop {
		_ = s.runToCompletion(ctx, ec)
	}

	if forked > 0 {
		// Type=forking: the launcher already exited; stop the daemon it fork()ed
		// (from PIDFile), not the dead launcher. There is no exit channel
		// for a process we did not spawn, so poll for it to be reaped after each signal.
		_ = syscall.Kill(forked, syscall.SIGTERM)
		if !waitPIDGone(forked, DefaultStopTimeout) {
			_ = syscall.Kill(forked, syscall.SIGKILL)
			waitPIDGone(forked, DefaultStopTimeout)
		}
	} else if main != nil && main.Process != nil && exited != nil {
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
	if err := s.startCmd(cmd); err != nil {
		if ec.IgnoreFailure {
			return nil
		}
		return err
	}
	info := <-s.registerWait(cmd)
	if !info.Clean() && !ec.IgnoreFailure {
		return fmt.Errorf("%s: exit code %d (signaled=%v)", ec.Path(), info.Code, info.Signaled)
	}
	return nil
}

// sharedNull is a single, process-wide /dev/null handed to children for stdin and
// for any nil stdout/stderr. os/exec opens its own /dev/null for a nil std stream
// and closes it only in Cmd.Wait(); under the reaper Wait() is never called, so
// giving it an *os.File we own (and never close) avoids one leaked fd per spawn.
var sharedNull, _ = os.OpenFile(os.DevNull, os.O_RDWR, 0)

// startCmd starts cmd with its std streams wired to fds this process owns.
//
// os/exec opens fds for the child's std streams -- a pipe for any stdout/stderr
// writer that is not an *os.File, and /dev/null for a nil stream -- and closes
// them only inside Cmd.Wait(). Under PID 1 the global SIGCHLD reaper owns
// waiting, so Wait() is never called and those fds stay open until a GC
// finalizer runs. A Restart=always crash-loop then leaks fds per spawn and
// eventually exhausts the process fd table (EMFILE). We hand os/exec fds we own
// instead: a shared /dev/null for stdin and nil output, and our own pipe (closed
// when the child's write end goes away) for a log writer.
func (s *Service) startCmd(cmd *exec.Cmd) error {
	if cmd.Stdin == nil && sharedNull != nil {
		cmd.Stdin = sharedNull
	}
	type pipePair struct {
		r, w *os.File
		dst  io.Writer
	}
	var pipes []pipePair
	wire := func(w io.Writer) io.Writer {
		if w == nil {
			if sharedNull != nil {
				return sharedNull // our own /dev/null; os/exec won't open (and leak) one
			}
			return nil
		}
		if _, ok := w.(*os.File); ok {
			return w // a real file is handed straight to the child; nothing to leak
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return w // best effort: fall back to os/exec's own (leaky) pipe
		}
		pipes = append(pipes, pipePair{r: pr, w: pw, dst: w})
		return pw
	}
	cmd.Stdout = wire(s.Stdout)
	if s.Stderr == s.Stdout {
		cmd.Stderr = cmd.Stdout // merge into the one pipe, matching the default
	} else {
		cmd.Stderr = wire(s.Stderr)
	}

	err := cmd.Start()
	for _, p := range pipes {
		p.w.Close() // the parent drops its write end; the child keeps its own dup
		if err != nil {
			p.r.Close()
			continue
		}
		go func(p pipePair) {
			_, _ = io.Copy(p.dst, p.r)
			p.r.Close()
		}(p)
	}
	return err
}

func (s *Service) command(ctx context.Context, ec ExecCommand, extraEnv []string) *exec.Cmd {
	var args []string
	if len(ec.Argv) > 1 {
		args = ec.Argv[1:]
	}
	cmd := exec.CommandContext(ctx, ec.Path(), args...)
	cmd.Env = s.baseEnv(extraEnv)
	cmd.Dir = s.cfg.WorkingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if s.cred != nil {
		cmd.SysProcAttr.Credential = s.cred
	}
	return cmd
}

func (s *Service) baseEnv(extra []string) []string {
	env := append([]string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}, s.cfg.Environment...)
	return append(env, extra...)
}

// commandActivated builds the main-process command for a socket-activated
// service: it re-execs atom's sd-exec trampoline so the listening fds are
// handed over as LISTEN_FDS with LISTEN_PID equal to the final service pid.
func (s *Service) commandActivated(ec ExecCommand, extraEnv []string) *exec.Cmd {
	cmd := socketact.Command(SelfExe, s.Listeners, s.FDName, ec.Argv, s.baseEnv(extraEnv))
	cmd.Dir = s.cfg.WorkingDir
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
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
		// Supplementary groups: without them a service running as e.g. 'greeter' loses
		// its video/input/render group memberships (no GPU, no input). Resolve them so
		// setgroups() sets the full set instead of clearing it to just the primary gid.
		if gids, e := u.GroupIds(); e == nil {
			sg := make([]uint32, 0, len(gids))
			for _, gs := range gids {
				if n, err2 := strconv.Atoi(gs); err2 == nil {
					sg = append(sg, uint32(n))
				}
			}
			s.cred.Groups = sg
		}
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
