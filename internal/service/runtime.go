package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/notify"
)

// State is the coarse activation state of a service. The full systemd
// sub-states are layered on in the FSM wiring; this is the load-bearing core.
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

// Service runs and supervises one [Service] unit.
//
// Process waiting here uses exec.Cmd.Wait per service, which is correct when
// the manager runs as a session manager / subreaper (the component-test
// mode). Under true PID 1 the global SIGCHLD reaper owns waiting and
// routes exits by PID; the spawn path switches to os.StartProcess then.
type Service struct {
	cfg Config

	// Stdout/Stderr receive the service's output (nil discards).
	Stdout io.Writer
	Stderr io.Writer

	// DryRun simulates start/stop without executing any process: readiness is
	// derived from Type alone. Used by the session-manager smoke test to
	// validate ordering and readiness against a real unit set with zero side
	// effects.
	DryRun bool

	mu      sync.Mutex
	state   State
	main    *exec.Cmd
	notify  *notify.Listener
	exited  chan struct{}
	exitErr error
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

func (s *Service) setState(st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

// Start activates the service and blocks until it reaches its readiness point
// for the configured Type (or fails / ctx is cancelled).
func (s *Service) Start(ctx context.Context) error {
	if s.DryRun {
		return s.startDry()
	}
	if len(s.cfg.ExecStart) == 0 {
		s.setState(Failed)
		return fmt.Errorf("%s: no ExecStart", s.cfg.Name)
	}
	s.setState(Activating)

	for _, pre := range s.cfg.ExecStartPre {
		if err := s.runToCompletion(ctx, pre); err != nil {
			s.setState(Failed)
			return fmt.Errorf("%s: ExecStartPre: %w", s.cfg.Name, err)
		}
	}

	switch s.cfg.Type {
	case TypeOneshot:
		return s.startOneshot(ctx)
	case TypeNotify:
		return s.startNotify(ctx)
	default:
		return s.startLongRunning(ctx)
	}
}

// startDry simulates a start: readiness comes from Type only, no exec.
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
	cmd := s.command(context.Background(), s.cfg.ExecStart[0], nil)
	if err := cmd.Start(); err != nil {
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}
	s.main = cmd

	if s.cfg.Type == TypeForking {
		// The started process is the launcher; success means the daemon
		// forked off. Without cgroup tracking the detached child is not yet
		// followed (handled later); we mark active on a clean launcher exit.
		err := cmd.Wait()
		if err != nil {
			s.setState(Failed)
			return fmt.Errorf("%s: forking launcher: %w", s.cfg.Name, err)
		}
		s.setState(Active)
		s.runPost(context.Background())
		return nil
	}

	s.exited = make(chan struct{})
	go s.waitMain()
	s.setState(Active)
	s.runPost(context.Background())
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
	go func() {
		for {
			r, err := l.Receive()
			if err != nil {
				return
			}
			if r.Msg.Ready {
				close(ready)
				return
			}
		}
	}()

	cmd := s.command(context.Background(), s.cfg.ExecStart[0], []string{notify.Env(sock)})
	if err := cmd.Start(); err != nil {
		l.Close()
		s.setState(Failed)
		return fmt.Errorf("%s: %w", s.cfg.Name, err)
	}
	s.main = cmd
	s.exited = make(chan struct{})
	go s.waitMain()

	select {
	case <-ready:
		s.setState(Active)
		s.runPost(context.Background())
		return nil
	case <-s.exited:
		s.setState(Failed)
		return fmt.Errorf("%s: exited before READY=1", s.cfg.Name)
	case <-ctx.Done():
		s.setState(Failed)
		return ctx.Err()
	}
}

// waitMain waits on the main process and updates state on exit.
func (s *Service) waitMain() {
	err := s.main.Wait()
	s.mu.Lock()
	s.exitErr = err
	if s.state == Active || s.state == Activating {
		if err != nil {
			s.state = Failed
		} else {
			s.state = Inactive
		}
	}
	s.mu.Unlock()
	close(s.exited)
}

// Stop deactivates the service: ExecStop, then SIGTERM, then SIGKILL on timeout.
func (s *Service) Stop(ctx context.Context) error {
	s.setState(Deactivating)
	for _, ec := range s.cfg.ExecStop {
		_ = s.runToCompletion(ctx, ec)
	}

	s.mu.Lock()
	main := s.main
	exited := s.exited
	s.mu.Unlock()

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
	return cmd
}

func (s *Service) notifyPath() string {
	return filepath.Join(s.cfg.RuntimeDir, "notify", s.cfg.Name+".sock")
}
