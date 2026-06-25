package notify

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Message is a decoded sd_notify datagram. Unknown keys are preserved in Raw.
type Message struct {
	Ready           bool
	Reloading       bool
	Stopping        bool
	Watchdog        bool
	WatchdogTrigger bool
	Barrier         bool
	FDStore         bool
	FDName          string
	Status          string
	Errno           int
	MainPID         int
	HasMainPID      bool
	Raw             map[string]string
}

// ParseMessage decodes a newline-separated sd_notify payload.
func ParseMessage(b []byte) Message {
	m := Message{Raw: map[string]string{}}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m.Raw[k] = v
		switch k {
		case "READY":
			m.Ready = v == "1"
		case "RELOADING":
			m.Reloading = v == "1"
		case "STOPPING":
			m.Stopping = v == "1"
		case "STATUS":
			m.Status = v
		case "ERRNO":
			if n, err := strconv.Atoi(v); err == nil {
				m.Errno = n
			}
		case "MAINPID":
			if n, err := strconv.Atoi(v); err == nil {
				m.MainPID = n
				m.HasMainPID = true
			}
		case "WATCHDOG":
			if v == "trigger" {
				m.WatchdogTrigger = true
			} else {
				m.Watchdog = v == "1"
			}
		case "FDSTORE":
			m.FDStore = v == "1"
		case "FDNAME":
			m.FDName = v
		case "BARRIER":
			m.Barrier = v == "1"
		}
	}
	return m
}

// Env returns the NOTIFY_SOCKET environment assignment a child needs to reach
// this socket. An abstract socket path begins with '@'.
func Env(path string) string { return "NOTIFY_SOCKET=" + path }

// Received pairs a decoded message with the sender's credentials (when the
// kernel supplied them), used to enforce NotifyAccess.
type Received struct {
	Msg     Message
	PID     int
	UID     uint32
	GID     uint32
	HasCred bool
}

// Listener is a SOCK_DGRAM unix socket that receives sd_notify messages.
type Listener struct {
	conn *net.UnixConn
	path string
}

// NewListener binds a datagram unix socket at path (filesystem or, with a
// leading '@', abstract) and requests peer credentials on it.
func NewListener(path string) (*Listener, error) {
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	// World-writable so a service that setuids away from root (e.g. dbus-daemon
	// dropping to the dbus user) can still send READY=1. NotifyAccess is matched
	// by the sender's PID (SO_PASSCRED), never by uid, exactly as systemd does.
	if !strings.HasPrefix(path, "@") && !strings.HasPrefix(path, "\x00") {
		_ = os.Chmod(path, 0o666)
	}
	if raw, err := conn.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1)
		})
	}
	return &Listener{conn: conn, path: path}, nil
}

// Receive blocks for the next message and decodes any attached credentials.
func (l *Listener) Receive() (Received, error) {
	buf := make([]byte, 8192)
	oob := make([]byte, 1024)
	n, oobn, _, _, err := l.conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return Received{}, err
	}
	r := Received{Msg: ParseMessage(buf[:n])}
	if oobn > 0 {
		if scms, e := syscall.ParseSocketControlMessage(oob[:oobn]); e == nil {
			for i := range scms {
				if scms[i].Header.Level != syscall.SOL_SOCKET {
					continue
				}
				switch scms[i].Header.Type {
				case syscall.SCM_CREDENTIALS:
					if cred, e2 := syscall.ParseUnixCredentials(&scms[i]); e2 == nil {
						r.PID, r.UID, r.GID, r.HasCred = int(cred.Pid), cred.Uid, cred.Gid, true
					}
				case syscall.SCM_RIGHTS:
					// sd_notify_barrier (and FDSTORE) pass file descriptors via
					// SCM_RIGHTS. We don't use them; CLOSING our copies is what
					// satisfies the barrier -- a sender blocked in sd_notify_barrier
					// unblocks once the manager closes the pipe fd. Leaking them
					// (the previous behavior) hung real libsystemd sd_notify.
					if fds, e2 := syscall.ParseUnixRights(&scms[i]); e2 == nil {
						for _, fd := range fds {
							_ = syscall.Close(fd)
						}
					}
				}
			}
		}
	}
	return r, nil
}

// Close releases the socket.
func (l *Listener) Close() error { return l.conn.Close() }

// Path returns the socket path.
func (l *Listener) Path() string { return l.path }
