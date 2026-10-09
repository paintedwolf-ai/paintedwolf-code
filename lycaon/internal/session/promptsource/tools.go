package promptsource

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/guidancedelivery"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/session/naming"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

type ToolsRepository interface {
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (handle string, patchedContent string, err error)
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (handle string, patchedContent string, err error)
	Get(ctx context.Context, id string) (*api.Session, error)
}
type Tools struct {
	Batch        *batchcontrol.Service
	DataDir      string
	Enricher     *guidance.ToolOutputEnricher
	Feedback     *policyfeedback.Service
	Frame        inject.CoordinatorTurnFrameSource
	Guards       *turnguards.Service
	Guidance     *guidancedelivery.Service
	History      *history.Service
	Invocations  invocation.Recorder
	Naming       *naming.Service
	Policy       *policyfacts.Service
	Presence     *agentpresence.Tracker
	Processes    *processcontrol.Service
	Projects     project.Registry
	Runtime      *coordinator.Runtime
	Sessions     ToolsRepository
	Verification *sessionverification.Service
	Visual       visual.Store
	WorkerState  *workeroutcomes.State
	Workers      workeroutcomes.CycleLedger
	Workspace    *sessionscope.Service
}

func (m *Tools) Build() promptloop.ToolsDeps {
	deps := promptloop.ToolsDeps{
		DataDir:       m.DataDir,
		Invocations:   m.Invocations,
		VisualStore:   m.Visual,
		AgentPresence: m.Presence,
		DesignateProjectCover: func(ctx context.Context, projectID, rootSessionID, artifactID string) error {
			if m.Visual == nil || m.Projects == nil {
				return nil
			}
			err := visual.DesignateCover(ctx, m.Visual, project.CoverBinding{Registry: m.Projects},
				func(ctx context.Context, sessionID string) (string, error) {
					sess, err := m.Sessions.Get(ctx, sessionID)
					if err != nil {
						return "", err
					}
					return sess.ProjectID, nil
				},
				visual.DesignateRequest{
					ProjectID:     projectID,
					RootSessionID: rootSessionID,
					ArtifactID:    artifactID,
				},
			)
			if err == nil {
				m.Naming.PublishProject(ctx, projectID)
			}
			return err
		},
		HeldCalls:     m.Processes,
		BlockPlane:    &toolfeedback.BlockPlane{Pipeline: m.Policy.Pipeline, Renderer: m.Feedback.Renderer()},
		BeforeToolRun: m.Guards.BeforeTool,
		AfterToolRun: func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, succeeded bool, out *tools.ToolInvocationOut) string {
			return m.AfterTool(ctx, sess, tool, args, output, succeeded, out)
		},
		EnrichToolOutput: m.EnrichTool,
	}
	deps.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		if m == nil || sess == nil {
			return "", content, nil
		}
		projectDir, err := m.Workspace.ActivePath(ctx, sess)
		if err != nil {
			return "", content, err
		}
		if artifactID != "" {
			return m.Sessions.CommitVisualEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, artifactID)
		}
		return m.Sessions.CommitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content)
	}
	deps.RecordSourceRunEvidence = m.Verification.RecordSourceRunEvidence
	deps.ConfirmVerifyResult = m.Verification.ConfirmVerifyResult
	deps.InFlightWorkerRosterNote = func(ctx context.Context, sess *api.Session) string {
		if m == nil || m.Workers == nil || sess == nil {
			return ""
		}
		active, err := workeroutcomes.ParentSessionInFlightWorkers(ctx, m.Workers, sess.ProjectID, sess.ID)
		if err != nil || len(active) == 0 {
			return ""
		}
		note, err := guidance.RenderWorkerInFlightRoster(ctx, guidance.BuildWorkerRosterLines(active))
		if err != nil {
			return ""
		}
		return note
	}
	deps.CompactOversizedToolResults = func(ctx context.Context, sessionID string, sess *api.Session) error {
		if m == nil {
			return nil
		}
		return m.History.ScheduleChunks(ctx, sess)
	}
	deps.CompactToolWire = func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
		if m == nil {
			return content, nil
		}
		return m.History.ToolWire(ctx, sess, toolName, content, opts)
	}
	deps.ReloadHistory = func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
		if m == nil {
			return nil, fmt.Errorf("session store not configured")
		}
		return m.History.Reload(ctx, sessionID, sess, surfaceID)
	}

	return deps
}

func (m *Tools) AfterTool(
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

func (m *Tools) EnrichTool(
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
	eval := toolpolicy.BuildEvalContext(ctx, m.Guards.PolicyDependencies(), sess, tool, args)
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
