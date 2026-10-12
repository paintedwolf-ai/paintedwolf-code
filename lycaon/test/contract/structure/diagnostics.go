package structure

import (
	"fmt"
	"sort"
	"strings"
)

// diagnostics is the structural evidence for one declared directory. Every
// field is a fact about how the graph uses the directory's members; none of
// them decides whether the directory is one concern.
type diagnostics struct {
	Kind        character
	Members     []string
	EntryPoints []string          // declared, for subsystems
	External    map[string][]string // member → consumer packages outside the directory
	Isolated    []string          // members with no external consumer that touches any sibling
	Groups      [][]string        // internally disconnected member groups
}

// analyze computes the diagnostics for one declared directory.
func analyze(graph *Graph, name string, d declaration) (*diagnostics, error) {
	members := graph.members("internal/" + name)
	if len(members) == 0 {
		return nil, fmt.Errorf("declared directory %s has no packages", name)
	}
	out := &diagnostics{Kind: d.Kind, Members: members, External: map[string][]string{}}
	for _, member := range members {
		out.External[member] = graph.outsideConsumers("internal/"+name, member)
	}
	out.Isolated = isolatedMembers(out.External, "internal/"+name)
	if d.Kind == subsystem {
		out.EntryPoints = d.EntryPoints
	}
	out.Groups = disconnectedGroups(graph, members)
	return out, nil
}

// isolatedMembers lists members no outside consumer ever imports alongside a
// sibling. An isolated member is evidence for review, not a verdict: a public
// entry point with internal collaborators, or an alternative behind one
// family contract, both look isolated and may be exactly right.
func isolatedMembers(external map[string][]string, root string) []string {
	var out []string
	for member := range external {
		shared := false
		for _, consumer := range external[member] {
			for other := range external {
				if other == member || !within(root, other) {
					continue
				}
				if contains(external[other], consumer) {
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
	sort.Strings(out)
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// disconnectedGroups splits members into sets that never reach each other
// through internal imports. Within a subsystem, disconnected groups are the
// strongest signal that the declared concern is really several.
func disconnectedGroups(graph *Graph, members []string) [][]string {
	set := map[string]bool{}
	for _, m := range members {
		set[m] = true
	}
	reach := func(from, to string) bool {
		if set[to] && graph.Imports[from][to] {
			return true
		}
		// An import through an unlisted internal package connects two members
		// only when that package imports the other side; hub-over-hub edges
		// count as connections between the groups they join.
		for _, dep := range graph.Imports[from] {
			if set[dep] && graph.Imports[dep][to] {
				return true
			}
		}
		return false
	}
	remaining := append([]string{}, members...)
	var groups [][]string
	for len(remaining) > 0 {
		group := []string{remaining[0]}
		remaining = remaining[1:]
		changed := true
		for changed {
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
					remaining = append(remaining[:i], remaining[i+1:]...)
					changed = true
					break
				}
			}
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// describe renders the diagnostics as report lines.
func (d *diagnostics) describe(name string) []string {
	var lines []string
	lines = append(lines, fmt.Sprintf("kind: %s", d.Kind))
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
			if len(d.External) == 0 || externalCount(d, entry) > 0 {
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

func externalCount(d *diagnostics, entry string) int {
	return len(d.External[strings.TrimPrefix(entry, module+"/")])
}