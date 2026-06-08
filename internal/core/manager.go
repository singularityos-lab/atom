package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/singularityos-lab/atom/internal/depgraph"
	"github.com/singularityos-lab/atom/internal/service"
	"github.com/singularityos-lab/atom/internal/unit"
)

// Unit is a loaded unit: its dependency projection plus, for services, a
// runtime. Targets and other passive kinds have a nil Svc and act as pure
// synchronization points.
type Unit struct {
	Name string
	Kind unit.Type
	File *unit.File
	Svc  *service.Service
}

// Manager owns the unit set and the graph.
type Manager struct {
	graph *depgraph.Graph
	units map[string]*Unit
}

// Build loads the named units and their dependency closure through loader,
// constructing the graph and per-unit runtimes.
func Build(loader *unit.Loader, names ...string) (*Manager, error) {
	m := &Manager{graph: depgraph.New(), units: map[string]*Unit{}}

	queue := append([]string(nil), names...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if _, ok := m.units[n]; ok {
			continue
		}

		f, err := loader.Load(n)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", n, err)
		}
		du := depgraph.FromFile(f)
		m.graph.Add(du)

		u := &Unit{Name: n, Kind: f.Type, File: f}
		if f.Type == unit.TypeService {
			cfg, err := service.ConfigFromFile(f)
			if err != nil {
				return nil, fmt.Errorf("config %s: %w", n, err)
			}
			u.Svc = service.New(cfg)
		}
		m.units[n] = u

		for _, dep := range neighbors(du) {
			if _, ok := m.units[dep]; !ok {
				queue = append(queue, dep)
			}
		}
	}
	return m, nil
}

func neighbors(u *depgraph.Unit) []string {
	var out []string
	out = append(out, u.After...)
	out = append(out, u.Before...)
	out = append(out, u.Requires...)
	out = append(out, u.Requisite...)
	out = append(out, u.Wants...)
	out = append(out, u.BindsTo...)
	out = append(out, u.PartOf...)
	out = append(out, u.Conflicts...)
	return out
}

// Get returns a loaded unit by name.
func (m *Manager) Get(name string) (*Unit, bool) {
	u, ok := m.units[name]
	return u, ok
}

// Plan exposes the computed start transaction for inspection/tests.
func (m *Manager) Plan(target string) *depgraph.Plan {
	return m.graph.PlanStart(target)
}

// StartTarget activates target by running each dependency layer to readiness
// before starting the next; units within a layer start concurrently.
func (m *Manager) StartTarget(ctx context.Context, target string) error {
	plan := m.graph.PlanStart(target)
	for _, layer := range plan.Layers {
		if err := m.startLayer(ctx, layer); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) startLayer(ctx context.Context, layer []string) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(layer))
	for _, name := range layer {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			errs <- m.startUnit(ctx, n)
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) startUnit(ctx context.Context, name string) error {
	u, ok := m.units[name]
	if !ok {
		return nil // synthesized / missing: a pure ordering point
	}
	if u.Svc != nil {
		return u.Svc.Start(ctx)
	}
	return nil // target/slice/etc: no process, readiness is immediate
}

// StopTarget deactivates target in reverse layer order.
func (m *Manager) StopTarget(ctx context.Context, target string) error {
	plan := m.graph.PlanStop(target)
	for _, layer := range plan.Layers {
		for _, name := range layer {
			if u, ok := m.units[name]; ok && u.Svc != nil {
				_ = u.Svc.Stop(ctx)
			}
		}
	}
	return nil
}

// State reports a unit's state as a string ("active" for passive units once the
// manager exists; the service state otherwise).
func (m *Manager) State(name string) string {
	u, ok := m.units[name]
	if !ok {
		return "unknown"
	}
	if u.Svc != nil {
		return u.Svc.State().String()
	}
	return "active"
}
