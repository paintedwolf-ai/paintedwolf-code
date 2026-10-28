package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

//go:embed released-baselines.json
var releasedBaselinesJSON []byte

// Before captures recovery data; After records the upgrade commit.
type UpgradeHooks struct {
	Before func(context.Context, *sql.DB, migrations.Plan) error
	// BeforeCurrentOpen captures application-version recovery using the existing reader.
	BeforeCurrentOpen func(context.Context, *sql.DB, migrations.Plan) error
	After             func(context.Context, migrations.Plan) error
	Progress          func(migrations.Phase)
}

func schemaRegistry(ctx context.Context) (*migrations.Registry, error) {
	current, err := CurrentBaseline(ctx)
	if err != nil {
		return nil, err
	}
	var released []migrations.Baseline
	if err := json.Unmarshal(releasedBaselinesJSON, &released); err != nil {
		return nil, fmt.Errorf("decode released schemas: %w", err)
	}
	programs := migrations.ProgramReceipts(current)
	effectPermits := migrations.EffectPermitIdentity(programs.From)
	away := migrations.VaultUnlockAway(effectPermits.From)
	resultPaths := migrations.ToolResultPaths()
	steps := []migrations.Step{
		migrations.SourceNamespace(), migrations.PresenceAuthenticators(resultPaths.From), resultPaths,
		migrations.SourceManifestLayout(away.From), away, effectPermits, programs,
	}
	return migrations.New(current, append(released, effectPermits.From, programs.From), steps)
}

// CurrentBaseline identifies the fresh-install schema.
func CurrentBaseline(ctx context.Context) (migrations.Baseline, error) {
	digest, err := BaselineShapeDigest(ctx)
	return migrations.Baseline{Revision: SchemaVersion, Shape: digest}, err
}

func inspectSchema(ctx context.Context, database migrations.DBTX) (migrations.Baseline, error) {
	revision, err := ReadUserVersion(ctx, database)
	if err != nil {
		return migrations.Baseline{}, err
	}
	digest, err := ShapeDigest(ctx, database)
	return migrations.Baseline{Revision: revision, Shape: digest}, err
}

// PlanSchemaUpgrade validates claimed archive identity; staged bytes must still be inspected.
func PlanSchemaUpgrade(ctx context.Context, source migrations.Baseline) (migrations.Plan, error) {
	registry, err := schemaRegistry(ctx)
	if err != nil {
		return migrations.Plan{}, err
	}
	return registry.Plan(source)
}

// PlanUpgrade checks the bytes and migration ledger before returning a complete route.
func PlanUpgrade(ctx context.Context, database DBTX) (migrations.Plan, error) {
	registry, err := schemaRegistry(ctx)
	if err != nil {
		return migrations.Plan{}, err
	}
	return planUpgrade(ctx, database, registry)
}

func planUpgrade(ctx context.Context, database DBTX, registry *migrations.Registry) (migrations.Plan, error) {
	source, err := inspectSchema(ctx, database)
	if err != nil {
		return migrations.Plan{}, err
	}
	plan, err := registry.Plan(source)
	if err != nil {
		return plan, storeIncompatible(RecoveryReasonSchemaMismatch, source.Revision, err.Error())
	}
	if err := registry.CheckLedger(ctx, database, source.Revision); err != nil {
		return plan, storeIncompatible(RecoveryReasonSchemaMismatch, source.Revision, err.Error())
	}
	return plan, nil
}

// UpgradeStaged upgrades only a caller-owned extracted database, before restore publication.
// The original archive and live installation remain the caller's recovery material.
func UpgradeStaged(ctx context.Context, path string) error {
	return upgradeExisting(ctx, path, UpgradeHooks{}, true)
}

func upgradeExisting(ctx context.Context, path string, hooks UpgradeHooks, staged bool) (err error) {
	registry, err := schemaRegistry(ctx)
	if err != nil {
		return err
	}
	return upgradeExistingUsing(ctx, path, hooks, staged, registry)
}

