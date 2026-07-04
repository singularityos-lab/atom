package socketact

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Kind is the socket family of a listen directive.
type Kind int

const (
	Stream   Kind = iota // ListenStream= (SOCK_STREAM: unix or tcp)
	Datagram             // ListenDatagram= (SOCK_DGRAM)
)

// Addr is one parsed listen directive.
type Addr struct {
	Kind    Kind
	Network string // "unix", "tcp", "udp", "unixgram"
	Address string // path or host:port
}

// Socket is the activation-relevant projection of a .socket unit.
type Socket struct {
	Name string
	// Service is the unit activated on connection. Defaults to the socket's
	// prefix + ".service".
	Service string
	FDName  string      // FileDescriptorName=, defaults to the socket name
	Mode    os.FileMode // SocketMode=, applied to pathname sockets (default 0666)
	Addrs   []Addr
}

// FromFile projects a parsed .socket unit.
func FromFile(f *unit.File) (*Socket, error) {
	s := &Socket{
		Name:   f.Name,
		FDName: f.GetDefault("Socket", "FileDescriptorName", f.Name),
	}
	if svc, ok := f.Get("Socket", "Service"); ok {
		s.Service = svc
	} else {
		s.Service = f.Prefix + ".service"
	}

	// SocketMode= (octal), defaulting to systemd's 0666 so a pathname socket is
	// connectable by non-root. Without it net.Listen leaves the socket at the
	// umask (typically 0755), which is exactly what makes the dbus system bus
	// unreachable for the user under this init.
	s.Mode = 0o666
	if v, ok := f.Get("Socket", "SocketMode"); ok {
		if m, err := strconv.ParseUint(strings.TrimSpace(v), 8, 32); err == nil {
			s.Mode = os.FileMode(m)
		}
	}

	for _, v := range f.List("Socket", "ListenStream") {
		a, err := parseAddr(v, Stream)
		if err != nil {
			return nil, err
		}
		s.Addrs = append(s.Addrs, a)
	}
	for _, v := range f.List("Socket", "ListenDatagram") {
		a, err := parseAddr(v, Datagram)
		if err != nil {
			return nil, err
		}
		s.Addrs = append(s.Addrs, a)
	}
	return s, nil
}

// parseAddr interprets a Listen* value the way systemd does: an absolute path or
// a leading '@' is a unix socket; "host:port", ":port", or a bare port number is
// an IP socket.
func parseAddr(v string, kind Kind) (Addr, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return Addr{}, fmt.Errorf("empty listen address")
	}

	unixNet, ipNet := "unix", "tcp"
	if kind == Datagram {
		unixNet, ipNet = "unixgram", "udp"
	}

	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "@") {
		addr := v
		if strings.HasPrefix(v, "@") { // abstract namespace
			addr = "\x00" + v[1:]
		}
		return Addr{Kind: kind, Network: unixNet, Address: addr}, nil
	}

	// bare port number => all interfaces
	if n, err := strconv.Atoi(v); err == nil {
		return Addr{Kind: kind, Network: ipNet, Address: fmt.Sprintf(":%d", n)}, nil
	}
	// host:port or :port
	return Addr{Kind: kind, Network: ipNet, Address: v}, nil
}

// Listener is an open listening fd plus the means to close it.
type Listener struct {
	File   *os.File
	closer func() error
}

// Close releases the listener.
func (l *Listener) Close() error {
	if l.closer != nil {
		return l.closer()
	}
	return l.File.Close()
}

// Open binds every address and returns the listening fds, in declaration order
// (which is the order they are presented to the service as LISTEN_FDS 3, 4, ...).
func (s *Socket) Open() ([]*Listener, error) {
	var out []*Listener
	cleanup := func() {
		for _, l := range out {
			l.Close()
		}
	}
	for _, a := range s.Addrs {
		l, err := openAddr(a)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("%s: listen %s %s: %w", s.Name, a.Network, a.Address, err)
		}
		if s.Mode != 0 && (a.Network == "unix" || a.Network == "unixgram") && !strings.HasPrefix(a.Address, "\x00") {
			_ = os.Chmod(a.Address, s.Mode)
		}
		out = append(out, l)
	}
	return out, nil
}

func openAddr(a Addr) (*Listener, error) {
	// Ensure the parent directory of a pathname unix socket exists (e.g.
	// /run/dbus for dbus.socket) before binding.
	if (a.Network == "unix" || a.Network == "unixgram") && !strings.HasPrefix(a.Address, "\x00") {
		_ = os.MkdirAll(filepath.Dir(a.Address), 0o755)
	}
	switch a.Kind {
	case Datagram:
		pc, err := net.ListenPacket(a.Network, a.Address)
		if err != nil {
			return nil, err
		}
		uc, ok := pc.(*net.UDPConn)
		if !ok {
			// unixgram
			if u, ok := pc.(*net.UnixConn); ok {
				f, err := u.File()
				if err != nil {
					pc.Close()
					return nil, err
				}
				return &Listener{File: f, closer: func() error { f.Close(); return pc.Close() }}, nil
			}
			pc.Close()
			return nil, fmt.Errorf("unsupported datagram listener %T", pc)
		}
		f, err := uc.File()
		if err != nil {
			pc.Close()
			return nil, err
		}
		return &Listener{File: f, closer: func() error { f.Close(); return pc.Close() }}, nil
	default:
		ln, err := net.Listen(a.Network, a.Address)
		if err != nil {
			return nil, err
		}
		var f *os.File
		switch t := ln.(type) {
		case *net.TCPListener:
			f, err = t.File()
		case *net.UnixListener:
			f, err = t.File()
		default:
			ln.Close()
			return nil, fmt.Errorf("unsupported stream listener %T", ln)
		}
		if err != nil {
			ln.Close()
			return nil, err
		}
		return &Listener{File: f, closer: func() error { f.Close(); return ln.Close() }}, nil
	}
}
