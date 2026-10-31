package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

func boundedRetainedRoute(plan migrations.Plan) bool {
	return plan.BoundedRetainedStorage()
}

func executeRetainedRoute(ctx context.Context, path string, plan migrations.Plan, progress func(migrations.Phase), registry *migrations.Registry, staged, sharedOriginal bool) error {
	if !boundedRetainedRoute(plan) {
		return executeUpgrade(ctx, path, plan, progress, registry)
	}
	return executeRetainedUpgrade(ctx, path, plan, progress, registry, staged, sharedOriginal)
}

// Only the unpublished replacement disables its journal. Activation opens
// the sealed, atomically published database with the ordinary WAL writer.
func executeRetainedUpgrade(ctx context.Context, path string, plan migrations.Plan, progress func(migrations.Phase), registry *migrations.Registry, staged, sharedOriginal bool) (err error) {
	writer, err := openRetainedUpgradeWriter(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	storage, err := InspectUpgradeStorage(ctx, writer, path, staged, sharedOriginal)
	if err != nil {
		return err
	}
	available, known, err := rebuildAvailableBytes(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("upgrade storage capacity is unavailable")
	}
	if err := checkUpgradeTempStorage(ctx, writer, plan.ScratchBytes()); err != nil {
		return err
	}
	limit, err := storage.PageLimit(available, plan.ScratchBytes())
	if err != nil {
		return err
	}
	restore, err := limitUpgradePages(ctx, writer, limit)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restore()) }()
	return registry.Execute(ctx, writer, plan, inspectSchema, progress)
}

func openRetainedUpgradeWriter(ctx context.Context, path string) (*sql.DB, error) {
	query := writerDSNQuery + "&_pragma=journal_mode(off)&_pragma=temp_store(file)&_pragma=threads(0)&_pragma=cache_size(-2000)"
	writer, err := sql.Open("sqlite", fileDSN(path, query))
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	var mode string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if mode != "off" {
		_ = writer.Close()
		return nil, fmt.Errorf("private upgrade requires disposable journal mode, got %s", mode)
	}
	return writer, nil
}

func limitUpgradePages(ctx context.Context, writer *sql.DB, limit uint64) (func() error, error) {
	var original, actual uint64
	if err := writer.QueryRowContext(ctx, "PRAGMA max_page_count").Scan(&original); err != nil {
		return nil, err
	}
	if err := writer.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", limit)).Scan(&actual); err != nil { // #nosec G701
		return nil, err
	}
	restore := func() error {
		_, err := writer.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA max_page_count=%d", original)) // #nosec G701
		return err
	}
	if actual > limit {
		return nil, errors.Join(fmt.Errorf("SQLite page limit %d exceeds admitted %d", actual, limit), restore())
	}
	return restore, nil
}
