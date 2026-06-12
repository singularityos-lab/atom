package depgraph

import "github.com/singularityos-lab/atom/internal/unit"

// Unit is the dependency-relevant projection of a unit file.
type Unit struct {
	Name string

	// Ordering axis.
	After  []string
	Before []string

	// Requirement axis.
	Requires  []string
	Requisite []string
	Wants     []string
	BindsTo   []string
	PartOf    []string
	Conflicts []string
}

// FromFile extracts the dependency projection from a parsed unit file,
// including systemd's implicit DefaultDependencies (unless DefaultDependencies=no).
func FromFile(f *unit.File) *Unit {
	u := &Unit{
		Name:      f.Name,
		After:     f.Strv("Unit", "After"),
		Before:    f.Strv("Unit", "Before"),
		Requires:  f.Strv("Unit", "Requires"),
		Requisite: f.Strv("Unit", "Requisite"),
		Wants:     f.Strv("Unit", "Wants"),
		BindsTo:   f.Strv("Unit", "BindsTo"),
		PartOf:    f.Strv("Unit", "PartOf"),
		Conflicts: f.Strv("Unit", "Conflicts"),
	}
	if f.Bool("Unit", "DefaultDependencies", true) {
		applyDefaultDeps(u, f.Type)
	}
	return u
}

// applyDefaultDeps adds the boot-ordering edges systemd injects implicitly when
// DefaultDependencies=yes. Only the After/Before edges that affect start
// ordering are added (the shutdown.target Conflicts/Before edges matter only to
// the stop transaction and are omitted from the boot closure). Honoring
// DefaultDependencies=no on early units (journald, udev, ...) is what keeps them
// from being ordered after basic.target and cycling.
func applyDefaultDeps(u *Unit, t unit.Type) {
	switch t {
	case unit.TypeService:
		u.After = append(u.After, "sysinit.target", "basic.target")
	case unit.TypeSocket:
		u.After = append(u.After, "sysinit.target")
		u.Before = append(u.Before, "sockets.target")
	case unit.TypeTimer:
		u.After = append(u.After, "sysinit.target")
		u.Before = append(u.Before, "timers.target")
	}
}

// Graph is a set of units addressable by name.
type Graph struct {
	units map[string]*Unit
}

// New returns an empty graph.
func New() *Graph { return &Graph{units: map[string]*Unit{}} }

// Add inserts or replaces a unit.
func (g *Graph) Add(u *Unit) { g.units[u.Name] = u }

// Get returns a unit by name.
func (g *Graph) Get(name string) (*Unit, bool) {
	u, ok := g.units[name]
	return u, ok
}

// Len reports the number of units in the graph.
func (g *Graph) Len() int { return len(g.units) }
