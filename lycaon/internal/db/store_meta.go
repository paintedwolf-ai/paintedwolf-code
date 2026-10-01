package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const (
	storeRevisionMetaKey          = "store_revision"
	appVersionMetaKey             = "app_version"
	bootPreviousAppVersionMetaKey = "boot_previous_app_version"
)

// BumpStoreRevision advances the restart identity exposed by GET /health.
func BumpStoreRevision(ctx context.Context, sqlDB Handle) (uint64, error) {
	if sqlDB == nil {
		return 0, fmt.Errorf("bump store revision: nil database")
	}

	queries := New(sqlDB)
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := queries.WithTx(tx)
	raw, err := qtx.GetStoreMetaValue(ctx, storeRevisionMetaKey)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		raw = "0"
	case err != nil:
		return 0, fmt.Errorf("read store revision: %w", err)
	}

	prev, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse store revision %q: %w", raw, err)
	}
	next := prev + 1

	if err := qtx.UpsertStoreMeta(ctx, UpsertStoreMetaParams{
		Key:   storeRevisionMetaKey,
		Value: strconv.FormatUint(next, 10),
	}); err != nil {
		return 0, fmt.Errorf("write store revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return next, nil
}

// ReadAppVersion returns the app version recorded for this store.
func ReadAppVersion(ctx context.Context, sqlDB DBTX) (string, bool, error) {
	return readStoreMetaString(ctx, sqlDB, appVersionMetaKey)
}

// ReadBootPreviousAppVersion returns the version this boot upgraded from.
func ReadBootPreviousAppVersion(ctx context.Context, sqlDB DBTX) (string, bool, error) {
	return readStoreMetaString(ctx, sqlDB, bootPreviousAppVersionMetaKey)
}

// RecordAppVersionTransition atomically stamps app_version and sets or clears the
// transient boot_previous_app_version key.
func RecordAppVersionTransition(ctx context.Context, sqlDB Handle, current, previous string, upgraded bool) error {
	if sqlDB == nil {
		return fmt.Errorf("record app version: nil database")
	}
	queries := New(sqlDB)
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record app version: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := queries.WithTx(tx)

	if err := qtx.UpsertStoreMeta(ctx, UpsertStoreMetaParams{
		Key:   appVersionMetaKey,
		Value: current,
	}); err != nil {
		return fmt.Errorf("stamp app_version: %w", err)
	}
	if upgraded {
		if err := qtx.UpsertStoreMeta(ctx, UpsertStoreMetaParams{
			Key:   bootPreviousAppVersionMetaKey,
			Value: previous,
		}); err != nil {
			return fmt.Errorf("stamp boot_previous_app_version: %w", err)
		}
	} else if err := qtx.DeleteStoreMeta(ctx, bootPreviousAppVersionMetaKey); err != nil {
		return fmt.Errorf("clear boot_previous_app_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record app version: commit: %w", err)
	}
	return nil
}

func readStoreMetaString(ctx context.Context, sqlDB DBTX, key string) (string, bool, error) {
	if sqlDB == nil {
		return "", false, fmt.Errorf("read store meta: nil database")
	}
	raw, err := New(sqlDB).GetStoreMetaValue(ctx, key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read store meta %q: %w", key, err)
	}
	return raw, true, nil
}
