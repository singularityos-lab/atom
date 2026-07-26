package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/reaper"
)

// A unit gated on ConditionPathExists must be skipped, not started, when the
// path is absent, and started when it is present. Before this existed the
// directive was parsed by nothing and the unit ran regardless, so a marker
// meant to gate it was inert. The "!" form must skip when the path DOES exist.
func TestConditionPathExists(t *testing.T) {
	requireBins(t, "/bin/sh")
	reg := reaper.NewRegistry()
	rp := reaper.NewReaper(reg)
	go rp.Run()
	defer rp.Stop()
	old := ReaperWait
	ReaperWait = reg.Wait
	defer func() { ReaperWait = old }()

	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "absent")

	run := func(name string, conds []string) (State, bool) {
		marker := filepath.Join(t.TempDir(), "ran")
		s := New(Config{
			Name:                name,
			Type:                TypeOneshot,
			ExecStart:           []ExecCommand{cmd(t, "/bin/sh -c 'echo x >> "+marker+"; sleep 1'")},
			ConditionPathExists: conds,
			RemainAfterExit:     true,
		})
		w := &sinkWriter{}
		s.Stdout, s.Stderr = w, w
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		st := s.State()
		_ = s.Stop(context.Background())
		_, err := os.Stat(marker)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return st, err == nil
	}

	if st, ran := run("gate-absent.service", []string{absent}); st == Active || ran {
		t.Errorf("required path absent: state=%v ran=%v, want skipped/inactive", st, ran)
	}
	if st, ran := run("gate-present.service", []string{present}); st != Active || !ran {
		t.Errorf("required path present: state=%v ran=%v, want active", st, ran)
	}
	if st, ran := run("gate-negated.service", []string{"!" + present}); st == Active || ran {
		t.Errorf("negated condition on existing path: state=%v ran=%v, want skipped", st, ran)
	}
	if st, ran := run("gate-negated-ok.service", []string{"!" + absent}); st != Active || !ran {
		t.Errorf("negated condition on absent path: state=%v ran=%v, want active", st, ran)
	}
	if st, ran := run("gate-trigger.service", []string{"|" + absent, "|" + present}); st != Active || !ran {
		t.Errorf("one trigger path present: state=%v ran=%v, want active", st, ran)
	}
	if st, ran := run("gate-trigger-absent.service", []string{"|" + absent, "|" + filepath.Join(dir, "also-absent")}); st == Active || ran {
		t.Errorf("all trigger paths absent: state=%v ran=%v, want skipped", st, ran)
	}

	noExec := New(Config{Name: "gate-no-exec.service", ConditionPathExists: []string{absent}})
	if err := noExec.Start(context.Background()); err != nil {
		t.Fatalf("unmet condition must skip before ExecStart validation: %v", err)
	}
}
