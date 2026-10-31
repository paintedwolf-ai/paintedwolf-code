package app

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

func cleanupRequiresIntegrity(name string) bool {
	switch name {
	case "store-coupled-reconcile", "source-blob-gc", "content-blob-gc", "history-retention", "prompt-attachment-maintenance", "content-density", "worker-branch-retention":
		return true
	}
	return false
}

func afterIntegrityAudit(database *db.Store, run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := database.WaitForIntegrityAudit(ctx); err != nil {
			return err
		}
		return run(ctx)
	}
}

func runStoreIntegrityAudit(ctx context.Context, database *db.Store, ready func() error) error {
	pending, err := database.IntegrityAuditPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		if err := db.AuditIntegrity(ctx, database); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}
