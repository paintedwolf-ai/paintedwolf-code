package settings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
)

func TestClassifyTierTerminalOpenParityWithCommand(t *testing.T) {
	proj := "/proj"
	contained := hitl.Contained{
		FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj},
	}
	recoverable := settings.ClassifyTier(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "terminal_open",
Args: map[string]any{"command": "go test ./..."},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
	if recoverable != settings.TierRecoverable {
		t.Fatalf("contained terminal_open = %v, want recoverable", recoverable)
	}
	push := settings.ClassifyTier(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "terminal_open",
Args: map[string]any{"command": "git push origin main"},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
	if push != settings.TierRecoverable {
		t.Fatalf("git push terminal_open = %v, want recoverable (Contained egress)", push)
	}
	uncontained := settings.ClassifyTier(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "terminal_open",
Args: map[string]any{"command": "go test ./..."},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
})
	if uncontained != settings.TierIrreversible {
		t.Fatalf("uncontained terminal_open = %v, want irreversible", uncontained)
	}
	send := settings.ClassifyTier(hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "terminal_send",
Args: map[string]any{"id": "x", "input": "hi"},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
})
	if send != settings.TierRecoverable {
		t.Fatalf("terminal_send = %v, want recoverable", send)
	}
}
