package core

import (
	"context"
	"fmt"
	"sort"
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

	// OnUnitError, if set, is called for each unit whose start fails. A failing
	// unit does NOT abort the transaction -- the boot continues, matching systemd
	// (a failed service does not wedge the boot; only its Requires= dependents
	// are affected).
	OnUnitError func(name string, err error)
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
		// Pull in units enabled via <name>.wants/ and <name>.requires/ symlinks
		// (the systemctl enable mechanism), equivalent to Wants=/Requires=.
		w, r := loader.EnabledDeps(n)
		du.Wants = append(du.Wants, w...)
		du.Requires = append(du.Requires, r...)
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

// SetDryRun toggles dry-run on every loaded service (no real exec on start).
func (m *Manager) SetDryRun(v bool) {
	for _, u := range m.units {
		if u.Svc != nil {
			u.Svc.DryRun = v
		}
	}
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
// before starting the next; units within a layer start concurrently. A unit
// that fails to start is reported via OnUnitError but does not abort the
// transaction, so the rest of the boot proceeds.
func (m *Manager) StartTarget(ctx context.Context, target string) error {
	plan := m.graph.PlanStart(target)
	for _, layer := range plan.Layers {
		m.startLayer(ctx, layer)
	}
	return nil
}

func (m *Manager) startLayer(ctx context.Context, layer []string) {
	var wg sync.WaitGroup
	for _, name := range layer {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if err := m.startUnit(ctx, n); err != nil && m.OnUnitError != nil {
				m.OnUnitError(n, err)
			}
		}(name)
	}
	wg.Wait()
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

// UnitStatus is a snapshot of a unit for the control plane.
type UnitStatus struct {
	Name  string
	Kind  string
	State string
}

// ListUnits returns every loaded unit's status, sorted by name.
func (m *Manager) ListUnits() []UnitStatus {
	out := make([]UnitStatus, 0, len(m.units))
	for name, u := range m.units {
		out = append(out, UnitStatus{Name: name, Kind: string(u.Kind), State: m.State(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StartUnit activates a single unit and its dependency closure. Unlike a boot
// transaction it reports the requested unit's own failure to the caller (so
// atomctl start can surface it).
func (m *Manager) StartUnit(ctx context.Context, name string) error {
	if _, ok := m.units[name]; !ok {
		return fmt.Errorf("unknown unit %s", name)
	}
	_ = m.StartTarget(ctx, name)
	if m.State(name) == "failed" {
		return fmt.Errorf("%s failed to start", name)
	}
	return nil
}

// StopUnit deactivates a single unit.
func (m *Manager) StopUnit(ctx context.Context, name string) error {
	u, ok := m.units[name]
	if !ok {
		return fmt.Errorf("unknown unit %s", name)
	}
	if u.Svc != nil {
		return u.Svc.Stop(ctx)
	}
	return nil
}
