package hitl_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// This is a bounded-input port fixture for identity and retained floor tests.
// Actual bounded matcher/work qualification uses the real capture producer at
// source convergence, not this legacy redactor composition fixture.
type nativeInspectionFixture struct{}

func (nativeInspectionFixture) ProjectOperationInputs(ctx context.Context, state toolcontract.ReviewStateAdmission, args map[string]any) (map[string]any, func(), error) {
	release, err := state.ReserveProgramState(ctx, 8192)
	if err != nil {
		return nil, nil, err
	}
	return observability.RedactCaptureValue(args).(map[string]any), release, nil
}

type nativeReviewMemory struct {
	held   int64
	refuse bool
}

func (m *nativeReviewMemory) ReserveProgramState(_ context.Context, n int64) (func(), error) {
	if m.refuse {
		return nil, errors.New("aggregate ceiling")
	}
	m.held += n
	return func() { m.held -= n }, nil
}

func TestNativeActionGrantIdentityAndInspectionPreserveRawBytes(t *testing.T) {
	memory := &nativeReviewMemory{}
	review := hitl.NewNativeActionReview(t.Context(), memory, nativeInspectionFixture{})
	defer review.Close()
	keys := map[string]bool{}
	for _, path := range []string{"fixture\xff", "fixture\xfe", "fixture\ufffd"} {
		args, input, err := review.Freeze(t.Context(), "read", map[string]any{"path": path, path: "raw key"})
		if err != nil {
			t.Fatal(err)
		}
		action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "read", Args: args, NativeInputs: input, Files: []string{path}}}
		key := hitl.GrantKey(action)
		if key == "" || keys[key] {
			t.Fatal("native approval identity collided")
		}
		keys[key] = true
		inspected, err := input.Inspection()
		if err != nil {
			t.Fatal(err)
		}
		if inspected["representation"] != toolcontract.LosslessOperationArgumentsV1 || !strings.Contains(inspected["canonical_arguments"].(string), "raw key") {
			t.Fatal("inspection lost explicit native representation")
		}
		verdict, decision := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
		if verdict != gate.Ask {
			t.Fatal("fixture lacks real decision")
		}
		plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{SessionID: "session", Kind: api.CheckpointKindToolApproval, ProposedAction: &action, Decision: decision})
		if err != nil {
			t.Fatal(err)
		}
		if plan.ActionDigest != key || plan.Subject.Targets[0].Details["args"].(map[string]any)["representation"] != toolcontract.LosslessOperationArgumentsV1 {
			t.Fatal("approval plan changed reviewed identity or displayed owner arguments")
		}
	}
	review.Close()
	review.Close()
	if memory.held != 0 {
		t.Fatal("native held review leaked aggregate state")
	}
}

func TestNativeActionReviewScreensOriginalValuesBeforeEncoding(t *testing.T) {
	memory := &nativeReviewMemory{}
	review := hitl.NewNativeActionReview(t.Context(), memory, nativeInspectionFixture{})
	defer review.Close()
	_, input, err := review.Freeze(t.Context(), "read", map[string]any{"path": "sensitive\xff", "password": "hidden\xfe"})
	if err != nil {
		t.Fatal(err)
	}
	// Catalog changes before presentation still screen the original raw scalar,
	// not its later base64 text. Named sensitive fields retain the existing floor.
	releaseRedactor := observability.SetCaptureRedactor(func(s string) string { return strings.ReplaceAll(s, "sensitive\xff", "catalog-screened") })
	defer releaseRedactor()
	inspected, err := input.Inspection()
	if err != nil {
		t.Fatal(err)
	}
	canonical := inspected["canonical_arguments"].(string)
	if !strings.Contains(canonical, "catalog-screened") || strings.Contains(canonical, base64.StdEncoding.EncodeToString([]byte("hidden\xfe"))) || strings.Contains(canonical, base64.StdEncoding.EncodeToString([]byte("sensitive\xff"))) {
		t.Fatal("encoded raw secret bypassed existing capture screening")
	}
	original, err := input.Reviewed()
	if err != nil {
		t.Fatal(err)
	}
	if inspected["reviewed_arguments_digest"] != original.ArgumentsDigest() || inspected["arguments_digest"] == original.ArgumentsDigest() {
		t.Fatal("screened view mislabeled original review digest")
	}
}

