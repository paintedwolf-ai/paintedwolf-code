package structure

import (
	"fmt"
	"slices"
	"strings"
)

// diagnostics is the structural evidence for one declared directory. The
// facts describe how the graph uses the members; they do not decide cohesion.
type diagnostics struct {
	Kind        character
	Members     []string
	EntryPoints []string            // declared, for subsystems
	External    map[string][]string // member → consumer packages outside the directory
	Isolated    []string            // members no outside consumer imports alongside a sibling
	Groups      [][]string          // internally disconnected member groups
}

// analyze computes the diagnostics for one declared directory. The key is the
// declaration's directory: "internal" itself, or one direct child of it.
func analyze(graph *Graph, key string, d declaration) (*diagnostics, error) {
	if err := validateBoundary(graph, key, d); err != nil {
		return nil, err
	}
	members := graph.members(key)
	if len(members) == 0 {
		return nil, fmt.Errorf("declared directory %s has no production packages", key)
	}
	out := &diagnostics{Kind: d.Kind, Members: members, External: map[string][]string{}}
	for _, member := range members {
		out.External[member] = graph.outsideConsumers(key, member)
	}
	out.Isolated = isolatedMembers(out.External, key)
	if d.Kind == subsystem {
		out.EntryPoints = d.EntryPoints
	}
	out.Groups = disconnectedGroups(graph, members)
	return out, nil
}

// isolatedMembers lists members no outside consumer imports alongside a
// sibling. Isolation is evidence, not a verdict: an entry point with internal
// collaborators can be exactly right.
func isolatedMembers(external map[string][]string, root string) []string {
	var out []string
	for member, consumers := range external {
		shared := false
		for _, consumer := range consumers {
			for other, otherConsumers := range external {
				if other == member || !within(root, other) {
					continue
				}
				if slices.Contains(otherConsumers, consumer) {
					shared = true
					break
				}
			}
			if shared {
				break
			}
		}
		if !shared {
			out = append(out, member)
		}
	}
	slices.Sort(out)
	return out
}

// disconnectedGroups splits members into sets that never reach each other
// through internal imports, directly or through one intermediate package.
func disconnectedGroups(graph *Graph, members []string) [][]string {
	set := map[string]bool{}
	for _, m := range members {
		set[m] = true
	}
	reach := func(from, to string) bool {
		if set[to] && graph.Imports[from][to] {
			return true
		}
		for dep := range graph.Imports[from] {
			if set[dep] && graph.Imports[dep][to] {
				return true
			}
		}
		return false
	}
	remaining := slices.Clone(members)
	var groups [][]string
	for len(remaining) > 0 {
		group := []string{remaining[0]}
		remaining = remaining[1:]
		for changed := true; changed; {
			changed = false
			for i, candidate := range remaining {
				joined := false
				for _, member := range group {
					if reach(member, candidate) || reach(candidate, member) {
						joined = true
						break
					}
				}
				if joined {
					group = append(group, candidate)
					remaining = slices.Delete(remaining, i, i+1)
					changed = true
					break
				}
			}
		}
		groups = append(groups, group)
	}
	slices.SortStableFunc(groups, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return groups
}

// describe renders the diagnostics as report lines.
func (d *diagnostics) describe() []string {
	var lines []string
	lines = append(lines, "kind: "+string(d.Kind))
	if len(d.Groups) > 1 {
		for _, group := range d.Groups {
			lines = append(lines, "disconnected group: "+strings.Join(group, ", "))
		}
	}
	for _, member := range d.Isolated {
		lines = append(lines, "isolated member: "+member)
	}
	if d.Kind == subsystem {
		var entries []string
		for _, entry := range d.EntryPoints {
			member := strings.TrimPrefix(entry, module+"/")
			if len(d.External[member]) > 0 {
				entries = append(entries, entry)
			} else {
				lines = append(lines, "unused entry point: "+entry)
			}
		}
		if len(entries) > 0 {
			lines = append(lines, "entry points: "+strings.Join(entries, ", "))
		}
	}
	return lines
}
