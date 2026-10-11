package promptsource

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ToolResults applies host feedback to a completed tool invocation.
type ToolResults struct {
	Batch       *batchcontrol.Service
	Enricher    *guidance.ToolOutputEnricher
	Frame       inject.CoordinatorTurnFrameSource
	Guards      *turnguards.Service
	Guidance    *guidancedelivery.Service
	Policy      *policyfacts.Service
	Runtime     *coordinator.Runtime
	WorkerState *workeroutcomes.State
}

func (m *ToolResults) AfterTool(
	ctx context.Context,
	sess *api.Session,
	tool string,
	args map[string]any,
	output string,
	succeeded bool,
	out *tools.ToolInvocationOut,
) string {
	output = toolpresentation.SanitizeForCoordinator(sess, tool, output)
	if tool == "task" && succeeded && out != nil && out.Dispatch != nil {
		if jobID := strings.TrimSpace(out.Dispatch.WorkerID); jobID != "" {
			agentType, _ := args["agent_type"].(string)
			m.Batch.TaskEnqueued(ctx, sess.ID, agentType)
			out.Facts = out.Facts.WithFeedback("BANNER_TASK_QUEUED", map[string]any{
				"agent_type": agentType, "job_id": jobID, "max_in_flight": spawn.MaxInFlightTaskWorkers,
			}, &api.FeedbackSubject{Kind: "worker", ID: jobID})
			out.Completion = &api.ToolCompletion{
				Operation: "worker_dispatch", State: "enqueued", ResourceKind: "worker", ResourceID: jobID,
			}
		}
	}
	return output
}

func (m *ToolResults) EnrichTool(
	ctx context.Context,
	sess *api.Session,
	tool string,
	args map[string]any,
	output string,
	raised guidance.ToolResultFacts,
	doomCompletionCountAfter int,
) (string, guidance.ToolResultFacts) {
	facts := raised
	if m == nil || sess == nil {
		return output, facts
	}
	if m.Policy.Pipeline != nil && m.Policy.Pipeline.AnchorEnforced(oar.AnchorToolPost) {
		var postFacts guidance.ToolResultFacts
		output, postFacts = m.Policy.AfterTool(
			ctx, sess, tool, args, output, doomCompletionCountAfter, raised,
		)
		facts = facts.Merge(postFacts)
	}
	if !facts.Succeeded() || m.Enricher == nil {
		return output, facts
	}
	eval, err := toolpolicy.BuildEvalContext(ctx, m.Guards.PolicyDependencies(), sess, tool, args)
	if err != nil {
		slog.ErrorContext(ctx, "tool output policy facts unavailable", "session_id", sess.ID, "tool", tool, "err", err)
		return output, facts
	}
	hostArgs := make(map[string]any, len(args)+1)
	for key, value := range args {
		hostArgs[key] = value
	}
	if phaseID, ok := scaffoldvars.PendingFeedbackPhase(eval.Vars); ok {
		hostArgs["_pending_feedback_line"] = fmt.Sprintf("pending_feedback: phase `%s`", phaseID)
	}
	wfEval := feedback.WorkflowEvaluationContext{}
	if m.Frame != nil {
		if frame, err := m.Frame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); err == nil {
			wfEval = inject.WorkflowEvaluation(frame)
		} else {
			slog.ErrorContext(ctx, "coordinator turn frame unavailable; tool-output gate feedback degrades to empty workflow context",
				"session_id", sess.ID, "tool", tool, "err", err)
		}
	}
	batchPhase := m.WorkerState.ForSession(ctx, sess).BatchPhase
	enriched := m.Enricher.Enrich(ctx, guidance.EnrichInput{
		SessionID: sess.ID, Session: sess, Tool: tool, Args: hostArgs, Output: output,
		PlanProgress: eval.PlanProgress, PlanContent: eval.PlanContent, BlueprintPath: eval.BlueprintPath,
		Workflow: wfEval, BatchPhase: batchPhase, Facts: facts,
	})
	merged := facts.Merge(enriched.Facts)
	if merged.HasCode("WORKFLOW_GATE_BLOCKED") {
		m.Guidance.Emit(ctx, sess.ID, anchor.GateBlocked, anchor.Envelope{})
		m.Runtime.CoordinatorLoop().Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, anchor.GateBlocked, "", anchor.Envelope{})
	}
	return enriched.Output, merged
}
