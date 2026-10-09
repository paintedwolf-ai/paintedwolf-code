package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// DelegationLegLookup supplies leg metadata for host closeout assembly.
type DelegationLegLookup interface {
	DelegationBySessionID(sessionID string) (string, bool)
	GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error)
	ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error)
}

func (m *Manager) SetDelegationLegLookup(store DelegationLegLookup) {
	if m != nil {
		m.delegations = store
		m.Closeout.SetDelegations(store)
	}
}

type runtimeBoardHook struct {
	rt *coordinator.Runtime
}

func (h runtimeBoardHook) WithRepositoryFacts(ctx context.Context) context.Context {
	if h.rt == nil || h.rt.Board() == nil {
		return ctx
	}
	return h.rt.Board().WithRepositoryFacts(ctx)
}

func (h runtimeBoardHook) PrependBoardIfChanged(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) (string, bool) {
	if h.rt == nil {
		return "", false
	}
	return h.rt.Board().PrependBoardIfChanged(ctx, sess, run)
}

func (h runtimeBoardHook) WorkerBoard(ctx context.Context, sess *api.Session) (string, bool) {
	if h.rt == nil {
		return "", false
	}
	return h.rt.Board().WorkerBoard(ctx, sess)
}

func (h runtimeBoardHook) BoardInjectHash(sessionID string) string {
	if h.rt == nil {
		return ""
	}
	return h.rt.Board().BoardInjectHash(sessionID)
}

func (h runtimeBoardHook) InvalidateOrientation(sessionID string) {
	if h.rt != nil {
		h.rt.Board().InvalidateOrientation(sessionID)
	}
}

type sessionWorkflowManifest struct {
	m *Manager
}

func (w sessionWorkflowManifest) ActiveManifest(ctx context.Context, sessionID string) (assembly.ActiveWorkflowManifest, bool) {
	if w.m == nil || w.m.workflows == nil {
		return assembly.ActiveWorkflowManifest{}, false
	}
	manifest, ok := w.m.workflows.ActiveManifest(ctx, sessionID)
	if !ok {
		return assembly.ActiveWorkflowManifest{}, false
	}
	return assembly.ActiveWorkflowManifest{CoordinatorProfile: manifest.CoordinatorProfile}, true
}

func (w sessionWorkflowManifest) RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error {
	if w.m == nil || w.m.workflows == nil {
		return nil
	}
	return w.m.workflows.RecordBoardOrientReady(ctx, sessionID, injectKey)
}

func (m *Manager) SetWebResearchConfig(cfg *webresearch.ConfigStore) {
	if m == nil {
		return
	}
	m.webResearchConfig = cfg
}

func (m *Manager) webSearchEnabled() bool {
	if m == nil || m.webResearchConfig == nil {
		return true
	}
	return m.webResearchConfig.SearchEnabled()
}

