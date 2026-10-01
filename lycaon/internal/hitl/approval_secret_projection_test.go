package hitl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApprovalPlanUsesCatalogRedactionForDisplayProjection(t *testing.T) {
	const secret = "4711"
	observability.SetCaptureRedactor(func(value string) string {
		return strings.ReplaceAll(value, secret, "[REDACTED]")
	})
	t.Cleanup(func() { observability.SetCaptureRedactor(nil) })
	action := ProposedAction{
		Tool: "request", Args: map[string]any{"note": "pin=" + secret},
		Files: []string{"notes-" + secret}, Command: "send " + secret,
		EstimatedImpact: "uses " + secret,
		FileChanges: []api.ApprovalFileChange{{
			Path: "conf/" + secret + ".env", FromPath: "old-" + secret, Operation: "write",
			Before: "TOKEN=" + secret + "\n", After: "TOKEN=" + secret + "\nNEXT=1\n",
			BeforeSHA256: "before-sum", AfterSHA256: "after-sum", BeforeBytes: 11, AfterBytes: 18,
		}},
	}
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	plan, err := CompileCheckpointApprovalPlan(CheckpointRequest{
		Title: "Approve " + secret, ProposedAction: &action,
		Decision: decision,
	})
	if err != nil {
		t.Fatalf("compile approval plan: %v", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal approval plan: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("approval display retained catalog secret: %s", raw)
	}
	// The file review keeps its exact identity beside the screened text.
	if len(plan.Presentation.FileChanges) != 1 {
		t.Fatalf("file changes = %+v", plan.Presentation.FileChanges)
	}
	change := plan.Presentation.FileChanges[0]
	if change.BeforeSHA256 != "before-sum" || change.AfterSHA256 != "after-sum" || change.BeforeBytes != 11 || change.AfterBytes != 18 {
		t.Fatalf("file change identity changed: %+v", change)
	}
	if !strings.Contains(change.After, "NEXT=1") || !strings.Contains(change.After, "[REDACTED]") {
		t.Fatalf("file change text was not screened in place: %+v", change)
	}
	// The digest still binds the unscreened action.
	if plan.ActionDigest != GrantKey(action) {
		t.Fatal("screening the presentation changed the action digest")
	}
}

func TestContentApplyWireUsesRedactedProjectionAndKeepsExactPlan(t *testing.T) {
	const secret = "4711"
	observability.SetCaptureRedactor(func(value string) string {
		return strings.ReplaceAll(value, secret, "[REDACTED]")
	})
	t.Cleanup(func() { observability.SetCaptureRedactor(nil) })
	before := "old " + secret
	plan, err := CompileContentApplyPlan(ContentApplyPayload{
		Tool: "write-" + secret, Path: "notes-" + secret,
		Before: &before, After: "new " + secret,
	})
	if err != nil {
		t.Fatalf("compile content apply plan: %v", err)
	}
	stored, err := storeContentApplyPlan(plan)
	if err != nil {
		t.Fatalf("store content apply plan: %v", err)
	}
	exact, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal exact content apply plan: %v", err)
	}
	projection := contentApplyFromPayload(StoredCheckpoint{Payload: map[string]any{
		"content_apply_plan": stored,
	}})
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal content apply projection: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("content apply projection retained catalog secret: %s", raw)
	}
	afterProjection, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal exact content apply plan after projection: %v", err)
	}
	if string(afterProjection) != string(exact) {
		t.Fatal("content apply projection mutated the authoritative plan")
	}
}
