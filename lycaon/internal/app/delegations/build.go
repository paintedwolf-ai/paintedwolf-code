package delegations

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workertopology"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Dependencies provides inputs for building delegation workers and workflow integration.
type Dependencies struct {
	Git                       *git.Manager
	Workflows                 *workflows.Runtime
	Sessions                  *sessions.Runtime
	Storage                   persistence.Runtime
	Execution                 execution.Runtime
	Scanning                  *scanning.Runtime
	Catalog                   configuration.Catalog
	Agents                    configuration.Agents
	Providers                 providers.Runtime
	Coordinator               func() *coordinator.Runtime
	StartOrchestratedTopology func(ctx context.Context, sessionID string, run *wire.WorkflowRun)
	Progress                  func() progress.RunScopedStore
}

// BuildWorkers constructs workers, executors, cancellations, and links workflow hooks.
func (r *Runtime) BuildWorkers(ctx context.Context, deps Dependencies) error {
	r.SetDependencies(deps)
	r.Criteria = &delegation.GitInspectorCriteriaChecker{
		Git:       deps.Git,
		Inspector: deps.Workflows.Inspector,
		Store:     r.Store,
	}

	r.InjectRenderer = prompts.NewInjectRenderer(deps.Sessions.PromptEngine)
	r.Executor = worker.NewLocalWorkerExecutor(deps.Sessions.Manager.Workers, r.Queue, deps.Sessions.Manager.Workspace, deps.Sessions.Manager.Submissions, deps.Sessions.Manager.Runner.Transcript, deps.Sessions.Manager.Runner.Execution, deps.Sessions.Manager.Workers.Cancel, deps.Sessions.Manager.Workers.Cancellations)
	r.Executor.Waits = &awaitstore.Store{DB: deps.Storage.Database}
	r.Queue.SetSessionAdmission(deps.Sessions.Manager.Chats.Gate.WithSessionTreeAdmission)
	r.Executor.SetPromptInjects(r.InjectRenderer)
	r.Executor.SetPhaseTouchPaths(deps.Workflows.Manager.Ambient)
	r.BranchRoot = enginepaths.WorkerBranchesRootUnder(deps.Storage.Directory)
	if r.Records == nil {
		r.Records = workerrecords.New(deps.Storage.Database)
	}
	if r.Plan == nil {
		r.Plan = &workerplan.Service{Records: r.Records}
	}
	if r.Topology == nil {
		r.Topology = workertopology.New(r.Records, r.Queue)
	}
	r.LaneRoots = r.Records.OpenLaneRoots
	if err := localdata.ClearRetiredWorkerSeeds(deps.Storage.Directory); err != nil {
		slog.WarnContext(ctx, "clear retired worker seeds", "err", err)
	}
	coordinator, err := newWorkerRecovery(deps.Storage.Directory, deps.Execution.PrivateGit)
	if err != nil {
		return err
	}
	r.Recovery = coordinator
	r.Workspace = workspace.NewManager(r.BranchRoot).WithPrivateGit(deps.Execution.PrivateGit).WithRecovery(coordinator)
	r.Queue.SetWorkerWorkspaceManager(r.Workspace)
	r.Queue.SetBaselineStore(deps.Storage.SourceLedger.Baselines)
	r.Queue.SetProjectStore(deps.Storage.Projects)

	r.Cancel = &worker.CancelService{
		Queue:         r.Queue,
		Graceful:      deps.Sessions.Manager.Workers.Cancel,
		Cancellations: deps.Sessions.Manager.Workers.Cancellations,
		Events:        deps.Sessions.Manager.Coordinator.Workers,
		Reject:        deps.Execution.Rejections,
		Reports: worker.ChangeReportDeps{
			Messages: func(c context.Context, childSessionID string) ([]wire.Message, error) {
				return deps.Storage.Sessions.GetMessages(c, childSessionID)
			},
		},
	}
	deps.Workflows.Manager.Controls.Cleanup.Workers = &worker.RunStopService{
		Queue:         r.Queue,
		Holds:         deps.Sessions.Manager.Workers.Cards,
		Cancellations: deps.Sessions.Manager.Workers.Cancellations,
		Reports:       r.Cancel.Reports,
		Delegations:   r.Store,
		Records:       r.Records,
	}
	deps.Workflows.Manager.Recovery.Busy = func(c context.Context, sessionID string) bool {
		sess, err := deps.Storage.Sessions.Get(c, sessionID)
		if err != nil || sess == nil {
			return false
		}
		return sess.Status == wire.SessionStatusBusy
	}
	r.Executor.Reports = r.Cancel.Reports
	r.Queue.SetCancellationReports(r.Cancel.Reports)

	workflowReady := workflow.RegistryWorkflowReadyChecker{
		Registry: deps.Workflows.Conditions,
		Sessions: deps.Storage.Sessions,
		Runs:     deps.Workflows.Store.Runs,
	}
	r.Plan.Records = r.Records
	r.Plan.Plans = deps.Workflows.Blueprints
	r.Plan.Approvals = deps.Workflows.Blueprints
	r.Plan.WorkflowReady = workflowReady
	r.Plan.Runs = deps.Workflows.Manager.Policy
	r.Plan.ActiveRuns = deps.Workflows.Store.Runs
	r.Plan.HeadSHA = deps.Git
	r.Plan.GitDiff = workerplan.NewGitDiffAdapter(deps.Git)
	r.Plan.SourceRuns = deps.Sessions.Manager.Verification
	r.Plan.Inspector = deps.Workflows.Inspector
	r.Plan.Security = deps.Scanning.Closeout
	r.Plan.Jobs = r.Queue
	r.Plan.HostDataDir = func(_ context.Context, projectID string) (string, error) {
		return project.EnsureHostDataDir(deps.Storage.Directory, projectID)
	}
	deps.Workflows.Manager.Fanout.PlannedWorkKnown = r.Plan.HasPlannedWorker
	deps.Sessions.Manager.Coordinator.Guards.SetPlanCloseout(r.Plan)
	r.Queue.SetRoundDeliveryHooks(
		func(c context.Context, task *wire.WorkerTask, projectDir string, jobComplete bool, summary string, diverged []string, report *wire.WorkerChangeReport) string {
			if task == nil {
				return ""
			}
			outcomes := r.Plan.EvaluateRoundDelivery(c, task.ParentSessionID, task.ID, task.ChildSessionID, projectDir, jobComplete, summary, diverged, report)
			return workerrecords.EncodeCriteriaOutcomes(outcomes)
		},
		r.NotifySettledPlanCloseout,
	)

	dispatchGate := delegation.ImplementWorkflowDispatchGate{
		Inner: delegation.WorkflowDispatchGate{
			Inner: delegation.AllowGate{},
			Store: r.Store,
			Runs:  deps.Workflows.Manager.Policy,
		},
		Store:         r.Store,
		Plans:         deps.Workflows.Blueprints,
		WorkflowReady: workflowReady,
	}
	r.Manager = delegation.NewManager(r.Store, r.Queue, deps.Sessions.Manager, dispatchGate)
	r.Manager.Projects = deps.Storage.Projects
	r.Manager.Plans = deps.Workflows.Blueprints
	r.Manager.HeadSHA = deps.Git
	r.Manager.InspectorCloseout = &delegation.InspectorCloseoutGate{
		Inspector: deps.Workflows.Inspector,
		Store:     r.Store,
		Security:  deps.Scanning.Closeout,
	}

	if err := r.configureDelegationWorkflow(deps); err != nil {
		return err
	}
	return r.wireWorkerContext(deps)
}
