package contract

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestOARHookProducersPopulateRequiredFactsAcrossAllEffectiveHooks(t *testing.T) {
	h := newHookProducerHarness(t)
	cells := representativeHostStateCells(t, h)
	var expectedActiveHooks []string
	for _, r := range h.rules.All() {
		if !h.pipeline.AnchorEnforced(r.Anchor) {
			t.Errorf("effective rule %s resolved to unenforced anchor %q", r.ID, r.Anchor)
		}
		if !slices.Contains(expectedActiveHooks, r.Anchor) {
			expectedActiveHooks = append(expectedActiveHooks, r.Anchor)
		}
	}
	slices.Sort(expectedActiveHooks)

	t.Run("hoststate_hook_producers_and_negative_witnesses", func(t *testing.T) {
		for _, cell := range cells {
			t.Run(cell.name, func(t *testing.T) {
				verifySharedCellHooks(t, h, cell)
				if cell.role == "worker" {
					verifyWorkerReportHooks(t, h, cell)
				} else {
					verifyCoordinatorOnlyHooks(t, h, cell)
				}
			})
		}
		verifyCredentialAssignmentHook(t, h)
	})

	t.Run("unknown_reach_and_known_empty_collection_witnesses", func(t *testing.T) {
		verifyUnknownReachAndEmptyCollections(t, h, expectedActiveHooks)
	})

	t.Run("occurrence_vs_recovery_authority_across_ask_to_agent_and_child_clamp", func(t *testing.T) {
		verifyOccurrenceVsRecoveryAuthority(t, h, cells)
	})
}

func verifySharedCellHooks(t *testing.T, h *hookProducerHarness, cell roleCell) {
	t.Helper()
	ctx := cell.ctx(t.Context())
	gc := oar.NewGuardContext()
	h.policy.FillSessionFacts(ctx, gc, cell.sess, "command", map[string]any{"command": "ls"})
	if gc.FromScenario || !gc.Reach.Known() {
		t.Fatalf("FromScenario=%v Reach.Known=%v", gc.FromScenario, gc.Reach.Known())
	}
	if gc.Session.SessionPosture != string(cell.posture) || gc.Session.SessionMode != string(cell.mode) {
		t.Fatalf("session posture/mode = %q/%q, want %q/%q",
			gc.Session.SessionPosture, gc.Session.SessionMode, cell.posture, cell.mode)
	}
	isWorker := cell.role == "worker"
	if gc.Session.WorkerLeg != isWorker || (!isWorker && gc.Session.Role != "coordinator") {
		t.Fatalf("worker_leg=%v role=%q", gc.Session.WorkerLeg, gc.Session.Role)
	}
	if (isWorker && gc.Session.Surface != "") || (!isWorker && gc.Session.Surface == "") {
		t.Fatalf("role=%s unexpected surface=%q", cell.role, gc.Session.Surface)
	}

	rejErr := h.blockPlane.RejectObservation(ctx, "read", cell.profileID, nil, &toolrejection.ToolReject{
		Code: "TOOL_COMMAND_OPERATION", Data: map[string]any{"need": "read"},
	})
	ref, ok := guidance.RefusalFromError(rejErr)
	if !ok || ref.Code() != "TOOL_COMMAND_OPERATION" {
		t.Fatalf("tool.rejected positive witness = %v", rejErr)
	}
	if esc := h.policy.EscalateRepeatedCode(ctx, cell.sess.ID, "command", ref, 1); esc != nil {
		t.Fatalf("tool.rejected negative witness escalated at total=1: %v", esc)
	}
	if esc := h.policy.EscalateRepeatedCode(ctx, cell.sess.ID, "command", ref, 3); esc == nil || esc.Code() != "DOOM_LOOP_CODE_REPEAT" {
		t.Fatalf("tool.rejected escalation at total=3 = %v, want DOOM_LOOP_CODE_REPEAT", esc)
	}

	askRef, askBlocked, askErr := h.policy.Block(ctx, oar.AnchorSessionPreInvoke, cell.sess, "task", nil, func(g *oar.GuardContext) error {
		g.Workers.AskWorkerWrites = true
		return nil
	})
	contractcheck.FailErr(t, "session.pre_invoke", askErr)
	wantAskBlock := cell.mode == api.SessionModeAsk
	if askBlocked != wantAskBlock || (wantAskBlock && askRef.Code() != "SESSION_ASK_MODE_ACTIVE") {
		t.Fatalf("session.pre_invoke blocked=%v ref=%v, want %v", askBlocked, askRef, wantAskBlock)
	}
	if _, negBlocked, negErr := h.policy.Block(ctx, oar.AnchorSessionPreInvoke, cell.sess, "task", nil, func(g *oar.GuardContext) error {
		g.Workers.AskWorkerWrites = false
		return nil
	}); negErr != nil || negBlocked {
		t.Fatalf("session.pre_invoke negative witness fired: blocked=%v err=%v", negBlocked, negErr)
	}

	verifyContentAndPostToolHooks(t, ctx, h, cell)
}

