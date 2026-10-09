package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/branchretention"
	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// branchRetentionDeps binds the sweep to this engine's branch root and queue.
func (b delegationWiring) branchRetentionDeps() branchretention.Deps {
	return branchretention.Deps{
		BranchRoot: b.workerBranchRoot,
		Jobs: func(ctx context.Context) (map[string]branchretention.JobState, error) {
			jobs, err := b.workerQueue.ListBranchJobs(ctx)
			if err != nil {
				return nil, err
			}
			out := make(map[string]branchretention.JobState, len(jobs))
			for _, job := range jobs {
				out[job.ID] = branchretention.JobState{Sealed: job.Sealed}
			}
			return out, nil
		},
		Evict: workspace.EvictJobTree,
	}
}

func (b delegationWiring) registerBackgroundRunners(app *ServeApp) {
	type registration struct {
		run     func(context.Context) error
		oneShot bool
	}
	byName := make(map[string]registration, len(serveRunnerOrder))
	byName["boot-recovery"] = registration{run: b.runServeRecovery, oneShot: true}
	if b.registry != nil && b.dataDir != "" {
		byName["store-coupled-reconcile"] = registration{run: func(ctx context.Context) error {
			if err := b.reconcileStoreCoupledStorage(ctx); err != nil {
				return err
			}
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					if err := b.reconcileStoreCoupledStorage(ctx); err != nil {
						return err
					}
				}
			}
		}}
	}
	if b.db != nil {
		byName["wal-checkpointer"] = registration{run: func(ctx context.Context) error {
			return db.RunWALCheckpointer(ctx, b.db, b.storePath)
		}}
	}
	if b.db != nil && b.retentionCfg.Enabled {
		byName["store-maintenance"] = registration{run: func(ctx context.Context) error {
			return db.RunMaintenance(ctx, b.db, b.retentionCfg)
		}}
	}
	if b.db != nil && b.db.IntegrityAuditDue() {
		byName["store-integrity-audit"] = registration{run: func(ctx context.Context) error {
			return db.RunIntegrityAudit(ctx, b.db)
		}}
	}
	if b.secretCaps != nil {
		byName["managed-secret-maintenance"] = registration{run: b.secretCaps.RunMaintenance}
	}
	if b.vaultUnlocks != nil {
		byName["vault-unlock-sweep"] = registration{run: b.vaultUnlocks.RunSweeper}
	}
	if b.workerPoller != nil {
		byName["worker-poller"] = registration{run: b.workerPoller.Run}
	}
	if b.scanRunner != nil {
		byName["scan-runner"] = registration{run: b.scanRunner.Run}
	}
	if b.scanCadence != nil {
		byName["scan-cadence"] = registration{run: b.scanCadence.Run}
	}
	if b.warmRunner != nil {
		byName["warm-runner"] = registration{run: b.warmRunner.Run}
	}
	if _, ok := b.deciderWarmer(); ok {
		byName["decision-engine-warm"] = registration{run: b.warmDecider, oneShot: true}
	}
	if b.sourceLedger != nil {
		byName["source-blob-gc"] = registration{run: func(ctx context.Context) error {
			return b.sourceLedger.RunBlobGC(ctx, sourceledger.BlobGCInterval, sourceledger.BlobGCRetry)
		}}
	}
	if b.db != nil && b.dataDir != "" {
		byName["content-blob-gc"] = registration{run: func(ctx context.Context) error {
			return contentblob.RunGC(ctx, contentblob.GCDeps{Database: b.db, Queries: db.New(b.db), DataDir: b.dataDir, Guard: b.storeClaim})
		}}
		byName["history-retention"] = registration{run: b.historyStorage.Run}
		byName["prompt-attachment-maintenance"] = registration{run: b.srv.Admin.Prompt.Attachments.RunPromptAttachmentMaintenance}
		byName["content-density"] = registration{run: func(ctx context.Context) error {
			deps := contentblob.DensityDeps{Queries: db.New(b.db), DataDir: b.dataDir}
			return contentblob.RunDensity(ctx, deps, contentblob.DefaultDensityConfig())
		}}
	}
	if b.dataDir != "" {
		byName["debug-retention"] = registration{run: func(ctx context.Context) error {
			return debugretention.Run(ctx, b.dataDir, debugretention.DefaultConfig())
		}}
	}
	if b.workerQueue != nil && b.workerBranchRoot != "" {
		byName["worker-branch-retention"] = registration{run: func(ctx context.Context) error {
			return branchretention.Run(ctx, b.branchRetentionDeps(), branchretention.DefaultConfig())
		}}
	}
	for _, name := range serveRunnerOrder {
		entry, ok := byName[name]
		if !ok {
			continue
		}
		if entry.oneShot {
			app.registerOneShotRunner(name, entry.run)
			continue
		}
		app.registerRunner(name, entry.run)
	}
}

