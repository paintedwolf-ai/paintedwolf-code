package session

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/promptresult"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/configlayout"
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
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
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
	}
}

func (m *Manager) SetProfileRuntimeRules(rules *toolpolicy.ProfileRuntimeRules) {
	if m != nil {
		m.profileRuntimeRules = rules
	}
}

func (m *Manager) SetMessageStorageRedactor(redact func(ctx context.Context, msg api.Message) (api.Message, bool)) {
	if m != nil {
		m.redactMessageForStorage = redact
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
	return assembly.ActiveWorkflowManifest{
		CoordinatorProfile: manifest.CoordinatorProfile,
		Sealed:             manifest.Sealed,
		ArchiveDir:         manifest.ArchiveDir,
	}, true
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
		Limits:                          m.effectiveLimits,
		DataDir:                         m.dataDir,
		CoordinatorSurfaceActivityLabel: surface.LoadCoordinatorSurfaceActivityLabel,
		SetPromptTurnSurface: func(sessionID, surfaceID string) {
			rt.Assembly().SetTurnSurfaceID(sessionID, surfaceID)
		},
		PromptTurnSurface: func(sessionID string) string {
			return rt.PromptTurnSurfaceID(sessionID)
		},
		ReconcileCoordinatorBatch: func(ctx context.Context, sessionID string) {
			m.reconcileCoordinatorBatchFromLedger(ctx, sessionID)
		},
		TakeUserSend:  m.takeQueuedSend,
		LLM:           m.llm,
		LLMService:    m.llmSvc,
		Cost:          m.cost,
		Events:        m.events,
		Tools:         m.tools,
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
				m.publishProjectUpdated(ctx, projectID)
			}
			return err
		},
		RootSessionID: func(ctx context.Context, sessionID string) string {
			return RootSessionID(ctx, m.store, sessionID)
		},
		Policy: m.buildToolpolicyEngine(),
		CoordinatorPostureRules: func(ctx context.Context, sess *api.Session) ([]string, error) {
			if sess == nil || sess.Posture == "" {
				return nil, nil
			}
			postures, err := m.effectivePostures(ctx, sess)
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
			_, count, _ := m.workspaceRootsForPrompt(ctx, sess)
			return count
		},
		OverlayRootPaths: func(ctx context.Context, sess *api.Session) []string {
			return m.overlayRootPaths(ctx, sess)
		},
		LoadedTools:  m.LoadedTools,
		ToolObserved: m.observeToolCall,
		LiveResources: func(sessionID string) toolcontract.ResourcePresence {
			presence := toolcontract.ResourcePresence{}
			if m != nil && m.bgRegistry != nil {
				presence.CommandJobs = m.bgRegistry.HasPipelineHandles(sessionID)
				presence.Terminals = m.bgRegistry.CountLivePTYs(sessionID) > 0
			}
			if m != nil && m.pageRegistry != nil {
				presence.Pages = m.pageRegistry.CountLive(sessionID) > 0
			}
			if m != nil {
				presence.HeldCalls = m.heldCalls.HasHandles(sessionID)
			}
			return presence
		},
		HeldCalls:  heldCallPort{m: m},
		DoomLoop:   m.doomLoop,
		RejectFmt:  m.rejectFmt,
		BlockPlane: &tools.BlockPlane{Pipeline: m.oarPipeline, Renderer: m.oarRenderer},
		HintConfig: m.workflowHints,
		FormatDoomLoopReject: func(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
			return m.formatDoomLoopReject(ctx, sessionID, tool, args, count, repeatedCode)
		},
		EscalateRepeatedCode: func(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
			total := m.CodeRejectResponses(sessionID, tool, original.Code())
			if total < loopguard.DoomLoopMaxCodeRepeats {
				return nil
			}
			return m.escalateRepeatedCode(ctx, sessionID, tool, original, total)
		},
		EvaluateContentAnchor: func(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
			reject, blocked, content, transformed, err := m.tryOARContentBlock(ctx, sess, anchor, segments, tool, args)
			if err != nil {
				return guidance.NewRefusal("", err.Error()), true, "", false
			}
			return reject, blocked, content, transformed
		},
		EvaluateCloseoutBlock: func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
			return m.evaluateOARCloseoutBlock(ctx, sess, gc)
		},
		BeforeToolRun:      m.beforePromptLoopToolRun,
		TakePolicyFeedback: m.takePolicyFeedback,
		TakePhaseGuidance:  m.takePhaseGuidance,
		AfterToolRun: func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, succeeded bool, out *tools.ToolInvocationOut) string {
			return m.afterPromptLoopToolRun(ctx, sess, tool, args, output, succeeded, out)
		},
		EnrichToolOutput:        m.enrichPromptLoopToolOutput,
		BuildMessages:           m.buildCompletionMessages,
		ConfigRoot:              configlayout.FindModuleRoot(),
		WebSearchEnabled:        m.webSearchEnabled,
		CoordinatorFrame:        m.coordinatorFrame,
		ImplementSessionState:   m.BuildImplementSessionState,
		RedactMessageForStorage: m.redactMessageForStorage,
		AppendMessages:          m.appendMessages,
		Streams:                 m.Streams(),
	}
	if m.prompts != nil {
		deps.ToolProcedures = m.renderToolProcedures
	}
	m.bindPromptLoopRuntimeDeps(&deps)
	m.bindPromptLoopWorkerDeps(&deps)
	m.bindPromptLoopCompactionDeps(&deps)
	return deps
}