func (m *Manager) buildPromptLoopDeps() promptloop.PromptLoopDeps {
	rt := m.ensureCoordinatorRuntime()
	// CommitWorkerContext is bound by the runtime after these deps are built.
	deps := promptloop.PromptLoopDeps{
		Context: promptloop.ContextDeps{
			Limits:                          m.Limits.Effective,
			CoordinatorSurfaceActivityLabel: surface.LoadCoordinatorSurfaceActivityLabel,
			SetPromptTurnSurface: func(sessionID, surfaceID string) {
				rt.Assembly().SetTurnSurfaceID(sessionID, surfaceID)
			},
			PromptTurnSurface: func(sessionID string) string {
				return rt.PromptTurnSurfaceID(sessionID)
			},
			Tools: m.tools,
			RootSessionID: func(ctx context.Context, sessionID string) string {
				return sessiontree.RootID(ctx, m.store, sessionID)
			},
			Policy: m.Guards.Policy(),
			CoordinatorPostureRules: func(ctx context.Context, sess *api.Session) ([]string, error) {
				if sess == nil || sess.Posture == "" {
					return nil, nil
				}
				postures, err := m.Profiles.EffectivePostures(ctx, sess)
				if err != nil || postures == nil {
					return nil, err
				}
				paths, err := postures.RulesPaths(sess.Posture)
				if err != nil {
					return nil, err
				}
				return append([]string(nil), paths...), nil
			},
			ProjectRootCount: func(ctx context.Context, sess *api.Session) int {
				_, count, _ := m.Workspace.PromptRootRows(ctx, sess)
				return count
			},
			OverlayRootPaths: func(ctx context.Context, sess *api.Session) []string {
				return m.Workspace.SettingsRoots(ctx, sess)
			},
			LoadedTools:  m.Loading.LoadedTools,
			ToolObserved: m.Loading.ObserveToolCall,
			LiveResources: func(sessionID string) toolcontract.ResourcePresence {
				presence := toolcontract.ResourcePresence{}
				if m != nil && m.Processes.Background != nil {
					presence.CommandJobs = m.Processes.Background.HasPipelineHandles(sessionID)
					presence.Terminals = m.Processes.Background.CountLivePTYs(sessionID) > 0
				}
				if m != nil && m.pageRegistry != nil {
					presence.Pages = m.pageRegistry.CountLive(sessionID) > 0
				}
				if m != nil {
					presence.HeldCalls = m.Processes.Held.HasHandles(sessionID)
				}
				return presence
			},
			BuildMessages:         m.buildCompletionMessages,
			WebSearchEnabled:      m.webSearchEnabled,
			CoordinatorFrame:      m.coordinatorFrame,
			ImplementSessionState: m.Workers.State.ForSession,
		},
		Tools: promptloop.ToolsDeps{
			DataDir:       m.dataDir,
			Invocations:   m.invocations,
			VisualStore:   m.visual,
			AgentPresence: m.agentPresence,
			DesignateProjectCover: func(ctx context.Context, projectID, rootSessionID, artifactID string) error {
				if m.visual == nil || m.projects == nil {
					return nil
				}
				err := visual.DesignateCover(ctx, m.visual, project.CoverBinding{Registry: m.projects},
					func(ctx context.Context, sessionID string) (string, error) {
						sess, err := m.store.Get(ctx, sessionID)
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
			BlockPlane:    &tools.BlockPlane{Pipeline: m.ToolPolicy.Pipeline, Renderer: m.Feedback.Renderer()},
			BeforeToolRun: m.Guards.BeforeTool,
			AfterToolRun: func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, succeeded bool, out *tools.ToolInvocationOut) string {
				return m.afterPromptLoopToolRun(ctx, sess, tool, args, output, succeeded, out)
			},
			EnrichToolOutput: m.enrichPromptLoopToolOutput,
		},
		Control: promptloop.ControlDeps{
			ReconcileCoordinatorBatch: func(ctx context.Context, sessionID string) {
				m.Batch.Reconcile(ctx, sessionID)
			},
		},
		Inbox: promptloop.InboxDeps{
			TakeUserSend:       m.Submissions.TakeSend,
			TakePolicyFeedback: m.Guidance.TakePolicy,
			TakePhaseGuidance:  m.Guidance.TakePhase,
		},
		Model: promptloop.ModelDeps{
			LLM:        m.llm,
			LLMService: m.llmSvc,
			Cost:       m.cost,
		},
		Projection: promptloop.ProjectionDeps{
			Events:                  m.events,
			RedactMessageForStorage: m.Transcript.Redact,
			AppendMessages:          m.Transcript.Append,
			Streams:                 m.Transcript.Streams,
		},
		Nudges: promptloop.NudgesDeps{
			DoomLoop: m.doomLoop,
			FormatDoomLoopReject: func(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
				return m.ToolPolicy.FormatDoomLoopReject(ctx, sessionID, tool, args, count, repeatedCode)
			},
			EscalateRepeatedCode: func(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
				total := m.ToolPolicy.CodeRejectResponses(sessionID, tool, original.Code())
				if total < loopguard.DoomLoopMaxCodeRepeats {
					return nil
				}
				return m.ToolPolicy.EscalateRepeatedCode(ctx, sessionID, tool, original, total)
			},
		},
		Closeout: promptloop.CloseoutDeps{
			RejectFmt:  m.rejectFmt,
			HintConfig: m.workflowHints,
			EvaluateContentAnchor: func(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
				reject, blocked, content, transformed, err := m.ToolPolicy.ContentBlock(ctx, sess, anchor, segments, tool, args)
				if err != nil {
					return guidance.NewRefusal("", err.Error()), true, "", false
				}
				return reject, blocked, content, transformed
			},
			EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
				return m.ToolPolicy.CloseoutBlock(ctx, sess, gc)
			},
		},
	}
	if m.prompts != nil {
		deps.Context.ToolProcedures = m.renderToolProcedures
	}
	m.bindPromptLoopRuntimeDeps(&deps)
	m.bindPromptLoopWorkerDeps(&deps)
	m.bindPromptLoopCompactionDeps(&deps)
	return deps
}

func (m *Manager) bindPromptLoopRuntimeDeps(deps *promptloop.PromptLoopDeps) {
	deps.Nudges.CheckSpendCeiling = func(ctx context.Context, sessionID string, sess *api.Session) (promptloop.SpendCeilingCheck, error) {
		st, err := m.Runner.Spend.State(ctx, sessionID, sess)
		if err != nil {
			slog.WarnContext(ctx, "check spend ceiling", "session_id", sessionID, "error", err)
			return promptloop.SpendCeilingCheck{}, nil
		}
		if st.Reached {
			return promptloop.SpendCeilingCheck{SoftStop: st.SoftStop}, st.ReachedError()
		}
		return promptloop.SpendCeilingCheck{
			Runway: promptloop.SpendRunway{Low: st.Low, CeilingUSD: st.CeilingUSD},
		}, nil
	}
	deps.Nudges.IsSpendCeiling = func(err error) bool { return errors.Is(err, spendguard.ErrCeiling) }
	deps.Control.AssertRunnable = m.assertWorkflowRunnable
	deps.Context.RefreshToolContext = func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error) {
		profileID := strings.TrimSpace(machine.ProfileID)
		if profileID == "" {
			var err error
			profileID, err = m.Profiles.PromptToolProfile(ctx, sess)
			if err != nil {
				return tools.ToolContext{}, err
			}
		}
		tctx, err := m.ToolContext.Build(ctx, sess, profileID, machine)
		if err != nil {
			return tools.ToolContext{}, err
		}
		return m.Workers.Workspaces.Enrich(ctx, sess, tctx)
	}
	deps.Closeout.TurnCloseoutNudge = m.Nudges.Closeout
	deps.Nudges.SecretWithheldNudge = m.Nudges.SecretWithheld
	deps.Closeout.IterationRunwayNudge = m.Nudges.IterationRunway
	deps.Nudges.WorkerBudgetRaisedNudge = m.Nudges.BudgetAnswer(anchor.WorkerBudgetRaised)
	deps.Nudges.WorkerBudgetDeclinedNudge = m.Nudges.BudgetAnswer(anchor.WorkerBudgetDeclined)
	deps.Nudges.WorkerBudgetAnswerWait = spawn.WorkerBudgetAnswerWait
	deps.Nudges.SurveyStreakNudge = m.Nudges.SurveyStreak
	deps.Nudges.SpendRunwayNudge = m.Nudges.SpendRunway
	deps.Nudges.SpendSoftStopNudge = m.Nudges.SpendSoftStop
	deps.Control.WorkerGracefulCancelPending = m.Workers.Cancel.Pending
	deps.Projection.OnToolReject = func(ctx context.Context, sessionID, toolCallID, code, content string, facts guidance.ToolResultFacts) {
		if m != nil && m.planToolStash != nil {
			m.planToolStash.Put(sessionID, toolCallID, code, content)
		}
		if m != nil {
			m.Workers.Rejections.RecordForChild(ctx, sessionID, code, content, facts)
		}
	}
	deps.Projection.AnnouncePendingToolAsk = func(ctx context.Context, sessionID string) {
		if m != nil && m.workflows != nil {
			m.workflows.AnnouncePendingAsk(ctx, sessionID)
		}
	}
	deps.Control.HumanApprovalAwaiting = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.loopWorkflowSource == nil {
			return false
		}
		awaiting, err := m.loopWorkflowSource.HumanApprovalAwaiting(ctx, sessionID)
		return err == nil && awaiting
	}
	deps.Control.HostObligationHeld = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.loopWorkflowSource == nil {
			return false
		}
		held, err := m.loopWorkflowSource.HostObligationHeld(ctx, sessionID)
		return err == nil && held
	}
	deps.Control.ParkBlockedLiveCommands = m.parkBlockedLiveCommands
	deps.Closeout.BeforeFinishNoToolTurn = m.Guards.BeforeFinish
	deps.Closeout.OnGroundedSynthesisAccepted = func(ctx context.Context, _ *api.Session, sessionID string) {
		m.Batch.AcceptSynthesis(ctx, sessionID)
	}
	deps.Closeout.ProseCitationGrounding = func(ctx context.Context, sess *api.Session, history []api.Message, _ string, prose, surfaceID string) *api.CitationGrounding {
		if m == nil || sess == nil || sess.IsWorkerChild() {
			return nil
		}
		roots, err := m.Closeout.CitationRoots(ctx, sess)
		if err != nil {
			return nil
		}
		grounding, err := guard.BuildCoordinatorProseCitationGrounding(ctx, m.Verification.Evidence, sess, history, prose, surfaceID, roots)
		if err != nil {
			return nil
		}
		return grounding
	}
	deps.Tools.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		if m == nil || sess == nil {
			return "", content, nil
		}
		projectDir, err := m.Workspace.ActivePath(ctx, sess)
		if err != nil {
			return "", content, err
		}
		if artifactID != "" {
			return m.store.CommitVisualEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content, artifactID)
		}
		return m.store.CommitEvidenceToolResult(ctx, sessionID, projectDir, toolName, args, content)
	}
}

