package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	sessionobservation "github.com/lycaon/lycaon/internal/session/observation"
	"github.com/lycaon/lycaon/internal/session/promptsource"
	"github.com/lycaon/lycaon/internal/session/toolpresentation"
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

func (m *Host) SetWebResearchConfig(cfg *webresearch.ConfigStore) {
	if m == nil {
		return
	}
	m.Coordinator.Context.WebResearch = cfg
	m.Coordinator.Assembly.WebResearch = cfg

}

func acquireCoordinatorSources(m *Host, store Store, client modelcall.LLMClient, svc *llm.Service, registry tools.ToolRegistry, tracker cost.CostTracker) {
	stash := toolpresentation.NewStash()
	scanWait := &m.Coordinator.Scans.Wait
	scanInFlight := func(ctx context.Context, id string) bool {
		return scanWait.InFlight != nil && scanWait.InFlight(ctx, id)
	}
	m.Coordinator.Context = &promptsource.Context{Frame: nil, Guards: m.Coordinator.Guards, Limits: m.Limits, Loading: m.Coordinator.Loading, Pages: nil, Policy: m.ToolPolicy, Processes: m.Processes, Profiles: m.Profiles, Prompts: nil, Runtime: nil, Sessions: store, ToolContext: m.ToolContext, Tools: registry, WebResearch: nil, WorkerState: m.Workers.State, Workspace: m.Workspace, Workspaces: m.Workers.Workspaces}
	m.Coordinator.Tools = &promptsource.Tools{Batch: m.Coordinator.Batch, DataDir: m.Workspace.DataDir, Enricher: nil, Feedback: m.Coordinator.Feedback, Frame: nil, Guards: m.Coordinator.Guards, Guidance: m.Coordinator.Guidance, History: m.Runner.History, Invocations: nil, Naming: m.Chats.Naming, Policy: m.ToolPolicy, Presence: nil, Processes: m.Processes, Projects: nil, Runtime: nil, Sessions: store, Verification: m.Verification, Visual: nil, WorkerState: m.Workers.State, Workers: nil, Workspace: m.Workspace}
	m.Coordinator.Control = &promptsource.Control{Batch: m.Coordinator.Batch, GracefulCancel: m.Workers.Cancel, Approvals: nil, Obligations: nil, Processes: m.Processes, Runtime: nil, Workflow: nil}
	m.Coordinator.Inbox = &promptsource.Inbox{Guidance: m.Coordinator.Guidance, Submissions: m.Submissions}
	m.Coordinator.Model = &promptsource.Model{Cost: tracker, History: m.Runner.History, LLM: client, LLMService: svc, Limits: m.Limits, Workspace: m.Workspace}
	m.Coordinator.Projection = &promptsource.Projection{Events: nil, Rejections: m.Workers.Rejections, Sessions: store, Stash: stash, Transcript: m.Runner.Transcript, Workflow: nil}
	m.Coordinator.Nudging = &promptsource.Nudges{DoomLoop: nil, Nudges: m.Coordinator.Nudges, Policy: m.ToolPolicy, Spend: m.Runner.Spend, Workers: nil}
	m.Coordinator.Completion = &promptsource.Closeout{Batch: m.Coordinator.Batch, Closeout: m.Coordinator.Closeout, Closeouts: m.Runner.Closeouts, Evidence: m.Verification.Evidence, Guards: m.Coordinator.Guards, Hints: nil, Nudges: m.Coordinator.Nudges, Policy: m.ToolPolicy, Rejects: nil, RenderKick: m.Workers.RenderKick, Reports: nil}
	m.Coordinator.Assembly = &promptsource.Assembly{Briefs: m.SourceBriefs, Catalog: &m.Catalog, Closeout: m.Coordinator.Closeout, Feedback: nil, Frame: nil, Guards: m.Coordinator.Guards, Hints: nil, Limits: m.Limits, Loading: m.Coordinator.Loading, Model: m.Coordinator.Model, Notes: m.Workers.Notes, PolicyIndex: m.Coordinator.PolicyIndex, Processes: m.Processes, Profiles: m.Profiles, Prompts: nil, Repository: nil, Runtime: nil, Scan: nil, Stash: stash, State: m.Workers.State, WebResearch: nil, WorkerContext: nil, Workspace: m.Workspace, Workspaces: m.Workers.Workspaces}
	m.Coordinator.Loop = &promptsource.Loop{ActiveRuns: nil, Admission: m.Admission, Events: nil, Frame: nil, Grounding: nil, Guidance: m.Coordinator.Guidance, Limits: m.Limits, Processes: m.Processes, Runtime: nil, ScanInFlight: scanInFlight, Sessions: store, Settlement: m.Runner.Settlement, State: m.Workers.State, Submissions: m.Submissions, Turns: m.Runner.Turns, Workers: nil, Workflow: nil}
}

// bindCoordinatorRuntime connects the acquired session peers to one coordinator loop.
func bindCoordinatorRuntime(m *Host, store Store) {
	runtime := coordinator.NewRuntime(m.Coordinator.RuntimeDependencies())
	m.Coordinator.Runtime = runtime
	m.Stops.SetCoordinator(runtime)
	m.Coordinator.Guidance.Bind(runtime.Kicks(), runtime.Anchors())
	m.ToolPolicy.SetSurface(runtime)
	m.Coordinator.Context.Runtime = runtime
	m.Coordinator.Tools.Runtime = runtime
	m.Coordinator.Control.Runtime = runtime
	m.Coordinator.Assembly.Runtime = runtime
	m.Coordinator.Loop.Runtime = runtime
	m.Resources.Work.Coordinator = runtime
	m.RewindRuntime.Coordinator = runtime
	m.Observations = sessionobservation.New(store, runtime.CoordinatorLoop().Admission, runtime.CoordinatorLoop().Nudges, m.Runner.Turns)
	m.Processes.SetLoop(runtime.CoordinatorLoop().Waits, runtime.CoordinatorLoop().Nudges)
	m.Admission.SetLoop(runtime.CoordinatorLoop().Nudges, runtime.CoordinatorLoop().Cycles)
	m.ProjectControl.SetAnchors(runtime.Anchors())
	m.Coordinator.Nudges.SetSurface(runtime)
	m.Coordinator.Guards.SetSurface(runtime)
	m.Coordinator.Batch.SetLoop(runtime.CoordinatorLoop().Waits)
	m.Runner.Settlement.SetRuntime(runtime)
	m.Runner.SetRuntime(runtime)

	m.Coordinator.Admission = m.Admission
	m.Coordinator.Runtime = runtime
	m.Coordinator.Workers = &coordinatorcontrol.Workers{Runtime: runtime, Batch: m.Coordinator.Batch, Settlement: m.Runner.Settlement, Admission: m.Admission, Digests: m.Workers.Digests, Results: m.Workers.Results}
	m.Coordinator.Scans.Runtime = runtime

	m.Coordinator.Profiles = m.Profiles
	m.Coordinator.ToolPolicy = m.ToolPolicy
}
