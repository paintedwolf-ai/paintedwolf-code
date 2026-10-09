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
)

func registerBackgroundRunners(b *serveBuilder, app *ServeApp) {
	type registration struct {
		run     func(context.Context) error
		oneShot bool
	}
	byName := make(map[string]registration, len(serveRunnerOrder))
	byName["boot-recovery"] = registration{run: b.runServeRecovery, oneShot: true}
	if b.storage.Projects != nil && b.storage.Directory != "" {
		byName["store-coupled-reconcile"] = registration{run: func(ctx context.Context) error {
			if err := reconcileStoreCoupledStorage(b, ctx); err != nil {
				return err
			}
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					if err := reconcileStoreCoupledStorage(b, ctx); err != nil {
						return err
					}
				}
			}
		}}
	}
	if b.storage.Database != nil {
		byName["wal-checkpointer"] = registration{run: func(ctx context.Context) error {
			return db.RunWALCheckpointer(ctx, b.storage.Database, b.storage.Path)
		}}
	}
	if b.storage.Database != nil && b.storage.Retention.Enabled {
		byName["store-maintenance"] = registration{run: func(ctx context.Context) error {
			return db.RunMaintenance(ctx, b.storage.Database, b.storage.Retention)
		}}
	}
	if b.storage.Database != nil && b.storage.Database.IntegrityAuditDue() {
		byName["store-integrity-audit"] = registration{run: func(ctx context.Context) error {
			return db.RunIntegrityAudit(ctx, b.storage.Database)
		}}
	}
	if b.security.Capabilities != nil {
		byName["managed-secret-maintenance"] = registration{run: b.security.Capabilities.RunMaintenance}
	}
	if b.security.Unlocks != nil {
		byName["vault-unlock-sweep"] = registration{run: b.security.Unlocks.RunSweeper}
	}
	if b.worker.poller != nil {
		byName["worker-poller"] = registration{run: b.worker.poller.Run}
	}
	if b.scanning != nil && b.scanning.Runner != nil {
		byName["scan-runner"] = registration{run: b.scanning.Runner.Run}
	}
	if b.scanning != nil && b.scanning.Cadence != nil {
		byName["scan-cadence"] = registration{run: b.scanning.Cadence.Run}
	}
	if b.boards != nil && b.boards.WarmRunner != nil {
		byName["warm-runner"] = registration{run: b.boards.WarmRunner.Run}
	}
	if _, ok := b.decisions.Warmer(); ok {
		byName["decision-engine-warm"] = registration{run: b.decisions.Warm, oneShot: true}
	}
	if b.storage.SourceLedger != nil {
		byName["source-blob-gc"] = registration{run: func(ctx context.Context) error {
			return b.storage.SourceLedger.RunBlobGC(ctx, sourceledger.BlobGCInterval, sourceledger.BlobGCRetry)
		}}
	}
	if b.storage.Database != nil && b.storage.Directory != "" {
		byName["content-blob-gc"] = registration{run: func(ctx context.Context) error {
			return contentblob.RunGC(ctx, contentblob.GCDeps{Database: b.storage.Database, Queries: db.New(b.storage.Database), DataDir: b.storage.Directory, Guard: b.storage.Claim})
		}}
		byName["history-retention"] = registration{run: b.server.HistoryStorage.Run}
		byName["prompt-attachment-maintenance"] = registration{run: b.server.Server.Admin.Prompt.Attachments.RunPromptAttachmentMaintenance}
		byName["content-density"] = registration{run: func(ctx context.Context) error {
			deps := contentblob.DensityDeps{Queries: db.New(b.storage.Database), DataDir: b.storage.Directory}
			return contentblob.RunDensity(ctx, deps, contentblob.DefaultDensityConfig())
		}}
	}
	if b.storage.Directory != "" {
		byName["debug-retention"] = registration{run: func(ctx context.Context) error {
			return debugretention.Run(ctx, b.storage.Directory, debugretention.DefaultConfig())
		}}
	}
	if b.delegations != nil && b.delegations.Queue != nil && b.delegations.BranchRoot != "" {
		byName["worker-branch-retention"] = registration{run: func(ctx context.Context) error {
			return branchretention.Run(ctx, b.delegations.BranchRetentionDeps(), branchretention.DefaultConfig())
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
func reconcileStoreCoupledStorage(b *serveBuilder, ctx context.Context) error {
	if b.storage.Claim != nil {
		if err := b.storage.Claim.Verify(); err != nil {
			return err
		}
	}
	projects, err := b.storage.Projects.List(ctx)
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
	removedHost, hostErr := project.ReconcileHostStorage(b.storage.Directory, ids)
	removedCheckpoints, checkpointErr := sessioncheckpoint.ReconcileRoots(b.storage.Directory, roots)
	removedCatalogs, catalogErr := sourcecatalog.Process().Trees.ReconcileTreeStores(ctx, sourcecatalog.TreeStoreRetention)
	var removedSandboxes int
	var sandboxErr error
	if b.delegations != nil {
		removedSandboxes, sandboxErr = b.delegations.ReconcileWorkerSandboxes(ctx, roots)
	}
	removedSpills, spillErr := scan.ReconcileSpills(ctx, b.storage.Directory, b.scanning.Store)
	if removedHost+removedCheckpoints+removedCatalogs+removedSandboxes+removedSpills > 0 {
		slog.InfoContext(ctx, "reconciled orphan store-coupled storage",
			"host_trees", removedHost, "checkpoint_roots", removedCheckpoints,
			"catalog_generations", removedCatalogs, "worker_sandboxes", removedSandboxes,
			"scan_spills", removedSpills)
	}
	return errors.Join(hostErr, checkpointErr, catalogErr, sandboxErr, spillErr)
}

func (b *serveBuilder) registerRecovery(e bootrecovery.Entry) error {
	if b.startup.recovery == nil {
		b.startup.recovery = bootrecovery.New()
	}
	return b.startup.recovery.Register(e)
}

func (b *serveBuilder) runBuildRecovery() error {
	return b.reportRecovery(b.startup.recovery.Run(b.startup.ctx, bootrecovery.PhaseBuild))
}

func (b *serveBuilder) runServeRecovery(ctx context.Context) error {
	return b.reportRecovery(b.startup.recovery.Run(ctx, bootrecovery.PhaseServe))
}

func (b *serveBuilder) reportRecovery(report bootrecovery.Report, err error) error {
	if err != nil {
		return err
	}
	log := b.startup.logger
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
