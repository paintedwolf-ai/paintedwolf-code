package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseSubmitVerdictArgsCoercion(t *testing.T) {
	verdict, cited, citedURLs, err := parseSubmitVerdictArgs(map[string]any{
		"verdict": map[string]any{
			"verdict":         "ADJUDICATED",
			"vulnerabilities": []any{},
			"dismissed":       []any{map[string]any{"issue": "x"}},
		},
		"cited_evidence": []any{
			map[string]any{"path": "go.mod", "line": float64(1), "excerpt": "module x"},
		},
		"cited_urls": []any{" https://example.com/advisory "},
	})
	testutil.FailErr(t, "parseSubmitVerdictArgs", err)
	if verdict["verdict"] != "ADJUDICATED" {
		t.Fatalf("verdict = %q", verdict["verdict"])
	}
	if verdict["vulnerabilities"] != "[]" {
		t.Fatalf("vulnerabilities = %q want coerced JSON text", verdict["vulnerabilities"])
	}
	if verdict["dismissed"] != `[{"issue":"x"}]` {
		t.Fatalf("dismissed = %q", verdict["dismissed"])
	}
	if len(cited) != 1 || cited[0].Path != "go.mod" || cited[0].Line != 1 {
		t.Fatalf("cited = %+v", cited)
	}
	if len(citedURLs) != 1 || citedURLs[0] != "https://example.com/advisory" {
		t.Fatalf("citedURLs = %+v", citedURLs)
	}

	if _, _, _, err := parseSubmitVerdictArgs(map[string]any{}); err == nil {
		t.Fatal("expected error for missing verdict object")
	}
	if _, _, _, err := parseSubmitVerdictArgs(map[string]any{
		"verdict":        map[string]any{"verdict": "X"},
		"cited_evidence": []any{map[string]any{"line": float64(3)}},
	}); err == nil {
		t.Fatal("expected error for cited_evidence without handle or path")
	}
	for name, args := range map[string]map[string]any{
		"unknown top-level field": {
			"verdict": map[string]any{"verdict": "X"}, "claims": []any{},
		},
		"evidence key instead of handle": {
			"verdict":        map[string]any{"verdict": "X"},
			"cited_evidence": []any{map[string]any{"evidence": "reviewer:read#1"}},
		},
		"invalid URL item": {
			"verdict": map[string]any{"verdict": "X"}, "cited_urls": []any{7},
		},
		"URL without host": {
			"verdict": map[string]any{"verdict": "X"}, "cited_urls": []any{"https://"},
		},
		"handle with path fields": {
			"verdict":        map[string]any{"verdict": "X"},
			"cited_evidence": []any{map[string]any{"handle": "reviewer:read#1", "line": float64(4)}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseSubmitVerdictArgs(args); err == nil {
				t.Fatalf("parseSubmitVerdictArgs(%s) unexpectedly succeeded", name)
			}
		})
	}
}

// walkPlanRunToReview drives a plan run intake→…→approve then fires critique into review.
func walkPlanRunToReview(ctx context.Context, t *testing.T, mgr *RunManager, blueprintMgr *blueprint.Manager, sessionID string) *api.WorkflowRun {
	t.Helper()
	run, err := startRun(ctx, mgr, sessionID, "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanThroughExpand", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	run, err = mgr.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition critique", err)
	if run.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", run.CurrentPhase)
	}
	return run
}

func TestSubmitVerdictTerminalAdvancesReview(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "scope matches the goal"},
	}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run submit_verdict", err)
	var res SubmitVerdictToolResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
	if !res.OK || !res.Terminal || res.EvidenceKey != "plan_review" {
		t.Fatalf("result = %+v", res)
	}

	after, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve (terminal verdict auto-advances)", after.CurrentPhase)
	}
}