func (m *Manager) bindPromptLoopRuntimeDeps(deps *promptloop.PromptLoopDeps) {
	deps.CheckSpendCeiling = func(ctx context.Context, sessionID string, sess *api.Session) (promptloop.SpendCeilingCheck, error) {
		st, err := m.spendCeilingState(ctx, sessionID, sess)
		if err != nil {
			slog.WarnContext(ctx, "check spend ceiling", "session_id", sessionID, "error", err)
			return promptloop.SpendCeilingCheck{}, nil
		}
		if st.Reached {
			return promptloop.SpendCeilingCheck{SoftStop: st.SoftStop}, st.reached()
		}
		return promptloop.SpendCeilingCheck{
			Runway: promptloop.SpendRunway{Low: st.Low, CeilingUSD: st.CeilingUSD},
		}, nil
	}
	deps.IsSpendCeiling = func(err error) bool { return errors.Is(err, ErrSessionSpendCeiling) }
	deps.AssertRunnable = m.assertWorkflowRunnable
	deps.RefreshToolContext = func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error) {
		profileID := strings.TrimSpace(machine.ProfileID)
		if profileID == "" {
			var err error
			profileID, err = m.promptToolProfile(ctx, sess)
			if err != nil {
				return tools.ToolContext{}, err
			}
		}
		tctx, err := m.buildToolContext(ctx, sess, profileID, machine)
		if err != nil {
			return tools.ToolContext{}, err
		}
		return m.EnrichWorkerToolContext(ctx, sess, tctx)
	}
	deps.TurnCloseoutNudge = m.turnCloseoutNudge
	deps.SecretWithheldNudge = m.secretWithheldNudge
	deps.IterationRunwayNudge = m.iterationRunwayNudge
	deps.WorkerBudgetRaisedNudge = m.workerBudgetAnswerNudge(anchor.WorkerBudgetRaised)
	deps.WorkerBudgetDeclinedNudge = m.workerBudgetAnswerNudge(anchor.WorkerBudgetDeclined)
	deps.WorkerBudgetAnswerWait = spawn.WorkerBudgetAnswerWait
	deps.SurveyStreakNudge = m.surveyStreakNudge
	deps.SpendRunwayNudge = m.spendRunwayNudge
	deps.SpendSoftStopNudge = m.spendSoftStopNudge
	deps.WorkerGracefulCancelPending = m.WorkerGracefulCancelPending
	deps.OnToolReject = func(ctx context.Context, sessionID, toolCallID, code, content string, facts guidance.ToolResultFacts) {
		if m != nil && m.planToolStash != nil {
			m.planToolStash.Put(sessionID, toolCallID, code, content)
		}
		if m != nil {
			m.recordPeerToolReject(ctx, sessionID, code, content, facts)
		}
	}
	deps.AnnouncePendingToolAsk = func(ctx context.Context, sessionID string) {
		if m != nil && m.workflows != nil {
			m.workflows.AnnouncePendingAsk(ctx, sessionID)
		}
	}
	deps.HasActiveWorkflow = m.hasActiveWorkflowRun
	deps.HumanApprovalAwaiting = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.loopWorkflowSource == nil {
			return false
		}
		awaiting, err := m.loopWorkflowSource.HumanApprovalAwaiting(ctx, sessionID)
		return err == nil && awaiting
	}
	deps.HostObligationHeld = func(ctx context.Context, sessionID string) bool {
		if m == nil || m.loopWorkflowSource == nil {
			return false
		}
		held, err := m.loopWorkflowSource.HostObligationHeld(ctx, sessionID)
		return err == nil && held
	}
	deps.ParkBlockedLiveCommands = m.parkBlockedLiveCommands
	deps.BeforeFinishNoToolTurn = m.beforePromptLoopFinish
	deps.OnGroundedSynthesisAccepted = func(ctx context.Context, _ *api.Session, sessionID string) {
		m.acceptCoordinatorGroundedSynthesis(ctx, sessionID)
	}
	deps.ProseCitationGrounding = func(ctx context.Context, sess *api.Session, history []api.Message, _ string, prose, surfaceID string) *api.CitationGrounding {
		if m == nil || sess == nil || sess.IsWorkerChild() {
			return nil
		}
		roots, err := m.sessionCitationRoots(ctx, sess)
		if err != nil {
			return nil
		}
		grounding, err := guard.BuildCoordinatorProseCitationGrounding(ctx, m.CloseoutEvidence(), sess, history, prose, surfaceID, roots)
		if err != nil {
			return nil
		}
		return grounding
	}
	deps.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		if m == nil || sess == nil {
			return "", content, nil
		}
		projectDir, err := m.sessionActiveRootPath(ctx, sess)
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
	deps.RecordSourceRunEvidence = m.recordSourceRunEvidence
	deps.ConfirmVerifyResult = m.confirmVerifyResult
	deps.PublishWorkerProgress = m.publishWorkerProgress
	deps.WorkerJob = m.workerJob
	deps.UpdateMessage = func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
		if m == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.updateMessage(ctx, sessionID, messageID, msg)
	}
	deps.ProjectLiveModelOutput = func(ctx context.Context, out store.LiveModelOutput) error {
		if m == nil || m.store == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.store.ProjectLiveModelOutput(ctx, out)
	}
	deps.AdmitModelResponse = m.store.AdmitModelResponse
	deps.SettleModelOutput = func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error) {
		if m == nil || m.store == nil {
			return store.ModelOutput{}, fmt.Errorf("session store not configured")
		}
		return m.store.SettleModelOutput(ctx, out)
	}
	deps.MarkModelOutputProjected = func(ctx context.Context, outputID string) error {
		if m == nil || m.store == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.store.MarkModelOutputProjected(ctx, outputID)
	}
	deps.CheckpointTurn = func(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error {
		if m == nil || m.store == nil {
			return fmt.Errorf("session store not configured")
		}
		return m.store.CheckpointTurn(ctx, turnID, attemptID, phase, checkpointJSON)
	}
	deps.AppendDraftVersion = func(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.store.AppendDraftVersion(ctx, sessionID, slotID, body, outcomeCode)
	}
	deps.CountDraftVersions = func(ctx context.Context, sessionID, slotID string) (int, error) {
		if m == nil {
			return 0, fmt.Errorf("session store not configured")
		}
		return m.store.CountDraftVersions(ctx, sessionID, slotID)
	}
	deps.InFlightWorkerRosterNote = func(ctx context.Context, sess *api.Session) string {
		if m == nil || m.workerQueue == nil || sess == nil {
			return ""
		}
		active, err := ParentSessionInFlightWorkers(ctx, m.workerQueue, sess.ProjectID, sess.ID)
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
	deps.CompactOversizedToolResults = func(ctx context.Context, sessionID string, sess *api.Session) error {
		if m == nil {
			return nil
		}
		return m.compactOversizedToolResultsInSession(ctx, sess)
	}
	deps.CompactToolWire = func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta) {
		if m == nil {
			return content, nil
		}
		return m.prepareToolWireContent(ctx, sess, toolName, content, opts)
	}
	deps.CompactionConfig = m.liveCompactionConfigFor
	deps.MCPAlwaysLoad = func(ctx context.Context, sess *api.Session) map[string]bool {
		if m.mcpRuntime == nil {
			return nil
		}
		return m.mcpRuntime.ToolLoadingModes(ctx, m.overlayProjectDir(ctx, sess))
	}
	deps.RecordCompactionTokenObservation = func(sessionID string, reportedPromptTokens, transcriptEstimate int) {
		if m != nil {
			m.RecordCompactionTokenObservation(sessionID, reportedPromptTokens, transcriptEstimate)
		}
	}
	deps.CompactionTokenCalibration = func(sessionID string) compaction.PromptTokenCalibration {
		if m == nil {
			return compaction.PromptTokenCalibration{}
		}
		return m.CompactionTokenCalibration(sessionID)
	}
	deps.ReloadHistory = func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
		if m == nil {
			return nil, fmt.Errorf("session store not configured")
		}
		return m.reloadAssembledHistory(ctx, sessionID, sess, surfaceID)
	}
	deps.EvidenceLedger = m.CloseoutEvidence()
	deps.MaxCloseoutCitationGroundingRetries = limits.DefaultCitationGroundingRetries
	deps.RenderHostKick = m.renderWorkerKick
	deps.BeginCloseoutIntent = m.beginCloseoutIntent
	deps.RecordGroundingFriction = m.RecordGroundingFriction
	deps.NoteCloseoutGroundingReject = m.NoteCloseoutGroundingReject
	deps.NoteCoordinatorToolTurn = m.NoteCoordinatorToolTurn
	deps.CloseoutStallState = m.CloseoutStallState
	deps.ClearCloseoutStall = m.ClearCloseoutStall
	if m.reportDocuments != nil {
		deps.CheckRunReportDocument = m.reportDocuments.CheckRunReportDocument
	}
	deps.AssembleLedgerCloseout = func(ctx context.Context, sessionID, surfaceID string, forcedBy []string, draftedContent string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return m.assembleLedgerCloseout(ctx, sessionID, surfaceID, forcedBy, draftedContent, retryCount)
	}
	deps.CitationRoots = func(ctx context.Context, sess *api.Session) evidence.CitationRoots {
		if m == nil || sess == nil {
			return evidence.CitationRoots{}
		}
		roots, err := m.sessionCitationRoots(ctx, sess)
		if err != nil {
			return evidence.CitationRoots{}
		}
		return roots
	}
}

