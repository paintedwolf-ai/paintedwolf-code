package inspector

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvidenceAnchorsRejectBareVerdict(t *testing.T) {
	ok, reason := EvidenceAnchored(evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		GateVerdict: string(evidence.GateVerdictPassed),
	})
	if ok || reason == "" {
		t.Fatal("expected missing anchor rejection")
	}
}

func TestEvidenceAnchorsAcceptVerifyWithExitCode(t *testing.T) {
	ok, _ := EvidenceAnchored(evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"exit_code": 0,
			"command":   "go test ./...",
		},
	})
	if !ok {
		t.Fatal("expected anchored verify to pass")
	}
}

func TestPlanReviewAdvisoryOnly(t *testing.T) {
	ok, _ := EvidenceAnchored(evidence.Record{
		GateType:    string(evidence.GateTypePlanReview),
		GateVerdict: string(evidence.GateVerdictApproved),
	})
	if ok {
		t.Fatal("plan_review must not satisfy gates")
	}
}

func TestSimpleInspectorCheckGates(t *testing.T) {
	dir := t.TempDir()
	store := NewJSONLStore(DefaultEvidenceDir)
	ins := NewSimpleInspector(store)
	ins.ProjectDir = func(context.Context, string) (string, error) { return dir, nil }
	ctx := context.Background()
	delegationID := "dep-1"
	taskID := "task-1"

	result, err := ins.CheckGates(ctx, delegationID, []string{taskID}, nil)
	testutil.FailErr(t, "ins.CheckGates failed", err)
	if result.OK {
		t.Fatal("expected missing verify evidence")
	}

	if err := store.Append(ctx, dir, evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		RunID:       delegationID,
		Slot:        taskID,
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"command":   "go test ./...",
			"exit_code": 0,
		},
	}); err != nil {
		testutil.FailErr(t, "store.Append verify evidence", err)
	}
	result, err = ins.CheckGates(ctx, delegationID, []string{taskID}, nil)
	testutil.FailErr(t, "ins.CheckGates failed", err)
	if !result.OK {
		t.Fatalf("expected gates ok: missing=%v errors=%v", result.Missing, result.Errors)
	}
}

func TestJSONLStoreAppendRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	store := NewJSONLStore(DefaultEvidenceDir)
	ctx := context.Background()
	rec := evidence.Record{
		GateType:    string(evidence.GateTypeVerify),
		RunID:       "d1",
		Slot:        "t1",
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"exit_code": 0,
			"command":   "go test ./...",
		},
	}
	if err := store.Append(ctx, dir, rec); err != nil {
		testutil.FailErr(t, "store.Append failed", err)
	}
	got, err := store.ReadAll(ctx, dir, "d1", "t1", evidence.GateTypeVerify)
	if err != nil || len(got) != 1 {
		t.Fatalf("read = %d err=%v", len(got), err)
	}
}
