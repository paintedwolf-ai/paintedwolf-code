package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
)

func beginRebuild(path string) (rebuildJournal, error) {
	var record rebuildJournal
	if _, err := os.Lstat(rebuildMarker(path)); !os.IsNotExist(err) { // #nosec G703
		return record, fmt.Errorf("store upgrade journal must be resolved before rebuilding")
	}
	work, err := os.MkdirTemp(filepath.Dir(path), StoreUpgradeDirectoryPrefix)
	if err != nil {
		return record, err
	}
	if err := probeRebuildExchange(filepath.Dir(path), work, fseffect.ExchangeGuarded); err != nil {
		return record, errors.Join(fmt.Errorf("store volume cannot atomically publish an upgraded database: %w", err), os.Remove(work)) // #nosec G703
	}
	identity, err := fspath.EntryIdentity(work)
	if err != nil {
		return record, errors.Join(err, os.Remove(work)) // #nosec G703
	}
	record = rebuildJournal{Version: 2, Database: filepath.Base(path), Directory: filepath.Base(work), DirectoryIdentity: identity, State: "building"}
	if err := writeRebuildJournal(path, record); err != nil {
		return record, errors.Join(err, discardRebuild(path, record))
	}
	return record, nil
}

func upgradeRetained(ctx context.Context, path string, source *sql.DB, plan migrations.Plan, hooks UpgradeHooks, staged bool, registry *migrations.Registry) (err error) {
	if !staged && hooks.Before == nil {
		return fmt.Errorf("schema upgrade requires installation recovery capture")
	}
	plan, err = plan.PrepareRetainedStorage(ctx, source)
	if err != nil {
		return err
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
	// Fold the existing WAL before copying so source reads do not resolve every
	// page through it. SQLite's checkpoint preserves the original logical store.
	if err := checkpointRebuildSource(ctx, path); err != nil {
		return err
	}
	if err := source.Close(); err != nil {
		return err
	}
	if err := sealRebuiltStore(ctx, path); err != nil {
		return err
	}
	work := filepath.Join(filepath.Dir(path), record.Directory)
	replacement := filepath.Join(work, "replacement.db")
	if hooks.Progress != nil {
		hooks.Progress(migrations.Migrating)
	}
	prepared, err := copyRetainedStore(ctx, path, replacement, plan, hooks.Progress)
	if err != nil {
		return err
	}
	// Recovery capture verifies the sealed released-shape image and complete
	// installation payload. Whole-file copies retain source bytes directly;
	// reconstructed images receive structural validation before capture.
	if err := captureRetainedRecovery(ctx, replacement, plan, hooks, staged, prepared); err != nil {
		return err
	}
	if err := executeRetainedRoute(ctx, replacement, plan, hooks.Progress, registry, staged, prepared.sharedOriginal); err != nil {
		return err
	}
	if err := sealRebuiltStore(ctx, replacement); err != nil {
		return err
	}
	if err := prepareRebuildPublication(path, replacement, &record); err != nil {
		return err
	}
	// Exchange publishes the commit atomically. Filesystem failures preserve the
	// journal and both files so restart can determine whether it committed.
	if err := finishRebuildPublication(path, record, nil); err != nil {
		return err
	}
	if staged {
		return CompleteStoreUpgrade(path)
	}
	return nil
}

func captureRetainedRecovery(ctx context.Context, replacement string, plan migrations.Plan, hooks UpgradeHooks, staged bool, prepared retainedCopyResult) (err error) {
	reader, err := openReader(ctx, replacement)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reader.Close()) }()
	if hooks.Progress != nil {
		hooks.Progress(migrations.Validating)
	}
	if prepared.preparedIdentity != "" {
		if err := requireRebuildIdentity(replacement, prepared.preparedIdentity); err != nil {
			return err
		}
	} else if err := validateUpgradeStructure(ctx, reader); err != nil {
		return err
	}
	if staged {
		return nil
	}
	if hooks.Progress != nil {
		hooks.Progress(migrations.Snapshotting)
	}
	return hooks.Before(ctx, UpgradeSource{DB: reader, SharedOriginal: prepared.sharedOriginal}, plan)
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
	return syncSealedStore(path, fssync.File)
}

func syncSealedStore(path string, syncFile func(*os.File) error) error {
	for _, name := range []string{path, filepath.Dir(path)} {
		file, err := os.Open(name) // #nosec G304,G703
		if err != nil {
			return err
		}
		if err := errors.Join(syncFile(file), file.Close()); err != nil {
			return err
		}
	}
	return nil
}

func prepareRebuildPublication(path, replacement string, record *rebuildJournal) error {
	if err := requireSealedRebuild(path); err != nil {
		return err
	}
	if err := requireSealedRebuild(replacement); err != nil {
		return err
	}
	identity, err := fspath.EntryIdentity(path)
	if err != nil {
		return err
	}
	record.OriginalIdentity = identity
	identity, err = fspath.EntryIdentity(replacement)
	if err != nil {
		return err
	}
	record.ReplacementIdentity = identity
	record.State = "prepared"
	return writeRebuildJournal(path, *record)
}

// Keep the inspected reader open until checkpoint completes, retaining its WAL
// index instead of recovering the large log again on a fresh connection.
func checkpointRebuildSource(ctx context.Context, path string) (err error) {
	writer, err := sql.Open("sqlite", fileDSN(path, "_pragma=synchronous(full)"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	result, err := checkpointDatabase(ctx, writer, WALCheckpointTruncate)
	if err != nil {
		return err
	}
	if !result.Complete() {
		return fmt.Errorf("source checkpoint still has active readers")
	}
	return nil
}

// Exercise the same cross-directory exchange as publication, before touching
// SQLite. These empty, exclusively created entries contain no user data.
func probeRebuildExchange(root, work string, exchange func(string, string, string, string, string) error) (err error) {
	first, err := os.CreateTemp(root, ".store-exchange-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(first.Name())) }() // #nosec G703
	if err := first.Close(); err != nil {
		return err
	}
	second, err := os.CreateTemp(work, ".store-exchange-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(second.Name())) }() // #nosec G703
	if err := second.Close(); err != nil {
		return err
	}
	firstIdentity, err := fspath.EntryIdentity(first.Name())
	if err != nil {
		return err
	}
	secondIdentity, err := fspath.EntryIdentity(second.Name())
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, second.Name())
	if err != nil {
		return err
	}
	err = exchange(root, filepath.Base(first.Name()), relative, firstIdentity, secondIdentity)
	// Some volumes report unsupported exchange flags as EINVAL. Both paths
	// and identities above were created here, so retain the cause and classify
	// this specific operation as unsupported without reclassifying other EINVALs.
	if errors.Is(err, syscall.EINVAL) {
		return errors.Join(fseffect.ErrUnsupported, err)
	}
	return err
}
