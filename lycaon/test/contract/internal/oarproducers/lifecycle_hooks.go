package oarproducers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/actionenvelope"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func joinActionCopy(fix, instead string) string {
	fix = strings.TrimSpace(fix)
	instead = strings.TrimSpace(instead)
	switch {
	case fix != "" && instead != "":
		return fix + "\n" + instead
	case fix != "":
		return fix
	default:
		return instead
	}
}

func (h *Harness) recordRefusal(out *[]FiredOutcome, anchor, evLabel string, cell AuthCell, ref *guidance.Refusal, checkClaims bool) {
	if ref == nil {
		return
	}
	fix := strings.TrimSpace(ref.Copy["fix"])
	actCopy := joinActionCopy(fix, ref.Copy["instead"])
	calls := extractRefusalNextCalls(ref)
	rule := h.ruleByAnchorAndCode(anchor, ref.Code())
	resolved, hasResolved := h.takeObservedNextSteps(rule)
	if actCopy != "" || len(calls) > 0 || (rule != nil && rule.NextSteps != nil) {
		*out = append(*out, FiredOutcome{
			Anchor:           anchor,
			RuleID:           ref.Code(),
			EventLabel:       evLabel,
			Cell:             cell,
			FixCopy:          fix,
			ActionCopy:       actCopy,
			CheckClaims:      checkClaims && !cell.IsBoundary,
			NextCalls:        calls,
			HasResolvedSteps: hasResolved,
			StepsText:        resolved.Text,
			Steps:            buildFiredStepOutcomes(rule, resolved),
			ExercisedEdges:   h.snapshotExercisedEdges(),
		})
	}
}

func (h *Harness) drainAdvisoriesTo(out *[]FiredOutcome, anchor, evLabel string, cell AuthCell, checkClaims bool) {
	edges := h.snapshotExercisedEdges()
	for _, adv := range h.takeAdvisories() {
		if adv == nil {
			continue
		}
		fix := strings.TrimSpace(adv.Copy["fix"])
		actCopy := joinActionCopy(fix, adv.Copy["instead"])
		calls := extractDecisionNextCalls(adv)
		rule := h.ruleByAnchorAndCode(anchor, adv.Code)
		resolved, hasResolved := h.takeObservedNextSteps(rule)
		if actCopy != "" || len(calls) > 0 || (rule != nil && rule.NextSteps != nil) {
			*out = append(*out, FiredOutcome{
				Anchor:           anchor,
				RuleID:           adv.Code,
				EventLabel:       evLabel,
				Cell:             cell,
				FixCopy:          fix,
				ActionCopy:       actCopy,
				CheckClaims:      checkClaims && !cell.IsBoundary,
				NextCalls:        calls,
				HasResolvedSteps: hasResolved,
				StepsText:        resolved.Text,
				Steps:            buildFiredStepOutcomes(rule, resolved),
				ExercisedEdges:   slices.Clone(edges),
			})
		}
	}
	h.clearObservedNextSteps()
}

func (h *Harness) runDoomLoopRejectedProducers(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if idx > 8 && !cell.IsBoundary && cell.Role != "worker" && idx%5 != 0 {
			continue
		}
		tool := "recall"
		if len(cell.Auth.Offered) > 0 {
			tool = cell.Auth.Offered[0]
		}
		sessID := fmt.Sprintf("doom-%d", idx)
		h.bindSession(sessID, cell)
		cctx := cell.Context(ctx, sessID)

		// Negative escalation at total=1.
		baseRef := guidance.NewRefusal("TOOL_ARGS_INVALID", "invalid args")
		_ = h.Policy.EscalateRepeatedCode(cctx, sessID, tool, baseRef, 1)
		h.EvalCount++
		// Positive escalation at total=3.
		esc := h.Policy.EscalateRepeatedCode(cctx, sessID, tool, baseRef, 3)
		h.EvalCount++
		h.recordRefusal(&out, oar.AnchorToolRejected, "doom.code_repeat[3]", cell, esc, len(cell.Auth.Offered) > 0)

		// Negative repeat at count=1.
		_, _ = h.Policy.FormatDoomLoopReject(cctx, sessID, tool, nil, 1, "")
		h.EvalCount++
		// Repeat with handle and without handle.
		if slices.Contains(cell.Auth.Offered, "command_output") || cell.IsBoundary {
			refH, _ := h.Policy.FormatDoomLoopReject(cctx, sessID, "command_output", map[string]any{"handle": "cmd-1"}, 9, "")
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorToolRejected, "doom.repeat[handle]", cell, refH, slices.Contains(cell.Auth.Offered, "command_output"))
		}
		refNoH, _ := h.Policy.FormatDoomLoopReject(cctx, sessID, tool, map[string]any{"query": "x"}, 9, "")
		h.EvalCount++
		h.recordRefusal(&out, oar.AnchorToolRejected, "doom.repeat[plain]", cell, refNoH, len(cell.Auth.Offered) > 0)
		// Deferred-unactivated branch on repeat.
		_, blkDef, _ := h.Policy.Block(cctx, oar.AnchorToolRejected, cell.Session(sessID), tool, nil, func(gc *oar.GuardContext) error {
			gc.Counters.RepeatCount = 9
			gc.Counters.DeferredUnactivated = 2
			return nil
		})
		h.EvalCount++
		_ = blkDef
		h.drainAdvisoriesTo(&out, oar.AnchorToolRejected, "doom.repeat[def]", cell, len(cell.Auth.Offered) > 0)
	}
	return out
}