func (m *Manager) beforePromptLoopToolRun(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	_ string,
	tool string,
	args map[string]any,
) (string, bool, error) {
	if reject, blocked, err := m.tryOARBlock(ctx, oar.AnchorSessionPreInvoke, sess, tool, args, nil); blocked || err != nil {
		if reject != nil {
			return "", false, reject
		}
		return "", blocked, err
	}
	implState := m.BuildImplementSessionState(ctx, sess)
	surfaceID := m.ensureCoordinatorRuntime().PromptTurnSurfaceID(sess.ID)
	root := RootSessionID(ctx, m.store, sess.ID)
	currentProgress := ""
	if m.progress != nil {
		currentProgress = m.progress.Get(ctx, root)
	}
	reviewLoopActive := m.workflows != nil && m.workflows.ActivePhaseHasReviewLoop(ctx, sess.ID)
	closureBaseline, closureArmed := m.progressClosureLatch(root)
	guardDeps := m.workerCycleGuardDeps()
	declaredVerify := ""
	if tool == "verify" && m.verifyConfig != nil {
		declaredVerify = m.verifyConfig.VerifyTestCommand(m.overlayProjectDir(ctx, sess))
		guard.PrepareVerifyCall(tool, declaredVerify, args)
	}
	if reject, skip, err := m.tryOARBlock(ctx, oar.AnchorCoordinatorPreInvoke, sess, tool, args, func(gc *oar.GuardContext) error {
		guard.ObserveTaskWhilePendingUserInput(sess, implState, tool, gc)
		guard.ObserveCoordinatorWorkerBranchPath(sess, tool, args, gc)
		guard.ObserveProgressMissingBeforeDispatch(sess, currentProgress, tool, reviewLoopActive, gc)
		guard.ObserveProgressItemNotClosedBeforeDispatch(sess, currentProgress, tool, closureBaseline, closureArmed, gc)
		guard.ObserveCoordinatorBatchPhaseTool(sess, tool, implState, m.coordinatorBatchTurnGuard(sess.ID), gc)
		guard.ObserveCoordinatorSynthesisWrapupTool(surfaceID, tool, tools.ToolOffered(ctx, tool), gc)
		guard.ObserveVerifyCommandUndeclared(tool, declaredVerify, args, gc)
		guard.ObserveProgressReconcileOnSynthesis(surfaceID, currentProgress, args, gc)
		return ObserveCoordinatorTaskInFlight(ctx, guardDeps, sess, tool, args, gc)
	}); skip || err != nil {
		if reject != nil {
			return "", false, reject
		}
		return "", skip, err
	}
	if closureArmed {
		m.clearProgressClosureIfSatisfied(root, currentProgress, closureBaseline)
	}
	return "", false, nil
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
			m.maybeAdvanceCoordinatorBatchOnTaskEnqueued(ctx, sess.ID, agentType)
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
	if m.oarPipeline != nil && m.oarPipeline.AnchorEnforced(oar.AnchorToolPost) {
		var postFacts guidance.ToolResultFacts
		output, postFacts = m.appendPostToolGuidance(
			ctx, sess, tool, args, output, doomCompletionCountAfter, raised,
		)
		facts = facts.Merge(postFacts)
	}
	if !facts.Succeeded() || m.toolOutputEnricher == nil {
		return output, facts
	}
	eval := toolpolicy.BuildEvalContext(ctx, m.toolpolicyEngineDeps(), sess, tool, args)
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
			wfEval = workflowEvaluationFromFrame(frame)
		} else {
			slog.ErrorContext(ctx, "coordinator turn frame unavailable; tool-output gate feedback degrades to empty workflow context",
				"session_id", sess.ID, "tool", tool, "err", err)
		}
	}
	batchPhase := m.BuildImplementSessionState(ctx, sess).BatchPhase
	enriched := m.toolOutputEnricher.Enrich(ctx, guidance.EnrichInput{
		SessionID: sess.ID, Session: sess, Tool: tool, Args: hostArgs, Output: output,
		PlanProgress: eval.PlanProgress, PlanContent: eval.PlanContent, BlueprintPath: eval.BlueprintPath,
		Workflow: wfEval, BatchPhase: batchPhase, Facts: facts,
	})
	merged := facts.Merge(enriched.Facts)
	if merged.HasCode("WORKFLOW_GATE_BLOCKED") {
		m.Emit(ctx, sess.ID, anchor.GateBlocked, anchor.Envelope{})
		m.NudgeCoordinatorLoop(ctx, sess.ID, anchor.PhaseAdvanced, anchor.GateBlocked, "", anchor.Envelope{})
	}
	return enriched.Output, merged
}

