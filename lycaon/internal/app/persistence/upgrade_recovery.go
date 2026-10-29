package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/version"
)

func (b *Runtime) openUpgradeableStore(ctx context.Context, path string) (*db.Store, error) {
	if err := db.CheckStoreHealth(path); err != nil {
		return nil, err
	}
	hooks := db.UpgradeHooks{
		Before: func(ctx context.Context, source db.UpgradeSource, plan migrations.Plan) error {
			var sourcePath string
			if err := source.DB.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&sourcePath); err != nil {
				return err
			}
			previous, _, err := db.ReadAppVersion(ctx, source.DB)
			if err != nil {
				return err
			}
			return backup.CaptureUpgradeRecovery(ctx, backup.CreateOpts{ConfigDir: filepath.Dir(path), DBPath: sourcePath,
				SQLDB: source.DB, SharedOriginal: source.SharedOriginal, AppVersion: previous, SchemaUserVersion: plan.Source.Revision}, plan, version.Version)
		},
		Progress: func(phase migrations.Phase) {
			b.logger.InfoContext(ctx, "store upgrade", "phase", phase)
			if b.startup == nil {
				return
			}
			var stage startupprotocol.Phase
			switch phase {
			case migrations.Snapshotting:
				stage = startupprotocol.PhaseUpgradeSnapshot
			case migrations.Migrating:
				stage = startupprotocol.PhaseSchemaUpgrade
			case migrations.Validating:
				stage = startupprotocol.PhaseUpgradeValidation
			default:
				return
			}
			_ = b.startup.Phase(stage)
		},
	}
	hooks.BeforeCurrentOpen = func(ctx context.Context, source *sql.DB, plan migrations.Plan) error {
		return b.captureVersionRecovery(ctx, source, plan, hooks)
	}
	database, err := db.OpenWithOptions(ctx, path, hooks, db.StoreOptions{})
	if err != nil {
		return nil, err
	}
	if err := b.prepareUpgradeReadiness(ctx, database, path); err != nil {
		_ = database.Shutdown(ctx)
		return nil, recoveryStateFailure(err, db.SchemaVersion, "Could not preserve upgrade recovery state before startup.")
	}
	return database, nil
}

func (b *Runtime) prepareUpgradeReadiness(ctx context.Context, database *db.Store, path string) error {
	dataDir := filepath.Dir(path)
	recoveryDir := filepath.Join(dataDir, db.UpgradeRecoveryDirName)
	pending := filepath.Join(recoveryDir, "pending.json")
	if _, err := os.Stat(pending); err == nil {
		if err := backup.ValidateLiveReferences(ctx, database, dataDir); err != nil {
			return &db.StoreIncompatibleError{Reason: db.RecoveryReasonIntegrityFailed, StoreSchemaVersion: db.SchemaVersion,
				Detail: fmt.Sprintf("upgraded history has unavailable retained files: %v", err)}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := backup.DetachUnclaimedUpgradeRecoveries(dataDir, version.Version); err != nil {
		return err
	}
	if _, err := os.Stat(recoveryDir); err == nil {
		b.UpgradeReady = func() error {
			pending, err := database.IntegrityAuditPending(context.WithoutCancel(ctx))
			if err != nil || pending {
				return err
			}
			err = backup.CompleteUpgradeRecovery(dataDir, version.Version)
			var prune *backup.RecoveryPruneError
			if errors.As(err, &prune) {
				b.logger.WarnContext(ctx, "could not remove older recovery snapshots; cleanup deferred", "error", err)
				err = nil
			}
			if err == nil {
				if cleanupErr := db.CompleteStoreUpgrade(path); cleanupErr != nil {
					b.logger.DebugContext(ctx, "store upgrade pre-image cleanup deferred", "error", cleanupErr)
				}
			}
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *Runtime) captureVersionRecovery(ctx context.Context, source *sql.DB, plan migrations.Plan, hooks db.UpgradeHooks) error {
	previous, found, err := db.ReadAppVersion(ctx, source)
	if err != nil || !found || previous == version.Version {
		return err
	}
	hooks.Progress(migrations.Snapshotting)
	if err := hooks.Before(ctx, db.UpgradeSource{DB: source}, plan); err != nil {
		return recoveryStateFailure(err, plan.Source.Revision, "Could not preserve history before the application update.")
	}
	return nil
}

func recoveryStateFailure(err error, revision int, detail string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	classified := db.ClassifyStoreFailure(err)
	if errors.Is(classified, db.ErrStoreIncompatible) {
		return classified
	}
	return errors.Join(&db.StoreIncompatibleError{Reason: db.RecoveryReasonStorageUnavailable,
		StoreSchemaVersion: revision, Detail: detail}, err)
}
