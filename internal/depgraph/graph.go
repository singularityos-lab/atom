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

// FromFile extracts the dependency projection from a parsed unit file.
func FromFile(f *unit.File) *Unit {
	return &Unit{
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