func (m *Manager) bindPromptLoopWorkerDeps(deps *promptloop.PromptLoopDeps) {
	deps.Tools.RecordSourceRunEvidence = m.Verification.RecordSourceRunEvidence
	deps.Tools.ConfirmVerifyResult = m.Verification.ConfirmVerifyResult
	deps.Nudges.PublishWorkerProgress = m.publishWorkerProgress
	deps.Nudges.WorkerJob = m.workerJob
	deps.Projection.UpdateMessage = func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
		if m == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.Transcript.Update(ctx, sessionID, messageID, msg)
	}

	deps.Projection.AdmitModelResponse = m.store.AdmitModelResponse
	deps.Projection.SettleModelOutput = func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error) {
		if m == nil || m.store == nil {
			return store.ModelOutput{}, fmt.Errorf("session store not configured")
		}
		return m.store.SettleModelOutput(ctx, out)
	}
	deps.Projection.MarkModelOutputProjected = func(ctx context.Context, outputID string) error {
		if m == nil || m.store == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.store.MarkModelOutputProjected(ctx, outputID)
	}
	deps.Projection.CheckpointTurn = func(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error {
		if m == nil || m.store == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.store.CheckpointTurn(ctx, turnID, attemptID, phase, checkpointJSON)
	}
	deps.Projection.AppendDraftVersion = func(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.store.AppendDraftVersion(ctx, sessionID, slotID, body, outcomeCode)
	}
	deps.Projection.CountDraftVersions = func(ctx context.Context, sessionID, slotID string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.store.CountDraftVersions(ctx, sessionID, slotID)
	}
	deps.Tools.InFlightWorkerRosterNote = func(ctx context.Context, sess *api.Session) string {
		if m == nil || m.workerQueue == nil || sess == nil {
			return ""
		}
		active, err := workeroutcomes.ParentSessionInFlightWorkers(ctx, m.workerQueue, sess.ProjectID, sess.ID)
		if err != nil || len(active) == 0 {
			return ""
		}
		note, err := guidance.RenderWorkerInFlightRoster(ctx, guidance.BuildWorkerRosterLines(active))
		if err != nil {
			return ""
		}
		return note
	}
}

