package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
)

func beginRebuild(path string) (rebuildJournal, error) {
	var record rebuildJournal
	if _, err := os.Lstat(rebuildMarker(path)); !os.IsNotExist(err) {
		return record, fmt.Errorf("store upgrade journal must be resolved before rebuilding")
	}
	work, err := os.MkdirTemp(filepath.Dir(path), StoreUpgradeDirectoryPrefix)
	if err != nil {
		return record, err
	}
	identity, err := fspath.EntryIdentity(work)
	if err != nil {
		return record, errors.Join(err, os.Remove(work))
	}
	record = rebuildJournal{Version: 1, Database: filepath.Base(path), Directory: filepath.Base(work), DirectoryIdentity: identity, State: "building"}
	if err := writeRebuildJournal(path, record); err != nil {
		return record, errors.Join(err, discardRebuild(path, record))
	}
	return record, nil
}

func upgradeRetained(ctx context.Context, path string, source *sql.DB, plan migrations.Plan, hooks UpgradeHooks, staged bool, registry *migrations.Registry) (err error) {
	if !staged && hooks.Before == nil {
		return fmt.Errorf("schema upgrade requires installation recovery capture")
	}
	record, err := beginRebuild(path)
	if err != nil {
		return err
	}
	defer func() {
		if record.State == "building" {
			err = errors.Join(err, discardRebuild(path, record))
		}
	}()
	work := filepath.Join(filepath.Dir(path), record.Directory)
	replacement := filepath.Join(work, "replacement.db")
	if hooks.Progress != nil {
		hooks.Progress(migrations.Migrating)
	}
	if err := copyRetainedStore(ctx, path, replacement, plan); err != nil {
		return err
	}
	if err := source.Close(); err != nil {
		return err
	}
	// The compact released-shape copy is itself verified before it becomes
	// recovery material. Capture retains every backed-up installation payload;
	// only unreachable legacy chunks and abandoned staging have been omitted.
	if err := captureRetainedRecovery(ctx, replacement, plan, hooks, staged); err != nil {
		return err
	}
	if err := executeUpgrade(ctx, replacement, plan, hooks.Progress, registry); err != nil {
		return err
	}
	if err := sealRebuiltStore(ctx, replacement); err != nil {
		return err
	}
	if err := prepareRebuildPublication(path, replacement, &record); err != nil {
		return err
	}
	// Once prepared is durable, cancellation cannot change the commit decision.
	// Any filesystem failure leaves the journal and both sets of files for retry.
	if err := finishRebuildPublication(path, record, nil); err != nil {
		return err
	}
	if staged {
		return CompleteStoreUpgrade(path)
	}
	return nil
}

func captureRetainedRecovery(ctx context.Context, replacement string, plan migrations.Plan, hooks UpgradeHooks, staged bool) (err error) {
	reader, err := openReader(ctx, replacement)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	if hooks.Progress != nil {
		hooks.Progress(migrations.Validating)
	}
	if err := validateStoreIntegrity(ctx, reader); err != nil {
		return err
	}
	if staged {
		return nil
	}
	if hooks.Progress != nil {
		hooks.Progress(migrations.Snapshotting)
	}
	return hooks.Before(ctx, reader, plan)
}

func sealRebuiltStore(ctx context.Context, path string) error {
	database, err := sql.Open("sqlite", fileDSN(path, "_pragma=synchronous(full)"))
	if err != nil {
		return err
	}
	var mode string
	err = database.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode)
	err = errors.Join(err, database.Close())
	if err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("rebuilt store still requires a journal")
	}
	for _, name := range []string{path, filepath.Dir(path)} {
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		if err := errors.Join(fssync.File(file), file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func prepareRebuildPublication(path, replacement string, record *rebuildJournal) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		identity, err := fspath.EntryIdentity(path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		record.Original = append(record.Original, rebuildFile{Suffix: suffix, Identity: identity})
	}
	identity, err := fspath.EntryIdentity(replacement)
	if err != nil {
		return err
	}
	record.ReplacementIdentity = identity
	record.State = "prepared"
	return writeRebuildJournal(path, *record)
}
