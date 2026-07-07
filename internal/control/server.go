package control

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/core"
)

// Server serves the control protocol backed by a manager.
type Server struct {
	ln      net.Listener
	m       *core.Manager
	selfUID uint32
}

// Listen creates the control socket and returns a server; call Serve to accept.
func Listen(path string, m *core.Manager) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return &Server{ln: ln, m: m, selfUID: uint32(os.Geteuid())}, nil
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

// Close stops the server.
func (s *Server) Close() error { return s.ln.Close() }

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	defer func() { _ = recover() }() // a bad control request must not kill PID 1
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var req Request
	if err := readFrame(conn, &req); err != nil {
		return
	}
	_ = writeFrame(conn, s.dispatch(conn, req))
}

func (s *Server) dispatch(conn net.Conn, req Request) Reply {
	switch req.Cmd {
	case "list-units":
		var us []UnitStatus
		for _, u := range s.m.ListUnits() {
			us = append(us, UnitStatus{Name: u.Name, Kind: u.Kind, State: u.State})
		}
		return Reply{OK: true, Units: us}
	case "status":
		if req.Unit == "" {
			return Reply{Error: "status: unit required"}
		}
		return Reply{OK: true, State: s.m.State(req.Unit)}
	case "logs":
		// Captured stdout/stderr can carry secrets, so -- unlike list-units/status,
		// which expose only unit names and states -- reading logs needs the same
		// privilege as mutation.
		if !s.authorize(conn) {
			return Reply{Error: "permission denied"}
		}
		if req.Unit == "" {
			return Reply{Error: "logs: unit required"}
		}
		return Reply{OK: true, Lines: s.m.UnitLogs(req.Unit)}
	case "start", "stop", "restart", "daemon-reload", "boot-confirm", "reboot", "poweroff", "halt":
		if !s.authorize(conn) {
			return Reply{Error: "permission denied"}
		}
		return s.mutate(req)
	default:
		return Reply{Error: "unknown command: " + req.Cmd}
	}
}

func (s *Server) mutate(req Request) Reply {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch req.Cmd {
	case "start":
		if err := s.m.StartUnit(ctx, req.Unit); err != nil {
			return Reply{Error: err.Error()}
		}
	case "stop":
		if err := s.m.StopUnit(ctx, req.Unit); err != nil {
			return Reply{Error: err.Error()}
		}
	case "restart":
		_ = s.m.StopUnit(ctx, req.Unit)
		if err := s.m.StartUnit(ctx, req.Unit); err != nil {
			return Reply{Error: err.Error()}
		}
	case "daemon-reload", "boot-confirm":
		// Accepted for wire compatibility; unit reload and boot confirmation
		// are not handled by the init.
	case "reboot":
		return s.shutdownAction(syscall.SIGINT) // waitForShutdown maps SIGINT -> reboot
	case "poweroff", "halt":
		return s.shutdownAction(syscall.SIGTERM)
	}
	return Reply{OK: true, State: s.m.State(req.Unit)}
}

// shutdownAction asks PID 1 to run its ordered shutdown then reboot(2)/power off.
// It signals the init (self) so the single tested shutdown path in waitForShutdown
// runs; it refuses when not running as the init, so a dev/test invocation cannot
// signal the host's real PID 1.
func (s *Server) shutdownAction(sig syscall.Signal) Reply {
	if os.Getpid() != 1 {
		return Reply{Error: "reboot/poweroff is only available to the init (pid 1)"}
	}
	if err := syscall.Kill(1, sig); err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true}
}

// authorize permits mutation only to root or the uid the server runs as,
// determined from the peer credentials of the unix socket.
func (s *Server) authorize(conn net.Conn) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var uid uint32
	var inner error
	if err := raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			inner = e
			return
		}
		uid = cred.Uid
	}); err != nil || inner != nil {
		return false
	}
	return uid == 0 || uid == s.selfUID
}