func (m *Manager) bindPromptLoopCompactionDeps(deps *promptloop.PromptLoopDeps) {
	deps.Tools.CompactOversizedToolResults = func(ctx context.Context, sessionID string, sess *api.Session) error {
		if m == nil {
			return nil
		}
		return m.Runner.History.ScheduleChunks(ctx, sess)
	}
	deps.Tools.CompactToolWire = func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
		if m == nil {
			return content, nil
		}
		return m.Runner.History.ToolWire(ctx, sess, toolName, content, opts)
	}
	deps.Model.CompactionConfig = m.Limits.Compaction
	deps.Context.MCPAlwaysLoad = func(ctx context.Context, sess *api.Session) map[string]bool {
		if m.ToolPolicy.MCP == nil {
			return nil
		}
		return m.ToolPolicy.MCP.ToolLoadingModes(ctx, m.Workspace.SettingsPath(ctx, sess))
	}
	deps.Model.RecordCompactionTokenObservation = func(sessionID string, reportedPromptTokens, transcriptEstimate int) {
		if m != nil {
			m.Runner.History.ObserveTokens(sessionID, reportedPromptTokens, transcriptEstimate)
		}
	}
	deps.Model.CompactionTokenCalibration = func(sessionID string) compaction.PromptTokenCalibration {
		if m == nil {
			return compaction.PromptTokenCalibration{}
		}
		return m.Runner.History.Calibration(sessionID)
	}
	deps.Tools.ReloadHistory = func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
		if m == nil {
			return nil, fmt.Errorf("session store not configured")
		}
		return m.Runner.History.Reload(ctx, sessionID, sess, surfaceID)
	}
	deps.Closeout.EvidenceLedger = m.Verification.Evidence
	deps.Closeout.MaxCloseoutCitationGroundingRetries = limits.DefaultCitationGroundingRetries
	deps.Closeout.RenderHostKick = m.Workers.RenderKick
	deps.Closeout.BeginCloseoutIntent = m.Runner.Closeouts.BeginIntent
	deps.Closeout.RecordGroundingFriction = m.Runner.Closeouts.RecordGroundingFriction
	deps.Closeout.NoteCloseoutGroundingReject = m.Runner.Closeouts.NoteCloseoutGroundingReject
	deps.Closeout.NoteCoordinatorToolTurn = m.Runner.Closeouts.NoteCoordinatorToolTurn
	deps.Closeout.CloseoutStallState = m.Runner.Closeouts.CloseoutStallState
	deps.Closeout.ClearCloseoutStall = m.Runner.Closeouts.ClearCloseoutStall
	if m.reportDocuments != nil {
		deps.Closeout.CheckRunReportDocument = m.reportDocuments.CheckRunReportDocument
	}
	deps.Closeout.AssembleLedgerCloseout = func(ctx context.Context, sessionID, surfaceID string, forcedBy []string, draftedContent string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return m.Closeout.Assemble(ctx, sessionID, surfaceID, forcedBy, draftedContent, retryCount)
	}
	deps.Closeout.CitationRoots = func(ctx context.Context, sess *api.Session) evidence.CitationRoots {
		if m == nil || sess == nil {
			return evidence.CitationRoots{}
		}
		roots, err := m.Closeout.CitationRoots(ctx, sess)
		if err != nil {
			return evidence.CitationRoots{}
		}
		return roots
	}
}