func TestSubmitVerdictNonTerminalReloops(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run submit_verdict", err)
	var res SubmitVerdictToolResult
	testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
	if !res.OK || res.Terminal || res.Attempt != 1 {
		t.Fatalf("result = %+v", res)
	}

	after, err := mgr.Get(ctx, run.ID)
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
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	args := map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}
	tctxFor := func(callID string) tools.ToolContext {
		tctx := toolContext("coordinator", "sess-1", projectDir)
		tctx.ToolCallID = callID
		return tctx
	}
	// plan.review's iteration_cap is 3 — three non-terminal rounds reach it.
	for i := 1; i <= 3; i++ {
		out, err := reg.Run(ctx, "submit_verdict", args, tctxFor(fmt.Sprintf("verdict-call-%d", i)))
		testutil.FailErr(t, "reg.Run submit_verdict", err)
		var res SubmitVerdictToolResult
		testutil.FailErr(t, "unmarshal result", json.Unmarshal([]byte(out), &res))
		if !res.OK || res.Terminal || res.Attempt != i {
			t.Fatalf("round %d result = %+v", i, res)
		}
	}

	out, err := reg.Run(ctx, "submit_verdict", args, tctxFor("verdict-call-4"))
	reject := requireVerdictRejection(t, out, err, SubmitVerdictIterationCapCode)
	if reject.Data["attempt"] != 3 || reject.Data["review_iteration_cap"] != 3 {
		t.Fatalf("cap observation = %+v", reject.Data)
	}

	after, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review (rejected round must not advance)", after.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if n := ReviewLoopAttempt(vars, "review"); n != 3 {
		t.Fatalf("attempt = %d want 3 (rejected round must not bump past the cap)", n)
	}
}

func TestSubmitVerdictExactToolReplayDoesNotConsumeAnotherAttempt(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register submit_verdict", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	args := map[string]any{
		"verdict": map[string]any{"verdict": "NEEDS_REVISION", "bullets": "verify section is missing"},
	}
	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.ToolCallID = "submit-verdict-call-1"
	first, err := reg.Run(ctx, "submit_verdict", args, tctx)
	testutil.FailErr(t, "submit first verdict", err)
	replayed, err := reg.Run(ctx, "submit_verdict", args, tctx)
	testutil.FailErr(t, "replay verdict", err)
	if replayed != first {
		t.Fatalf("replay = %s want %s", replayed, first)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get review vars", err)
	if attempt := ReviewLoopAttempt(vars, "review"); attempt != 1 {
		t.Fatalf("attempt = %d want 1", attempt)
	}
}

func TestRecoverPreparedVerdictOperation(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	input := VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := verdictOperation{ToolCallID: "recover-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "recover-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.prepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)
	testutil.FailErr(t, "recover verdict operation", mgr.RecoverVerdictOperations(ctx))
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get recovered vars", err)
	if attempt := ReviewLoopAttempt(vars, "review"); attempt != 1 {
		t.Fatalf("attempt = %d want 1", attempt)
	}
	stored, found, err := mgr.Store.getVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "committed" {
		t.Fatalf("verdict status = %q found=%v", stored.Status, found)
	}
}

func TestSubmitVerdictRejectsOffReviewLoopPhase(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "x"},
	}, toolContext("coordinator", "sess-1", projectDir))
	_ = requireVerdictRejection(t, out, err, SubmitVerdictUnavailableCode)
}

func TestSubmitVerdictInvalidEchoesSchema(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")

	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Out = &tools.ToolInvocationOut{}
	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "SHIP_IT"},
	}, tctx)
	reject := requireVerdictRejection(t, out, err, ReviewLoopVerdictInvalidCode)
	expected, _ := reject.Data["expected_call"].(string)
	if !strings.Contains(expected, "APPROVED|NEEDS_REVISION") || !strings.Contains(expected, "bullets") {
		t.Fatalf("expected_call must echo the expected schema, got %q", expected)
	}
	if tctx.Out.Facts.Resolution() != api.ToolResultOutcomeRejected || tctx.Out.Facts.PrimaryCode() != ReviewLoopVerdictInvalidCode {
		t.Fatalf("facts = %+v", tctx.Out.Facts)
	}

	out, err = reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{
			"verdict": "APPROVED", "bullets": "looks good",
			"cited_evidence": []any{map[string]any{"handle": "reviewer:read#1"}},
		},
	}, tctx)
	reject = requireVerdictRejection(t, out, err, ReviewLoopVerdictInvalidCode)
	if reject.Code != ReviewLoopVerdictInvalidCode || !strings.Contains(reject.Data["reason"].(string), "cited_evidence") {
		t.Fatalf("nested citation rejection = %+v", reject)
	}

	// An invalid submission is not a review round — the run holds in review, attempt unchanged.
	after, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get run", err)
	if after.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", after.CurrentPhase)
	}
}

