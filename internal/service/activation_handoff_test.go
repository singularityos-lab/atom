package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/singularityos-lab/atom/internal/socketact"
	"github.com/singularityos-lab/atom/internal/unit"
)

// TestMain lets this test binary act as the sd-exec trampoline when re-exec'd
// with SelfExe pointed at it, so socket activation can be verified end-to-end
// without a VM.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sd-exec" {
		os.Exit(socketact.SdExec(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestSocketActivationHandoff(t *testing.T) {
	requireBins(t, "/bin/sh")
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "bus.sock")
	result := filepath.Join(dir, "result")

	f, err := unit.Parse("act.socket", strings.NewReader("[Socket]\nListenStream="+sockPath+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	sock, err := socketact.FromFile(f)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := sock.Open()
	if err != nil {
		t.Fatalf("open listener: %v", err)
	}
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()

	old := SelfExe
	SelfExe = os.Args[0] // re-exec THIS test binary as the sd-exec trampoline
	defer func() { SelfExe = old }()

	s := New(Config{
		Name: "act.service",
		Type: TypeSimple,
		ExecStart: []ExecCommand{cmd(t,
			"/bin/sh -c 'echo FDS=$LISTEN_FDS PID=$LISTEN_PID SELF=$$ NAMES=$LISTEN_FDNAMES > "+result+"; exec sleep 5'")},
	})
	s.Listeners = ls
	s.FDName = sock.FDName

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(context.Background())

	// Wait for the activated service to write the file.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(result); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("activated service never wrote its result: %v", err)
	}
	out := strings.TrimSpace(string(data))
	t.Logf("activated env: %s", out)

	// LISTEN_FDS=1 proves the listening fd was handed over; PID==SELF proves
	// LISTEN_PID equals the FINAL exec'd process (the sd-exec trampoline point).
	fields := map[string]string{}
	for _, kv := range strings.Fields(out) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			fields[k] = v
		}
	}
	if fields["FDS"] != "1" {
		t.Errorf("LISTEN_FDS = %q, want 1", fields["FDS"])
	}
	if fields["NAMES"] != "act.socket" {
		t.Errorf("LISTEN_FDNAMES = %q, want act.socket", fields["NAMES"])
	}
	if fields["PID"] == "" || fields["PID"] != fields["SELF"] {
		t.Errorf("LISTEN_PID=%q must equal the final process pid SELF=%q", fields["PID"], fields["SELF"])
	}
}
