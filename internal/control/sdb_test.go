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

	devGate := filepath.Join(dir, "dev.enabled")
	marker := filepath.Join(dir, "state", "enabled")
	sock := filepath.Join(dir, "sdb.sock")
	srv, err := ListenSDB(sock, m, SDBConfig{
		Unit:             "sdbd.service",
		Marker:           marker,
		DevelopmentGate:  devGate,
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
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("enable succeeded without development mode")
	}
	if err := os.WriteFile(devGate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err = Send(sock, Request{Cmd: "sdb-enable"})
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
