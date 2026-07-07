package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

// TestLogsRequireAuth verifies that reading a unit's captured logs needs the same
// privilege as a mutation, while list-units/status stay open to any local peer.
// Captured stdout/stderr can hold secrets, so an unauthenticated `logs` read was a
// local information leak.
func TestLogsRequireAuth(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	udir := t.TempDir()
	writeUnit(t, udir, "foo.service", "[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	writeUnit(t, udir, "t.target", "[Unit]\nWants=foo.service\n")

	m, err := core.Build(&unit.Loader{Paths: []string{udir}}, "t.target")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "c.sock")
	srv, err := Listen(sock, m)
	if err != nil {
		t.Fatal(err)
	}
	// Make this test an unauthorized peer: the server's uid is set to neither root
	// nor our own uid before it starts serving (no concurrent write to selfUID).
	srv.selfUID = 4294967294
	go srv.Serve()
	defer srv.Close()

	rep, err := Send(sock, Request{Cmd: "logs", Unit: "foo.service"})
	if err != nil {
		t.Fatalf("send logs: %v", err)
	}
	if rep.OK || rep.Error != "permission denied" {
		t.Errorf("unauthorized logs: got %+v, want permission denied", rep)
	}

	// Introspection (unit names + states) stays readable to any local peer.
	rep, err = Send(sock, Request{Cmd: "status", Unit: "foo.service"})
	if err != nil || !rep.OK {
		t.Fatalf("status should stay open to any peer: err=%v rep=%+v", err, rep)
	}
}