func verifyContentAndPostToolHooks(t *testing.T, ctx context.Context, h *hookProducerHarness, cell roleCell) {
	t.Helper()
	for wantCode, obs := range map[string]oar.ContentObservation{
		"MODEL_RESPONSE_EMPTY":   {EmptyResponse: &oar.EmptyResponseObservation{ToolsCallable: true}},
		"MODEL_RESPONSE_INVALID": {InvalidResponse: true},
		"TOOL_CALL_UNPARSED":     {ToolCallUnparsed: true, ToolCallDiagnostic: "unexpected_end"},
		"TOOL_ACTION_IN_TEXT":    {TextAction: actionenvelope.Observation{Tools: []string{"read"}, Fenced: true}},
	} {
		cRef, cBlocked, cErr := h.policy.ContentBlock(ctx, cell.sess, oar.AnchorContentOutput, obs, "", nil)
		contractcheck.FailErr(t, "content.output "+wantCode, cErr)
		if !cBlocked || cRef == nil || cRef.Code() != wantCode {
			t.Fatalf("content.output %s = (%v, %v)", wantCode, cBlocked, cRef)
		}
	}
	cleanObs := oar.ContentObservation{Segments: []oar.ContentSegment{{Content: "Done.", Role: "assistant", Origin: "model", Authority: "none", TrustTier: "trusted"}}}
	if cRef, cBlocked, cErr := h.policy.ContentBlock(ctx, cell.sess, oar.AnchorContentOutput, cleanObs, "", nil); cErr != nil || cBlocked {
		t.Fatalf("content.output negative witness fired: %v %v", cRef, cErr)
	}

	postOut, postFacts := h.policy.AfterTool(ctx, cell.sess, "command", map[string]any{"command": "touch /opt/x"}, "ok", 1, guidance.ToolResultFacts{
		Confine: confine.Observation{
			Applied:      true,
			FailedStages: []string{"touch /opt/x"},
			Refusals: confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{{
				Operation: "file-write-create", Target: "/opt/x", Process: "touch", Count: 1,
				Recovery: confine.RecoverWriteRoot, Grant: "/opt", Layer: confine.FloorOutsideWriteRoots,
			}}},
		},
		SharedTemp: &guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Tolerated: false},
	})
	if !postFacts.HasCode("SANDBOX_TRY_WRITE_ROOT") || !postFacts.HasCode("SANDBOX_SHARED_TEMP_REFUSED") || postOut == "ok" {
		t.Fatalf("tool.post_invoke/shared_temp positive witness facts=%+v out=%q", postFacts, postOut)
	}
	_, tolFacts := h.policy.AfterTool(ctx, cell.sess, "command", map[string]any{"command": "build"}, "ok", 1, guidance.ToolResultFacts{
		SharedTemp: &guidance.SharedTempSettlement{Refused: []string{"/tmp"}, Tolerated: true},
	})
	if !tolFacts.HasCode("SANDBOX_SHARED_TEMP_TOLERATED") || tolFacts.HasCode("SANDBOX_SHARED_TEMP_REFUSED") {
		t.Fatalf("sandbox.shared_temp tolerated witness = %+v", tolFacts)
	}
	cleanOut, cleanFacts := h.policy.AfterTool(ctx, cell.sess, "command", map[string]any{"command": "ls"}, "ok", 1, guidance.ToolResultFacts{
		Confine:    confine.Observation{Applied: true, FailedStages: []string{}, Refusals: confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{}}},
		SharedTemp: &guidance.SharedTempSettlement{Refused: []string{}},
	})
	if cleanOut != "ok" || len(cleanFacts.Feedback) != 0 {
		t.Fatalf("tool.post_invoke/shared_temp empty-collection witness fired: %+v", cleanFacts)
	}
}

