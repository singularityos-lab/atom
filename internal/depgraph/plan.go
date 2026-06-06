package depgraph

import "sort"

// Plan is a computed transaction: the closure of units to act on, arranged into
// layers that may proceed in parallel, plus diagnostics.
type Plan struct {
	// Units is the full closure, sorted.
	Units []string
	// Layers are start (or stop) waves: every unit in layer i may proceed once
	// all units in layers < i have reached their readiness point.
	Layers [][]string
	// Missing lists requirement targets pulled in but absent from the graph.
	Missing []string
	// BrokenEdges records ordering edges dropped to break a cycle, as "a->b"
	// meaning "a was required before b".
	BrokenEdges []string
	// Conflicts lists conflict pairs discovered in the closure.
	Conflicts [][2]string
}

// PlanStart computes the start transaction needed to activate goal: the
// requirement closure (Requires/Requisite/Wants/BindsTo), the ordering layers
// among that closure (After/Before), deterministic cycle-breaking, and the set
// of conflicts to resolve.
func (g *Graph) PlanStart(goal string) *Plan {
	p := &Plan{}
	closure := map[string]bool{}
	missing := map[string]bool{}

	// Requirement closure via BFS over pull-in edges.
	queue := []string{goal}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if closure[name] {
			continue
		}
		u, ok := g.units[name]
		if !ok {
			if name != goal {
				missing[name] = true
			}
			continue
		}
		closure[name] = true
		for _, dep := range pullIn(u) {
			if !closure[dep] {
				queue = append(queue, dep)
			}
		}
	}

	for m := range missing {
		p.Missing = append(p.Missing, m)
	}
	sort.Strings(p.Missing)

	// Conflicts within the closure.
	seen := map[[2]string]bool{}
	for name := range closure {
		u := g.units[name]
		for _, c := range u.Conflicts {
			if closure[c] {
				pair := orderedPair(name, c)
				if !seen[pair] {
					seen[pair] = true
					p.Conflicts = append(p.Conflicts, pair)
				}
			}
		}
	}
	sort.Slice(p.Conflicts, func(i, j int) bool {
		if p.Conflicts[i][0] != p.Conflicts[j][0] {
			return p.Conflicts[i][0] < p.Conflicts[j][0]
		}
		return p.Conflicts[i][1] < p.Conflicts[j][1]
	})

	// Ordering edges restricted to the closure. waitsOn[n] = units that must be
	// active before n may start.
	waitsOn := map[string]map[string]bool{}
	for name := range closure {
		waitsOn[name] = map[string]bool{}
	}
	for name := range closure {
		u := g.units[name]
		for _, a := range u.After {
			if closure[a] {
				waitsOn[name][a] = true
			}
		}
		for _, b := range u.Before {
			if closure[b] {
				waitsOn[b][name] = true
			}
		}
	}

	p.BrokenEdges = breakCycles(waitsOn)
	p.Layers = layerize(waitsOn)

	for name := range closure {
		p.Units = append(p.Units, name)
	}
	sort.Strings(p.Units)
	return p
}

// PlanStop computes the stop transaction for goal: it is the reverse-ordered
// mirror, so stop layers run in the opposite order to start layers.
func (g *Graph) PlanStop(goal string) *Plan {
	p := g.PlanStart(goal)
	// reverse the layer order for stopping
	for i, j := 0, len(p.Layers)-1; i < j; i, j = i+1, j-1 {
		p.Layers[i], p.Layers[j] = p.Layers[j], p.Layers[i]
	}
	return p
}

func pullIn(u *Unit) []string {
	var out []string
	out = append(out, u.Requires...)
	out = append(out, u.Requisite...)
	out = append(out, u.Wants...)
	out = append(out, u.BindsTo...)
	return out
}

// breakCycles removes edges from waitsOn until it is acyclic, deterministically,
// and returns the removed edges as "pred->node" strings.
func breakCycles(waitsOn map[string]map[string]bool) []string {
	var broken []string
	const (
		white = 0
		gray  = 1
		black = 2
	)
	for {
		color := map[string]int{}
		var removed bool

		nodes := sortedKeysOf(waitsOn)
		var visit func(n string) bool
		visit = func(n string) bool {
			color[n] = gray
			for _, pred := range sortedKeysOf(waitsOn[n]) {
				if !waitsOn[n][pred] {
					continue
				}
				switch color[pred] {
				case gray:
					// back edge pred->n closes a cycle; drop n waits-on pred.
					delete(waitsOn[n], pred)
					broken = append(broken, pred+"->"+n)
					removed = true
					return true
				case white:
					if visit(pred) {
						return true
					}
				}
			}
			color[n] = black
			return false
		}

		for _, n := range nodes {
			if color[n] == white {
				if visit(n) {
					break
				}
			}
		}
		if !removed {
			break
		}
	}
	return broken
}

// layerize assigns each node to the earliest layer after all its waits-on are
// satisfied (Kahn longest-path layering). Within a layer, names are sorted.
func layerize(waitsOn map[string]map[string]bool) [][]string {
	depth := map[string]int{}
	var compute func(n string) int
	inProgress := map[string]bool{}
	compute = func(n string) int {
		if d, ok := depth[n]; ok {
			return d
		}
		if inProgress[n] {
			return 0 // guard; cycles already broken, but be safe
		}
		inProgress[n] = true
		max := -1
		for pred := range waitsOn[n] {
			if d := compute(pred); d > max {
				max = d
			}
		}
		depth[n] = max + 1
		inProgress[n] = false
		return depth[n]
	}
	maxDepth := 0
	for n := range waitsOn {
		d := compute(n)
		if d > maxDepth {
			maxDepth = d
		}
	}
	layers := make([][]string, maxDepth+1)
	for n, d := range depth {
		layers[d] = append(layers[d], n)
	}
	for i := range layers {
		sort.Strings(layers[i])
	}
	return layers
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orderedPair(a, b string) [2]string {
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}
