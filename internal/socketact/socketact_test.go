package socketact

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singularityos-lab/atom/internal/unit"
)

func TestSdExecPlan(t *testing.T) {
	n, argv, env, err := sdExecPlan([]string{"2", "a:b", "--", "/bin/svc", "-x"}, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2", n)
	}
	if strings.Join(argv, " ") != "/bin/svc -x" {
		t.Errorf("argv = %v", argv)
	}
	want := map[string]bool{"LISTEN_PID=4242": true, "LISTEN_FDS=2": true, "LISTEN_FDNAMES=a:b": true}
	for _, e := range env {
		delete(want, e)
	}
	if len(want) != 0 {
		t.Errorf("missing LISTEN env entries: %v (got %v)", want, env)
	}

	if _, _, _, err := sdExecPlan([]string{"1", "x"}, 1); err == nil {
		t.Error("expected error when -- cmd is missing")
	}
}

func TestActivationCommandArgs(t *testing.T) {
	cmd := Command("/usr/lib/atom/atom", []*Listener{{Name: "foo.socket"}, {Name: "foo.socket"}},
		[]string{"/bin/svc", "--serve"}, nil)
	got := strings.Join(cmd.Args, " ")
	want := "/usr/lib/atom/atom sd-exec 2 foo.socket:foo.socket -- /bin/svc --serve"
	if got != want {
		t.Errorf("command args = %q, want %q", got, want)
	}
	if len(cmd.ExtraFiles) != 2 {
		t.Errorf("ExtraFiles = %d, want 2", len(cmd.ExtraFiles))
	}
}

func TestParseAddr(t *testing.T) {
	cases := []struct {
		in      string
		network string
		address string
	}{
		{"/run/foo.sock", "unix", "/run/foo.sock"},
		{":8080", "tcp", ":8080"},
		{"8080", "tcp", ":8080"},
		{"127.0.0.1:9000", "tcp", "127.0.0.1:9000"},
	}
	for _, c := range cases {
		a, err := parseAddr(c.in, Stream)
		if err != nil {
			t.Fatalf("parseAddr(%q): %v", c.in, err)
		}
		if a.Network != c.network || a.Address != c.address {
			t.Errorf("parseAddr(%q) = %s/%s, want %s/%s", c.in, a.Network, a.Address, c.network, c.address)
		}
	}
	if a, _ := parseAddr("@abstract", Stream); a.Address != "\x00abstract" {
		t.Errorf("abstract socket address = %q", a.Address)
	}
}

func TestFromFileDefaults(t *testing.T) {
	f, _ := unit.Parse("foo.socket", strings.NewReader("[Socket]\nListenStream=/run/foo.sock\n"))
	s, err := FromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Service != "foo.service" {
		t.Errorf("Service = %q, want foo.service (derived from prefix)", s.Service)
	}
	if s.FDName != "foo.socket" {
		t.Errorf("FDName = %q", s.FDName)
	}
	if len(s.Addrs) != 1 {
		t.Fatalf("addrs = %v", s.Addrs)
	}
}

func TestOpenUnixStream(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	f, _ := unit.Parse("svc.socket", strings.NewReader("[Socket]\nListenStream="+sock+"\n"))
	s, err := FromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	if len(ls) != 1 {
		t.Fatalf("got %d listeners", len(ls))
	}

	// Reconstruct a listener from the passed fd and prove it accepts.
	ln, err := net.FileListener(ls[0].File)
	if err != nil {
		t.Fatalf("FileListener: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Write([]byte("hi"))
			c.Close()
		}
	}()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	buf := make([]byte, 2)
	if _, err := c.Read(buf); err != nil || string(buf) != "hi" {
		t.Errorf("read = %q, %v", buf, err)
	}
}

func TestParseNetlink(t *testing.T) {
	a, err := parseNetlinkAddr("kobject-uevent 1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != Netlink || a.NLProto != 15 || a.NLGroups != 1 {
		t.Errorf("netlink parse = %+v, want Netlink proto15 groups1", a)
	}
	if parseSize("128M") != 128<<20 || parseSize("64K") != 64<<10 || parseSize("") != 0 {
		t.Errorf("parseSize wrong: 128M=%d 64K=%d", parseSize("128M"), parseSize("64K"))
	}
	if _, err := parseNetlinkAddr(""); err == nil {
		t.Error("empty netlink spec should error")
	}
}
