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