func (m *Manager) beforePromptLoopFinish(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	_ string,
	lastAssistant string,
	surfaceID string,
	workersIdle bool,
	turnTools []string,
	invokeAllowed bool,
) (reject *guidance.Refusal, blocked bool) {
	if m == nil || sess == nil {
		return nil, false
	}
	if sess.IsWorkerChild() && m.workerDecisionPending(ctx, sess.ID) {
		return nil, false
	}
	defer func() {
		if !blocked {
			reject, blocked = m.evaluateAgentPostTurn(ctx, sess, lastAssistant, workersIdle)
		}
	}()
	if sess.IsWorkerChild() {
		projectDir, _ := m.sessionActiveRootPath(ctx, sess)
		return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
			workercompletion.ObserveImplementerFinishWithoutWrite(
				ctx, sess, history, lastAssistant, projectDir, m.workspaceCheck, gc,
			)
			return nil
		})
	}
	implState := m.BuildImplementSessionState(ctx, sess)
	batchTurn := m.coordinatorBatchTurnGuard(sess.ID)
	if reject, block := m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		guard.ObserveCoordinatorHostNoToolTurn(
			sess, history, lastAssistant, turnTools, surfaceID, workersIdle, implState, batchTurn, gc,
		)
		return nil
	}); block {
		return reject, true
	}
	if reject, block := m.maybeRejectCloseoutForMissingVerdict(ctx, sess, workersIdle, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.maybeRejectCloseoutBeforeReportPhase(ctx, sess, lastAssistant, surfaceID, invokeAllowed); block {
		return reject, true
	}
	if reject, block := m.maybeRejectCloseoutForSourceEvidence(ctx, sess, history, surfaceID, invokeAllowed); block {
		return reject, true
	}
	assessment := closeoutVerification(history)
	if !assessment.Valid() || assessment.Method != "blocked" {
		if reject, block := m.maybeRejectCloseoutForOpenGates(ctx, sess, workersIdle, invokeAllowed); block {
			return reject, true
		}
	}
	return m.maybeRejectCloseoutForOpenProgress(
		ctx, sess, history, surfaceID, workersIdle, implState, invokeAllowed,
	)
}

