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
		Tool: "terminal_open", ProjectDir: proj,
		Args:      map[string]any{"command": "go test ./..."},
		Contained: contained,
	})
	if recoverable != settings.TierRecoverable {
		t.Fatalf("contained terminal_open = %v, want recoverable", recoverable)
	}
	push := settings.ClassifyTier(hitl.ProposedAction{
		Tool: "terminal_open", ProjectDir: proj,
		Args:      map[string]any{"command": "git push origin main"},
		Contained: contained,
	})
	if push != settings.TierRecoverable {
		t.Fatalf("git push terminal_open = %v, want recoverable (Contained egress)", push)
	}
	uncontained := settings.ClassifyTier(hitl.ProposedAction{
		Tool: "terminal_open", ProjectDir: proj,
		Args: map[string]any{"command": "go test ./..."},
	})
	if uncontained != settings.TierIrreversible {
		t.Fatalf("uncontained terminal_open = %v, want irreversible", uncontained)
	}
	send := settings.ClassifyTier(hitl.ProposedAction{
		Tool: "terminal_send", ProjectDir: proj,
		Args: map[string]any{"id": "x", "input": "hi"},
	})
	if send != settings.TierRecoverable {
		t.Fatalf("terminal_send = %v, want recoverable", send)
	}
}
