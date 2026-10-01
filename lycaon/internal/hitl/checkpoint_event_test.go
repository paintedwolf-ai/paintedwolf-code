package hitl_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func storedPlan(command string, band api.ConsequenceBand, code api.ConsequenceCode) map[string]any {
	action := hitl.ProposedAction{Tool: "command", Command: command}
	presentation, reasons := approvalPlanPresentation()
	presentation.Tool = "command"
	presentation.Command = command
	presentation.Impact = "Run this command."
	presentation.ConsequenceBand = string(band)
	presentation.ConsequenceCode = string(code)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Approval needed",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: command}},
	}, presentation, reasons, []hitl.ApprovalOption{hitl.CurrentActionOption()}, hitl.FaceContext{})
	if err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		panic(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(encoded, &stored); err != nil {
		panic(err)
	}
	return stored
}

func TestStoredToolApprovalEventProjectsImmutablePlan(t *testing.T) {
	event := hitl.StoredCheckpointToEvent(hitl.StoredCheckpoint{
		ID: "checkpoint-1", SessionID: "child-session", Kind: api.CheckpointKindToolApproval,
		Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"approval_plan": storedPlan("git push origin main", "", "")},
	})
	if event.SessionID != "child-session" || event.ToolApproval == nil {
		t.Fatalf("event = %+v", event)
	}
	if got := event.ToolApproval.Plan.Presentation.Command; got != "git push origin main" {
		t.Fatalf("command = %q", got)
	}
	if got := event.ToolApproval.Plan.Subject.Targets[0].Label; got != "git push origin main" {
		t.Fatalf("subject = %q", got)
	}
}

func TestStoredCheckpointConsequenceBandRoundTrip(t *testing.T) {
	event := hitl.StoredCheckpointToEvent(hitl.StoredCheckpoint{
		ID: "checkpoint-2", SessionID: "sess", Kind: api.CheckpointKindToolApproval,
		Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"approval_plan": storedPlan("publish", api.ConsequenceBandHighRisk, api.ConsequenceCodeSecret)},
	})
	presentation := event.ToolApproval.Plan.Presentation
	if presentation.ConsequenceBand != api.ConsequenceBandHighRisk || presentation.ConsequenceCode != api.ConsequenceCodeSecret {
		t.Fatalf("presentation = %+v", presentation)
	}
}

func TestStoredCheckpointPayloadConsequenceOverrideRoundTrip(t *testing.T) {
	event := hitl.StoredCheckpointToEvent(hitl.StoredCheckpoint{
		ID: "checkpoint-4", SessionID: "sess", Kind: api.CheckpointKindToolApproval,
		Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"approval_plan":    storedPlan("git status", api.ConsequenceBandStandard, ""),
			"consequence_band": string(api.ConsequenceBandHighRisk),
			"consequence_code": string(api.ConsequenceCodeDetection),
		},
	})
	if event.ToolApproval.ConsequenceBand != api.ConsequenceBandHighRisk {
		t.Fatalf("payload band = %q want high_risk", event.ToolApproval.ConsequenceBand)
	}
	if event.ToolApproval.ConsequenceCode != api.ConsequenceCodeDetection {
		t.Fatalf("payload code = %q want detection", event.ToolApproval.ConsequenceCode)
	}
	if event.ToolApproval.Plan.Presentation.ConsequenceBand != api.ConsequenceBandStandard {
		t.Fatalf("plan band = %q want standard", event.ToolApproval.Plan.Presentation.ConsequenceBand)
	}
}

func TestStoredCheckpointRepeatRoundTrip(t *testing.T) {
	event := hitl.StoredCheckpointToEvent(hitl.StoredCheckpoint{
		ID: "checkpoint-repeat", SessionID: "sess", Kind: api.CheckpointKindToolApproval,
		Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{
			"approval_plan": storedPlan("aws s3 rb s3://b", "", ""),
			"repeat": map[string]any{
				"reason_key":         "authority_misuse:aws-cli/s3-remove-bucket",
				"count":              3,
				"subjects":           []any{"aws s3 rb s3://a"},
				"subjects_truncated": false,
				"suppressed_count":   1,
			},
		},
	})
	if event.ToolApproval == nil || event.ToolApproval.Repeat == nil {
		t.Fatal("repeat missing from event")
	}
	got := event.ToolApproval.Repeat
	if got.Count != 3 || got.ReasonKey != "authority_misuse:aws-cli/s3-remove-bucket" {
		t.Fatalf("repeat = %+v", got)
	}
	if len(got.Subjects) != 1 || got.Subjects[0] != "aws s3 rb s3://a" {
		t.Fatalf("subjects = %+v", got.Subjects)
	}
	if got.SuppressedCount != 1 {
		t.Fatalf("suppressed = %d want 1", got.SuppressedCount)
	}
}

func TestStoredCheckpointDoesNotExposeAuthorityDeltas(t *testing.T) {
	event := hitl.StoredCheckpointToEvent(hitl.StoredCheckpoint{
		ID: "checkpoint-3", SessionID: "sess", Kind: api.CheckpointKindToolApproval,
		Status: hitl.DecisionStatusPending, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"approval_plan": storedPlan("git status", "", "")},
	})
	if len(event.ToolApproval.Plan.Options) != 1 || event.ToolApproval.Plan.Options[0].ID != "approve_current_action" {
		t.Fatalf("options = %+v", event.ToolApproval.Plan.Options)
	}
}
