package hitl

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDecisionSubjectToolApprovalCommand(t *testing.T) {
	got := decisionSubject(storedApprovalCheckpoint(t, ProposedAction{Tool: "command", Command: "git push origin main"}))
	if got != "git push origin main" {
		t.Fatalf("subject = %q", got)
	}
}

func TestDecisionCausingCommandWriteRoot(t *testing.T) {
	task := ApprovalOption{
		ID: "task", Kind: ApprovalOptionLease, Rung: ApprovalRungChat, Scope: ApprovalGrantScopeChat,
		Title: TitleAllowForThisChat, Coverage: "c", ExpiresWhen: ExpiresWhenChatDeleted, ReaskWhen: "r",
		DecisionAction: ApprovalOptionApprove,
		Authority: []ApprovalAuthorityDelta{{
			Kind: AuthorityWriteRootChat, ChatSessionID: "s1", WriteRoots: []string{"/Users/me/go"},
			Grant: &ApprovalGrant{ID: "g1"},
		}},
	}
	plan, err := NewApprovalPlan(
		ProposedAction{Tool: "write_root", Args: map[string]any{"proposed_write_root": "/Users/me/go"}},
		ApprovalStagePreSpawn,
		ApprovalSubject{
			Kind: ApprovalSubjectWriteRootSet, Title: "Allow write access",
			Targets: []ApprovalTarget{{Kind: "write_root", Label: "/Users/me/go"}},
		},
		ApprovalPresentation{
			Action: "Sandbox write access", Tool: "command", Command: "go mod tidy", Impact: "writes",
			Gate:  api.GateOutsideRootsWrite,
			Cited: []PresentedFact{{Gate: api.GateOutsideRootsWrite, Key: "file.path", Value: "/Users/me/go", Source: "boundary"}},
		},
		[]api.ApprovalGate{api.GateOutsideRootsWrite},
		[]ApprovalOption{task},
		FaceContext{})
	testutil.FailErr(t, "create approval plan", err)
	stored, err := storeApprovalPlan(plan)
	testutil.FailErr(t, "store approval plan", err)
	row := StoredCheckpoint{
		Kind:     api.CheckpointKindToolApproval,
		ToolName: "write_root",
		Payload:  map[string]any{"approval_plan": stored},
	}
	if got := decisionCausingCommand(row); got != "go mod tidy" {
		t.Fatalf("causing = %q", got)
	}
	if got := decisionTool(row); got != "command" {
		t.Fatalf("tool = %q", got)
	}
	if got := decisionSubject(row); got != "/Users/me/go" {
		t.Fatalf("subject = %q", got)
	}
}

func TestDecisionCausingCommandSuppressedWhenSubjectIsArgv(t *testing.T) {
	got := decisionCausingCommand(storedApprovalCheckpoint(t, ProposedAction{Tool: "command", Command: "git status"}))
	if got != "" {
		t.Fatalf("causing = %q want empty", got)
	}
}

func TestDecisionSubjectContentApplyPath(t *testing.T) {
	got := decisionSubject(StoredCheckpoint{
		Kind: api.CheckpointKindContentApply,
		Path: "src/app.ts",
	})
	if got != "src/app.ts" {
		t.Fatalf("subject = %q", got)
	}
}

func TestDecisionSubjectWritePathArg(t *testing.T) {
	got := decisionSubject(storedApprovalCheckpoint(t, ProposedAction{Tool: "write", Files: []string{"README.md"}}))
	if got != "README.md" {
		t.Fatalf("subject = %q", got)
	}
}

func TestApprovalPlanIdentityRejectsPersistedContentMutation(t *testing.T) {
	row := storedApprovalCheckpoint(t, ProposedAction{Tool: "command", Command: "safe command"})
	raw := row.Payload["approval_plan"].(map[string]any)
	subject := raw["subject"].(map[string]any)
	targets := subject["targets"].([]any)
	targets[0].(map[string]any)["label"] = "different command"
	if _, err := approvalPlanFromMap(raw); err == nil {
		t.Fatal("mutated persisted plan must fail identity validation")
	}
}

func storedApprovalCheckpoint(t *testing.T, action ProposedAction) StoredCheckpoint {
	t.Helper()
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	plan, err := CompileCheckpointApprovalPlan(CheckpointRequest{
		Title: "Approval needed", ProposedAction: &action, Decision: decision,
	})
	if err != nil {
		t.Fatalf("compile approval plan: %v", err)
	}
	stored, err := storeApprovalPlan(plan)
	if err != nil {
		t.Fatalf("store approval plan: %v", err)
	}
	return StoredCheckpoint{Kind: api.CheckpointKindToolApproval, Payload: map[string]any{"approval_plan": stored}}
}
