package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/singularityos-lab/atom/internal/core"
	"github.com/singularityos-lab/atom/internal/unit"
)

func TestSixSwitchGatesTrial(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.Mkdir(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "base.target", "[Unit]\n")
	writeUnit(t, units, "six-bridge.service", "[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/bin/true\n")
	m, err := core.Build(&unit.Loader{Paths: []string{units}}, "base.target")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "boot-state")
	if err := os.WriteFile(state, []byte("target=active\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "six", "enabled")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	attempt := filepath.Join(dir, "six", "attempted-0123456789abcdef")
	if err := os.WriteFile(attempt, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &SDBServer{m: m, cfg: SDBConfig{
		SixUnit:      "six-bridge.service",
		SixMarker:    marker,
		SixBootState: state,
	}}

	if rep := srv.sixStatus(); !rep.OK || rep.State != "off" {
		t.Fatalf("initial: %+v", rep)
	}
	if rep := srv.setSixEnabled(true); !rep.OK || rep.State != "on" {
		t.Fatalf("enable: %+v", rep)
	}
	if _, err := os.Stat(attempt); !os.IsNotExist(err) {
		t.Fatalf("attempt record kept: %v", err)
	}
	if got := m.State("six-bridge.service"); got != "active" {
		t.Fatalf("unit state %q", got)
	}

	if err := os.WriteFile(state, []byte("target=next\ntrial=1\nattempts=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rep := srv.sixStatus(); !rep.OK || rep.State != "ready" {
		t.Fatalf("staged: %+v", rep)
	}
	if rep := srv.setSixEnabled(false); !rep.OK || rep.State != "off" {
		t.Fatalf("disable: %+v", rep)
	}
	data, err := os.ReadFile(state)
	if err != nil || string(data) != "target=active\n" {
		t.Fatalf("staged trial not cancelled: %q %v", data, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker kept: %v", err)
	}
}