func upgradeExistingUsing(ctx context.Context, path string, hooks UpgradeHooks, staged bool, registry *migrations.Registry) (err error) {
	if err := ResumeStoreUpgrade(path); err != nil {
		return err
	}
	if hooks.Progress != nil {
		hooks.Progress(migrations.Inspecting)
		defer func() {
			if err != nil {
				hooks.Progress(migrations.Failed)
			}
		}()
	}
	reader, err := openReader(ctx, path)
	if err != nil {
		return ClassifyStoreFailure(err)
	}
	defer func() { _ = reader.Close() }()
	plan, err := planUpgrade(ctx, reader, registry)
	if err != nil {
		return err
	}
	// Recorded damage is refused before any scan, capture, or migration.
	if err := prepareIntegrity(ctx, reader); err != nil {
		return err
	}
	if !plan.Required() {
		if staged {
			return validateStoreIntegrity(ctx, reader)
		}
		if hooks.BeforeCurrentOpen != nil {
			return hooks.BeforeCurrentOpen(ctx, reader, plan)
		}
		return nil
	}
	if err := refuseRecordedUpgradeDamage(ctx, reader); err != nil {
		return err
	}
	if plan.CopyPlan() != nil {
		if err := upgradeRetained(ctx, path, reader, plan, hooks, staged, registry); err != nil {
			return classifyUpgradeFailure(err, plan.Source.Revision)
		}
		if hooks.After != nil {
			return hooks.After(ctx, plan)
		}
		return nil
	}
	if !staged {
		if hooks.Before == nil {
			return storeIncompatible(RecoveryReasonSchemaMismatch, plan.Source.Revision, "schema upgrade requires installation recovery capture")
		}
		if hooks.Progress != nil {
			hooks.Progress(migrations.Snapshotting)
		}
		if err := hooks.Before(ctx, reader, plan); err != nil {
			return upgradeOperationError(err, RecoveryReasonStorageUnavailable, plan.Source.Revision, "capture upgrade recovery")
		}
	}
	if err := executeUpgrade(ctx, path, plan, hooks.Progress, registry); err != nil {
		return classifyUpgradeFailure(err, plan.Source.Revision)
	}
	if hooks.After != nil {
		if err := hooks.After(ctx, plan); err != nil {
			return upgradeOperationError(err, RecoveryReasonStorageUnavailable, plan.Target.Revision, "record upgrade commit")
		}
	}
	return nil
}

func classifyUpgradeFailure(err error, revision int) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, migrations.ErrIntegrity) {
		return storeIncompatible(RecoveryReasonIntegrityFailed, revision, err.Error())
	}
	var damaged *StoreIncompatibleError
	if classified := integrityCheckError(err, "migrate store"); errors.As(classified, &damaged) {
		return classified
	}
	// Preserve operational causes for the open boundary's shared classifier.
	return fmt.Errorf("migrate store: %w", err)
}

func executeUpgrade(ctx context.Context, path string, plan migrations.Plan, progress func(migrations.Phase), registry *migrations.Registry) error {
	writer, err := openWriter(ctx, path, StoreOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = writer.Close() }()
	return registry.Execute(ctx, writer, plan, inspectSchema, progress)
}

func upgradeOperationError(err error, fallback RecoveryReason, revision int, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	classified := ClassifyStoreFailure(err)
	var failure *StoreIncompatibleError
	if errors.As(classified, &failure) {
		result := *failure
		if result.StoreSchemaVersion == 0 {
			result.StoreSchemaVersion = revision
		}
		return &result
	}
	return storeIncompatible(fallback, revision, fmt.Sprintf("%s: %v", operation, err))
}

// A pending audit is work to complete, not evidence that the source is damaged.
// Whole-file verification runs against the migrated state before commit.
func refuseRecordedUpgradeDamage(ctx context.Context, reader *sql.DB) error {
	var value string
	err := reader.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, integrityAuditFailedMetaKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if value != "" && value != "integrity audit pending" {
		version, _ := ReadUserVersion(ctx, reader)
		return storeIncompatible(RecoveryReasonIntegrityFailed, version, value)
	}
	return nil
}
