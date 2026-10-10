package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func startReviewLoopRun(ctx context.Context, t *testing.T, mgr *RunManager) *api.WorkflowRun {
	t.Helper()
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": reviewLoopTestManifest()})
	run, err := startRun(ctx, mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "startRun rltest", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase = %q want judge", run.CurrentPhase)
	}
	return run
}

func persistReviewResponse(t *testing.T, mgr *RunManager, session string, msg api.Message) {
	t.Helper()
	assistant := api.Message{ID: msg.ToolResult.AssistantMessageID, Role: api.MessageRoleAssistant, CreatedAt: time.Now().UTC(), ToolCalls: []api.ToolCall{{ID: msg.ToolResult.ToolCallID, Name: "submit_verdict"}}}
	msg.CreatedAt = assistant.CreatedAt.Add(time.Millisecond)
	msg.Role = api.MessageRoleTool
	testutil.FailErr(t, "persist review response", mgr.Sessions.AppendMessages(t.Context(), session, assistant, msg))
}

func TestReviewRepairBlocksOnceAndResumeRetainsWork(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 3; i++ {
		msg := api.Message{ID: fmt.Sprintf("result-%d", i), WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", Outcome: api.ToolResultOutcomeRejected, AssistantMessageID: fmt.Sprintf("response-%d", i), ToolCallID: fmt.Sprintf("call-%d", i), Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "verdict.coverage"}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
		testutil.FailErr(t, "record repair", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
		testutil.FailErr(t, "replay repair", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
	}
	got, err := mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusPaused || got.PauseReason != ReviewBlockedReason {
		t.Fatalf("run did not pause: %+v", got)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read vars", err)
	repair, err := CurrentReviewRepair(vars, "judge")
	testutil.FailErr(t, "read repair", err)
	if repair == nil || len(repair.Responses) != 3 || repair.State != "blocked" {
		t.Fatalf("repair: %+v", repair)
	}
	if mgr.Policy.ActiveReviewVerdictPending(t.Context(), run.SessionID) {
		t.Fatal("blocked review still demands a verdict")
	}
	if workflowgates.SatisfiedInVars(vars, "evidence_passed:rl_key") {
		t.Fatal("block passed evidence gate")
	}
	resumed, err := mgr.Controls.Resume(t.Context(), run.ID)
	testutil.FailErr(t, "resume", err)
	if resumed.CurrentPhase != "judge" || resumed.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("resume changed work: %+v", resumed)
	}
}

func TestReviewContractBlocksWithoutModelAttempts(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	testutil.FailErr(t, "block impossible contract", ReviewRepairs{mgr}.BlockContract(t.Context(), run.ID, fmt.Errorf("host id rejected by schema")))
	got, err := mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read blocked run", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read snapshot", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode snapshot", err)
	if got.Status != api.WorkflowRunStatusPaused || repair == nil || repair.Snapshot == nil || len(repair.Responses) != 0 {
		t.Fatalf("contract not blocked atomically: run=%+v repair=%+v", got, repair)
	}
	if workflowgates.SatisfiedInVars(vars, "evidence_passed:rl_key") {
		t.Fatal("contract failure stamped a successful verdict")
	}
}

func TestReviewRepairRecoveryCountsResponsesOnce(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 3; i++ {
		msg := api.Message{ID: fmt.Sprintf("durable-result-%d", i), Role: api.MessageRoleTool, WorkflowRunID: run.ID, CreatedAt: time.Now().UTC(), ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: fmt.Sprintf("call-%d", i), AssistantMessageID: fmt.Sprintf("response-%d", i), Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"workflow_phase": run.CurrentPhase, "field": "verdict.coverage"}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
	}
	testutil.FailErr(t, "recover response accounting", ReviewRepairs{mgr}.Recover(t.Context()))
	testutil.FailErr(t, "repeat recovery", ReviewRepairs{mgr}.Recover(t.Context()))
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read recovered episode", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode recovered episode", err)
	if repair == nil || len(repair.Responses) != 3 || repair.State != "blocked" {
		t.Fatalf("recovery episode: %+v", repair)
	}
}

func TestReviewRepairIgnoresResultsFromPriorPhase(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	msg := api.Message{ID: "old-result", WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "old-call", Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"workflow_phase": "previous_phase"}}}}}
	testutil.FailErr(t, "ignore old result", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read current episode", err)
	repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode episode", err)
	if repair != nil {
		t.Fatal("prior phase refusal affected current repair budget")
	}
}

func TestAcceptedVerdictResolvesResumedBlockedEpisode(t *testing.T) {
	vars := map[string]any{reviewRepairsKey: []ReviewRepair{{ID: "episode", Phase: "claims", State: "blocked", Snapshot: &ReviewSnapshot{Unavailable: []string{"worker ledger"}}}}}
	updated, err := resolveReviewRepair(vars, "claims")
	testutil.FailErr(t, "resolve resumed episode", err)
	repair, err := CurrentReviewRepair(updated, "claims")
	testutil.FailErr(t, "read resolved episode", err)
	if repair == nil || repair.State != "resolved" || repair.Snapshot == nil {
		t.Fatalf("resolved repair lost historical snapshot: %+v", repair)
	}
}

