// Package toolsurface models the tool availability compiled for one model turn.
package toolsurface

import (
	"sort"
	"strings"
)

// Availability describes how a tool schema enters the current model turn.
type Availability uint8

const (
	Unavailable Availability = iota
	Immediate
	Deferred
)

// Plan is a compiled per-turn availability snapshot.
// An empty compiled plan is a closed surface.
type Plan struct {
	compiled bool
	modes    map[string]Availability
}

// Compile builds a plan; immediate names take precedence.
func Compile(immediate, deferred []string) Plan {
	p := Plan{compiled: true, modes: make(map[string]Availability, len(immediate)+len(deferred))}
	for _, name := range deferred {
		if name = strings.TrimSpace(name); name != "" {
			p.modes[name] = Deferred
		}
	}
	for _, name := range immediate {
		if name = strings.TrimSpace(name); name != "" {
			p.modes[name] = Immediate
		}
	}
	return p
}

// Compiled reports whether the plan represents a compiled surface.
func (p Plan) Compiled() bool {
	return p.compiled
}

// Availability returns the current mode for name.
func (p Plan) Availability(name string) Availability {
	return p.modes[strings.TrimSpace(name)]
}

// Addressable reports whether the current surface can resolve name.
func (p Plan) Addressable(name string) bool {
	return p.Availability(name) != Unavailable
}

// Immediate reports whether name's schema is available on this model turn.
func (p Plan) Immediate(name string) bool {
	return p.Availability(name) == Immediate
}

// Deferred reports whether name is available through request_tools.
func (p Plan) Deferred(name string) bool {
	return p.Availability(name) == Deferred
}

// Promote returns a plan with names available immediately.
func (p Plan) Promote(names ...string) Plan {
	return p.withMode(Immediate, names...)
}

// Defer makes unavailable names requestable.
func (p Plan) Defer(names ...string) Plan {
	return p.withMode(Deferred, names...)
}

func (p Plan) withMode(mode Availability, names ...string) Plan {
	out := Plan{compiled: true, modes: make(map[string]Availability, len(p.modes)+len(names))}
	for name, current := range p.modes {
		out.modes[name] = current
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || (mode == Deferred && out.modes[name] == Immediate) {
			continue
		}
		out.modes[name] = mode
	}
	return out
}

// Without returns a plan with names removed.
func (p Plan) Without(names ...string) Plan {
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			remove[name] = struct{}{}
		}
	}
	out := Plan{compiled: p.compiled, modes: make(map[string]Availability, len(p.modes))}
	for name, mode := range p.modes {
		if _, drop := remove[name]; !drop {
			out.modes[name] = mode
		}
	}
	return out
}

// ImmediateNames returns the sorted immediate schema set.
func (p Plan) ImmediateNames() []string {
	return p.names(Immediate)
}

// DeferredNames returns the sorted requestable schema set.
func (p Plan) DeferredNames() []string {
	return p.names(Deferred)
}

// AddressableNames returns every sorted name on the surface.
func (p Plan) AddressableNames() []string {
	out := make([]string, 0, len(p.modes))
	for name := range p.modes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p Plan) names(mode Availability) []string {
	out := make([]string, 0, len(p.modes))
	for name, current := range p.modes {
		if current == mode {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
