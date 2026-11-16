package workerruntime

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/providers"
	"github.com/lycaon/lycaon/internal/app/scanning"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workflows"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator"
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

	r.InjectRenderer = prompts.NewInjectRenderer(deps.Sessions.PromptEngine)
	r.Executor = worker.NewLocalWorkerExecutor(deps.Sessions.Manager.Workers, r.Queue, deps.Sessions.Manager.Workspace, deps.Sessions.Manager.Submissions, deps.Sessions.Manager.Runner.Transcript, deps.Sessions.Manager.Runner.Execution, deps.Sessions.Manager.Workers.Cancel, deps.Sessions.Manager.Workers.Cancellations)
	r.Executor.Waits = &awaitstore.Store{DB: deps.Storage.Database}
	r.Queue.SetSessionAdmission(deps.Sessions.Manager.Chats.Gate.WithSessionTreeAdmission)
	r.Executor.SetPromptInjects(r.InjectRenderer)
	r.Executor.SetPhaseTouchPaths(deps.Workflows.Manager.Ambient)
	// Branch state is keyed by path, and processes and mounts report resolved
	// paths, so the branch root is resolved once here.
	stateDir, err := filepath.EvalSymlinks(deps.Storage.Directory)
	if err != nil {
		return fmt.Errorf("resolve state directory: %w", err)
	}
	r.BranchRoot = enginepaths.WorkerBranchesRootUnder(stateDir)
	if r.Records == nil {
		r.Records = workerrecords.New(deps.Storage.Database)
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
	plan, err := workerplan.New(workerplan.Deps{
		Records:       r.Records,
		Plans:         deps.Workflows.Blueprints,
		Approvals:     deps.Workflows.Blueprints,
		WorkflowReady: workflowReady,
		Runs:          deps.Workflows.Manager.Policy,
		ActiveRuns:    deps.Workflows.Store.Runs,
		HeadSHA:       deps.Git,
		GitDiff:       workerplan.NewGitDiffAdapter(deps.Git),
		SourceRuns:    deps.Sessions.Manager.Verification,
		Jobs:          r.Queue,
		Security:      deps.Scanning.Closeout,
		HostDataDir: func(_ context.Context, projectID string) (string, error) {
			return project.EnsureHostDataDir(deps.Storage.Directory, projectID)
		},
	})
	if err != nil {
		return err
	}
	r.Plan = plan
	deps.Workflows.Manager.Fanout.PlannedWorkKnown = r.Plan.HasPlannedWorker
	deps.Sessions.Manager.Coordinator.Guards.SetPlanCloseout(r.Plan)
	r.Queue.SetRoundDeliveryHooks(
		func(c context.Context, task *wire.WorkerTask, projectDir string, jobComplete bool, summary string, diverged []string, report *wire.WorkerChangeReport) string {
			if task == nil {
				return ""
			}
			outcomes, err := r.Plan.EvaluateRoundDelivery(c, task.ParentSessionID, task.ID, workerplan.DeliveryInput{
				ChildSessionID: task.ChildSessionID, ProjectDir: projectDir, JobComplete: jobComplete,
				Summary: summary, ModifiedPaths: diverged, ChangeReport: report,
			})
			if err != nil {
				// The result settles without outcomes; closeout evaluates
				// its criteria again and reports what it cannot read.
				slog.WarnContext(c, "evaluate round criteria", "job_id", task.ID, "err", err)
				return ""
			}
			return workerrecords.EncodeCriteriaOutcomes(outcomes)
		},
		r.NotifySettledPlanCloseout,
	)

	if err := r.configureWorkflow(deps); err != nil {
		return err
	}
	return r.wireWorkerContext(deps)
}
