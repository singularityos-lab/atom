package syslogd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the daemon configuration.
type Config struct {
	SocketPath string // datagram socket to serve; default /dev/log
	OutputPath string // file to append records to; default /var/log/messages
	MaxBytes   int64  // truncate-and-restart the file past this size; default 8 MiB
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

func (c *Config) applyDefaults() {
	if c.SocketPath == "" {
		c.SocketPath = "/dev/log"
	}
	if c.OutputPath == "" {
		c.OutputPath = "/var/log/messages"
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 8 << 20
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Server is a running syslog daemon.
type Server struct {
	cfg  Config
	conn *net.UnixConn
	out  *os.File
	mu   sync.Mutex
	size int64
}

// New binds the datagram socket and opens the output file. It removes a stale
// socket first (a crashed predecessor leaves the path bound).
func New(cfg Config) (*Server, error) {
	cfg.applyDefaults()
	if !strings.HasPrefix(cfg.SocketPath, "@") {
		_ = os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o755)
		_ = os.Remove(cfg.SocketPath)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: cfg.SocketPath, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("syslogd: bind %s: %w", cfg.SocketPath, err)
	}
	// World-writable so unprivileged services can log, exactly as /dev/log is.
	if !strings.HasPrefix(cfg.SocketPath, "@") {
		_ = os.Chmod(cfg.SocketPath, 0o666)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.OutputPath), 0o755); err != nil {
		conn.Close()
		return nil, err
	}
	out, err := os.OpenFile(cfg.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		conn.Close()
		return nil, err
	}
	st, _ := out.Stat()
	s := &Server{cfg: cfg, conn: conn, out: out}
	if st != nil {
		s.size = st.Size()
	}
	return s, nil
}

// Serve reads datagrams until the socket is closed.
func (s *Server) Serve() error {
	buf := make([]byte, 64<<10)
	for {
		n, _, err := s.conn.ReadFromUnix(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if e := s.write(format(s.cfg.Now(), buf[:n])); e != nil {
			// Logging must not crash the logger; report and keep serving.
			fmt.Fprintf(os.Stderr, "syslogd: write: %v\n", e)
		}
	}
}

// Close releases the socket and output file.
func (s *Server) Close() error {
	s.conn.Close()
	return s.out.Close()
}

// write appends a record, truncating the file when it exceeds MaxBytes so the
// log can never fill the disk (a simple bounded ring, no rotation files).
func (s *Server) write(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.size+int64(len(line)) > s.cfg.MaxBytes {
		if err := s.out.Truncate(0); err != nil {
			return err
		}
		if _, err := s.out.Seek(0, 0); err != nil {
			return err
		}
		s.size = 0
	}
	nn, err := s.out.WriteString(line)
	s.size += int64(nn)
	return err
}

// format decodes a syslog datagram into a stable log line: an ISO-8601 timestamp,
// the decoded priority (facility.severity), and the message. A leading "<PRI>" is
// parsed when present; otherwise the raw payload is kept.
func format(now time.Time, raw []byte) string {
	msg := strings.TrimRight(string(raw), "\n\x00")
	pri := ""
	if strings.HasPrefix(msg, "<") {
		if end := strings.IndexByte(msg, '>'); end > 1 {
			if p, err := strconv.Atoi(msg[1:end]); err == nil {
				pri = fmt.Sprintf("%d.%d ", p/8, p%8) // facility.severity
				msg = msg[end+1:]
			}
		}
	}
	return now.UTC().Format(time.RFC3339) + " " + pri + msg + "\n"
}