// RunLifecycleHookProducers executes production hook producers indexed by their
// lifecycle anchor across coordinator, worker, and boundary cells.
func (h *Harness) RunLifecycleHookProducers(ctx context.Context, cells []AuthCell) ([]FiredOutcome, error) {
	restore := h.installNextStepObserver()
	defer restore()
	byAnchor := map[string]func(context.Context, []AuthCell) []FiredOutcome{
		oar.AnchorSessionPreInvoke:         h.runSessionPreInvokeHook,
		oar.AnchorCoordinatorPreInvoke:     h.runCoordinatorPreInvokeHook,
		oar.AnchorContentOutput:            h.runContentOutputHook,
		oar.AnchorToolPost:                 h.runToolPostAndSharedTempHook,
		oar.AnchorCredentialAssignment:     h.runCredentialAssignmentHook,
		oar.AnchorCoordinatorCloseoutCheck: h.runCoordinatorCloseoutHook,
		oar.AnchorWorkerReportCheck:        h.runWorkerReportHook,
		oar.AnchorCoordinatorMidTurnNote:   h.runCoordinatorMidTurnNoteHook,
		oar.AnchorCoordinatorPostTurn:      h.runCoordinatorPostTurnHook,
	}
	anchors := make([]string, 0, len(byAnchor))
	for a := range byAnchor {
		anchors = append(anchors, a)
	}
	slices.Sort(anchors)
	var out []FiredOutcome
	for _, a := range anchors {
		out = append(out, byAnchor[a](ctx, cells)...)
	}
	return out, nil
}

func (h *Harness) runSessionPreInvokeHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if idx > 8 && !cell.IsBoundary {
			continue
		}
		sessID := fmt.Sprintf("sess-pre-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		_, _, _ = h.Policy.Block(cctx, oar.AnchorSessionPreInvoke, sess, "task", nil, func(gc *oar.GuardContext) error {
			gc.Workers.AskWorkerWrites = false
			return nil
		})
		h.EvalCount++
		ref, _, _ := h.Policy.Block(cctx, oar.AnchorSessionPreInvoke, sess, "task", nil, func(gc *oar.GuardContext) error {
			gc.Workers.AskWorkerWrites = true
			return nil
		})
		h.EvalCount++
		h.recordRefusal(&out, oar.AnchorSessionPreInvoke, "session.pre_invoke/ask_writes", cell, ref, slices.Contains(cell.Auth.Offered, "task"))
	}
	return out
}

