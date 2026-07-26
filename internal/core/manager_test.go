package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singularityos-lab/atom/internal/unit"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOrderedBootViaOneshot uses oneshot services (whose Start blocks until the
// command completes) so the recorded execution order is a deterministic witness
// that the scheduler honored After=.
func TestOrderedBootViaOneshot(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "order.log")

	mkSvc := func(name, after string) string {
		s := "[Unit]\n"
		if after != "" {
			s += "After=" + after + "\n"
		}
		s += "[Service]\nType=oneshot\n"
		s += "ExecStart=/bin/sh -c 'echo " + name + " >> " + log + "'\n"
		return s
	}
	write(t, filepath.Join(dir, "c.service"), mkSvc("c", ""))
	write(t, filepath.Join(dir, "b.service"), mkSvc("b", "c.service"))
	write(t, filepath.Join(dir, "a.service"), mkSvc("a", "b.service"))
	write(t, filepath.Join(dir, "boot.target"),
		"[Unit]\nWants=a.service b.service c.service\n")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "boot.target")
	if err != nil {
		t.Fatal(err)
	}

	// The graph closure should hold target + 3 services.
	if _, ok := m.Get("a.service"); !ok {
		t.Fatal("a.service not loaded")
	}

	if err := m.StartTarget(context.Background(), "boot.target"); err != nil {
		t.Fatalf("StartTarget: %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	got := strings.Fields(strings.TrimSpace(string(data)))
	want := []string{"c", "b", "a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("execution order = %v, want %v", got, want)
	}
}

func TestFailingUnitDoesNotAbortBoot(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "good.service"),
		"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	write(t, filepath.Join(dir, "bad.service"), "[Service]\nType=oneshot\n") // no ExecStart -> fails
	write(t, filepath.Join(dir, "t.target"), "[Unit]\nWants=good.service bad.service\n")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "t.target")
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	m.OnUnitError = func(n string, e error) { failed = append(failed, n) }

	if err := m.StartTarget(context.Background(), "t.target"); err != nil {
		t.Fatalf("StartTarget should not abort: %v", err)
	}
	// good.service must still come up despite bad.service failing.
	if m.State("good.service") != "active" {
		t.Errorf("good.service = %s, want active (a sibling failure must not abort)", m.State("good.service"))
	}
	found := false
	for _, f := range failed {
		if f == "bad.service" {
			found = true
		}
	}
	if !found {
		t.Errorf("bad.service failure not reported via OnUnitError: %v", failed)
	}
}

func TestMissingEnabledUnitSkipped(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "real.service"),
		"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	write(t, filepath.Join(dir, "t.target"), "[Unit]\n")
	// Enable both via .wants symlink dir, but ghost.service has no unit file.
	write(t, filepath.Join(dir, "t.target.wants", "real.service"), "")
	write(t, filepath.Join(dir, "t.target.wants", "ghost.service"), "")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "t.target")
	if err != nil {
		t.Fatal(err)
	}
	miss := m.Missing()
	if len(miss) != 1 || miss[0] != "ghost.service" {
		t.Errorf("Missing() = %v, want [ghost.service]", miss)
	}
	if m.State("ghost.service") != "not-found" {
		t.Errorf("ghost state = %s, want not-found", m.State("ghost.service"))
	}
	var failures []string
	m.OnUnitError = func(n string, e error) { failures = append(failures, n) }
	if err := m.StartTarget(context.Background(), "t.target"); err != nil {
		t.Fatal(err)
	}
	// The ghost must NOT be reported as a failed start, and real must come up.
	for _, f := range failures {
		if f == "ghost.service" {
			t.Errorf("missing unit reported as a failure (should be skipped): %v", failures)
		}
	}
	if m.State("real.service") != "active" {
		t.Errorf("real.service = %s, want active", m.State("real.service"))
	}
}

