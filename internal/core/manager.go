package core

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/singularityos-lab/atom/internal/depgraph"
	"github.com/singularityos-lab/atom/internal/logd"
	"github.com/singularityos-lab/atom/internal/service"
	"github.com/singularityos-lab/atom/internal/socketact"
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

	// For .socket units: the parsed socket and its opened listeners.
	Sock      *socketact.Socket
	Listeners []*socketact.Listener
}

// Manager owns the unit set and the graph.
type Manager struct {
	graph *depgraph.Graph
	units map[string]*Unit

	// OnUnitStart/OnUnitActive/OnUnitError are progress hooks (used for
	// atom.debug per-unit boot logging). A failing unit does NOT abort the
	// transaction -- the boot continues, matching systemd.
	OnUnitStart  func(name string)
	OnUnitActive func(name string)
	OnUnitError  func(name string, err error)

	// OnUnitStop reports a unit stopped as a side effect of another unit
	// starting (Conflicts=), with a short reason. Used for boot logging.
	OnUnitStop func(name, reason string)

	// missing tracks units that were pulled in (e.g. via a .wants/.requires
	// enablement symlink) but have no unit file -- systemd's "unit not found".
	missing map[string]bool

	// logs, if attached, captures each service's stdout/stderr.
	logs *logd.Registry

	// svcToSockets maps a service name to the .socket units that activate it. A
	// service can have several (systemd-udevd has control + kernel-netlink +
	// varlink), and all of their fds are handed over together.
	svcToSockets map[string][]string
}

// AttachLogs wires a log registry into every service so stdout/stderr is
// captured, and makes UnitLogs available to the control plane.
func (m *Manager) AttachLogs(reg *logd.Registry) {
	m.logs = reg
	for name, u := range m.units {
		if u.Svc != nil {
			w := reg.Writer(name)
			u.Svc.Stdout = w
			u.Svc.Stderr = w
		}
	}
}

// UnitLogs returns a unit's captured log lines.
func (m *Manager) UnitLogs(name string) []string {
	if m.logs == nil {
		return nil
	}
	return m.logs.Lines(name)
}

// Build loads the named units and their dependency closure through loader,
// constructing the graph and per-unit runtimes.
func Build(loader *unit.Loader, names ...string) (*Manager, error) {
	m := &Manager{graph: depgraph.New(), units: map[string]*Unit{}, svcToSockets: map[string][]string{}}

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
			if f.Path == "" {
				// Enabled (via .wants/.requires) but no unit file present:
				// systemd treats this as "unit not found" and skips it.
				if m.missing == nil {
					m.missing = map[string]bool{}
				}
				m.missing[n] = true
			} else {
				cfg, err := service.ConfigFromFile(f)
				if err != nil {
					return nil, fmt.Errorf("config %s: %w", n, err)
				}
				u.Svc = service.New(cfg)
			}
		}
		if f.Type == unit.TypeSocket && f.Path != "" {
			if sock, err := socketact.FromFile(f); err == nil {
				u.Sock = sock
				m.svcToSockets[sock.Service] = append(m.svcToSockets[sock.Service], n)
			}
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

// Critical returns the units annotated X-Atom-BootCritical=yes -- the set the
// boot-success health gate waits on before confirming the deployment.
func (m *Manager) Critical() []string {
	var out []string
	for name, u := range m.units {
		if u.File != nil && u.File.Bool("Unit", "X-Atom-BootCritical", false) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
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
	m.openSockets()
	plan := m.graph.PlanStart(target)
	for _, layer := range plan.Layers {
		m.startLayer(ctx, layer)
	}
	return nil
}

// openSockets binds all .socket listeners up front, before any service starts,
// so a socket-activated service (e.g. dbus.service --systemd-activation) is
// handed its already-listening fd. Done before the layers to avoid racing a
// socket's open against its service's start.
func (m *Manager) openSockets() {
	for name, u := range m.units {
		if u.Sock == nil || u.Listeners != nil {
			continue
		}
		ls, err := u.Sock.Open()
		if err != nil {
			u.Listeners = []*socketact.Listener{} // mark attempted; do not retry
			if m.OnUnitError != nil {
				m.OnUnitError(name, err)
			}
			continue
		}
		u.Listeners = ls
	}
}

func (m *Manager) startLayer(ctx context.Context, layer []string) {
	var wg sync.WaitGroup
	for _, name := range layer {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if m.OnUnitStart != nil {
				m.OnUnitStart(n)
			}
			if err := m.startUnit(ctx, n); err != nil {
				if m.OnUnitError != nil {
					m.OnUnitError(n, err)
				}
			} else if m.OnUnitActive != nil {
				m.OnUnitActive(n)
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
	// Conflicts=: tear down any active unit that conflicts with this one before
	// it starts (systemd semantics, resolved at activation time).
	m.stopConflicts(ctx, u)
	if u.Svc != nil {
		// If a .socket activates this service, hand it the listening fds.
		if socks := m.svcToSockets[name]; len(socks) > 0 {
			sort.Strings(socks)
			var agg []*socketact.Listener
			for _, sn := range socks {
				if su := m.units[sn]; su != nil {
					agg = append(agg, su.Listeners...)
				}
			}
			if len(agg) > 0 {
				u.Svc.Listeners = agg
			}
		}
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
	if m.missing[name] {
		return "not-found"
	}
	if u.Svc != nil {
		return u.Svc.State().String()
	}
	return "active"
}

// Missing returns units enabled but lacking a unit file (systemd "not found"),
// sorted by name.
func (m *Manager) Missing() []string {
	out := make([]string, 0, len(m.missing))
	for n := range m.missing {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
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

// conflictTargets returns the units that conflict with u, in both directions:
// units u lists in Conflicts=, and units that list u in theirs. Conflicts= is
// symmetric in systemd, so either edge counts.
func (m *Manager) conflictTargets(u *Unit) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && n != u.Name && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if u.File != nil {
		for _, c := range u.File.Strv("Unit", "Conflicts") {
			add(c)
		}
	}
	for name, v := range m.units {
		if name == u.Name || v.File == nil {
			continue
		}
		for _, c := range v.File.Strv("Unit", "Conflicts") {
			if c == u.Name {
				add(name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// stopConflicts stops any currently-active unit that conflicts with u, before u
// starts. Service Stop is synchronous through process reap, so a conflicting
// unit's device holds are released first -- e.g. a boot splash owning DRM master
// is torn down before the compositor that Conflicts= it starts, letting the
// compositor become DRM master (the seam logind's session switch used to drive).
func (m *Manager) stopConflicts(ctx context.Context, u *Unit) {
	for _, name := range m.conflictTargets(u) {
		v := m.units[name]
		if v == nil || v.Svc == nil {
			continue
		}
		switch v.Svc.State() {
		case service.Active, service.Activating:
			if m.OnUnitStop != nil {
				m.OnUnitStop(name, "conflicts "+u.Name)
			}
			_ = m.StopUnit(ctx, name)
		}
	}
}