func (h *Harness) runCoordinatorPreInvokeHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if cell.Role == "worker" {
			continue
		}
		sessID := fmt.Sprintf("coord-pre-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)

		// Negative clean pre_invoke witness (records #when == false for all coordinator.pre_invoke rules).
		_, _, _ = h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, "verify", nil, func(gc *oar.GuardContext) error {
			gc.Workers.ScopeMode = "write"
			gc.Progress.VerifyDeclared = true
			guard.ObserveProgressMissingBeforeDispatch(sess, "- [ ] step 1", "verify", false, gc)
			return nil
		})
		h.EvalCount++

		// Progress-gated dispatch / reconciliation using an actually offered gated tool on cell.
		gatedTool := pickProgressGatedTool(cell)
		if gatedTool != "" || cell.IsBoundary {
			tName := gatedTool
			if tName == "" {
				tName = "task"
			}
			refPM, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, tName, nil, func(gc *oar.GuardContext) error {
				if gc.Workers.ProfileMutationCapable {
					gc.Workers.ScopeMode = "write"
				}
				guard.ObserveProgressMissingBeforeDispatch(sess, "", tName, false, gc)
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/progress_missing", cell, refPM, gatedTool != "")

			refPR, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, tName, nil, func(gc *oar.GuardContext) error {
				if gc.Workers.ProfileMutationCapable {
					gc.Workers.ScopeMode = "write"
				}
				gc.Progress.ProgressClosureArmed = true
				gc.Progress.ProgressGatedTool = true
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/progress_reconcile", cell, refPR, gatedTool != "")
		}

		if slices.Contains(cell.Auth.Offered, "pack_board") || cell.IsBoundary {
			refB, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, "pack_board", nil, func(gc *oar.GuardContext) error {
				gc.Workflow.BatchPhase = "synthesize"
				gc.Workflow.BatchClosed = false
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/batch_wrong_phase", cell, refB, slices.Contains(cell.Auth.Offered, "pack_board"))
		}
		if slices.Contains(cell.Auth.Offered, "command") || cell.IsBoundary {
			refO, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, "command", nil, func(gc *oar.GuardContext) error {
				gc.Workers.PathIsWorkerBranch = true
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/overlay_path", cell, refO, slices.Contains(cell.Auth.Offered, "command"))
		}
		if slices.Contains(cell.Auth.Offered, "task") || cell.IsBoundary {
			refS, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, "task", nil, func(gc *oar.GuardContext) error {
				if cell.IsBoundary {
					gc.Workers.ProfileMutationCapable = true
				}
				gc.Workers.ScopeMode = "read"
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/task_scope", cell, refS, slices.Contains(cell.Auth.Offered, "task"))
		}
		if slices.Contains(cell.Auth.Offered, "verify") || cell.IsBoundary {
			refV, _, _ := h.Policy.Block(cctx, oar.AnchorCoordinatorPreInvoke, sess, "verify", nil, func(gc *oar.GuardContext) error {
				gc.Progress.VerifyHasCommand = false
				gc.Progress.VerifyDeclared = false
				return nil
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorPreInvoke, "coordinator.pre_invoke/verify_undeclared", cell, refV, slices.Contains(cell.Auth.Offered, "verify"))
		}
	}
	return out
}

func pickProgressGatedTool(cell AuthCell) string {
	for _, cand := range []string{"task", "verify", "promote_overlay"} {
		if slices.Contains(cell.Auth.Offered, cand) {
			return cand
		}
	}
	if slices.Contains(cell.Auth.Offered, "command") && cell.Auth.Reader.Held()[toolcontract.ReachCommandMutation] {
		return "command"
	}
	return ""
}

func (h *Harness) runContentOutputHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	obsVariants := []oar.ContentObservation{
		{TextAction: actionenvelope.Observation{Tools: []string{"recall", "read", "verify"}, Fenced: true}},
		{TextAction: actionenvelope.Observation{Tools: []string{"ask_user", "task", "verify"}, Fenced: false}},
		{TextAction: actionenvelope.Observation{Tools: []string{"nonexistent_tool"}, Fenced: true}},
	}
	cleanObs := oar.ContentObservation{Segments: []oar.ContentSegment{{Content: "Done.", Role: "assistant", Origin: "model", Authority: "none", TrustTier: "trusted"}}}
	for idx, cell := range cells {
		if idx > 8 && !cell.IsBoundary && cell.Role != "worker" && idx%5 != 0 {
			continue
		}
		sessID := fmt.Sprintf("content-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		if idx == 0 {
			_, _, _ = h.Policy.ContentBlock(cctx, sess, oar.AnchorContentOutput, cleanObs, "Done.", nil)
			h.EvalCount++
		}
		for vIdx, obs := range obsVariants {
			ref, _, _ := h.Policy.ContentBlock(cctx, sess, oar.AnchorContentOutput, obs, "", nil)
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorContentOutput, fmt.Sprintf("content.output[%d]", vIdx), cell, ref, len(cell.Auth.Offered) > 0)
		}
	}
	return out
}

func (h *Harness) runToolPostAndSharedTempHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	refusalSets := sandboxRefusalSets
	for idx, cell := range cells {
		hasCapRunner := cell.Reach.AcceptsField("command", "capability_request") || cell.Reach.AcceptsField("verify", "capability_request")
		hasCmd := slices.Contains(cell.Auth.Offered, "command")
		if !hasCapRunner && !hasCmd && !cell.IsBoundary {
			continue
		}
		sessID := fmt.Sprintf("post-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		if idx == 0 {
			_ = h.takeAdvisories()
			_, _ = h.Policy.AfterTool(cctx, sess, "command", map[string]any{"command": "ls"}, "ok", 1, guidance.ToolResultFacts{
				Confine:    confine.Observation{Applied: true},
				SharedTemp: &guidance.SharedTempSettlement{Refused: []string{}},
			})
			h.EvalCount++
			_ = h.takeAdvisories()
		}
		if hasCapRunner || cell.IsBoundary {
			for rIdx, refs := range refusalSets {
				for _, running := range []bool{true, false} {
					if rIdx > 0 && !running && idx > 3 && !cell.IsBoundary {
						continue
					}
					toolName := "command"
					if rIdx == len(refusalSets)-1 && slices.Contains(cell.Auth.Offered, "command_output") {
						toolName = "command_output"
					}
					_ = h.takeAdvisories()
					_, _ = h.Policy.AfterTool(cctx, sess, toolName, map[string]any{"command": "run"}, "ok", 1, guidance.ToolResultFacts{
						Confine: confine.Observation{
							Applied: true, Running: running, FailedStages: []string{"run"},
							Refusals: confine.SandboxRefusals{Refusals: refs},
						},
					})
					h.EvalCount++
					h.drainAdvisoriesTo(&out, oar.AnchorToolPost, fmt.Sprintf("tool.post_invoke/confine[%d,run=%t]", rIdx, running), cell, hasCapRunner)
				}
			}
		}
		if hasCmd || cell.IsBoundary {
			for _, code := range []string{toolrejection.GitNoRepositoryOnWriteLegCode, toolrejection.HTTPRequestWebPageCode, toolrejection.SourceAnalysisUnavailableCode} {
				_ = h.takeAdvisories()
				_, _ = h.Policy.AfterTool(cctx, sess, "command", map[string]any{"command": "ls"}, "ok", 1, guidance.ToolResultFacts{
					Codes: []string{code}, Feedback: []api.ToolFeedback{{Code: code, Details: map[string]any{"url": "https://example.com"}}},
				})
				h.EvalCount++
				h.drainAdvisoriesTo(&out, oar.AnchorToolPost, "tool.post_invoke/"+code, cell, hasCmd)
			}
			_ = h.takeAdvisories()
			_, _ = h.Policy.AfterTool(cctx, sess, "command", map[string]any{"command": "ls"}, "ok", 4, guidance.ToolResultFacts{})
			h.EvalCount++
			h.drainAdvisoriesTo(&out, oar.AnchorToolPost, "tool.post_invoke/doom_warn", cell, hasCmd)
			if slices.Contains(cell.Auth.Offered, "command_output") || cell.IsBoundary {
				_ = h.takeAdvisories()
				_, _ = h.Policy.AfterTool(cctx, sess, "command_output", map[string]any{"handle": "cmd-1"}, "ok", 4, guidance.ToolResultFacts{})
				h.EvalCount++
				h.drainAdvisoriesTo(&out, oar.AnchorToolPost, "tool.post_invoke/doom_warn_handle", cell, slices.Contains(cell.Auth.Offered, "command_output"))
			}
		}
	}
	return out
}

func (h *Harness) runCredentialAssignmentHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	if len(cells) > 0 {
		harvSess := cells[0].Session("cred-harvest-neg")
		h.bindSession(harvSess.ID, cells[0])
		harvVal := "foxtrot_golf_hotel_india_123456"
		h.Harvest.Harvest(secretharvest.ContainerRead{RootSessionID: harvSess.ID, Container: ".env", Content: []byte("SERVICE_SECRET=" + harvVal + "\n")})
		_ = h.takeAdvisories()
		_, _ = h.Policy.AfterTool(cells[0].Context(ctx, harvSess.ID), harvSess, "write", map[string]any{"content": "SERVICE_SECRET=" + harvVal + "\n"}, "ok", 1, guidance.ToolResultFacts{})
		h.EvalCount++
		_ = h.takeAdvisories()
	}
	for idx, cell := range cells {
		sessID := fmt.Sprintf("cred-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		_ = h.takeAdvisories()
		h.resetExercisedEdges()
		_, _ = h.Policy.AfterTool(cctx, sess, "write", map[string]any{"content": fmt.Sprintf("API_TOKEN=alpha_bravo_charlie_delta_%06d\n", idx)}, "ok", 1, guidance.ToolResultFacts{})
		h.EvalCount++
		h.drainAdvisoriesTo(&out, oar.AnchorCredentialAssignment, "credential.assignment/strong", cell, len(cell.Auth.Offered) > 0)

		_ = h.takeAdvisories()
		h.resetExercisedEdges()
		_, _ = h.Policy.AfterTool(cctx, sess, "write", map[string]any{"content": fmt.Sprintf("MYSQL_PASSWORD=password%d\n", idx%3)}, "ok", 1, guidance.ToolResultFacts{})
		h.EvalCount++
		h.drainAdvisoriesTo(&out, oar.AnchorCredentialAssignment, "credential.assignment/weak", cell, len(cell.Auth.Offered) > 0)
	}
	return out
}

func (h *Harness) runCoordinatorCloseoutHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if cell.Role == "worker" {
			continue
		}
		sessID := fmt.Sprintf("closeout-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		if idx == 0 {
			_, _ = h.Policy.FinishBlock(cctx, sess, func(gc *oar.GuardContext) error {
				gc.Progress.VerifyRequired, gc.Progress.VerifierPass = true, true
				return nil
			})
			h.EvalCount++
		}
		closeoutCases := []struct {
			name string
			fn   func(*oar.GuardContext) error
		}{
			{"criteria_unmet_all", func(gc *oar.GuardContext) error {
				gc.Published["paintedwolf.grounding_verdict"] = "COORDINATOR_CRITERIA_UNMET"
				gc.ObservationData = map[string]any{"closeout_unstarted": true, "workflow_work_id": "w1", "worker": "impl-1", "overlay_id": "res-1", "closeout_criteria_unmet": true, "closeout_changes_pending": true, "closeout_changes_shelved": true, "closeout_changes_rejected": true}
				return nil
			}},
			{"criteria_unmet_empty", func(gc *oar.GuardContext) error {
				gc.Published["paintedwolf.grounding_verdict"] = "COORDINATOR_CRITERIA_UNMET"
				gc.ObservationData = map[string]any{"closeout_unstarted": false, "workflow_work_id": "", "closeout_criteria_unmet": false, "closeout_changes_pending": false, "closeout_changes_shelved": false, "closeout_changes_rejected": false}
				return nil
			}},
			{"ungrounded_claim", func(gc *oar.GuardContext) error {
				gc.Published["paintedwolf.grounding_verdict"] = "COORDINATOR_UNGROUNDED_CLAIM"
				return nil
			}},
			{"citation_unverifiable", func(gc *oar.GuardContext) error {
				gc.Grounding.CitationUnverifiable = true
				gc.ObservationData = map[string]any{"path": "README.md"}
				return nil
			}},
			{"pending_overlays", func(gc *oar.GuardContext) error {
				gc.Workers.PendingOverlayPromote = true
				gc.Rejection.RejectObservation = "closeout_pending_overlays"
				gc.ObservationData = map[string]any{"worker": "impl-1", "overlay_id": "res-1"}
				return nil
			}},
			{"gates_open", func(gc *oar.GuardContext) error {
				gc.Workers.WorkersIdle, gc.Workflow.CloseoutGatesOpen = true, true
				return nil
			}},
			{"progress_open", func(gc *oar.GuardContext) error {
				gc.Workers.WorkersIdle, gc.Progress.ProgressOpenItems, gc.Workflow.BatchReadyIgnoringProgress = true, 2, true
				return nil
			}},
			{"review_verdict_missing", func(gc *oar.GuardContext) error {
				gc.Workers.WorkersIdle, gc.Workflow.ReviewVerdictGateOpen = true, true
				return nil
			}},
		}
		for _, tc := range closeoutCases {
			ref, _ := h.Policy.FinishBlock(cctx, sess, tc.fn)
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorCoordinatorCloseoutCheck, "closeout/"+tc.name, cell, ref, len(cell.Auth.Offered) > 0)
		}
	}
	return out
}

