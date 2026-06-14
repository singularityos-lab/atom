package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

func writeUnit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestControlRoundTrip(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	udir := t.TempDir()
	writeUnit(t, udir, "foo.service", "[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	writeUnit(t, udir, "t.target", "[Unit]\nWants=foo.service\n")

	loader := &unit.Loader{Paths: []string{udir}}
	m, err := core.Build(loader, "t.target")
	if err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(t.TempDir(), "c.sock")
	srv, err := Listen(sock, m)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	// list-units
	rep, err := Send(sock, Request{Cmd: "list-units"})
	if err != nil || !rep.OK {
		t.Fatalf("list-units: err=%v rep=%+v", err, rep)
	}
	if len(rep.Units) < 2 {
		t.Errorf("list-units returned %d units, want >=2", len(rep.Units))
	}

	// start foo.service (we run as our own uid, so authorize allows it)
	rep, err = Send(sock, Request{Cmd: "start", Unit: "foo.service"})
	if err != nil || !rep.OK {
		t.Fatalf("start: err=%v rep=%+v", err, rep)
	}

	// status
	rep, err = Send(sock, Request{Cmd: "status", Unit: "foo.service"})
	if err != nil || !rep.OK {
		t.Fatalf("status: err=%v rep=%+v", err, rep)
	}
	if rep.State != "active" {
		t.Errorf("foo.service state = %q, want active (oneshot RemainAfterExit)", rep.State)
	}

	// unknown command
	rep, err = Send(sock, Request{Cmd: "bogus"})
	if err != nil {
		t.Fatalf("send bogus: %v", err)
	}
	if rep.OK || rep.Error == "" {
		t.Errorf("bogus command should error, got %+v", rep)
	}
}
