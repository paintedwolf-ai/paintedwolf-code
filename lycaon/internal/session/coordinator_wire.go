package session

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/tools"
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
	policy WorkflowPolicy
	fanout WorkflowFanout
}

func (w sessionWorkflowManifest) ActiveManifest(ctx context.Context, sessionID string) (assembly.ActiveWorkflowManifest, bool) {
	if w.policy == nil {
		return assembly.ActiveWorkflowManifest{}, false
	}
	manifest, ok := w.policy.ActiveManifest(ctx, sessionID)
	if !ok {
		return assembly.ActiveWorkflowManifest{}, false
	}
	return assembly.ActiveWorkflowManifest{CoordinatorProfile: manifest.CoordinatorProfile}, true
}

func (w sessionWorkflowManifest) RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error {
	if w.fanout == nil {
		return nil
	}
	return w.fanout.RecordBoardOrientReady(ctx, sessionID, injectKey)
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
	var policy promptsource.WorkflowRunnable
	var asks promptsource.WorkflowAsks
	var approvals promptsource.WorkflowApprovals
	var obligations promptsource.WorkflowObligations
	if m.workflows != nil {
		policy = m.workflows.Policy
		asks = m.workflows.Asks
	}
	if m.loopWorkflowSource != nil {
		approvals = m.loopWorkflowSource.Approvals
		obligations = m.loopWorkflowSource.Obligations
	}
	return promptloop.PromptLoopDeps{
		Context:    (&promptsource.Context{Frame: m.coordinatorFrame, Guards: m.Guards, Limits: m.Limits, Loading: m.Loading, Pages: m.pageRegistry, Policy: m.ToolPolicy, Processes: m.Processes, Profiles: m.Profiles, Prompts: m.prompts, Runtime: m.ensureCoordinatorRuntime(), Sessions: m.store, ToolContext: m.ToolContext, Tools: m.tools, WebResearch: m.webResearchConfig, WorkerState: m.Workers.State, Workspace: m.Workspace, Workspaces: m.Workers.Workspaces}).Build(),
		Tools:      (&promptsource.Tools{Batch: m.Batch, DataDir: m.dataDir, Enricher: m.toolOutputEnricher, Feedback: m.Feedback, Frame: m.coordinatorFrame, Guards: m.Guards, Guidance: m.Guidance, History: m.Runner.History, Invocations: m.invocations, Naming: m.Naming, Policy: m.ToolPolicy, Presence: m.agentPresence, Processes: m.Processes, Projects: m.projects, Runtime: m.ensureCoordinatorRuntime(), Sessions: m.store, Verification: m.Verification, Visual: m.visual, WorkerState: m.Workers.State, Workers: m.workerQueue, Workspace: m.Workspace}).Build(),
		Control:    (&promptsource.Control{Batch: m.Batch, GracefulCancel: m.Workers.Cancel, Approvals: approvals, Obligations: obligations, Processes: m.Processes, Runtime: m.ensureCoordinatorRuntime(), Workflow: policy}).Build(),
		Inbox:      (&promptsource.Inbox{Guidance: m.Guidance, Submissions: m.Submissions}).Build(),
		Model:      (&promptsource.Model{Cost: m.cost, History: m.Runner.History, LLM: m.llm, LLMService: m.llmSvc, Limits: m.Limits}).Build(),
		Projection: (&promptsource.Projection{Events: m.events, Rejections: m.Workers.Rejections, Sessions: m.store, Stash: m.planToolStash, Transcript: m.Transcript, Workflow: asks}).Build(),
		Nudges:     (&promptsource.Nudges{DoomLoop: m.doomLoop, Nudges: m.Nudges, Policy: m.ToolPolicy, Spend: m.Runner.Spend, Workers: m.workerQueue}).Build(),
		Closeout:   (&promptsource.Closeout{Batch: m.Batch, Closeout: m.Closeout, Closeouts: m.Runner.Closeouts, Evidence: m.Verification.Evidence, Guards: m.Guards, Hints: m.workflowHints, Nudges: m.Nudges, Policy: m.ToolPolicy, Rejects: m.rejectFmt, RenderKick: m.Workers.RenderKick, Reports: m.reportDocuments}).Build(),
	}
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
		Workflows:             m.workflowManifestSource(),
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
		BoardOrientReady: m.workflowManifestSource(),
		EnrichHistory: func(sessionID string, history []api.Message) []api.Message {
			if m == nil || m.planToolStash == nil {
				return history
			}
			return toolpresentation.EnrichHistory(sessionID, history, m.planToolStash)
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
				return m.workflows.Runs.ActiveBySession(ctx, sessionID)
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

func (m *Manager) workflowManifestSource() sessionWorkflowManifest {
	if m.workflows == nil {
		return sessionWorkflowManifest{}
	}
	return sessionWorkflowManifest{policy: m.workflows.Policy, fanout: m.workflows.Fanout}
}
