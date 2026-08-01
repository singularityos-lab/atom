package control

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/singularityos-lab/atom/internal/core"
)

const (
	DefaultSDBSocket  = "/run/atom/sdb-control.sock"
	defaultSDBUnit    = "sdbd.service"
	defaultSDBMarker  = "/var/lib/sinty-sdb/enabled"
	defaultBrokerExe  = "/usr/bin/ush-broker"
	defaultSDBGroup   = "singularity-session"
	defaultMinimumUID = 1000
)

// SDBConfig defines the single gated service exposed to the desktop broker.
// The dedicated socket never accepts arbitrary unit names or init operations.
type SDBConfig struct {
	Unit             string
	Marker           string
	BrokerExecutable string
	GroupID          int
	MinimumUID       uint32
}

// DefaultSDBConfig resolves the session group used by the installed image.
func DefaultSDBConfig() (SDBConfig, error) {
	group, err := user.LookupGroup(defaultSDBGroup)
	if err != nil {
		return SDBConfig{}, fmt.Errorf("look up %s group: %w", defaultSDBGroup, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return SDBConfig{}, fmt.Errorf("parse %s gid: %w", defaultSDBGroup, err)
	}
	return SDBConfig{
		Unit:             defaultSDBUnit,
		Marker:           defaultSDBMarker,
		BrokerExecutable: defaultBrokerExe,
		GroupID:          gid,
		MinimumUID:       defaultMinimumUID,
	}, nil
}

// ListenSDB creates the narrow control socket used by ush-broker.
func ListenSDB(path string, m *core.Manager, cfg SDBConfig) (*SDBServer, error) {
	if cfg.Unit == "" || cfg.Marker == "" || cfg.BrokerExecutable == "" ||
		cfg.GroupID < 0 || cfg.MinimumUID == 0 {
		return nil, fmt.Errorf("incomplete sdb control configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, 0, cfg.GroupID); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set sdb control group: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set sdb control mode: %w", err)
	}
	return &SDBServer{ln: ln, m: m, cfg: cfg}, nil
}

// SDBServer serves only status, enable and disable for sdbd.service.
type SDBServer struct {
	ln  net.Listener
	m   *core.Manager
	cfg SDBConfig
}

func (s *SDBServer) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *SDBServer) Close() error { return s.ln.Close() }

func (s *SDBServer) handle(conn net.Conn) {
	defer conn.Close()
	defer func() { _ = recover() }()
	_ = conn.SetDeadline(time.Now().Add(70 * time.Second))

	var req Request
	if err := readFrame(conn, &req); err != nil {
		return
	}
	if !s.authorize(conn) {
		_ = writeFrame(conn, Reply{Error: "permission denied"})
		return
	}

	switch req.Cmd {
	case "sdb-status":
		_ = writeFrame(conn, Reply{OK: true, State: s.m.State(s.cfg.Unit)})
	case "sdb-enable":
		_ = writeFrame(conn, s.setEnabled(true))
	case "sdb-disable":
		_ = writeFrame(conn, s.setEnabled(false))
	default:
		_ = writeFrame(conn, Reply{Error: "unknown command: " + req.Cmd})
	}
}

func (s *SDBServer) setEnabled(on bool) Reply {
	if on {
		if err := os.MkdirAll(filepath.Dir(s.cfg.Marker), 0o755); err != nil {
			return Reply{Error: err.Error()}
		}
		f, err := os.OpenFile(s.cfg.Marker, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return Reply{Error: err.Error()}
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(s.cfg.Marker)
			_ = syncMarkerDir(s.cfg.Marker)
			return Reply{Error: err.Error()}
		}
		if err := f.Close(); err != nil {
			return Reply{Error: err.Error()}
		}
		if err := syncMarkerDir(s.cfg.Marker); err != nil {
			_ = os.Remove(s.cfg.Marker)
			_ = syncMarkerDir(s.cfg.Marker)
			return Reply{Error: err.Error()}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.m.StartUnit(ctx, s.cfg.Unit); err != nil {
			_ = os.Remove(s.cfg.Marker)
			_ = syncMarkerDir(s.cfg.Marker)
			return Reply{Error: err.Error()}
		}
		return Reply{OK: true, State: s.m.State(s.cfg.Unit)}
	}

	if err := os.Remove(s.cfg.Marker); err != nil {
		if !os.IsNotExist(err) {
			return Reply{Error: err.Error()}
		}
	} else if err := syncMarkerDir(s.cfg.Marker); err != nil {
		return Reply{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.m.StopUnit(ctx, s.cfg.Unit); err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true, State: s.m.State(s.cfg.Unit)}
}

func syncMarkerDir(marker string) error {
	dir, err := os.Open(filepath.Dir(marker))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *SDBServer) authorize(conn net.Conn) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var inner error
	if err := raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || inner != nil || cred == nil {
		return false
	}
	return s.authorizePeer(cred)
}

func (s *SDBServer) authorizePeer(cred *syscall.Ucred) bool {
	if cred.Uid == 0 {
		return true
	}
	if cred.Uid < s.cfg.MinimumUID {
		return false
	}
	peerExe, err := os.Stat(filepath.Join("/proc", strconv.Itoa(int(cred.Pid)), "exe"))
	if err != nil {
		return false
	}
	trustedExe, err := os.Stat(s.cfg.BrokerExecutable)
	if err != nil {
		return false
	}
	return os.SameFile(peerExe, trustedExe)
}