func (m *Manager) afterPromptLoopToolRun(
	ctx context.Context,
	sess *api.Session,
	tool string,
	args map[string]any,
	output string,
	succeeded bool,
	out *tools.ToolInvocationOut,
) string {
	output = SanitizeToolOutputForCoordinator(sess, tool, output)
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

func (m *Manager) enrichPromptLoopToolOutput(
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
	if m.ToolPolicy.Pipeline != nil && m.ToolPolicy.Pipeline.AnchorEnforced(oar.AnchorToolPost) {
		var postFacts guidance.ToolResultFacts
		output, postFacts = m.ToolPolicy.AfterTool(
			ctx, sess, tool, args, output, doomCompletionCountAfter, raised,
		)
		facts = facts.Merge(postFacts)
	}
	if !facts.Succeeded() || m.toolOutputEnricher == nil {
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
	if m.coordinatorFrame != nil {
		if frame, err := m.coordinatorFrame.BuildCoordinatorTurnFrame(ctx, sess.ID, sess); err == nil {
			wfEval = inject.WorkflowEvaluation(frame)
		} else {
			slog.ErrorContext(ctx, "coordinator turn frame unavailable; tool-output gate feedback degrades to empty workflow context",
				"session_id", sess.ID, "tool", tool, "err", err)
		}
	}
	batchPhase := m.Workers.State.ForSession(ctx, sess).BatchPhase
	enriched := m.toolOutputEnricher.Enrich(ctx, guidance.EnrichInput{
		SessionID: sess.ID, Session: sess, Tool: tool, Args: hostArgs, Output: output,
		PlanProgress: eval.PlanProgress, PlanContent: eval.PlanContent, BlueprintPath: eval.BlueprintPath,
		Workflow: wfEval, BatchPhase: batchPhase, Facts: facts,
	})
	merged := facts.Merge(enriched.Facts)
	if merged.HasCode("WORKFLOW_GATE_BLOCKED") {
		m.Guidance.Emit(ctx, sess.ID, anchor.GateBlocked, anchor.Envelope{})
		m.NudgeCoordinatorLoop(ctx, sess.ID, anchor.PhaseAdvanced, anchor.GateBlocked, "", anchor.Envelope{})
	}
	return enriched.Output, merged
}

func (m *Manager) buildAssemblyDeps() assembly.AssemblyDeps {
	rt := m.ensureCoordinatorRuntime()
	var workerCtx assembly.WorkerContextBuilder
	if m != nil {
		workerCtx = m.workerContext
	}
	return assembly.AssemblyDeps{
		Prompts:               m.prompts,
		Injects:               prompts.NewInjectRenderer(m.prompts),
		Limits:                m.Limits.Effective,
		Agents:                m.Profiles.Agents,
		Workflows:             sessionWorkflowManifest{m: m},
		CoordinatorFrame:      m.coordinatorFrame,
		WorkerContext:         workerCtx,
		SiblingNoteDelivery:   m.Workers.Notes,
		PeerReservations:      m.Workers.Workspaces,
		ImplementSessionState: m.Workers.State.ForSession,
		LoadExecutionModeState: func(_ context.Context, sessionID string) surface.ExecutionModeState {
			return rt.ExecutionModeStore().Load(sessionID)
		},
		SaveExecutionModeState: func(_ context.Context, sessionID, family string) {
			rt.ExecutionModeStore().Save(sessionID, family)
		},
		WorkflowHints: m.workflowHints,
		GateFeedback:  m.gateFeedback,
		ScanGuidance:  m.scanGuidance,
		RepoKnownEmpty: func(ctx context.Context, workspacePath string) bool {
			return repoinfo.MeasuredEmpty(ctx, m.repoProvider, workspacePath)
		},
		Board:            runtimeBoardHook{rt: rt},
		BoardOrientReady: sessionWorkflowManifest{m: m},
		EnrichHistory: func(sessionID string, history []api.Message) []api.Message {
			if m == nil || m.planToolStash == nil {
				return history
			}
			return EnrichHistoryToolPartsForAgent(sessionID, history, m.planToolStash)
		},
		PromptToolLister:        m.listPromptToolsForCoordinator,
		LoadedTools:             m.Loading.LoadedTools,
		OmittedUnits:            m.Loading.OmittedUnits,
		SkillPreload:            m.Loading.SkillPreload,
		SkillPointer:            m.Loading.SkillPointer,
		WorkspaceRoots:          m.Workspace.PromptRootRows,
		ProjectOverlayRootPaths: m.Workspace.PromptRoots,
		SessionView:             m.Catalog.ViewForSession,
		AgentsMDIndex:           m.PolicyIndex.Index,
		AgentsMDChain:           m.PolicyIndex.Chain,
		WebSearchEnabled:        m.webSearchEnabled,
		SynthesisEvidence:       m.Closeout,
		CommandJobs: func(sessionID string) []bgprocess.JobSnapshot {
			if m == nil || m.Processes.Background == nil {
				return nil
			}
			return m.Processes.Background.ActiveJobs(sessionID)
		},
		HeldCalls: func(sessionID string) []heldcall.Running {
			if m == nil {
				return nil
			}
			return m.Processes.Held.Ledger(sessionID)
		},
		TurnSourceBriefs:       m.SourceBriefs.Recorded,
		ModelVision:            m.SessionModelVision,
		EffectivePromptSurface: m.agentPromptSurface,
	}
}

func (m *Manager) agentPromptSurface(ctx context.Context, sess *api.Session) prompts.AgentPromptSurface {
	if m == nil || sess == nil {
		return prompts.AgentPromptSurface{}
	}
	profileID, _ := m.Profiles.PromptToolProfile(ctx, sess)
	return m.Profiles.CompileMachine(ctx, sess, profileID).Surface
}

// SessionModelVision reports whether the session's active coordinator model declares vision.
func (m *Manager) SessionModelVision(ctx context.Context, sess *api.Session) bool {
	if m == nil || m.llmSvc == nil || m.llmSvc.Registry == nil || m.llmSvc.Router == nil || sess == nil {
		return false
	}
	router := m.llmSvc.Router.WithOverlayRoots(m.Workspace.SettingsRoots(ctx, sess))
	sel, err := router.ResolveSession(ctx, sess)
	if err != nil || sel == nil {
		return false
	}
	return m.llmSvc.Registry.ModelHasVision(ctx, sel.ProviderID, sel.Model)
}

func (m *Manager) listPromptToolsForCoordinator(ctx context.Context, sess *api.Session, profileID string) ([]tools.ToolMeta, error) {
	if m == nil || sess == nil {
		return nil, nil
	}
	policy := m.Guards.Policy()
	if policy == nil {
		return nil, nil
	}
	metas := policy.ListForPrompt(ctx, sess, profileID)
	machine := m.Profiles.CompileMachine(ctx, sess, profileID)
	return tools.HideSkillsReadWhenEmpty(metas, machine.SkillCount), nil
}

func (m *Manager) buildLoopWakeDeps() loopwake.LoopDeps {
	return loopwake.LoopDeps{
		RunPrompt: func(ctx context.Context, sessionID string) (*promptresult.Result, error) {
			return m.Submissions.LoopWake(ctx, sessionID)
		},
		RunWaitResume:   m.Submissions.WaitResume,
		HostTurnBlocked: m.Runner.Turns.HostTurnBlocked,
		GetSession: func(ctx context.Context, sessionID string) (*api.Session, error) {
			return m.store.Get(ctx, sessionID)
		},
		Limits:           m.Limits.Effective,
		IsEscalated:      m.groundingEscalated,
		WorkflowSource:   m.loopWorkflowSource,
		CoordinatorFrame: m.coordinatorFrame,
		BoardWillForceInject: func(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool {
			return m.ensureCoordinatorRuntime().Board().BoardWillForceInject(ctx, sess, run)
		},
		QueueInform: func(ctx context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
			m.Guidance.Emit(ctx, sessionID, inform, env)
		},
		HasQueuedKick: func(sessionID, kickID string) bool {
			return m.ensureCoordinatorRuntime().Kicks().HasQueuedKick(sessionID, kickID)
		},
		DropPendingKicksForBatchSeq: func(sessionID string, batchSeq int) {
			m.ensureCoordinatorRuntime().Kicks().DropPendingKicksForBatchSeq(sessionID, batchSeq)
		},
		DropPendingKicksBeforeBatchSeq: func(sessionID string, liveSeq int) {
			m.ensureCoordinatorRuntime().Kicks().DropPendingKicksBeforeBatchSeq(sessionID, liveSeq)
		},
		OnLoopQuiescent: func(ctx context.Context, sessionID string) {
			if err := m.Runner.Settlement.SettlePending(ctx, sessionID); err != nil {
				slog.ErrorContext(ctx, "settle deferred user turn", "session_id", sessionID, "error", err)
			}
			m.Admission.RoundEnd(ctx, sessionID)
		},
		IsCoordinatorSession:    m.isCoordinatorSessionForLoop,
		WorkflowObligationsOpen: m.Guidance.WorkflowObligationsOpen,
		WorkerCycleIdle: func(ctx context.Context, sess *api.Session, completingJobID string) (bool, error) {
			if m == nil || sess == nil {
				return true, nil
			}
			return workeroutcomes.ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sess.ID, completingJobID)
		},
		HostWakeOverlayPromoteDue: func(ctx context.Context, sessionID string) bool {
			if m == nil {
				return false
			}
			sess, err := m.store.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return false
			}
			state := m.Workers.State.ForSession(ctx, sess)
			return len(state.PendingOverlayIDs) > 0
		},
		ScanCycleOpen: func(ctx context.Context, sessionID string) bool {
			return m != nil && m.scanWaits.InFlight != nil && m.scanWaits.InFlight(ctx, sessionID)
		},
		ProcessRunning:   m.Processes.HandlesRunning,
		ProcessState:     m.Processes.HandleState,
		ProcessReport:    m.Processes.Report,
		PublishWaitLease: m.publishWaitLease,
		HostWakeActionable: loopwake.BuildHostWakeActionable(loopwake.HostWakeActionableDeps{
			GetMessages: func(ctx context.Context, sessionID string) ([]api.Message, error) {
				if m == nil {
					return nil, nil
				}
				return m.store.GetMessages(ctx, sessionID)
			},
			GetSession: func(ctx context.Context, sessionID string) (*api.Session, error) {
				if m == nil {
					return nil, nil
				}
				return m.store.Get(ctx, sessionID)
			},
			ImplementSessionState: func(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
				if m == nil || sess == nil {
					return surface.ImplementSessionState{}
				}
				return m.Workers.State.ForSession(ctx, sess)
			},
			ActiveRun: func(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
				if m == nil || m.workflows == nil {
					return nil, nil
				}
				return m.workflows.GetActive(ctx, sessionID)
			},
			WorkflowObligationsOpen: m.Guidance.WorkflowObligationsOpen,
		}),
	}
}

func (m *Manager) groundingEscalated(sessionID string) bool {
	if m == nil || m.grounding == nil {
		return false
	}
	return m.grounding.IsEscalated(sessionID)
}

// PushExecutionModeTransitionCause records the next execution-mode entry.
func (m *Manager) PushExecutionModeTransitionCause(sessionID string, cause surface.ModeTransitionCause) {
	if m == nil {
		return
	}
	m.ensureCoordinatorRuntime().PushModeTransitionCause(sessionID, cause)
}

func (m *Manager) ensureCoordinatorRuntime() *coordinator.Runtime {
	if m == nil {
		return coordinator.NewRuntime(coordinator.RuntimeDeps{})
	}
	m.coordinatorRuntimeOnce.Do(func() {
		if m.coordinatorRuntime != nil {
			return
		}
		m.coordinatorRuntime = coordinator.NewRuntime(coordinator.RuntimeDeps{
			LoopDeps:     m.buildPromptLoopDeps,
			AssemblyDeps: m.buildAssemblyDeps,
			LoopWakeDeps: m.buildLoopWakeDeps,
		})
		m.Stops.SetCoordinator(m.coordinatorRuntime)
		m.Guidance.Bind(m.coordinatorRuntime.Kicks(), m.coordinatorRuntime.Anchors())
		m.ToolPolicy.SetSurface(m.coordinatorRuntime)
		if m.boardBuilder != nil || m.boardFormatter != nil {
			m.coordinatorRuntime.SetBoardInject(m.boardBuilder, m.boardFormatter, func() bool {
				return m.coordinatorFrame != nil
			})
			m.coordinatorRuntime.SetPromotePathOverlay(m.Promotion.PromotePathBoardLines)
			m.coordinatorRuntime.SetOverlayMergePlan(m.OverlayMergePlanFn())
			m.coordinatorRuntime.SetActiveReservations(m.Workers.Workspaces.ReservationEntries)
			m.coordinatorRuntime.Board().SetWorkerRoots(m.Workers.Workspaces.BoardRoots)
			if m.includeScanLegend != nil {
				m.coordinatorRuntime.SetIncludeScanLegend(m.includeScanLegend)
			}
		}
	})
	return m.coordinatorRuntime
}

func (m *Manager) SetCoordinatorRuntime(rt *coordinator.Runtime) {
	m.coordinatorRuntime = rt
	m.Processes.SetLoop(rt.CoordinatorLoop())
	m.Admission.SetLoop(rt.CoordinatorLoop())
	m.ProjectControl.SetAnchors(rt.Anchors())
	if rt != nil {
		m.Nudges.SetSurface(rt)
		m.Guards.SetSurface(rt)
		m.Batch.SetLoop(rt.CoordinatorLoop())
		m.Runner.Settlement.SetRuntime(rt)
		if m.Runner != nil {
			m.Runner.SetRuntime(rt)
		}
	}
	m.Stops.SetCoordinator(rt)
	if m.Guidance != nil && rt != nil {
		m.Guidance.Bind(rt.Kicks(), rt.Anchors())
		m.ToolPolicy.SetSurface(rt)
	}
	if rt == nil {
		return
	}
	if m.prompts != nil {
		rt.Kicks().SetPromptEngine(m.prompts)
	}
	if m.boardBuilder != nil || m.boardFormatter != nil {
		rt.SetBoardInject(m.boardBuilder, m.boardFormatter, func() bool {
			return m.coordinatorFrame != nil
		})
		rt.SetPromotePathOverlay(m.Promotion.PromotePathBoardLines)
		rt.SetOverlayMergePlan(m.OverlayMergePlanFn())
		rt.SetActiveReservations(m.Workers.Workspaces.ReservationEntries)
		rt.Board().SetWorkerRoots(m.Workers.Workspaces.BoardRoots)
		if m.includeScanLegend != nil {
			rt.SetIncludeScanLegend(m.includeScanLegend)
		}
	}
}

// OverlayMergePlanFn returns pending overlay merge plans.
func (m *Manager) OverlayMergePlanFn() func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
	return func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
		plan := overlayplan.Build(sessionID, tasks, func(sid, jobID string) (overlayplan.PreviewSnapshot, bool) {
			order, after, blocked, clean, conflict, ok := m.Promotion.OverlayPreviewSnapshot(sid, jobID)
			if !ok {
				return overlayplan.PreviewSnapshot{}, false
			}
			return overlayplan.PreviewSnapshot{
				PromoteOrder:  order,
				PromoteAfter:  after,
				BlockedBy:     blocked,
				CleanPaths:    clean,
				ConflictPaths: conflict,
			}, true
		})
		if plan.PendingCount == 0 {
			return nil
		}
		cp := plan
		return &cp
	}
}