func TestReviewRepairCountsOneResponseAndRetainsLatestCandidate(t *testing.T) {
	episode := ReviewRepair{}
	if !episode.ObserveResponse("r1", "first", "a") || !episode.ObserveResponse("r1", "second", "b") {
		t.Fatal("new results were not retained")
	}
	if len(episode.Responses) != 1 || episode.Repeated != 1 || episode.Fingerprint != "b" {
		t.Fatalf("same response counted twice: %+v", episode)
	}
	if episode.ObserveResponse("r1", "first", "a") || episode.Fingerprint != "b" {
		t.Fatal("replay replaced the newer diagnostic")
	}
	episode.ObserveResponse("r2", "third", "b")
	if episode.Repeated != 2 {
		t.Fatal("repetition did not follow the prior response's final defect")
	}
}

func TestReviewRepairTotalBudgetBoundsChangingDefects(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	for i := 0; i < 8; i++ {
		msg := api.Message{ID: fmt.Sprintf("result-%d", i), WorkflowRunID: run.ID, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: fmt.Sprintf("call-%d", i), AssistantMessageID: fmt.Sprintf("response-%d", i), Outcome: api.ToolResultOutcomeRejected, Codes: []string{"TOOL_ARGS_INVALID"}, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": fmt.Sprintf("verdict.claims[%d]", i)}}}}}
		persistReviewResponse(t, mgr, run.SessionID, msg)
		testutil.FailErr(t, "record changing defect", mgr.RecordReviewToolResult(t.Context(), run.SessionID, msg))
		got, err := mgr.Store.Runs.Get(t.Context(), run.ID)
		testutil.FailErr(t, "read repair state", err)
		if i < 7 && got.Status != api.WorkflowRunStatusRunning {
			t.Fatalf("repair stopped early at %d", i+1)
		}
		if i == 7 && (got.Status != api.WorkflowRunStatusPaused || got.PauseReason != ReviewBlockedReason) {
			t.Fatal("changing defects exceeded the response allowance")
		}
	}
}

func TestReviewRepairFingerprintTracksAllRepairsAsASet(t *testing.T) {
	feedback := func(rows ...runstate.VerdictRepair) []api.ToolFeedback {
		return []api.ToolFeedback{{Code: workflowvalidation.ReviewLoopVerdictInvalidCode, Details: map[string]any{"repairs": rows}}}
	}
	first := runstate.VerdictRepair{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "coverage"}}
	second := runstate.VerdictRepair{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"field": "claims"}}
	before := reviewIssueFingerprint(feedback(first, second))
	if before != reviewIssueFingerprint(feedback(second, first)) {
		t.Fatal("repair ordering changed defect identity")
	}
	if before == reviewIssueFingerprint(feedback(first)) {
		t.Fatal("fixing a secondary repair did not change defect identity")
	}
}

type unavailableReviewLedger struct{ err error }

func (l unavailableReviewLedger) ListReviewLoopEvidence(context.Context, string, string, string) ([]evidence.Record, error) {
	return nil, l.err
}

func TestReviewVerdictsDoesNotTreatUnreadableLedgerAsEmpty(t *testing.T) {
	want := errors.New("ledger unavailable")
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "claims", ReviewLoop: &workflowdef.ReviewLoopDef{}}}}
	_, err := workflowpresentation.ReviewVerdicts(t.Context(), unavailableReviewLedger{want}, &api.WorkflowRun{ID: "run", SessionID: "session"}, manifest)
	if !errors.Is(err, want) {
		t.Fatalf("ledger failure hidden: %v", err)
	}
}

func TestReviewRepairDoesNotInferSameDefectFromCodeOrProse(t *testing.T) {
	identity := reviewIssueFingerprint([]api.ToolFeedback{{Code: workflowvalidation.ReviewLoopVerdictInvalidCode, Details: map[string]any{"reason": "invalid claim"}}})
	if identity != "" {
		t.Fatal("unstructured diagnostic treated as a proven repeated defect")
	}
	var episode ReviewRepair
	for i := 0; i < 3; i++ {
		episode.ObserveResponse(fmt.Sprint(i), fmt.Sprint(i), identity)
	}
	if episode.Repeated != 0 || len(episode.Responses) != 3 {
		t.Fatalf("unstructured errors charged the wrong budget: %+v", episode)
	}
}

func TestReviewRepairWaitsForWholeResponse(t *testing.T) {
	assistant := api.Message{ID: "response", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "first", Name: "submit_verdict"}, {ID: "second", Name: "submit_verdict"}}}
	first := api.Message{ID: "result-1", ToolResult: &api.ToolResult{Tool: "submit_verdict", AssistantMessageID: "response", ToolCallID: "first"}}
	second := api.Message{ID: "result-2", ToolResult: &api.ToolResult{Tool: "submit_verdict", AssistantMessageID: "response", ToolCallID: "second"}}
	ready, err := finalReviewResponseResult([]api.Message{assistant, first}, first)
	testutil.FailErr(t, "inspect unfinished response", err)
	if ready {
		t.Fatal("first rejection can interrupt the response's later correction")
	}
	history := []api.Message{assistant, first, second}
	ready, err = finalReviewResponseResult(history, first)
	testutil.FailErr(t, "replay earlier result", err)
	if ready {
		t.Fatal("recovery accounted the earlier candidate")
	}
	ready, err = finalReviewResponseResult(history, second)
	testutil.FailErr(t, "inspect settled response", err)
	if !ready {
		t.Fatal("final candidate was not admitted for repair accounting")
	}
}
