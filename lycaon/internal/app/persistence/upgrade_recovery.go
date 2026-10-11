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
	hooks := db.UpgradeHooks{
		Before: func(ctx context.Context, source *sql.DB, plan migrations.Plan) error {
			previous, _, err := db.ReadAppVersion(ctx, source)
			if err != nil {
				return err
			}
			return backup.CaptureUpgradeRecovery(ctx, backup.CreateOpts{ConfigDir: filepath.Dir(path), DBPath: path,
				SQLDB: source, AppVersion: previous, SchemaUserVersion: plan.Source.Revision}, plan, version.Version)
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
	if _, err := os.Stat(path); err == nil {
		if err := b.captureVersionRecovery(ctx, path, hooks); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	database, err := db.OpenWithOptions(ctx, path, hooks, db.StoreOptions{})
	if err != nil {
		return nil, err
	}
	if err := b.prepareUpgradeReadiness(ctx, database, filepath.Dir(path)); err != nil {
		_ = database.Shutdown(ctx)
		if errors.Is(err, db.ErrStoreIncompatible) {
			return nil, err
		}
		return nil, errors.Join(&db.StoreIncompatibleError{Reason: db.RecoveryReasonIntegrityFailed,
			StoreSchemaVersion: db.SchemaVersion, Detail: "Could not preserve upgrade recovery state before startup."}, err)
	}
	return database, nil
}

func (b *Runtime) prepareUpgradeReadiness(ctx context.Context, database *db.Store, dataDir string) error {
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
			err := backup.CompleteUpgradeRecovery(dataDir, version.Version)
			var prune *backup.RecoveryPruneError
			if errors.As(err, &prune) {
				b.logger.WarnContext(ctx, "could not remove older recovery snapshots; cleanup deferred", "error", err)
				return nil
			}
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *Runtime) captureVersionRecovery(ctx context.Context, path string, hooks db.UpgradeHooks) error {
	source, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		// OpenWithOptions classifies corrupt and incompatible stores.
		return nil
	}
	defer func() { _ = source.Close() }()
	plan, err := db.PlanUpgrade(ctx, source)
	if err != nil || plan.Required() {
		return nil
	}
	previous, found, err := db.ReadAppVersion(ctx, source)
	if err != nil || !found || previous == version.Version {
		return err
	}
	hooks.Progress(migrations.Snapshotting)
	if err := hooks.Before(ctx, source, plan); err != nil {
		return errors.Join(&db.StoreIncompatibleError{Reason: db.RecoveryReasonIntegrityFailed,
			StoreSchemaVersion: plan.Source.Revision, Detail: "Could not preserve history before the application update."}, err)
	}
	return nil
}