func (m *Manager) CoordinatorRuntimeDeps() coordinator.RuntimeDeps {
	return coordinator.RuntimeDeps{
		LoopDeps:     m.buildPromptLoopDeps,
		AssemblyDeps: m.buildAssemblyDeps,
		LoopWakeDeps: m.buildLoopWakeDeps,
	}
}

// publishWaitLease exposes an armed sleep on the activity plane.
func (m *Manager) publishWaitLease(ctx context.Context, sessionID string, lease loopwake.WaitLease) {
	if m == nil || m.events == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.TrimSpace(lease.ActivityID) == "" {
		return
	}
	// Terminal events outlive the turn that armed them.
	ctx = context.WithoutCancel(ctx)
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	status := api.ActivityStatusDone
	if lease.Active {
		status = api.ActivityStatusActive
	}
	triggers := make([]string, 0, len(lease.Triggers))
	for _, trigger := range lease.Triggers {
		triggers = append(triggers, string(trigger))
	}
	m.events.PublishActivity(ctx, sessionProjectKey(sess), sessionID, api.ActivityEvent{
		ActivityID:   lease.ActivityID,
		SessionID:    sessionID,
		Kind:         api.ActivityKindAwaitingWake,
		Status:       status,
		StartedAt:    lease.StartedAt,
		WaitTriggers: triggers,
	})
}

func (m *Manager) isCoordinatorSessionForLoop(_ context.Context, sess *api.Session) bool {
	if sess == nil {
		return false
	}
	return surface.IsCoordinatorSession(sess)
}
