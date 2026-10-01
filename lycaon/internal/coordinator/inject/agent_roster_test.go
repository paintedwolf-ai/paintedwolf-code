package inject

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
)

func TestResolveAgentRosterPartitionsDeclared(t *testing.T) {
	declared := spawn.AmbientAllowedAgents()
	roster := ResolveAgentRoster("implement_investigate", declared, 1, false, true)
	if !reflect.DeepEqual(roster.Declared, declared) {
		t.Fatalf("declared = %v want %v", roster.Declared, declared)
	}
	// Effective + excluded partition the declared set exactly.
	seen := map[string]bool{}
	for _, name := range roster.Effective {
		seen[name] = true
	}
	for _, ex := range roster.Excluded {
		if seen[ex.Name] {
			t.Fatalf("agent %q both effective and excluded", ex.Name)
		}
		seen[ex.Name] = true
	}
	for _, name := range declared {
		if !seen[name] {
			t.Fatalf("agent %q dropped without an exclusion row", name)
		}
	}
}

func TestResolveAgentRosterEmptyRepoExcludesWithCode(t *testing.T) {
	declared := spawn.AmbientAllowedAgents()
	open := ResolveAgentRoster("implement_investigate", declared, 1, false, true)
	empty := ResolveAgentRoster("implement_investigate", declared, 1, true, true)
	if len(empty.Effective) >= len(open.Effective) {
		t.Fatalf("empty repo should narrow the roster: %v vs %v", empty.Effective, open.Effective)
	}
	if len(empty.Excluded) == 0 {
		t.Fatal("empty repo must attribute exclusions")
	}
	for _, ex := range empty.Excluded {
		if ex.Code != RosterExcludeRepoEmpty {
			t.Fatalf("exclusion %q code = %q want %q", ex.Name, ex.Code, RosterExcludeRepoEmpty)
		}
	}
}

func TestResolveAgentRosterWebSearchOff(t *testing.T) {
	declared := append(spawn.AmbientAllowedAgents(), "web-researcher")
	roster := ResolveAgentRoster("implement_investigate", declared, 1, false, false)
	for _, name := range roster.Effective {
		if name == "web-researcher" {
			t.Fatal("web-researcher must not be effective with search off")
		}
	}
	found := false
	for _, ex := range roster.Excluded {
		if ex.Name == "web-researcher" {
			found = true
			if ex.Code != RosterExcludeWebSearchOff {
				t.Fatalf("code = %q want %q", ex.Code, RosterExcludeWebSearchOff)
			}
		}
	}
	if !found {
		t.Fatal("web-researcher exclusion row missing")
	}
}

func TestBuildActiveWorkflowInjectDataUsesRoster(t *testing.T) {
	frame := CoordinatorTurnFrame{
		Roster: &AgentRoster{
			Declared:  []string{"implementer", "repo-researcher"},
			Effective: []string{"implementer"},
			Excluded:  []ExcludedAgent{{Name: "repo-researcher", Code: RosterExcludeRepoEmpty}},
		},
	}
	frame.RunContext.WorkflowID = "wf"
	frame.RunContext.AllowedAgents = []string{"implementer", "repo-researcher"}
	data := BuildActiveWorkflowInjectData(frame)
	if !reflect.DeepEqual(data.AllowedAgents, []string{"implementer"}) {
		t.Fatalf("allowed = %v want effective roster only", data.AllowedAgents)
	}
	if len(data.ExcludedAgents) != 1 || data.ExcludedAgents[0].Code != RosterExcludeRepoEmpty {
		t.Fatalf("excluded = %+v", data.ExcludedAgents)
	}
}
