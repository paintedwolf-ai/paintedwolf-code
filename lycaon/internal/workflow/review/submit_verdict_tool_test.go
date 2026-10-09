package review_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

// walkPlanRunToReview drives a plan run intake→…→approve then fires critique into review.

func TestSubmitVerdictTerminalAdvancesReview(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "scope matches the goal"},
	}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run submit_verdict", err)
	var res workflowreview.SubmitVerdictToolResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
	if !res.OK || !res.Terminal || res.EvidenceKey != "plan_review" {
		t.Fatalf("result = %+v", res)
	}

	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve (terminal verdict auto-advances)", after.CurrentPhase)
	}
}

func TestSubmitVerdictNonTerminalReloops(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run submit_verdict", err)
	var res workflowreview.SubmitVerdictToolResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
	if !res.OK || res.Terminal || res.Attempt != 1 {
		t.Fatalf("result = %+v", res)
	}

	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review (non-terminal verdict re-loops)", after.CurrentPhase)
	}
}

// A non-terminal verdict submitted after the review loop already reached its
// iteration_cap is rejected outright rather than accepted and bumped past the
// cap (RecordReviewLoopVerdict's IterationCapExceeded branch).
func TestSubmitVerdictRejectsNonTerminalPastIterationCap(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	args := map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}
	tctxFor := func(callID string) tools.ToolContext {
		tctx := toolContext("coordinator", "sess-1", projectDir)
		tctx.Identity.ToolCallID = callID
		return tctx
	}
	// plan.review's iteration_cap is 3 — three non-terminal rounds reach it.
	for i := 1; i <= 3; i++ {
		out, err := reg.Run(ctx, "submit_verdict", args, tctxFor(fmt.Sprintf("verdict-call-%d", i)))
		testutil.FailErr(t, "reg.Run submit_verdict", err)
		var res workflowreview.SubmitVerdictToolResult
		testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
		if !res.OK || res.Terminal || res.Attempt != i {
			t.Fatalf("round %d result = %+v", i, res)
		}
	}

	out, err := reg.Run(ctx, "submit_verdict", args, tctxFor("verdict-call-4"))
	reject := requireVerdictRejection(t, out, err, workflowreview.SubmitVerdictIterationCapCode)
	if reject.Data["attempt"] != 3 || reject.Data["review_iteration_cap"] != 3 {
		t.Fatalf("cap observation = %+v", reject.Data)
	}

	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review (rejected round must not advance)", after.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := runstate.ReviewLoopAttempt(vars, "review"); n != 3 {
		t.Fatalf("attempt = %d want 3 (rejected round must not bump past the cap)", n)
	}
}

func TestSubmitVerdictExactToolReplayDoesNotConsumeAnotherAttempt(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "register submit_verdict", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	args := map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}
	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Identity.ToolCallID = "submit-verdict-call-1"
	first, err := reg.Run(ctx, "submit_verdict", args, tctx)
	testutil.FailErr(t, "submit first verdict", err)
	replayed, err := reg.Run(ctx, "submit_verdict", args, tctx)
	testutil.FailErr(t, "replay verdict", err)
	if replayed != first {
		t.Fatalf("replay = %s want %s", replayed, first)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get review vars", err)
	if attempt := runstate.ReviewLoopAttempt(vars, "review"); attempt != 1 {
		t.Fatalf("attempt = %d want 1", attempt)
	}
}

func TestRecoverPreparedVerdictOperation(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	input := runstate.VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := runstate.VerdictOperation{ToolCallID: "recover-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "recover-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.Verdicts.PrepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)
	testutil.FailErr(t, "recover verdict operation", mgr.Verdicts.RecoverVerdictOperations(ctx))
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get recovered vars", err)
	if attempt := runstate.ReviewLoopAttempt(vars, "review"); attempt != 1 {
		t.Fatalf("attempt = %d want 1", attempt)
	}
	stored, found, err := mgr.Store.Verdicts.GetVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "committed" {
		t.Fatalf("verdict status = %q found=%v", stored.Status, found)
	}
}

func TestSubmitVerdictRejectsOffReviewLoopPhase(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "x"},
	}, toolContext("coordinator", "sess-1", projectDir))
	_ = requireVerdictRejection(t, out, err, workflowreview.SubmitVerdictUnavailableCode)
}

func TestSubmitVerdictInvalidEchoesSchema(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "SHIP_IT"},
	}, tctx)
	reject := requireVerdictRejection(t, out, err, workflowvalidation.ReviewLoopVerdictInvalidCode)
	expected, _ := reject.Data["expected_call"].(string)
	if !strings.Contains(expected, "APPROVED|NEEDS_REVISION") || !strings.Contains(expected, "bullets") {
		t.Fatalf("expected_call must echo the expected schema, got %q", expected)
	}
	if tctx.Effects.Out.Facts.Resolution() != api.ToolResultOutcomeRejected || tctx.Effects.Out.Facts.PrimaryCode() != workflowvalidation.ReviewLoopVerdictInvalidCode {
		t.Fatalf("facts = %+v", tctx.Effects.Out.Facts)
	}

	out, err = reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{
			"verdict": "APPROVED", "bullets": "looks good",
			"cited_evidence": []any{map[string]any{"handle": "reviewer:read#1"}},
		},
	}, tctx)
	reject = requireVerdictRejection(t, out, err, workflowvalidation.ReviewLoopVerdictInvalidCode)
	if reject.Code != workflowvalidation.ReviewLoopVerdictInvalidCode || !strings.Contains(reject.Data["reason"].(string), "cited_evidence") {
		t.Fatalf("nested citation rejection = %+v", reject)
	}

	// An invalid submission is not a review round — the run holds in review, attempt unchanged.
	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", after.CurrentPhase)
	}
}

