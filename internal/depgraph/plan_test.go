package depgraph

import (
	"strings"
	"testing"

	"github.com/singularityos-lab/atom/internal/unit"
)

func layersString(p *Plan) string {
	var b strings.Builder
	for i, l := range p.Layers {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(strings.Join(l, ","))
	}
	return b.String()
}

func TestChainOrdering(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "a", After: []string{"b"}, Wants: []string{"b"}})
	g.Add(&Unit{Name: "b", After: []string{"c"}, Wants: []string{"c"}})
	g.Add(&Unit{Name: "c"})

	p := g.PlanStart("a")
	if got := layersString(p); got != "c | b | a" {
		t.Errorf("layers = %q, want %q", got, "c | b | a")
	}
	if len(p.BrokenEdges) != 0 {
		t.Errorf("unexpected broken edges: %v", p.BrokenEdges)
	}
}

func TestWantsClosure(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "target", Wants: []string{"a", "b"}})
	g.Add(&Unit{Name: "a", After: []string{"b"}})
	g.Add(&Unit{Name: "b"})
	g.Add(&Unit{Name: "unrelated"})

	p := g.PlanStart("target")
	if len(p.Units) != 3 {
		t.Errorf("closure = %v, want 3 units (target,a,b)", p.Units)
	}
	for _, u := range p.Units {
		if u == "unrelated" {
			t.Errorf("unrelated should not be in closure")
		}
	}
	// b before a (a After b); target has no ordering, lands in layer 0.
	if !beforeIn(p, "b", "a") {
		t.Errorf("b must be layered before a: %q", layersString(p))
	}
}

func TestBeforeInversion(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "early", Before: []string{"late"}})
	g.Add(&Unit{Name: "late", Wants: []string{"early"}})
	p := g.PlanStart("late")
	if !beforeIn(p, "early", "late") {
		t.Errorf("Before= should make early precede late: %q", layersString(p))
	}
}

func TestCycleBreaking(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "a", After: []string{"b"}, Wants: []string{"b"}})
	g.Add(&Unit{Name: "b", After: []string{"a"}})
	p := g.PlanStart("a")
	if len(p.BrokenEdges) != 1 {
		t.Errorf("expected exactly 1 broken edge, got %v", p.BrokenEdges)
	}
	// Still produces a total layering of both units.
	total := 0
	for _, l := range p.Layers {
		total += len(l)
	}
	if total != 2 {
		t.Errorf("layering lost units after cycle break: %q", layersString(p))
	}
}

func TestConflicts(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "target", Wants: []string{"x", "y"}})
	g.Add(&Unit{Name: "x", Conflicts: []string{"y"}})
	g.Add(&Unit{Name: "y"})
	p := g.PlanStart("target")
	if len(p.Conflicts) != 1 || p.Conflicts[0] != [2]string{"x", "y"} {
		t.Errorf("conflicts = %v, want [[x y]]", p.Conflicts)
	}
}

func TestMissingRequirement(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "target", Requires: []string{"ghost"}})
	p := g.PlanStart("target")
	if len(p.Missing) != 1 || p.Missing[0] != "ghost" {
		t.Errorf("missing = %v, want [ghost]", p.Missing)
	}
}

func TestPlanStopReversesStart(t *testing.T) {
	g := New()
	g.Add(&Unit{Name: "a", After: []string{"b"}, Wants: []string{"b"}})
	g.Add(&Unit{Name: "b", After: []string{"c"}, Wants: []string{"c"}})
	g.Add(&Unit{Name: "c"})
	start := g.PlanStart("a")
	stop := g.PlanStop("a")
	if layersString(start) != "c | b | a" {
		t.Fatalf("start layers wrong: %q", layersString(start))
	}
	if layersString(stop) != "a | b | c" {
		t.Errorf("stop layers = %q, want reverse", layersString(stop))
	}
}

func TestFromFileIntegration(t *testing.T) {
	src := `[Unit]
After=network.target
Wants=network.target
Requires=dbus.service
Conflicts=rescue.service
`
	f, err := unit.Parse("demo.service", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	u := FromFile(f)
	if len(u.After) != 1 || u.After[0] != "network.target" {
		t.Errorf("After = %v", u.After)
	}
	if len(u.Requires) != 1 || u.Requires[0] != "dbus.service" {
		t.Errorf("Requires = %v", u.Requires)
	}
	if len(u.Conflicts) != 1 || u.Conflicts[0] != "rescue.service" {
		t.Errorf("Conflicts = %v", u.Conflicts)
	}
}

// beforeIn reports whether unit a is in a strictly earlier layer than b.
func beforeIn(p *Plan, a, b string) bool {
	la, lb := -1, -1
	for i, l := range p.Layers {
		for _, n := range l {
			if n == a {
				la = i
			}
			if n == b {
				lb = i
			}
		}
	}
	return la >= 0 && lb >= 0 && la < lb
}
