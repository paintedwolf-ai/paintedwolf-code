package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

//go:embed released-baselines.json
var releasedBaselinesJSON []byte

// Before captures recovery data; After records the upgrade commit.
type UpgradeHooks struct {
	Before   func(context.Context, *sql.DB, migrations.Plan) error
	After    func(context.Context, migrations.Plan) error
	Progress func(migrations.Phase)
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
	steps := []migrations.Step{migrations.SourceNamespace(current)}
	return migrations.New(current, released, steps)
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
		return classifyStoreOpenFailure(err)
	}
	defer func() { _ = reader.Close() }()
	plan, err := planUpgrade(ctx, reader, registry)
	if err != nil {
		return err
	}
	if !plan.Required() {
		return prepareIntegrity(ctx, reader)
	}
	if err := validateStoreIntegrity(ctx, reader); err != nil {
		return err
	}
	if !staged {
		if hooks.Before == nil {
			return storeIncompatible(RecoveryReasonSchemaMismatch, plan.Source.Revision, "schema upgrade requires installation recovery capture")
		}
		if hooks.Progress != nil {
			hooks.Progress(migrations.Snapshotting)
		}
		if err := hooks.Before(ctx, reader, plan); err != nil {
			return storeIncompatible(RecoveryReasonIntegrityFailed, plan.Source.Revision, fmt.Sprintf("capture upgrade recovery: %v", err))
		}
	}
	if err := reader.Close(); err != nil {
		return err
	}
	if err := executeUpgrade(ctx, path, plan, hooks.Progress, registry); err != nil {
		return storeIncompatible(RecoveryReasonSchemaMismatch, plan.Source.Revision, err.Error())
	}
	if hooks.After != nil {
		if err := hooks.After(ctx, plan); err != nil {
			return storeIncompatible(RecoveryReasonIntegrityFailed, plan.Target.Revision, fmt.Sprintf("record upgrade commit: %v", err))
		}
	}
	return nil
}

func executeUpgrade(ctx context.Context, path string, plan migrations.Plan, progress func(migrations.Phase), registry *migrations.Registry) error {
	writer, err := openWriter(ctx, path, StoreOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = writer.Close() }()
	return registry.Execute(ctx, writer, plan, inspectSchema, progress)
}
