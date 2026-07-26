package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/unit"
)

// A unit deliberately shipped outside every target.wants (so it never starts at
// boot) must still be startable on demand: StartUnit loads it lazily. Before
// this, StartUnit only knew the boot graph, so an explicit start of such a unit
// returned "unknown unit" and the unit could never run. This matters for a gated
// service like the debug bridge, which must be startable by a request but must
// not start at boot.
func TestStartUnitLazyLoadsOffGraph(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "gate")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The off-graph unit, gated on a marker that IS present so it may run.
	write(t, filepath.Join(dir, "ondemand.service"),
		"[Unit]\nConditionPathExists="+marker+"\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	// A boot target that does NOT want ondemand.service.
	write(t, filepath.Join(dir, "boot.target"), "[Unit]\n")

	loader := &unit.Loader{Paths: []string{dir}}
	m, err := Build(loader, "boot.target")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("ondemand.service"); ok {
		t.Fatal("ondemand.service should not be in the boot graph")
	}
	if st := m.State("ondemand.service"); st != "inactive" {
		t.Fatalf("off-graph unit state = %q, want inactive", st)
	}
	if st := m.State("missing.service"); st != "unknown" {
		t.Fatalf("missing unit state = %q, want unknown", st)
	}
	if err := m.StartUnit(context.Background(), "ondemand.service"); err != nil {
		t.Fatalf("StartUnit lazy load: %v", err)
	}
	if st := m.State("ondemand.service"); st != "active" {
		t.Errorf("off-graph unit state = %q, want active", st)
	}

	// And an off-graph unit whose condition is UNMET is loaded but skipped, not
	// started: the two fixes compose.
	absent := filepath.Join(dir, "nope")
	write(t, filepath.Join(dir, "gated.service"),
		"[Unit]\nConditionPathExists="+absent+"\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	if err := m.StartUnit(context.Background(), "gated.service"); err != nil {
		t.Fatalf("StartUnit gated (skip is clean): %v", err)
	}
	if st := m.State("gated.service"); st == "active" {
		t.Errorf("off-graph unit with unmet condition = %q, want not active", st)
	}
}