func (m *Manager) liveCompactionConfigFor(ctx context.Context, sess *api.Session) compaction.CompactionConfig {
	cfg, _ := m.liveBudgetResolveForRoots(m.overlayRootPaths(ctx, sess))
	return cfg
}

// liveBudgetResolveForRoots applies the active model policy.
func (m *Manager) liveBudgetResolveForRoots(projectDirs []string) (compaction.CompactionConfig, llm.SessionLimitFields) {
	base := compaction.DefaultCompactionConfig()
	windows := modelinfo.DefaultModelContextWindows()
	var policy llm.ModelPolicy
	var lookup llm.ContextLengthLookup
	if m != nil && m.llmSvc != nil {
		if m.llmSvc.Policy != nil {
			var err error
			policy, err = m.llmSvc.Policy.GetForProjectRoots(projectDirs)
			if err != nil {
				fallback := llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow)
				if m.compactor != nil {
					return m.compactor.Config(), fallback
				}
				return base, fallback
			}
		}
		if m.llmSvc.Registry != nil {
			lookup = m.llmSvc.Registry
		}
	}
	cfg, limits, err := llm.ApplyLiveBudget(base, policy, lookup, windows)
	if err != nil {
		fallback := llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow)
		if m != nil && m.compactor != nil {
			return m.compactor.Config(), fallback
		}
		return base, fallback
	}
	return cfg, limits
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
		Limits:                m.effectiveLimits,
		Agents:                m.agents,
		Workflows:             sessionWorkflowManifest{m: m},
		CoordinatorFrame:      m.coordinatorFrame,
		WorkerContext:         workerCtx,
		SiblingNoteDelivery:   m,
		PeerReservations:      m,
		ImplementSessionState: m.BuildImplementSessionState,
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
			return sessionWorkspaceKnownEmpty(ctx, m.repoProvider, workspacePath)
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
		LoadedTools:             m.LoadedTools,
		OmittedUnits:            m.OmittedUnits,
		SkillPreload:            m.SkillPreload,
		SkillPointer:            m.SkillPointer,
		WorkspaceRoots:          m.workspaceRootsForPrompt,
		ProjectOverlayRootPaths: m.promptOverlayRootPaths,
		SessionView:             m.Catalog().ViewForSession,
		AgentsMDIndex:           m.agentsMDIndexInject,
		AgentsMDChain:           m.agentsMDChainInject,
		WebSearchEnabled:        m.webSearchEnabled,
		SynthesisEvidence:       m,
		CommandJobs: func(sessionID string) []bgprocess.JobSnapshot {
			if m == nil || m.bgRegistry == nil {
				return nil
			}
			return m.bgRegistry.ActiveJobs(sessionID)
		},
		HeldCalls: func(sessionID string) []heldcall.Running {
			if m == nil {
				return nil
			}
			return m.heldCalls.Ledger(sessionID)
		},
		TurnSourceBriefs:       m.turnSourceBriefs,
		ModelVision:            m.SessionModelVision,
		EffectivePromptSurface: m.agentPromptSurface,
	}
}