func TestNativeActionReviewRetainsExistingSemanticGrantRules(t *testing.T) {
	memory := &nativeReviewMemory{}
	review := hitl.NewNativeActionReview(t.Context(), memory, nativeInspectionFixture{})
	var actions []hitl.ProposedAction
	var full []string
	for _, limit := range []int{10, 20} {
		args, input, err := review.Freeze(t.Context(), "read", map[string]any{"path": "same\xff", "limit": limit})
		if err != nil {
			t.Fatal(err)
		}
		reviewed, err := input.Reviewed()
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, reviewed.ArgumentsDigest())
		actions = append(actions, hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "read", Args: args, NativeInputs: input}})
	}
	if hitl.GrantKey(actions[0]) == "" || hitl.GrantKey(actions[0]) != hitl.GrantKey(actions[1]) || full[0] == full[1] {
		t.Fatal("native review changed the existing authority-neutral grant rule")
	}
	structured := actions[0]
	structured.Invocation.NativeInputs = nil
	if hitl.GrantKey(structured) == hitl.GrantKey(actions[0]) {
		t.Fatal("structured and native review domains aliased")
	}
	mismatched := actions[0]
	mismatched.Invocation.Tool = "write"
	if hitl.GrantKey(mismatched) != "" {
		t.Fatal("native reviewed input used under another operation")
	}
	review.Close()
	if hitl.GrantKey(actions[0]) != "" || memory.held != 0 {
		t.Fatal("closed review regained structured fallback authority")
	}
}

func TestNativeActionCheckpointPersistsExplicitInspectionAndDistinctIdentity(t *testing.T) {
	database, manager, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	insertSession(t, database, sessionID)
	memory := &nativeReviewMemory{}
	review := hitl.NewNativeActionReview(ctx, memory, nativeInspectionFixture{})
	defer review.Close()
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, path := range []string{"fixture\xff", "fixture\xfe", "fixture\ufffd"} {
		args, input, err := review.Freeze(ctx, "read", map[string]any{"path": path})
		if err != nil {
			t.Fatal(err)
		}
		action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "read", Args: args, Files: []string{path}, NativeInputs: input}, Scope: hitl.ActionScope{SessionID: sessionID}}
		key := hitl.GrantKey(action)
		response, err := requestExplicitApprovalCheckpoint(t, ctx, manager, hitl.CheckpointRequest{SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ToolCallID: "actual-model-call", ProposedAction: &action, CoalesceChat: sessionID, CoalesceGrantKey: key})
		if err != nil {
			t.Fatal(err)
		}
		if ids[response.CheckpointID] || keys[key] || key == "" {
			t.Fatal("distinct native byte operands shared a checkpoint identity")
		}
		ids[response.CheckpointID] = true
		keys[key] = true
		var rawArgs, rawPayload string
		if err := database.QueryRowContext(ctx, "SELECT args_json,payload_json FROM checkpoints WHERE id=?", response.CheckpointID).Scan(&rawArgs, &rawPayload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rawArgs, "operation_arguments_v1") || !strings.Contains(rawPayload, "native_arguments_representation") || !strings.Contains(rawPayload, key) {
			t.Fatal("checkpoint omitted explicit native inspection/correlation format")
		}
	}
}

func TestNativeActionInspectionRefusesMissingBoundedProducer(t *testing.T) {
	memory := &nativeReviewMemory{}
	review := hitl.NewNativeActionReview(t.Context(), memory, nil)
	_, input, err := review.Freeze(t.Context(), "read", map[string]any{"path": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.Inspection(); err == nil {
		t.Fatal("native inspection fell back to legacy unbounded screening")
	}
	review.Close()
	if memory.held != 0 {
		t.Fatal("failed inspection retained native state")
	}
}
