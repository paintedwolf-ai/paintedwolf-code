package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/scan"
)

func cliExtensionOwner(ctx context.Context) (*extensionstate.Owner, func(context.Context) error, error) {
	root := configlayout.FindModuleRoot()
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil, nil, err
	}
	dbPath, err := db.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	sqlDB, err := db.Open(dbPath) //nolint:contextcheck // Open creates its boot context.
	if err != nil {
		return nil, nil, err
	}
	journal := extensionstate.NewSQLJournal(sqlDB)
	if err := journal.Recover(ctx, nil, nil); err != nil {
		return nil, nil, errors.Join(err, sqlDB.Shutdown(ctx))
	}
	owner := &extensionstate.Owner{
		Views:    catalogview.NewCache(root, slog.New(slog.DiscardHandler)),
		Scanners: scan.RequirementChecker{ModuleRoot: root, HomeDir: homeDir},
		Journal:  journal,
	}
	return owner, sqlDB.Shutdown, nil
}

func applyDeviceExtensionIntent(ctx context.Context, op extensionstate.Op) (extensionstate.Result, error) {
	return applyExtensionIntent(ctx, extensionstate.Scope{Kind: "device"}, op)
}

func applyProjectUnitIntent(
	ctx context.Context,
	projectDir string,
	op extensionstate.SetUnitDisabledOp,
) (extensionstate.Result, error) {
	return applyExtensionIntent(ctx, extensionstate.Scope{Kind: "project", ProjectDir: projectDir}, op)
}

func applyExtensionIntent(ctx context.Context, scope extensionstate.Scope, op extensionstate.Op) (result extensionstate.Result, err error) {
	owner, closeOwner, err := cliExtensionOwner(ctx)
	if err != nil {
		return extensionstate.Result{}, err
	}
	defer func() { err = errors.Join(err, closeOwner(ctx)) }()
	revision, err := owner.CurrentRevision(scope.ProjectDir)
	if err != nil {
		return extensionstate.Result{}, err
	}
	result, err = owner.Apply(ctx, extensionstate.Intent{
		Scope:            scope,
		ExpectedRevision: revision,
		Op:               op,
	})
	if err != nil {
		return extensionstate.Result{}, fmt.Errorf("extensions: %w", err)
	}
	return result, nil
}