func verifyWorkerReportHooks(t *testing.T, h *hookProducerHarness, cell roleCell) {
	t.Helper()
	ctx := cell.ctx(t.Context())
	reqRef, reqBlocked := h.policy.FinishBlock(ctx, cell.sess, func(g *oar.GuardContext) error {
		g.Rejection.RejectObservation = "worker_report_required"
		return nil
	})
	if !reqBlocked || reqRef == nil || reqRef.Code() != "WORKER_REPORT_REQUIRED" {
		t.Fatalf("worker.report_check FinishBlock positive = (%v, %v)", reqBlocked, reqRef)
	}
	if negRef, negBlk := h.policy.FinishBlock(ctx, cell.sess, nil); negBlk {
		t.Fatalf("worker.report_check FinishBlock negative fired: %v", negRef)
	}
	posEval, evalErr := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{
		AgentType:      "implementer",
		ChildSessionID: cell.sess.ID,
		Pipeline:       h.pipeline,
		Reach:          cell.auth.Reach,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Edited target.", LegStatus: "complete"},
	})
	contractcheck.FailErr(t, "EvaluateWorkerSummary positive", evalErr)
	if posEval.HintCode != "WORKER_SUMMARY_NO_ARTIFACT" {
		t.Fatalf("worker.report_check EvaluateWorkerSummary = %+v, want WORKER_SUMMARY_NO_ARTIFACT", posEval)
	}
	negEval, negEvalErr := workercompletion.EvaluateWorkerSummary(ctx, workercompletion.WorkerSummaryEvalInput{
		AgentType:      "implementer",
		ChildSessionID: cell.sess.ID,
		Pipeline:       h.pipeline,
		Reach:          cell.auth.Reach,
		Report:         workercompletion.WorkerCompletionReport{Brief: "Blocked on upstream.", LegStatus: "partial"},
	})
	contractcheck.FailErr(t, "EvaluateWorkerSummary negative", negEvalErr)
	if negEval.HintCode != "" {
		t.Fatalf("worker.report_check partial negative fired %q", negEval.HintCode)
	}
}

func verifyCoordinatorOnlyHooks(t *testing.T, h *hookProducerHarness, cell roleCell) {
	t.Helper()
	ctx := cell.ctx(t.Context())
	preRef, preBlk, preErr := h.policy.Block(ctx, oar.AnchorCoordinatorPreInvoke, cell.sess, "task", nil, func(g *oar.GuardContext) error {
		if g.Workers.ProfileMutationCapable {
			g.Workers.ScopeMode = "write"
		}
		guard.ObserveProgressMissingBeforeDispatch(cell.sess, "", "task", false, g)
		return nil
	})
	contractcheck.FailErr(t, "coordinator.pre_invoke", preErr)
	wantProg := cell.posture != api.SessionPostureSpec
	if preBlk != wantProg || (wantProg && preRef.Code() != "PROGRESS_MISSING") {
		t.Fatalf("coordinator.pre_invoke posture=%s blk=%v ref=%v", cell.posture, preBlk, preRef)
	}
	if _, hasProgBlk, err := h.policy.Block(ctx, oar.AnchorCoordinatorPreInvoke, cell.sess, "task", nil, func(g *oar.GuardContext) error {
		if g.Workers.ProfileMutationCapable {
			g.Workers.ScopeMode = "write"
		}
		guard.ObserveProgressMissingBeforeDispatch(cell.sess, "- [ ] step 1", "task", false, g)
		return nil
	}); err != nil || hasProgBlk {
		t.Fatalf("coordinator.pre_invoke negative witness fired: blk=%v err=%v", hasProgBlk, err)
	}

	clRef, clBlk := h.policy.FinishBlock(ctx, cell.sess, func(g *oar.GuardContext) error {
		g.Progress.VerifyRequired = true
		g.Progress.VerifierPass = false
		return nil
	})
	if !clBlk || clRef == nil || clRef.Code() != "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT" {
		t.Fatalf("coordinator.closeout_check positive witness = (%v, %v)", clBlk, clRef)
	}
	if negClRef, negClBlk := h.policy.FinishBlock(ctx, cell.sess, func(g *oar.GuardContext) error {
		g.Progress.VerifyRequired = true
		g.Progress.VerifierPass = true
		return nil
	}); negClBlk {
		t.Fatalf("coordinator.closeout_check negative witness fired: %v", negClRef)
	}

	h.takeAdvisories()
	midGC := oar.NewGuardContext()
	midGC.Rejection.RejectObservation = "mid_turn_note_ungrounded"
	midGC.Session.MidTurnCitationsRequired = true
	midGC.Session.MidTurnCitationAttempted = false
	contractcheck.FailErr(t, "MidTurnNoteFeedback positive", h.policy.MidTurnNoteFeedback(ctx, cell.sess, midGC))
	if adv := h.takeAdvisories(); len(adv) != 1 || adv[0].Code != "MID_TURN_NOTE_UNGROUNDED" {
		t.Fatalf("coordinator.mid_turn_note positive advisories = %+v", adv)
	}
	midNeg := oar.NewGuardContext()
	midNeg.Rejection.RejectObservation = "mid_turn_note_ungrounded"
	midNeg.Session.MidTurnCitationsRequired = true
	midNeg.Session.MidTurnCitationAttempted = true
	contractcheck.FailErr(t, "MidTurnNoteFeedback negative", h.policy.MidTurnNoteFeedback(ctx, cell.sess, midNeg))
	if adv := h.takeAdvisories(); len(adv) != 0 {
		t.Fatalf("coordinator.mid_turn_note negative witness fired: %+v", adv)
	}

	h.takeAdvisories()
	if postRef, postBlk := h.turnGuards.AfterTurn(ctx, cell.sess, "Summary ready.", true); postBlk || postRef != nil {
		t.Fatalf("coordinator.post_turn negative witness blocked: %v", postRef)
	}
	if adv := h.takeAdvisories(); len(adv) != 0 {
		t.Fatalf("coordinator.post_turn negative witness emitted advisory: %+v", adv)
	}
	postTurnGC := oar.NewGuardContext()
	h.policy.FillSessionFacts(ctx, postTurnGC, cell.sess, "", nil)
	postTurnGC.Source.ReproducedSourceLines = 35
	postTurnGC.Source.ReproducedSourceFileLines = 40
	ptRes, ptErr := h.pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorPostTurn, postTurnGC)
	contractcheck.FailErr(t, "coordinator.post_turn positive", ptErr)
	if ptRes == nil || ptRes.Decision == nil || ptRes.Decision.Code != "REPRODUCED_SOURCE_IN_ANSWER" {
		t.Fatalf("coordinator.post_turn positive decision = %+v", ptRes)
	}
}

