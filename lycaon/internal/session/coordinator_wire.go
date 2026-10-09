package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/overlayplan"
	"github.com/lycaon/lycaon/internal/session/promptsource"
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

func (m *Manager) SetWebResearchConfig(cfg *webresearch.ConfigStore) {
	if m == nil {
		return
	}
	m.webResearchConfig = cfg
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
	m.Resources.Work.Coordinator = rt
	m.RewindRuntime.Coordinator = rt
	if m.Coordinator != nil {
		m.Coordinator.Runtime = rt
		m.Coordinator.Workers.Runtime = rt
		m.Coordinator.Scans.Runtime = rt
	}
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

func (m *Manager) buildAssemblyDeps() assembly.AssemblyDeps {
	var manifests assembly.WorkflowManifestSource
	var orientation assembly.BoardOrientReadyRecorder
	if m.workflows != nil {
		manifests = m.workflows.Policy
		orientation = m.workflows.Fanout
	}
	return (&promptsource.Assembly{Briefs: m.SourceBriefs, Catalog: &m.Catalog, Closeout: m.Closeout, Feedback: m.gateFeedback, Frame: m.coordinatorFrame, Guards: m.Guards, Hints: m.workflowHints, Limits: m.Limits, Loading: m.Loading, Model: &promptsource.Model{LLMService: m.llmSvc, Workspace: m.Workspace}, Notes: m.Workers.Notes, PolicyIndex: m.PolicyIndex, Processes: m.Processes, Profiles: m.Profiles, Prompts: m.prompts, Repository: m.repoProvider, Runtime: m.ensureCoordinatorRuntime(), Scan: m.scanGuidance, Stash: m.planToolStash, State: m.Workers.State, WebResearch: m.webResearchConfig, WorkerContext: m.workerContext, Workspace: m.Workspace, Workspaces: m.Workers.Workspaces}).Build(manifests, orientation)
}

func (m *Manager) buildLoopWakeDeps() loopwake.LoopDeps {
	var active promptsource.ActiveRuns
	if m.workflows != nil {
		active = m.workflows.Runs
	}
	return (&promptsource.Loop{ActiveRuns: active, Admission: m.Admission, Events: m.events, Frame: m.coordinatorFrame, Grounding: m.grounding, Guidance: m.Guidance, Limits: m.Limits, Processes: m.Processes, Runtime: m.ensureCoordinatorRuntime(), ScanInFlight: m.Coordinator.Scans.Wait.InFlight, Sessions: m.store, Settlement: m.Runner.Settlement, State: m.Workers.State, Submissions: m.Submissions, Turns: m.Runner.Turns, Workers: m.workerQueue, Workflow: m.loopWorkflowSource}).Build()
}