func (h *Harness) runWorkerReportHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	var parentCoordReach = cells[0].Reach
	for _, c := range cells {
		if c.Role == "coordinator" && slices.Contains(c.Auth.Offered, "instruct_worker") && slices.Contains(c.Auth.Offered, "task") {
			parentCoordReach = c.Reach
			break
		}
	}
	for idx, cell := range cells {
		if cell.Role != "worker" && !cell.IsBoundary {
			continue
		}
		wCell := cell
		wCell.Role = "worker"
		sessID := fmt.Sprintf("worker-rep-%d", idx)
		h.bindSession(sessID, wCell)
		sess := wCell.Session(sessID)
		cctx := wCell.Context(ctx, sessID)
		if idx == 0 {
			_, _ = workercompletion.EvaluateWorkerSummary(cctx, workercompletion.WorkerSummaryEvalInput{
				AgentType: "implementer", ChildSessionID: sessID, Pipeline: h.Pipeline, Reach: wCell.Reach,
				Report: workercompletion.WorkerCompletionReport{Brief: "Partial.", LegStatus: "partial"},
			})
			h.EvalCount++
		}
		_, _ = workercompletion.EvaluateWorkerSummary(cctx, workercompletion.WorkerSummaryEvalInput{
			AgentType: "implementer", ChildSessionID: sessID, Pipeline: h.Pipeline, Reach: wCell.Reach,
			Report: workercompletion.WorkerCompletionReport{Brief: "Done.", LegStatus: "complete"},
		})
		h.EvalCount++
		_, _ = workercompletion.EvaluateWorkerSummary(cctx, workercompletion.WorkerSummaryEvalInput{
			AgentType: "implementer", ChildSessionID: sessID, Pipeline: h.Pipeline, Reach: parentCoordReach,
			Report: workercompletion.WorkerCompletionReport{Brief: "Done.", LegStatus: "complete"},
		})
		h.EvalCount++
		_, _ = workercompletion.EvaluateWorkerSummary(cctx, workercompletion.WorkerSummaryEvalInput{
			AgentType: "scout", ChildSessionID: sessID, Pipeline: h.Pipeline, Reach: wCell.Reach,
			Report: workercompletion.WorkerCompletionReport{Brief: "Scouted.", LegStatus: "complete"},
		})
		h.EvalCount++

		workerChecks := []struct {
			name string
			fn   func(*oar.GuardContext) error
		}{
			{"report_required", func(gc *oar.GuardContext) error {
				gc.Rejection.RejectObservation = "worker_report_required"
				return nil
			}},
			{"summary_too_long", func(gc *oar.GuardContext) error { gc.Rejection.RejectObservation = "summary_too_long"; return nil }},
			{"implement_no_artifact", func(gc *oar.GuardContext) error {
				gc.Grounding.WorkerArtifactMeasured, gc.Grounding.WorkerArtifactPresent, gc.Workers.WorkerAttemptedMutation = true, false, true
				return nil
			}},
			{"scout_no_survey", func(gc *oar.GuardContext) error {
				gc.Workers.AgentIsScout = true
				return nil
			}},
			{"url_not_observed", func(gc *oar.GuardContext) error {
				gc.Grounding.UnobservedCitedURLs = []string{"https://example.com"}
				return nil
			}},
			{"page_measure_ungrounded", func(gc *oar.GuardContext) error { gc.Grounding.PageMeasureUngrounded = true; return nil }},
			{"surface_claim_ungrounded", func(gc *oar.GuardContext) error { gc.Grounding.SurfaceClaimUngrounded = true; return nil }},
		}
		for _, wc := range workerChecks {
			_ = h.takeAdvisories()
			ref, _, _ := h.Policy.Block(cctx, oar.AnchorWorkerReportCheck, sess, "complete_leg", nil, func(gc *oar.GuardContext) error {
				gc.Grounding.WorkerSummaryPresent = true
				return wc.fn(gc)
			})
			h.EvalCount++
			h.recordRefusal(&out, oar.AnchorWorkerReportCheck, "worker.report/"+wc.name, wCell, ref, !cell.IsBoundary && len(wCell.Auth.Offered) > 0)
			h.drainAdvisoriesTo(&out, oar.AnchorWorkerReportCheck, "worker.report/"+wc.name, wCell, !cell.IsBoundary && len(wCell.Auth.Offered) > 0)
		}
	}
	return out
}