func TestSubmitVerdictGroundingRejectReturnsRecoverableCode(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	mgr.VerdictGrounding = func(context.Context, string, []api.CitationGroundingCitedEvidence, []string, []string) (guidance.VerdictGroundingEval, error) {
		return guidance.VerdictGroundingEval{
			Code:            guidance.VerdictCitationsRequiredCode,
			ObservedHandles: []string{"reviewer-1:read#1"},
		}, nil
	}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "RegisterSubmitVerdictTool", RegisterSubmitVerdictTool(reg, mgr))
	ctx := context.Background()
	run := walkPlanRunToReview(ctx, t, mgr, blueprintMgr, "sess-1")
	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Out = &tools.ToolInvocationOut{}

	out, err := reg.Run(ctx, "submit_verdict", map[string]any{
		"verdict": map[string]any{"verdict": "APPROVED", "bullets": "scope matches the goal"},
	}, tctx)
	reject := requireVerdictRejection(t, out, err, guidance.VerdictCitationsRequiredCode)
	expected, _ := reject.Data["expected_call"].(string)
	if !strings.Contains(expected, `"handle":"<observed-handle>"`) {
		t.Fatalf("expected_call = %q, want handle citation contract", expected)
	}
	if tctx.Out.Facts.Resolution() != api.ToolResultOutcomeRejected || tctx.Out.Facts.PrimaryCode() != guidance.VerdictCitationsRequiredCode {
		t.Fatalf("facts = %+v", tctx.Out.Facts)
	}
	after, err := mgr.Get(ctx, run.ID)
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
	input := VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := verdictOperation{ToolCallID: "orphan-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "orphan-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.prepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	_, err = mgr.Cancel(ctx, run.ID, "user canceled")
	testutil.FailErr(t, "cancel run", err)

	testutil.FailErr(t, "recover verdict operations", mgr.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.getVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "diverged" {
		t.Fatalf("verdict status = %q found=%v, want diverged", stored.Status, found)
	}
	pending, err := mgr.Store.pendingVerdictOperations(ctx)
	testutil.FailErr(t, "list pending", err)
	if len(pending) != 0 {
		t.Fatalf("pending after recovery = %+v, want none (recovery must converge)", pending)
	}

	// A live duplicate of the diverged tool call gets an honest error, not a retry.
	if _, err := mgr.RecordReviewLoopVerdict(withVerdictOperationID(ctx, op.ToolCallID), "sess-1", input.Verdict, nil, nil); err == nil ||
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
	input := VerdictSubmission{SessionID: "sess-1", Verdict: map[string]string{
		"verdict": "NEEDS_REVISION", "bullets": "verify section is missing",
	}}
	raw, err := json.Marshal(input)
	testutil.FailErr(t, "marshal verdict input", err)
	digest := sha256.Sum256(raw)
	op := verdictOperation{ToolCallID: "drift-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "drift-evidence", EvidenceJSON: string(raw)}
	_, _, err = mgr.Store.prepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	// Advance the run revision under the same phase (unrelated vars write).
	fresh, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get vars", err)
	testutil.FailErr(t, "bump revision", mgr.Store.UpdateVars(ctx, fresh, projectDir, SetHostVar(vars, "drift.marker", "1")))

	testutil.FailErr(t, "recover verdict operations", mgr.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.getVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "committed" {
		t.Fatalf("verdict status = %q found=%v, want committed after rebase", stored.Status, found)
	}
	after, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get recovered vars", err)
	if attempt := ReviewLoopAttempt(after, "review"); attempt != 1 {
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
	op := verdictOperation{ToolCallID: "corrupt-verdict-call", RunID: run.ID, SourceRevision: run.Revision,
		Phase: run.CurrentPhase, InputDigest: hex.EncodeToString(digest[:]), EvidenceRecordID: "corrupt-evidence", EvidenceJSON: "[]"}
	_, _, err := mgr.Store.prepareVerdictOperation(ctx, op)
	testutil.FailErr(t, "prepare verdict operation", err)

	testutil.FailErr(t, "recover verdict operations", mgr.RecoverVerdictOperations(ctx))

	stored, found, err := mgr.Store.getVerdictOperation(ctx, op.ToolCallID)
	testutil.FailErr(t, "get verdict operation", err)
	if !found || stored.Status != "diverged" {
		t.Fatalf("verdict status = %q found=%v, want diverged", stored.Status, found)
	}
	if !strings.Contains(stored.Error, "invalid") {
		t.Fatalf("row diagnostic = %q, want decode reason", stored.Error)
	}
}

func requireVerdictRejection(t *testing.T, output string, err error, code string) *tools.ToolReject {
	t.Helper()
	reject := tools.AsToolReject(err)
	if output != "" || reject == nil || reject.Code != code {
		t.Fatalf("verdict refusal: output=%q error=%v want %s", output, err, code)
	}
	return reject
}
