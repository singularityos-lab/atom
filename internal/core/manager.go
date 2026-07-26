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
	graph  *depgraph.Graph
	units  map[string]*Unit
	loader *unit.Loader
	mu     sync.RWMutex

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

	// svcToSocket maps a service name to the .socket unit that activates it.
	svcToSocket map[string]string
}

// AttachLogs wires a log registry into every service so stdout/stderr is
// captured, and makes UnitLogs available to the control plane.
func (m *Manager) AttachLogs(reg *logd.Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.logs == nil {
		return nil
	}
	return m.logs.Lines(name)
}

// Build loads the named units and their dependency closure through loader,
// constructing the graph and per-unit runtimes.
func Build(loader *unit.Loader, names ...string) (*Manager, error) {
	m := &Manager{graph: depgraph.New(), units: map[string]*Unit{}, loader: loader, svcToSocket: map[string]string{}}

	queue := append([]string(nil), names...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if _, ok := m.units[n]; ok {
			continue
		}

		f, err := loader.Load(n)
		if err != nil {
			// A single unparseable/unreadable unit must NOT abort the whole boot
			// transaction: as PID 1 that would leave the machine unbootable. Treat it
			// like a missing unit (skip + mark) so the rest of the target still comes up.
			if m.missing == nil {
				m.missing = map[string]bool{}
			}
			m.missing[n] = true
			continue
		}
		if f.Masked {
			// Masked (fragment -> /dev/null): present but inert, and its
			// dependencies are NOT pulled in. A .wants link to it is a no-op.
			m.units[n] = &Unit{Name: n, Kind: f.Type, File: f}
			continue
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
					// A broken service config (e.g. an unterminated ExecStart quote)
					// must not abort the boot as PID 1. Mark it missing and leave Svc
					// nil (inert), exactly like a file-less enabled unit, so the rest
					// of the target still starts.
					if m.missing == nil {
						m.missing = map[string]bool{}
					}
					m.missing[n] = true
				} else {
					u.Svc = service.New(cfg)
				}
			}
		}
		if f.Type == unit.TypeSocket && f.Path != "" {
			if sock, err := socketact.FromFile(f); err == nil {
				u.Sock = sock
				m.svcToSocket[sock.Service] = n
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.units {
		if u.Svc != nil {
			u.Svc.DryRun = v
		}
	}
}

// Get returns a loaded unit by name.
func (m *Manager) Get(name string) (*Unit, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.units[name]
	return u, ok
}

// Plan exposes the computed start transaction for inspection/tests.
func (m *Manager) Plan(target string) *depgraph.Plan {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.graph.PlanStart(target)
}

// StartTarget activates target by running each dependency layer to readiness
// before starting the next; units within a layer start concurrently. A unit
// that fails to start is reported via OnUnitError but does not abort the
// transaction, so the rest of the boot proceeds.
func (m *Manager) StartTarget(ctx context.Context, target string) error {
	m.openSockets()
	m.mu.RLock()
	plan := m.graph.PlanStart(target)
	m.mu.RUnlock()
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
	type openError struct {
		name string
		err  error
	}
	var failures []openError

	m.mu.Lock()
	for name, u := range m.units {
		if u.Sock == nil || u.Listeners != nil {
			continue
		}
		ls, err := u.Sock.Open()
		if err != nil {
			u.Listeners = []*socketact.Listener{} // mark attempted; do not retry
			failures = append(failures, openError{name: name, err: err})
			continue
		}
		u.Listeners = ls
	}
	hook := m.OnUnitError
	m.mu.Unlock()

	if hook != nil {
		for _, failure := range failures {
			hook(failure.name, failure.err)
		}
	}
}

func (m *Manager) startLayer(ctx context.Context, layer []string) {
	var wg sync.WaitGroup
	for _, name := range layer {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			// A panic while starting one unit must never kill PID 1: recover and
			// demote it to a failed unit, so the rest of the boot still proceeds.
			defer func() {
				if r := recover(); r != nil {
					if m.OnUnitError != nil {
						m.OnUnitError(n, fmt.Errorf("panic starting unit: %v", r))
					}
				}
			}()
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
	m.mu.RLock()
	u, ok := m.units[name]
	sockName := m.svcToSocket[name]
	var socketUnit *Unit
	if sockName != "" {
		socketUnit = m.units[sockName]
	}
	m.mu.RUnlock()
	if !ok {
		return nil // synthesized / missing: a pure ordering point
	}
	// Conflicts=: tear down any active unit that conflicts with this one before
	// it starts (systemd semantics, resolved at activation time).
	m.stopConflicts(ctx, u)
	if u.Svc != nil {
		// If a .socket activates this service, hand it the listening fds.
		if socketUnit != nil && len(socketUnit.Listeners) > 0 {
			u.Svc.Listeners = socketUnit.Listeners
			u.Svc.FDName = socketUnit.Sock.FDName
		}
		return u.Svc.Start(ctx)
	}
	return nil // target/slice/etc: no process, readiness is immediate
}

// StopTarget deactivates target in reverse layer order.
func (m *Manager) StopTarget(ctx context.Context, target string) error {
	m.mu.RLock()
	plan := m.graph.PlanStop(target)
	m.mu.RUnlock()
	for _, layer := range plan.Layers {
		for _, name := range layer {
			if u, ok := m.Get(name); ok && u.Svc != nil {
				_ = u.Svc.Stop(ctx)
			}
		}
	}
	return nil
}

// State reports a unit's state as a string ("active" for passive units once the
// manager exists; the service state otherwise).
func (m *Manager) State(name string) string {
	m.mu.RLock()
	if _, ok := m.units[name]; ok {
		state := m.state(name)
		m.mu.RUnlock()
		return state
	}
	loader := m.loader
	m.mu.RUnlock()
	if loader != nil {
		if f, err := loader.Load(name); err == nil && f.Path != "" {
			return "inactive"
		}
	}
	return "unknown"
}

func (m *Manager) state(name string) string {
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]UnitStatus, 0, len(m.units))
	for name, u := range m.units {
		out = append(out, UnitStatus{Name: name, Kind: string(u.Kind), State: m.state(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StartUnit activates a single unit and its dependency closure. Unlike a boot
// transaction it reports the requested unit's own failure to the caller (so
// atomctl start can surface it).
func (m *Manager) StartUnit(ctx context.Context, name string) error {
	if err := m.loadUnit(name); err != nil {
		return err
	}
	_ = m.StartTarget(ctx, name)
	if m.State(name) == "failed" {
		return fmt.Errorf("%s failed to start", name)
	}
	return nil
}

func (m *Manager) loadUnit(name string) error {
	m.mu.RLock()
	_, loaded := m.units[name]
	missing := m.missing[name]
	m.mu.RUnlock()
	if loaded {
		if missing {
			return fmt.Errorf("unknown unit %s", name)
		}
		return nil
	}

	added, err := Build(m.loader, name)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for n := range added.missing {
		if m.missing == nil {
			m.missing = map[string]bool{}
		}
		m.missing[n] = true
	}
	for n, u := range added.units {
		if _, exists := m.units[n]; exists {
			continue
		}
		m.units[n] = u
		if u.File != nil && !u.File.Masked {
			du := depgraph.FromFile(u.File)
			wants, requires := m.loader.EnabledDeps(n)
			du.Wants = append(du.Wants, wants...)
			du.Requires = append(du.Requires, requires...)
			m.graph.Add(du)
		}
		if u.Svc != nil && m.logs != nil {
			w := m.logs.Writer(n)
			u.Svc.Stdout = w
			u.Svc.Stderr = w
		}
	}
	for serviceName, socketName := range added.svcToSocket {
		m.svcToSocket[serviceName] = socketName
	}
	if m.missing[name] {
		return fmt.Errorf("unknown unit %s", name)
	}
	return nil
}

// StopUnit deactivates a single unit.
func (m *Manager) StopUnit(ctx context.Context, name string) error {
	u, ok := m.Get(name)
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
		v, _ := m.Get(name)
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