func TestSubmitVerdictGroundingRejectReturnsRecoverableCode(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	mgr.Verdicts.VerdictGrounding = func(context.Context, string, []api.CitationGroundingCitedEvidence, []string, []string) (guidance.VerdictGroundingEval, error) {
		return guidance.VerdictGroundingEval{
			Code:            guidance.VerdictCitationsRequiredCode,
			ObservedHandles: []string{"reviewer-1:read#1"},
		}, nil
	}
	reg := catalogRegistry(t)
	testutil.FailErr(t, "workflowreview.RegisterSubmitVerdictTool", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Effects.Out = &tools.ToolInvocationOut{}

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "scope matches the goal"},
	}, tctx)
	reject := requireVerdictRejection(t, out, err, guidance.VerdictCitationsRequiredCode)
	expected, _ := reject.Data["expected_call"].(string)
	if !strings.Contains(expected, `"handle":"<observed-handle>"`) {
		t.Fatalf("expected_call = %q, want handle citation contract", expected)
	}
	if tctx.Effects.Out.Facts.Resolution() != api.ToolResultOutcomeRejected || tctx.Effects.Out.Facts.PrimaryCode() != guidance.VerdictCitationsRequiredCode {
		t.Fatalf("facts = %+v", tctx.Effects.Out.Facts)
	}
	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", after.CurrentPhase)
	}
}

// TestRecoverVerdictOperationConvergesWhenRunGone: a pending journal row whose run was
// canceled resolves terminally at recovery, so it is neither re-listed on every boot
// nor fatal to serve.
func TestRecoverVerdictOperationConvergesWhenRunGone(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	input := runstate.VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := runstate.VerdictOperation{ToolCallID: "orphan-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "orphan-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.Verdicts.PrepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	_, err = mgr.Controls.Cancel(ctx, run.ID, "user canceled")
	testutil.FailErr(t, "cancel run", err)

	testutil.FailErr(t, "recover verdict operations", mgr.Verdicts.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.Verdicts.GetVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "diverged" {
		t.Fatalf("verdict status = %q found=%v, want diverged", stored.Status, found)
	}
	pending, err := mgr.Store.Verdicts.PendingVerdictOperations(ctx)
	testutil.FailErr(t, "list pending", err)
	if len(pending) != 0 {
		t.Fatalf("pending after recovery = %+v, want none (recovery must converge)", pending)
	}

	// A live duplicate of the diverged tool call gets an honest error, not a retry.
	if _, err := mgr.Verdicts.RecordReviewLoopVerdict(workflowreview.WithOperationID(ctx, op.ToolCallID), "sess-1", input.Verdict, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "can no longer apply") {
		t.Fatalf("diverged replay err = %v, want 'can no longer apply'", err)
	}
}

// TestRecoverVerdictOperationRebasesRevisionDrift: same run and phase but the revision
// advanced after prepare (a failed first attempt) — recovery rebases and commits instead
// of wedging on the revision CAS forever.
func TestRecoverVerdictOperationRebasesRevisionDrift(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	input := runstate.VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := runstate.VerdictOperation{ToolCallID: "drift-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "drift-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.Verdicts.PrepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	// Advance the run revision under the same phase (unrelated vars write).
	fresh, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get vars", err)
	testutil.FailErr(t, "bump revision", mgr.Store.State.UpdateVars(ctx, fresh, projectDir, runstate.SetHostVar(vars, "drift.marker", "1")))

	testutil.FailErr(t, "recover verdict operations", mgr.Verdicts.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.Verdicts.GetVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "committed" {
		t.Fatalf("verdict status = %q found=%v, want committed after rebase", stored.Status, found)
	}
	after, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get recovered vars", err)
	if attempt := runstate.ReviewLoopAttempt(after, "review"); attempt != 1 {
		t.Fatalf("attempt = %d want 1", attempt)
	}
}

// TestRecoverVerdictOperationUndecodableInputResolves: a corrupt journal row resolves
// terminally with the diagnostic on the row instead of erroring every boot.
func TestRecoverVerdictOperationUndecodableInputResolves(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	digest := sha256.Sum256([]byte("[]"))
	op := runstate.VerdictOperation{ToolCallID: "corrupt-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "corrupt-evidence", EvidenceJSON: "[]"}
	_, _, err := mgr.Store.Verdicts.PrepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	testutil.FailErr(t, "recover verdict operations", mgr.Verdicts.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.Verdicts.GetVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "diverged" {
		t.Fatalf("verdict status = %q found=%v, want diverged", stored.Status, found)
	}
	if !strings.Contains(stored.Error, "invalid") {
		t.Fatalf("row diagnostic = %q, want decode reason", stored.Error)
	}
}

func requireVerdictRejection(t *testing.T, output string, err error, code string) *toolrejection.ToolReject {
	t.Helper()
	reject := toolrejection.AsToolReject(err)
	if output != "" || reject == nil || reject.Code != code {
		t.Fatalf("verdict refusal: output=%q error=%v want %s", output, err, code)
	}
	return reject
}
