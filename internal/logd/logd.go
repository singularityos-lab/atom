package logd

import (
	"bytes"
	"io"
	"sync"
)

// Ring is a bounded ring buffer of recent log lines for one unit. It implements
// io.Writer so it can be wired directly as a service's Stdout/Stderr.
type Ring struct {
	mu      sync.Mutex
	lines   []string
	max     int
	partial []byte
}

// NewRing returns a ring holding at most max lines.
func NewRing(max int) *Ring {
	if max <= 0 {
		max = 1000
	}
	return &Ring{max: max}
}

// Write appends data, splitting it into lines on '\n' and trimming to capacity.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, string(r.partial[:i]))
		r.partial = append(r.partial[:0], r.partial[i+1:]...)
		if len(r.lines) > r.max {
			r.lines = append(r.lines[:0], r.lines[len(r.lines)-r.max:]...)
		}
	}
	return len(p), nil
}

// Lines returns the buffered lines (plus any unterminated partial line).
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	if len(r.partial) > 0 {
		out = append(out, string(r.partial))
	}
	return out
}

// Registry holds one ring per unit.
type Registry struct {
	mu    sync.Mutex
	rings map[string]*Ring
	max   int
}

// NewRegistry returns a registry whose rings hold at most maxLines each.
func NewRegistry(maxLines int) *Registry {
	return &Registry{rings: map[string]*Ring{}, max: maxLines}
}

func (reg *Registry) ring(unit string) *Ring {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	r := reg.rings[unit]
	if r == nil {
		r = NewRing(reg.max)
		reg.rings[unit] = r
	}
	return r
}

// Writer returns the io.Writer that captures a unit's output.
func (reg *Registry) Writer(unit string) io.Writer { return reg.ring(unit) }

// Lines returns a unit's captured log lines (nil if the unit has no ring yet).
func (reg *Registry) Lines(unit string) []string {
	reg.mu.Lock()
	r := reg.rings[unit]
	reg.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.Lines()
}
