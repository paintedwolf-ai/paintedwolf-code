package delegations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/branchretention"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Runtime manages delegation stores, workers, executors, cancellations, and workspaces.
type Runtime struct {
	Manager         *delegation.Manager
	Store           *delegation.SQLStore
	Queue           *worker.SQLQueue
	Executor        *worker.LocalWorkerExecutor
	Cancel          *worker.CancelService
	Workspace       *workspace.Manager
	Recovery        *recovery.Coordinator
	Criteria        delegation.CriteriaChecker
	InjectRenderer  *prompts.InjectRenderer
	PlaybookMatcher *prompts.PlaybookMatcher
	ContextLoader   *delegation.WorkerContextLoader
	BranchRoot      string
	SeedRoot        string
	deps            Dependencies
}

// SetRepoProvider binds the repo provider to the worker context loader.
func (r *Runtime) SetRepoProvider(p repoinfo.Provider) {
	if r.ContextLoader != nil {
		r.ContextLoader.Repo = p
	}
}

// SetDependencies binds dependencies for the delegation runtime.
func (r *Runtime) SetDependencies(deps Dependencies) {
	r.deps = deps
}

// New initializes the delegation store and worker queue.
func New(database *db.Store, outbox *eventoutbox.Outbox, workersCfg worker.WorkersConfig) *Runtime {
	store := delegation.NewSQLStore(database)
	if outbox != nil {
		store.SetEventOutbox(outbox)
	}
	queue := worker.NewSQLQueue(database, workersCfg.Poller.MaxConcurrency)
	if outbox != nil {
		queue.SetEventOutbox(outbox)
	}
	queue.SetWorkersConfig(workersCfg)
	return &Runtime{
		Store: store,
		Queue: queue,
	}
}

// BranchRetentionDeps binds sweep parameters to this engine's branch root and queue.
func (r *Runtime) BranchRetentionDeps() branchretention.Deps {
	return branchretention.Deps{
		BranchRoot: r.BranchRoot,
		Jobs: func(ctx context.Context) (map[string]branchretention.JobState, error) {
			jobs, err := r.Queue.ListBranchJobs(ctx)
			if err != nil {
				return nil, err
			}
			out := make(map[string]branchretention.JobState, len(jobs))
			for _, job := range jobs {
				out[job.ID] = branchretention.JobState{Sealed: job.Sealed}
			}
			return out, nil
		},
		Evict: worker.BranchEvictor(r.Queue),
	}
}

// SandboxSettler settles a worker's runtime before any path deletes its sandbox.
func (r *Runtime) SandboxSettler() workspace.SandboxSettler {
	if r.Workspace == nil {
		return nil
	}
	return r.Workspace.SettleBranchRuntime
}

// RecoverWorkerViews runs once at engine startup, before the poller can claim. Collection runs
// here too, because no publisher can be mid-capture yet; a failure is reported, not fatal.
func (r *Runtime) RecoverWorkerViews(ctx context.Context) error {
	if r.Recovery == nil || r.Queue == nil {
		return nil
	}
	err := worker.RecoverWorkerViewsAtStartup(ctx, r.Queue, r.Workspace)
	if _, gcErr := r.Recovery.CollectGarbage(ctx); gcErr != nil {
		slog.WarnContext(ctx, "collect retained worker checkpoints", "err", gcErr)
	}
	r.reportLocalDataInventory(ctx)
	return err
}

// reportLocalDataInventory states what startup found in each local-data bucket.
func (r *Runtime) reportLocalDataInventory(ctx context.Context) {
	inventory, err := workspace.BuildLocalDataInventory(ctx, r.BranchRoot, r.SeedRoot, r.Recovery)
	if err != nil {
		slog.WarnContext(ctx, "build worker local data inventory", "err", err)
		return
	}
	bytesByBucket := make(map[recovery.InventoryBucket]int64, 3)
	for _, entry := range inventory.Entries {
		bytesByBucket[entry.Bucket] += entry.Bytes
	}
	slog.InfoContext(ctx, "worker local data",
		"retained_results_bytes", bytesByBucket[recovery.BucketRetainedWorkerResults],
		"interrupted_bytes", bytesByBucket[recovery.BucketInterruptedWorkingData],
		"disposable_bytes", bytesByBucket[recovery.BucketDisposableRuntimeCache])
}

// ReconcileWorkerSandboxes preserves durable job references even when their captured root is detached.
func (r *Runtime) ReconcileWorkerSandboxes(ctx context.Context, roots []string) (int, error) {
	if r.BranchRoot == "" || r.Queue == nil {
		return 0, nil
	}
	loadRetainedJobs := func(c context.Context) (map[string]struct{}, error) {
		jobs, err := r.Queue.ListBranchJobs(c)
		if err != nil {
			return nil, err
		}
		retained := make(map[string]struct{}, len(jobs))
		for _, job := range jobs {
			retained[job.ID] = struct{}{}
		}
		return retained, nil
	}
	settle := r.SandboxSettler()
	removed, err := workspace.ReconcileSandboxRoots(ctx, r.BranchRoot, r.SeedRoot, roots, settle, loadRetainedJobs)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, root := range roots {
		removedJobs, jobErr := worker.ReconcileProjectSandboxes(ctx, r.BranchRoot, root, settle, func(c context.Context) ([]wire.WorkerTask, error) {
			return r.Queue.ListByWorkspacePath(c, root)
		})
		removed += removedJobs
		if jobErr != nil {
			errs = append(errs, jobErr)
		}
	}
	return removed, errors.Join(errs...)
}

// DecodeCompleteLeg validates and decodes complete_leg invocations.
func (r *Runtime) DecodeCompleteLeg(ctx context.Context, args map[string]any, tctx tools.ToolContext, validateCoverage func(context.Context, *wire.WorkerTask, *wire.CoverageReview) error) (workertools.CompleteLegRecord, error) {
	record, err := workercompletion.CompleteLegDecoder(ctx, args, tctx)
	if err != nil || record.LegStatus != "complete" {
		return record, err
	}
	if tctx.Identity.WorkerJobID == "" {
		return record, nil
	}
	task, ok := r.Queue.Lookup(ctx, tctx.Identity.WorkerJobID)
	if !ok {
		return record, fmt.Errorf("worker job %q unavailable", tctx.Identity.WorkerJobID)
	}
	report, _ := workercompletion.ReportFromCompleteLegArgs(args)
	if validateCoverage != nil {
		return record, validateCoverage(ctx, task, report.CoverageReview)
	}
	return record, nil
}