func (h *Harness) runCoordinatorMidTurnNoteHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if cell.Role == "worker" {
			continue
		}
		sessID := fmt.Sprintf("mid-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		if idx == 0 {
			neg := oar.NewGuardContext()
			neg.Rejection.RejectObservation = "mid_turn_note_ungrounded"
			neg.Session.MidTurnCitationsRequired, neg.Session.MidTurnCitationAttempted = true, true
			_ = h.Policy.MidTurnNoteFeedback(cctx, sess, neg)
			h.EvalCount++
			_ = h.takeAdvisories()
		}
		for _, obsCode := range []string{"mid_turn_note_ungrounded", "mid_turn_note_wait_fallback"} {
			_ = h.takeAdvisories()
			gc := oar.NewGuardContext()
			gc.Rejection.RejectObservation = obsCode
			gc.Session.MidTurnCitationsRequired = true
			_ = h.Policy.MidTurnNoteFeedback(cctx, sess, gc)
			h.EvalCount++
			h.drainAdvisoriesTo(&out, oar.AnchorCoordinatorMidTurnNote, "mid_turn_note/"+obsCode, cell, len(cell.Auth.Offered) > 0)
		}
	}
	return out
}

func (h *Harness) runCoordinatorPostTurnHook(ctx context.Context, cells []AuthCell) []FiredOutcome {
	var out []FiredOutcome
	for idx, cell := range cells {
		if cell.Role == "worker" {
			continue
		}
		sessID := fmt.Sprintf("post-turn-%d", idx)
		h.bindSession(sessID, cell)
		sess := cell.Session(sessID)
		cctx := cell.Context(ctx, sessID)
		if idx == 0 {
			h.resetExercisedEdges()
			_, _ = h.TurnGuards.AfterTurn(cctx, sess, "Summary ready.", true)
			h.EvalCount++
			_ = h.takeAdvisories()
		}

		// AmbientGroundingCoordinator.AfterPrompt with unsummarized worker job.
		missSessID := sessID + "-miss1"
		h.bindSession(missSessID, cell)
		h.WorkerQueue.SetJobs(missSessID, []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusComplete}})
		h.Transcripts.SetMessages(missSessID, nil)
		_ = h.takeAdvisories()
		h.resetExercisedEdges()
		_ = h.Ambient.AfterPrompt(cell.Context(ctx, missSessID), missSessID, nil)
		h.EvalCount++
		h.drainAdvisoriesTo(&out, oar.AnchorCoordinatorPostTurn, "post_turn/ungrounded[1]", cell, len(cell.Auth.Offered) > 0)

		// AmbientGroundingCoordinator.AfterPrompt with ungrounded audit and 0 missing summaries.
		auditSessID := sessID + "-miss0"
		h.bindSession(auditSessID, cell)
		h.WorkerQueue.SetJobs(auditSessID, nil)
		h.Transcripts.SetMessages(auditSessID, []api.Message{{
			ID:        "a1",
			Role:      api.MessageRoleAssistant,
			Content:   "Completed.",
			Grounding: &api.CitationGrounding{Traced: false, HintCode: grounding.AmbientUngroundedCompletionCode},
		}})
		_ = h.takeAdvisories()
		h.resetExercisedEdges()
		_ = h.Ambient.AfterPrompt(cell.Context(ctx, auditSessID), auditSessID, nil)
		h.EvalCount++
		h.drainAdvisoriesTo(&out, oar.AnchorCoordinatorPostTurn, "post_turn/ungrounded[0]", cell, len(cell.Auth.Offered) > 0)

		_ = h.takeAdvisories()
		h.resetExercisedEdges()
		gc2 := oar.NewGuardContext()
		h.Policy.FillSessionFacts(cctx, gc2, sess, "", nil)
		gc2.Grounding.GroundingEscalated = true
		_, _ = h.Pipeline.EvaluateBlock(cctx, oar.AnchorCoordinatorPostTurn, gc2)
		h.EvalCount++
		h.drainAdvisoriesTo(&out, oar.AnchorCoordinatorPostTurn, "post_turn/escalated", cell, len(cell.Auth.Offered) > 0)
	}
	return out
}
