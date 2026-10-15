package toolusage

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWriteRootPreparationApprovesOnlyTheOwnedReadLease(t *testing.T) {
	fixture := &SandboxEvidence{Kind: sandboxWriteRoot, Path: "/var/folders/leaf", PreparationCallID: "folder-inspection"}
	checkpoint := wire.CheckpointEvent{ToolApproval: &wire.ToolApprovalPayload{
		ToolCallID: "folder-inspection",
		Plan: wire.ApprovalPlan{
			DirectoryScopes: []string{"/private/var/folders/leaf", "/private/var/folders", "/"},
			Options: []wire.ApprovalOption{
				{ID: "once", Kind: wire.ApprovalOptionKindCurrentAction, Rung: wire.ApprovalOptionRungOnce, DecisionAction: wire.ApprovalOptionDecisionApprove},
				{ID: "unscoped-lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DecisionAction: wire.ApprovalOptionDecisionApprove},
				{ID: "parent-lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DirectoryScope: "/private/var/folders", DecisionAction: wire.ApprovalOptionDecisionApprove},
				{ID: "read-lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DirectoryScope: "/private/var/folders/leaf", DecisionAction: wire.ApprovalOptionDecisionApprove},
			},
		},
	}}
	if got := fixtureApprovalOption(fixture, checkpoint); got != "read-lease" {
		t.Fatalf("prepared folder inspection option = %q, want read-lease", got)
	}
	checkpoint.ToolApproval.ToolCallID = "candidate-write"
	if got := fixtureApprovalOption(fixture, checkpoint); got != "" {
		t.Fatalf("candidate write inherited preparation authority: %q", got)
	}
	checkpoint.ToolApproval.ToolCallID = "folder-inspection"
	checkpoint.ToolApproval.JoinedCount = 2
	if got := fixtureApprovalOption(fixture, checkpoint); got != "" {
		t.Fatalf("joined action inherited preparation authority: %q", got)
	}
	checkpoint.ToolApproval.JoinedCount = 1
	checkpoint.ToolApproval.Plan.Options = append(checkpoint.ToolApproval.Plan.Options, wire.ApprovalOption{
		ID: "duplicate-read-lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DirectoryScope: "/private/var/folders/leaf", DecisionAction: wire.ApprovalOptionDecisionApprove,
	})
	if got := fixtureApprovalOption(fixture, checkpoint); got != "" {
		t.Fatalf("ambiguous duplicate lease matched: %q", got)
	}
	checkpoint.ToolApproval.Plan.Options = checkpoint.ToolApproval.Plan.Options[:4]
	checkpoint.ToolApproval.Plan.Options[3].Disabled = true
	if got := fixtureApprovalOption(fixture, checkpoint); got != "" {
		t.Fatalf("unavailable lease fell back to an insufficient option: %q", got)
	}
}
