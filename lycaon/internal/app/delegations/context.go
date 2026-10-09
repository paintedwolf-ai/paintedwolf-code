package delegations

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (r *Runtime) configureDelegationWorkflow(deps Dependencies) error {
	deps.Sessions.Manager.SetWorkflowDomains(&session.WorkflowDomains{
		Runs: deps.Workflows.Store.Runs, Policy: deps.Workflows.Manager.Policy,
		Ambient: deps.Workflows.Manager.Ambient, Blueprints: deps.Workflows.Manager.Blueprints,
		Batch: deps.Workflows.Manager.Batch, Slash: deps.Workflows.Manager.Slash,
		Requests: deps.Workflows.Manager.Requests, Feedback: deps.Workflows.Manager.Feedback,
		Transcript: deps.Workflows.Manager.Transcript, Asks: deps.Workflows.Manager.Asks,
		Fanout: deps.Workflows.Manager.Fanout, Phases: deps.Workflows.Manager.Phases,
		Reports: deps.Workflows.Manager.Reports, Recovery: deps.Workflows.Manager.Recovery,
		Cleanup: deps.Workflows.Manager, Reviews: deps.Workflows.Manager.Repairs,
	})
	deps.Sessions.Manager.Profiles.SetWorkflowToolAccessView(deps.Workflows.Manager.Policy)
	deps.Sessions.Manager.Stops.SetWorkflowStop(deps.Workflows.Manager.Controls)
	deps.Workflows.Manager.Starts.Barrier = deps.Sessions.Manager.Chats.Gate
	deps.Workflows.Manager.Controls.SessionExit = deps.Sessions.Manager.Stops
	deps.Workflows.Manager.Requests.OnRequestAccepted = deps.Sessions.Manager.Runner.Curation.AcceptedWorkflowRequest
	if deps.Execution.Hints == nil {
		return fmt.Errorf("hint registry: not loaded")
	}
	gateCfg, gateErr := feedback.LoadGateFeedbackCatalog()
	if gateErr != nil {
		return fmt.Errorf("gate feedback: %w", gateErr)
	}
	deps.Sessions.Manager.SetWorkflowHints(deps.Execution.Hints, gateCfg)
	if evidenceBinding, err := evidence.LoadBinding(); err != nil {
		return fmt.Errorf("evidence binding: %w", err)
	} else {
		evidence.SetBinding(evidenceBinding)
	}
	deps.Sessions.Manager.SetWorkspaceChecker(&workercompletion.CompositeWorkspaceChangeChecker{
		Git: &workercompletion.GitWorkspaceChangeChecker{Git: deps.Git},
	})
	if err := deps.Sessions.Manager.Coordinator.Guidance.InstallAnchorRegistry(); err != nil {
		return fmt.Errorf("anchor registry: %w", err)
	}
	deps.Sessions.Manager.SetLoopWorkflowSource(&loopwake.WorkflowDomains{
		Runs: deps.Workflows.Store.Runs, Approvals: deps.Workflows.Manager.Policy, Obligations: deps.Workflows.Manager.Obligations,
	})

	deps.Workflows.Manager.Phases.PhaseEnterHook = r.OnWorkflowPhaseEnter
	deps.Workflows.Manager.Phases.PhaseReenterHook = r.OnWorkflowPhaseReenter
	deps.Workflows.Manager.Publication.OnPhaseAutoAdvanced = r.OnWorkflowPhaseAutoAdvanced
	deps.Workflows.Manager.Controls.OnRunResumed = r.OnWorkflowRunResumed
	deps.Workflows.Manager.Children.OnRunCompleted = r.OnWorkflowRunCompleted
	deps.Workflows.Manager.Approvals.OnHumanApprovalAdvanced = r.OnWorkflowHumanApprovalAdvanced
	deps.Workflows.Manager.Feedback.OnFeedbackPending = r.OnWorkflowFeedbackPending
	deps.Workflows.Manager.Asks.OnToolAskOpened = r.OnWorkflowToolAskOpened
	deps.Workflows.Manager.Feedback.OnFeedbackResolved = r.OnWorkflowFeedbackResolved
	deps.Workflows.Manager.Verdicts.OnReviewLoopHeld = r.OnWorkflowReviewLoopHeld
	r.Manager.OnCloseout = func(ctx context.Context, _, sessionID, workflowRunID string) {
		r.OnDelegationCloseout(ctx, sessionID, workflowRunID)
	}

	deps.Sessions.Manager.SetCoordinatorTurnFrameSource(&workflowruntime.CoordinatorFrames{
		Runs: deps.Workflows.Store.Runs, Resolver: &deps.Workflows.Manager.Resolver,
		Snapshots: deps.Workflows.Manager.Snapshots, Policy: deps.Workflows.Manager.Policy,
		Obligations:    deps.Workflows.Manager.Obligations,
		SessionStore:   deps.Workflows.Drafts,
		ConfigRoot:     deps.Catalog.ModuleRoot,
		VerdictCatalog: r.SessionVerdictCatalog,
	})
	if deps.Providers.Curator != nil {
		deps.Sessions.Manager.Coordinator.Closeout.SetSynthesisCurator(deps.Providers.Curator)
	}
	return nil
}

func (r *Runtime) wireWorkerContext(deps Dependencies) error {
	personaContract, err := prompts.LoadPersonaContract()
	if err != nil {
		return fmt.Errorf("persona contract: %w", err)
	}
	r.PlaybookMatcher, err = prompts.LoadPlaybookMatcherEffective(personaContract)
	if err != nil {
		return fmt.Errorf("playbooks: %w", err)
	}
	legToolLister := delegation.LegToolListerFunc(func(ctx context.Context, sess *wire.Session, profileID string) []string {
		policy := deps.Sessions.Manager.Coordinator.Guards.Policy()
		if policy == nil || sess == nil {
			return nil
		}
		var names []string
		for _, meta := range policy.ListForPrompt(ctx, sess, profileID) {
			if strings.TrimSpace(meta.Name) != "" {
				names = append(names, meta.Name)
			}
		}
		return names
	})
	agentsForSession := func(sess *wire.Session) profiles.AgentProfileResolver {
		if view := deps.Sessions.Manager.Catalog.ViewForSession(context.Background(), sess); view != nil {
			return view
		}
		return deps.Agents.Registry
	}
	playbooksForSession := func(sess *wire.Session) delegation.PlaybookMatcherInterface {
		if view := deps.Sessions.Manager.Catalog.ViewForSession(context.Background(), sess); view != nil && view.Playbooks != nil {
			return view.Playbooks
		}
		return r.PlaybookMatcher
	}
	r.ContextLoader = &delegation.WorkerContextLoader{
		Store:         r.Store,
		Tasks:         r.Queue,
		Runs:          deps.Workflows.Manager.Phases,
		MatcherFor:    playbooksForSession,
		AgentsFor:     agentsForSession,
		Tools:         legToolLister,
		Scans:         deps.Scanning.Coordinator,
		AgentsMDChain: deps.Sessions.Manager.Coordinator.PolicyIndex.Chain,
		Repo:          nil, // Set when repo provider is wired via SetRepoProvider
		Topology: func(workflowID string) string {
			if strings.TrimSpace(workflowID) == "default-pipeline" {
				return "pipeline"
			}
			return strings.TrimSpace(workflowID)
		},
	}
	deps.Sessions.Manager.SetWorkerContextBuilder(&delegation.CompositeWorkerContext{
		Delegation: r.ContextLoader,
		Tools:      legToolLister,
		AgentsFor:  agentsForSession,
		MatcherFor: playbooksForSession,
	})
	return nil
}
