package workerruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workercontext"
	"github.com/lycaon/lycaon/internal/workerrecords"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (r *Runtime) configureWorkflow(deps Dependencies) error {
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
	legToolLister := workercontext.LegToolListerFunc(func(ctx context.Context, sess *wire.Session, profileID string) []string {
		policy := deps.Sessions.Manager.Coordinator.Guards.Policy()
		if sess == nil {
			return nil
		}
		metas := policy.ListForPrompt(ctx, sess, profileID)
		// The leg names exactly what the worker's request offers.
		if machine := deps.Sessions.Manager.Profiles.ProjectMachine(ctx, sess, profileID); machine.Compiled() {
			metas = tools.HideSkillsReadWhenEmpty(metas, machine.SkillCount)
		}
		var names []string
		for _, meta := range metas {
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
	playbooksForSession := func(sess *wire.Session) workercontext.PlaybookMatcherInterface {
		if view := deps.Sessions.Manager.Catalog.ViewForSession(context.Background(), sess); view != nil && view.Playbooks != nil {
			return view.Playbooks
		}
		return r.PlaybookMatcher
	}
	records := r.Records
	r.ContextLoader = &workercontext.CompositeWorkerContext{
		Records:       records,
		Tasks:         r.Queue,
		Runs:          deps.Workflows.Manager.Phases,
		Tools:         legToolLister,
		Scans:         deps.Scanning.Coordinator,
		AgentsMDChain: deps.Sessions.Manager.Coordinator.PolicyIndex.ForPaths,
		Repo:          nil, // Bound via SetRepoProvider.
		Topology: func(workflowID string) string {
			if strings.TrimSpace(workflowID) == "default-pipeline" {
				return "pipeline"
			}
			return strings.TrimSpace(workflowID)
		},
		AgentsFor:  agentsForSession,
		MatcherFor: playbooksForSession,
		WorkflowIDForRun: func(ctx context.Context, runID string) string {
			run, getErr := deps.Workflows.Store.Runs.Get(ctx, runID)
			if getErr != nil || run == nil {
				return ""
			}
			return run.WorkflowID
		},
		ReviewSubject: func(ctx context.Context, jobID string) (string, string, bool, error) {
			return reviewSubjectNames(ctx, records, jobID)
		},
	}
	deps.Sessions.Manager.SetWorkerContextBuilder(r.ContextLoader)
	return nil
}

// reviewSubjectNames names the result a reviewer's job reads and the worker
// that delivered it.
func reviewSubjectNames(ctx context.Context, records *workerrecords.Store, jobID string) (string, string, bool, error) {
	round, ok, err := records.RoundByJob(ctx, jobID)
	if err != nil || !ok {
		return "", "", false, err
	}
	subject, reviewer := round.Subject()
	if !reviewer {
		return "", "", false, nil
	}
	names, _, err := records.NamesByJob(ctx, subject.JobID)
	if err != nil {
		return "", "", false, err
	}
	return names.Worker, subject.ResultID, true, nil
}
