package socketact

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/singularityos-lab/atom/internal/unit"
)

// Kind is the socket family of a listen directive.
type Kind int

const (
	Stream   Kind = iota // ListenStream= (SOCK_STREAM: unix or tcp)
	Datagram             // ListenDatagram= (SOCK_DGRAM)
	Netlink              // ListenNetlink= (AF_NETLINK, e.g. kobject-uevent)
)

// Addr is one parsed listen directive.
type Addr struct {
	Kind    Kind
	Network string // "unix", "tcp", "udp", "unixgram", "netlink"
	Address string // path or host:port (or the raw ListenNetlink= spec)
	// Netlink-only: protocol family, multicast group mask, and the receive
	// buffer to force (ReceiveBuffer=), which matters for udev coldplug where a
	// small buffer drops uevents.
	NLProto  int
	NLGroups uint32
	RcvBuf   int
}

// Socket is the activation-relevant projection of a .socket unit.
type Socket struct {
	Name string
	// Service is the unit activated on connection. Defaults to the socket's
	// prefix + ".service".
	Service       string
	FDName        string // FileDescriptorName=, defaults to the socket name
	ReceiveBuffer int    // ReceiveBuffer= in bytes (0 = unset)
	Addrs         []Addr
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
	if rb, ok := f.Get("Socket", "ReceiveBuffer"); ok {
		s.ReceiveBuffer = parseSize(rb)
	}
	for _, v := range f.List("Socket", "ListenNetlink") {
		a, err := parseNetlinkAddr(v)
		if err != nil {
			return nil, err
		}
		a.RcvBuf = s.ReceiveBuffer
		s.Addrs = append(s.Addrs, a)
	}
	return s, nil
}

// parseNetlinkAddr parses a ListenNetlink= spec: a family name ("kobject-uevent",
// "route") or numeric protocol, optionally followed by a multicast group mask.
func parseNetlinkAddr(v string) (Addr, error) {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return Addr{}, fmt.Errorf("empty netlink spec")
	}
	var proto int
	switch fields[0] {
	case "kobject-uevent":
		proto = 15 // NETLINK_KOBJECT_UEVENT
	case "route":
		proto = 0 // NETLINK_ROUTE
	case "audit":
		proto = 9 // NETLINK_AUDIT
	default:
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			return Addr{}, fmt.Errorf("unknown netlink family %q", fields[0])
		}
		proto = n
	}
	var groups uint32
	if len(fields) > 1 {
		g, err := strconv.ParseUint(fields[1], 0, 32)
		if err != nil {
			return Addr{}, fmt.Errorf("bad netlink group %q", fields[1])
		}
		groups = uint32(g)
	}
	return Addr{Kind: Netlink, Network: "netlink", Address: v, NLProto: proto, NLGroups: groups}, nil
}

// parseSize parses a systemd size like "128M", "64K", "1G" into bytes.
func parseSize(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	mult := 1
	switch v[len(v)-1] {
	case 'K', 'k':
		mult, v = 1<<10, v[:len(v)-1]
	case 'M', 'm':
		mult, v = 1<<20, v[:len(v)-1]
	case 'G', 'g':
		mult, v = 1<<30, v[:len(v)-1]
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n * mult
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
	Name   string // LISTEN_FDNAMES entry for this fd (the socket's FDName)
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
		l.Name = s.FDName
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
	case Netlink:
		return openNetlink(a)
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

// openNetlink opens and binds an AF_NETLINK socket (e.g. NETLINK_KOBJECT_UEVENT for
// udev coldplug), forcing the receive buffer so a flood of coldplug uevents is not
// dropped, and returns it as an inheritable listener fd.
func openNetlink(a Addr) (*Listener, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, a.NLProto)
	if err != nil {
		return nil, err
	}
	if a.RcvBuf > 0 {
		// SO_RCVBUFFORCE bypasses the rmem_max cap (we run as root); fall back to
		// SO_RCVBUF if it is refused.
		if e := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, a.RcvBuf); e != nil {
			_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, a.RcvBuf)
		}
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK, Groups: a.NLGroups}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &Listener{File: os.NewFile(uintptr(fd), "netlink:"+a.Address)}, nil
}