// reconcileStoreCoupledStorage removes host storage the registry no longer
// names. It refuses once the store path is replaced: the registry then describes
// another store and every tree would look orphaned.
func (b delegationWiring) reconcileStoreCoupledStorage(ctx context.Context) error {
	if b.storeClaim != nil {
		if err := b.storeClaim.Verify(); err != nil {
			return err
		}
	}
	projects, err := b.registry.List(ctx)
	if err != nil {
		return err
	}
	ids := make(map[string]struct{}, len(projects))
	var roots []string
	for i := range projects {
		ids[projects[i].ID] = struct{}{}
		for _, root := range projects[i].Roots {
			roots = append(roots, root.Path)
		}
	}
	removedHost, hostErr := project.ReconcileHostStorage(b.dataDir, ids)
	removedCheckpoints, checkpointErr := sessioncheckpoint.ReconcileRoots(b.dataDir, roots)
	removedCatalogs, catalogErr := sourcecatalog.Process().Trees.ReconcileTreeStores(ctx, sourcecatalog.TreeStoreRetention)
	removedSandboxes, sandboxErr := b.reconcileWorkerSandboxes(ctx, roots)
	removedSpills, spillErr := scan.ReconcileSpills(ctx, b.dataDir, b.scanStore)
	if removedHost+removedCheckpoints+removedCatalogs+removedSandboxes+removedSpills > 0 {
		slog.InfoContext(ctx, "reconciled orphan store-coupled storage",
			"host_trees", removedHost, "checkpoint_roots", removedCheckpoints,
			"catalog_generations", removedCatalogs, "worker_sandboxes", removedSandboxes,
			"scan_spills", removedSpills)
	}
	return errors.Join(hostErr, checkpointErr, catalogErr, sandboxErr, spillErr)
}

// reconcileWorkerSandboxes preserves durable job references even when their
// captured project root is no longer attached.
func (b delegationWiring) reconcileWorkerSandboxes(ctx context.Context, roots []string) (int, error) {
	if b.workerBranchRoot == "" {
		return 0, nil
	}
	if b.workerQueue == nil {
		return 0, nil
	}
	loadRetainedJobs := func(ctx context.Context) (map[string]struct{}, error) {
		jobs, err := b.workerQueue.ListBranchJobs(ctx)
		if err != nil {
			return nil, err
		}
		retained := make(map[string]struct{}, len(jobs))
		for _, job := range jobs {
			retained[job.ID] = struct{}{}
		}
		return retained, nil
	}
	removed, err := workspace.ReconcileSandboxRoots(ctx, b.workerBranchRoot, b.workerSeedRoot, roots, loadRetainedJobs)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, root := range roots {
		removedJobs, jobErr := worker.ReconcileProjectSandboxes(ctx, b.workerBranchRoot, root, func(ctx context.Context) ([]wire.WorkerTask, error) {
			return b.workerQueue.ListByWorkspacePath(ctx, root)
		})
		removed += removedJobs
		if jobErr != nil {
			errs = append(errs, jobErr)
		}
	}
	return removed, errors.Join(errs...)
}

func (b delegationWiring) registerRecovery(e bootrecovery.Entry) error {
	if b.recovery == nil {
		b.recovery = bootrecovery.New()
	}
	return b.recovery.Register(e)
}

func (b delegationWiring) runBuildRecovery() error {
	return b.reportRecovery(b.recovery.Run(b.ctx, bootrecovery.PhaseBuild))
}

func (b delegationWiring) runServeRecovery(ctx context.Context) error {
	return b.reportRecovery(b.recovery.Run(ctx, bootrecovery.PhaseServe))
}

func (b delegationWiring) reportRecovery(report bootrecovery.Report, err error) error {
	if err != nil {
		return err
	}
	log := b.logger
	if log == nil {
		log = slog.Default()
	}
	for _, outcome := range report.Degraded() {
		switch {
		case outcome.Skipped():
			log.Error("startup recovery skipped; a dependency did not settle",
				"entry", outcome.Name, "kind", string(outcome.Kind),
				"phase", string(outcome.Phase), "blocked_by", outcome.Blocked)
		default:
			log.Error("startup recovery degraded; this subsystem stays unresolved",
				"entry", outcome.Name, "kind", string(outcome.Kind),
				"phase", string(outcome.Phase), "error", outcome.Err)
		}
	}
	return report.Err()
}