func verifyCredentialAssignmentHook(t *testing.T, h *hookProducerHarness) {
	t.Helper()
	credSess := &api.Session{ID: "cred-session", ProjectID: "proj-cred", Posture: api.SessionPostureBuild, Mode: api.SessionModeAgent}
	strongSecret := "alpha_bravo_charlie_delta_987654"
	_, strongFacts := h.policy.AfterTool(t.Context(), credSess, "write", map[string]any{"content": "API_TOKEN=" + strongSecret + "\n"}, "written", 1, guidance.ToolResultFacts{})
	if !strongFacts.HasCode("CREDENTIAL_LITERAL") {
		t.Fatalf("credential.assignment strong literal = %+v", strongFacts)
	}
	_, weakFacts := h.policy.AfterTool(t.Context(), credSess, "write", map[string]any{"content": "MYSQL_PASSWORD=password\n"}, "written", 1, guidance.ToolResultFacts{})
	if !weakFacts.HasCode("WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("credential.assignment weak literal = %+v", weakFacts)
	}
	harvestedVal := "foxtrot_golf_hotel_india_123456"
	h.harvest.Harvest(secretharvest.ContainerRead{RootSessionID: credSess.ID, Container: ".env", Content: []byte("SERVICE_SECRET=" + harvestedVal + "\n")})
	_, harvFacts := h.policy.AfterTool(t.Context(), credSess, "write", map[string]any{"content": "SERVICE_SECRET=" + harvestedVal + "\n"}, "written", 1, guidance.ToolResultFacts{})
	if harvFacts.HasCode("CREDENTIAL_LITERAL") || harvFacts.HasCode("WEAK_CREDENTIAL_LITERAL") {
		t.Fatalf("harvested credential triggered warning: %+v", harvFacts)
	}
}

func verifyUnknownReachAndEmptyCollections(t *testing.T, h *hookProducerHarness, expectedActiveHooks []string) {
	t.Helper()
	sess := &api.Session{ID: "unknown-reach-sess", Posture: api.SessionPostureBuild, Mode: api.SessionModeAgent}
	gc := oar.NewGuardContext()
	h.policy.FillSessionFacts(t.Context(), gc, sess, "command", map[string]any{})
	gc.Refusals.RefusedReadGrants = []string{}
	gc.Refusals.OrdinaryWriteGrants = []string{}
	gc.Grounding.UnobservedCitedHandles = []string{}
	if gc.Reach.Known() {
		t.Fatal("bare context must leave gc.Reach.Known() == false")
	}
	for _, anchor := range expectedActiveHooks {
		if anchor == oar.AnchorWorkerReportCheck || anchor == oar.AnchorCredentialAssignment {
			continue
		}
		res, err := h.pipeline.EvaluateBlock(t.Context(), anchor, gc)
		contractcheck.FailErr(t, "unknown-reach EvaluateBlock "+anchor, err)
		if res != nil && res.Decision != nil {
			t.Errorf("bare unknown-reach + known-empty GuardContext fired %s on %s", res.Decision.Code, anchor)
		}
	}
	for _, r := range h.rules.All() {
		if r.NextSteps == nil {
			continue
		}
		steps := oar.ResolveNextSteps(r, gc)
		if len(steps.Calls) > 0 {
			t.Errorf("rule %s emitted next-step calls %+v when !gc.Reach.Known()", r.ID, steps.Calls)
		}
	}
}
