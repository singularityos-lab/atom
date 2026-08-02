package control

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

func TestSDBControlOwnsGateAndService(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the production socket owner check requires root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.Mkdir(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "base.target", "[Unit]\n")
	writeUnit(t, units, "sdbd.service", "[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	m, err := core.Build(&unit.Loader{Paths: []string{units}}, "base.target")
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(dir, "state", "enabled")
	sock := filepath.Join(dir, "sdb.sock")
	srv, err := ListenSDB(sock, m, SDBConfig{
		Unit:             "sdbd.service",
		Marker:           marker,
		BrokerExecutable: exe,
		GroupID:          os.Getgid(),
		MinimumUID:       1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	rep, err := Send(sock, Request{Cmd: "sdb-enable"})
	if err != nil || !rep.OK || rep.State != "active" {
		t.Fatalf("enable: err=%v rep=%+v", err, rep)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}

	rep, err = Send(sock, Request{Cmd: "sdb-disable"})
	if err != nil || !rep.OK || rep.State != "inactive" {
		t.Fatalf("disable: err=%v rep=%+v", err, rep)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker remains after disable: %v", err)
	}
}

func TestSDBControlChecksPeerIdentity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	srv := &SDBServer{cfg: SDBConfig{
		BrokerExecutable: exe,
		MinimumUID:       1000,
	}}
	if !srv.authorizePeer(&syscall.Ucred{Uid: 0, Pid: int32(os.Getpid())}) {
		t.Fatal("root peer refused")
	}
	if srv.authorizePeer(&syscall.Ucred{Uid: 102, Pid: int32(os.Getpid())}) {
		t.Fatal("system user accepted")
	}
	if !srv.authorizePeer(&syscall.Ucred{Uid: 1000, Pid: int32(os.Getpid())}) {
		t.Fatal("trusted executable refused")
	}

	copyPath := filepath.Join(t.TempDir(), "ush-broker")
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o755); err != nil {
		t.Fatal(err)
	}
	srv.cfg.BrokerExecutable = copyPath
	if srv.authorizePeer(&syscall.Ucred{Uid: 1000, Pid: int32(os.Getpid())}) {
		t.Fatal("different inode accepted")
	}
}

func TestSDBControlDelegatesOnlySessionPowerActions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the production socket owner check requires root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.Mkdir(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "base.target", "[Unit]\n")
	m, err := core.Build(&unit.Loader{Paths: []string{units}}, "base.target")
	if err != nil {
		t.Fatal(err)
	}

	srv, err := ListenSDB(filepath.Join(dir, "sdb.sock"), m, SDBConfig{
		Unit:             "sdbd.service",
		Marker:           filepath.Join(dir, "state", "enabled"),
		BrokerExecutable: exe,
		GroupID:          os.Getgid(),
		MinimumUID:       1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan syscall.Signal, 2)
	srv.shutdown = func(sig syscall.Signal) Reply {
		signals <- sig
		return Reply{OK: true}
	}
	go srv.Serve()
	defer srv.Close()

	for _, tc := range []struct {
		cmd  string
		want syscall.Signal
	}{
		{cmd: "session-reboot", want: syscall.SIGINT},
		{cmd: "session-poweroff", want: syscall.SIGTERM},
	} {
		rep, err := Send(filepath.Join(dir, "sdb.sock"), Request{Cmd: tc.cmd})
		if err != nil || !rep.OK {
			t.Fatalf("%s: err=%v rep=%+v", tc.cmd, err, rep)
		}
		if got := <-signals; got != tc.want {
			t.Fatalf("%s signal=%v, want %v", tc.cmd, got, tc.want)
		}
	}

	rep, err := Send(filepath.Join(dir, "sdb.sock"), Request{Cmd: "restart", Unit: "other.service"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Error == "" {
		t.Fatalf("arbitrary init command accepted: %+v", rep)
	}
}
