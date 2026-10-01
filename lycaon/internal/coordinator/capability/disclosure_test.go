package capability_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/prompts"
)

func TestFilterAgentsForDisclosureShowDefault(t *testing.T) {
	agents := []prompts.SpawnAgentView{{ID: "plan-writer"}, {ID: "plan-reviewer"}}
	reg := capability.DisclosureRegistry{DefaultAgents: capability.DisclosureShow}
	got := capability.FilterAgentsForDisclosure(agents, reg)
	if len(got) != 2 {
		t.Fatalf("got %d want 2 SHOW agents", len(got))
	}
}

func TestFilterAgentsForDisclosureHideOmitted(t *testing.T) {
	agents := []prompts.SpawnAgentView{{ID: "plan-writer"}, {ID: "plan-reviewer"}}
	reg := capability.DisclosureRegistry{
		DefaultAgents:  capability.DisclosureShow,
		AgentOverrides: map[string]capability.DisclosurePolicy{"plan-writer": capability.DisclosureHide},
	}
	got := capability.FilterAgentsForDisclosure(agents, reg)
	if len(got) != 1 || got[0].ID != "plan-reviewer" {
		t.Fatalf("got %+v want only plan-reviewer", got)
	}
}

func TestToolDisclosurePolicyDefaultHide(t *testing.T) {
	reg := capability.DisclosureRegistry{DefaultTools: capability.DisclosureHide}
	if got := capability.ToolDisclosurePolicy(reg, "read"); got != capability.DisclosureHide {
		t.Fatalf("default tool policy = %q want hide", got)
	}
}
