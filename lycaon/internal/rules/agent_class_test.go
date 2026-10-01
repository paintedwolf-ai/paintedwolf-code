package rules

import "testing"

// Dispatch class follows the capabilities an agent declares, so every agent with
// a discovery or critique capability is classified without being listed here.
func TestAgentTaskClassFollowsDeclaredCapabilities(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"repo-researcher", "path-explorer", "security-reviewer", "web-researcher"} {
		if !isResearchAgentTask(map[string]any{"agent_type": agent}) {
			t.Errorf("%s declares a discovery capability but is not a research task", agent)
		}
	}
	for _, agent := range []string{"plan-reviewer", "plan-reviewer-alt", "code-reviewer", "skeptic"} {
		if !isCriticAgentTask(map[string]any{"agent_type": agent}) {
			t.Errorf("%s declares a critique capability but is not a critic task", agent)
		}
	}
	for _, class := range []struct {
		name string
		fn   func(map[string]any) bool
	}{{"research", isResearchAgentTask}, {"critic", isCriticAgentTask}} {
		if class.fn(map[string]any{"agent_type": "implementer"}) {
			t.Errorf("implementer classified as %s", class.name)
		}
		if class.fn(map[string]any{"agent_type": "not-an-agent"}) {
			t.Errorf("unregistered agent classified as %s", class.name)
		}
	}
}