func (m *Manager) agentPromptSurface(ctx context.Context, sess *api.Session) prompts.AgentPromptSurface {
	if m == nil || sess == nil {
		return prompts.AgentPromptSurface{}
	}
	profileID, _ := m.promptToolProfile(ctx, sess)
	return m.CompileMachine(ctx, sess, profileID).Surface
}

// SessionModelVision reports whether the session's active coordinator model declares vision.
func (m *Manager) SessionModelVision(ctx context.Context, sess *api.Session) bool {
	if m == nil || m.llmSvc == nil || m.llmSvc.Registry == nil || m.llmSvc.Router == nil || sess == nil {
		return false
	}
	router := m.llmSvc.Router.WithOverlayRoots(m.overlayRootPaths(ctx, sess))
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
	policy := m.PromptToolPolicy()
	if policy == nil {
		return nil, nil
	}
	metas := policy.ListForPrompt(ctx, sess, profileID)
	machine := m.CompileMachine(ctx, sess, profileID)
	return tools.HideSkillsReadWhenEmpty(metas, machine.SkillCount), nil
}

func (m *Manager) buildLoopWakeDeps() loopwake.LoopDeps {
	return loopwake.LoopDeps{
		RunPrompt: func(ctx context.Context, sessionID string) (*promptresult.Result, error) {
			return m.promptHostLoopWake(ctx, sessionID)
		},
		RunWaitResume:   m.promptWaitResume,
		HostTurnBlocked: m.hostTurnBlocked,
		GetSession: func(ctx context.Context, sessionID string) (*api.Session, error) {
			return m.store.Get(ctx, sessionID)
		},
		Limits:           m.effectiveLimits,
		IsEscalated:      m.groundingEscalated,
		WorkflowSource:   m.loopWorkflowSource,
		CoordinatorFrame: m.coordinatorFrame,
		BoardWillForceInject: func(ctx context.Context, sess *api.Session, run api.CoordinatorRunContext) bool {
			return m.ensureCoordinatorRuntime().Board().BoardWillForceInject(ctx, sess, run)
		},
		QueueInform: func(ctx context.Context, sessionID string, inform anchor.ID, env anchor.Envelope) {
			m.Emit(ctx, sessionID, inform, env)
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
			if err := m.settleDeferredUserTurn(ctx, sessionID); err != nil {
				slog.ErrorContext(ctx, "settle deferred user turn", "session_id", sessionID, "error", err)
			}
		},
		IsCoordinatorSession:    m.isCoordinatorSessionForLoop,
		WorkflowObligationsOpen: m.workflowObligationsOpen,
		WorkerCycleIdle: func(ctx context.Context, sess *api.Session, completingJobID string) (bool, error) {
			if m == nil || sess == nil {
				return true, nil
			}
			return ParentSessionWorkerCycleIdle(ctx, m.workerQueue, sess.ProjectID, sess.ID, completingJobID)
		},
		HostWakeOverlayPromoteDue: func(ctx context.Context, sessionID string) bool {
			if m == nil {
				return false
			}
			sess, err := m.store.Get(ctx, sessionID)
			if err != nil || sess == nil {
				return false
			}
			state := m.BuildImplementSessionState(ctx, sess)
			return len(state.PendingOverlayIDs) > 0
		},
		ScanCycleOpen: func(ctx context.Context, sessionID string) bool {
			return m != nil && m.scanWaits.InFlight != nil && m.scanWaits.InFlight(ctx, sessionID)
		},
		ProcessRunning:   m.processHandlesRunning,
		ProcessState:     m.processHandleState,
		ProcessReport:    m.processReport,
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
				return m.BuildImplementSessionState(ctx, sess)
			},
			ActiveRun: func(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
				if m == nil || m.workflows == nil {
					return nil, nil
				}
				return m.workflows.GetActive(ctx, sessionID)
			},
			WorkflowObligationsOpen: m.workflowObligationsOpen,
		}),
	}
}

