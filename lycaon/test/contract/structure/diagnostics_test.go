package structure

import (
	"slices"
	"strings"
	"testing"
)

// graphFor builds a Graph from member → consumer listings.
func graphFor(members map[string][]string, internalImports map[string][]string) *Graph {
	g := &Graph{Imports: map[string]map[string]bool{}, Consumers: map[string]map[string]bool{}, Packages: map[string]bool{}}
	for member, consumers := range members {
		g.Packages[member] = true
		for _, consumer := range consumers {
			if g.Consumers[member] == nil {
				g.Consumers[member] = map[string]bool{}
			}
			g.Consumers[member][consumer] = true
			if g.Imports[consumer] == nil {
				g.Imports[consumer] = map[string]bool{}
			}
			g.Imports[consumer][member] = true
		}
	}
	for pkg, imports := range internalImports {
		g.Packages[pkg] = true
		for _, dep := range imports {
			if g.Imports[pkg] == nil {
				g.Imports[pkg] = map[string]bool{}
			}
			if g.Consumers[dep] == nil {
				g.Consumers[dep] = map[string]bool{}
			}
			g.Imports[pkg][dep] = true
			g.Consumers[dep][pkg] = true
		}
	}
	return g
}

func TestIsolatedMembersSharedConsumers(t *testing.T) {
	t.Parallel()
	// Both members are consumed by the same outside package: neither isolated.
	g := graphFor(map[string][]string{
		"internal/session":       {"app"},
		"internal/session/store": {"app"},
	}, nil)
	d, err := analyze(g, "internal/session", declaration{Kind: container, Description: "hub"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(d.Isolated) != 0 {
		t.Fatalf("isolated = %#v", d.Isolated)
	}
}

func TestIsolatedMemberOnlyConsumer(t *testing.T) {
	t.Parallel()
	// Each member has a disjoint consumer: both isolated.
	g := graphFor(map[string][]string{
		"internal/session":       {"app"},
		"internal/session/store": {"search"},
	}, nil)
	d, err := analyze(g, "internal/session", declaration{Kind: container, Description: "hub"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !slices.Equal(d.Isolated, []string{"internal/session", "internal/session/store"}) {
		t.Fatalf("isolated = %#v", d.Isolated)
	}
}

func TestDisconnectedGroupsViaIntermediate(t *testing.T) {
	t.Parallel()
	// Two clusters connected through one intermediate member are one group;
	// a third member with no internal edges is its own group.
	g := graphFor(map[string][]string{
		"internal/a": {"app"},
		"internal/b": {"app"},
		"internal/c": {"app"},
		"internal/m": nil,
	}, map[string][]string{
		"internal/m": {"internal/a", "internal/b"},
	})
	d, err := analyze(g, "internal", declaration{Kind: container, Description: "boundary"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(d.Groups) != 2 {
		t.Fatalf("groups = %#v", d.Groups)
	}
	var flat []string
	for _, group := range d.Groups {
		flat = append(flat, group...)
	}
	if len(flat) != 4 {
		t.Fatalf("groups do not cover members: %#v", d.Groups)
	}
}

func TestSubsystemUnusedEntryPointReported(t *testing.T) {
	t.Parallel()
	g := graphFor(map[string][]string{
		"internal/db": {"app"},
	}, nil)
	d, err := analyze(g, "internal/db", declaration{
		Kind:        subsystem,
		Description: "SQL adapter.",
		EntryPoints: []string{module + "/internal/db"},
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	lines := d.describe()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "kind: subsystem") || !strings.Contains(joined, "entry points: "+module+"/internal/db") {
		t.Fatalf("describe = %#v", lines)
	}
}

func TestDescribeContainerNamesIsolatedMembers(t *testing.T) {
	t.Parallel()
	g := graphFor(map[string][]string{
		"internal/a": {"app"},
		"internal/b": {"search"},
	}, nil)
	d, err := analyze(g, "internal", declaration{Kind: container, Description: "boundary"})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	joined := strings.Join(d.describe(), "\n")
	if !strings.Contains(joined, "isolated member: internal/a") || !strings.Contains(joined, "isolated member: internal/b") {
		t.Fatalf("describe = %s", joined)
	}
}