func TestTargetAliasPullsEnabledUnits(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "graphical.target"), "[Unit]\n")
	write(t, filepath.Join(dir, "graphical.target.wants", "compositor.service"), "")
	write(t, filepath.Join(dir, "compositor.service"),
		"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	if err := os.Symlink("/nonexistent/usr/lib/systemd/system/graphical.target", filepath.Join(dir, "default.target")); err != nil {
		t.Fatal(err)
	}

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "default.target")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("compositor.service"); !ok {
		t.Fatal("compositor.service not pulled in through graphical.target.wants")
	}
	if err := m.StartTarget(context.Background(), "default.target"); err != nil {
		t.Fatal(err)
	}
	if st := m.State("compositor.service"); st != "active" {
		t.Errorf("compositor.service = %s, want active", st)
	}
}

// TestConflictStopsActiveUnit proves Conflicts= is enforced at activation: an
// active unit (a stand-in for the boot splash holding a device) is torn down
// when a unit that Conflicts= it starts. This is what lets the compositor take
// DRM master from the splash under sinit, where logind no longer drives it.
func TestConflictStopsActiveUnit(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "splash.service"),
		"[Service]\nType=simple\nExecStart=/bin/sh -c 'sleep 30'\n")
	write(t, filepath.Join(dir, "greetd.service"),
		"[Unit]\nConflicts=splash.service\nAfter=splash.service\n"+
			"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	write(t, filepath.Join(dir, "gfx.target"),
		"[Unit]\nWants=splash.service greetd.service\n")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "gfx.target")
	if err != nil {
		t.Fatal(err)
	}
	var stopped []string
	m.OnUnitStop = func(n, _ string) { stopped = append(stopped, n) }
	if err := m.StartTarget(context.Background(), "gfx.target"); err != nil {
		t.Fatal(err)
	}
	if st := m.State("splash.service"); st != "inactive" {
		t.Errorf("splash = %s, want inactive (greetd Conflicts= must stop it)", st)
	}
	found := false
	for _, s := range stopped {
		if s == "splash.service" {
			found = true
		}
	}
	if !found {
		t.Errorf("OnUnitStop not fired for splash: %v", stopped)
	}
}

func TestManagerBuildsClosureAndState(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x.service"),
		"[Unit]\nRequires=dep.service\nAfter=dep.service\n[Service]\nType=oneshot\nExecStart=/bin/true\n")
	write(t, filepath.Join(dir, "dep.service"),
		"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "x.service")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("dep.service"); !ok {
		t.Fatal("dependency not pulled into closure")
	}
	if err := m.StartTarget(context.Background(), "x.service"); err != nil {
		t.Fatal(err)
	}
	if st := m.State("dep.service"); st != "active" {
		t.Errorf("dep state = %s, want active (RemainAfterExit)", st)
	}
}

// TestBrokenUnitDoesNotAbortBuild is the regression for a single unit whose
// config cannot be parsed (here an unterminated ExecStart quote) must NOT make Build
// return an error. As PID 1 an aborted build hangs the machine unbootable; instead the
// broken unit is marked missing/inert and the rest of the target still builds + starts.
func TestBrokenUnitDoesNotAbortBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken.service"),
		"[Service]\nExecStart=/bin/echo \"unterminated\n")
	write(t, filepath.Join(dir, "t.target"), "[Unit]\n")
	write(t, filepath.Join(dir, "t.target.wants", "broken.service"), "")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "t.target")
	if err != nil {
		t.Fatalf("Build must not fail on one broken unit: %v", err)
	}
	found := false
	for _, x := range m.Missing() {
		if x == "broken.service" {
			found = true
		}
	}
	if !found {
		t.Errorf("broken.service should be marked missing, got Missing()=%v", m.Missing())
	}
	if err := m.StartTarget(context.Background(), "t.target"); err != nil {
		t.Fatalf("StartTarget must not fail on the inert broken unit: %v", err)
	}
}