func (m *Manager) groundingEscalated(sessionID string) bool {
	if m == nil || m.grounding == nil {
		return false
	}
	return m.grounding.IsEscalated(sessionID)
}

func (m *Manager) buildToolpolicyEngine() toolpolicy.Engine {
	if m == nil {
		return toolpolicy.NewEngine(toolpolicy.EngineDeps{})
	}
	return toolpolicy.NewEngine(m.toolpolicyEngineDeps())
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
		if m.boardBuilder != nil || m.boardFormatter != nil {
			m.coordinatorRuntime.SetBoardInject(m.boardBuilder, m.boardFormatter, func() bool {
				return m.coordinatorFrame != nil
			})
			m.coordinatorRuntime.SetPromotePathOverlay(m.PromotePathBoardLines)
			m.coordinatorRuntime.SetOverlayMergePlan(m.OverlayMergePlanFn())
			m.coordinatorRuntime.SetActiveReservations(m.ActiveReservationBoardEntries)
			m.coordinatorRuntime.Board().SetWorkerRoots(m.workerBoardRoots)
			if m.includeScanLegend != nil {
				m.coordinatorRuntime.SetIncludeScanLegend(m.includeScanLegend)
			}
		}
	})
	return m.coordinatorRuntime
}

func (m *Manager) SetCoordinatorRuntime(rt *coordinator.Runtime) {
	m.coordinatorRuntime = rt
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
		rt.SetPromotePathOverlay(m.PromotePathBoardLines)
		rt.SetOverlayMergePlan(m.OverlayMergePlanFn())
		rt.SetActiveReservations(m.ActiveReservationBoardEntries)
		rt.Board().SetWorkerRoots(m.workerBoardRoots)
		if m.includeScanLegend != nil {
			rt.SetIncludeScanLegend(m.includeScanLegend)
		}
	}
}

// OverlayMergePlanFn returns pending overlay merge plans.
func (m *Manager) OverlayMergePlanFn() func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
	return func(sessionID string, tasks []api.WorkerTask) *api.OverlayMergePlan {
		plan := overlayplan.Build(sessionID, tasks, func(sid, jobID string) (overlayplan.PreviewSnapshot, bool) {
			order, after, blocked, clean, conflict, ok := m.OverlayPreviewSnapshot(sid, jobID)
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

func (m *Manager) PromptLoopForTest() *promptloop.PromptLoop {
	return m.ensureCoordinatorRuntime().PromptLoop()
}

func (m *Manager) PromptToolPolicy() toolpolicy.Engine {
	return m.buildToolpolicyEngine()
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
