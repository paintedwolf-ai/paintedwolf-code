package backup

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/localdata"
)

// preflightUpgrade refuses an upgrade the volume cannot finish. The route
// writes its pages to the WAL and, where they are new, grows the store by as
// much again; scratch is the steps' working space. Before capture it also
// needs the recovery point: a clone diverges only by what the upgrade writes
// and the log it folds in, while a copy costs the whole store.
func preflightUpgrade(ctx context.Context, opts CreateOpts, plan migrations.Plan, capture bool) error {
	plan, err := plan.PrepareRetainedStorage(ctx, opts.SQLDB)
	if err != nil {
		return err
	}
	if plan.BoundedRetainedStorage() {
		return preflightRetainedUpgrade(ctx, opts, plan, capture)
	}
	available, known, err := availableUpgradeBytes(opts.ConfigDir)
	if err != nil {
		return fmt.Errorf("inspect upgrade disk space: %w", err)
	}
	if !known {
		return nil
	}
	var pages, pageBytes uint64
	if err := opts.SQLDB.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err := opts.SQLDB.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageBytes); err != nil {
		return err
	}
	if pageBytes == 0 || pages > math.MaxUint64/pageBytes {
		return fmt.Errorf("invalid store size for upgrade")
	}
	databaseBytes := pages * pageBytes
	writes, err := plan.WriteBytes(ctx, opts.SQLDB, databaseBytes)
	if err != nil {
		return err
	}
	needed, err := sumSpace(writes, writes, plan.ScratchBytes())
	if err != nil {
		return err
	}
	if capture {
		links, err := recoveryBodyLinksAvailable(opts.ConfigDir)
		if err != nil {
			return err
		}
		payload, metadata, err := recoveryPayloadEstimate(ctx, opts.ConfigDir, databaseBytes, links)
		if err != nil {
			return err
		}
		cloned, err := fileclone.SupportedAt(opts.ConfigDir)
		if err != nil {
			return err
		}
		snapshotBytes := payload
		if cloned {
			snapshotBytes, err = sumSpace(writes, walBytes(opts.DBPath), metadata)
			if err != nil {
				return err
			}
		}
		needed, err = sumSpace(needed, snapshotBytes)
		if err != nil {
			return err
		}
	}
	return requireUpgradeSpace(needed, available)
}

// walBytes is the size of the store's log, which a cloned recovery point
// folds into itself.
func walBytes(dbPath string) uint64 {
	if dbPath == "" {
		return 0
	}
	info, err := os.Stat(dbPath + "-wal")
	if err != nil {
		return 0
	}
	size := info.Size()
	if size < 0 {
		return 0
	}
	return uint64(size)
}

func preflightRestore(ctx context.Context, opts StageOpts, manifest Manifest, localSnapshot bool) error {
	available, known, err := availableUpgradeBytes(opts.ConfigDir)
	if err != nil {
		return err
	}
	if !known {
		return nil
	}
	inventory, err := manifestInventory(manifest)
	if err != nil {
		return err
	}
	plan, err := db.PlanSchemaUpgrade(ctx, migrations.Baseline{Revision: manifest.SchemaUserVersion, Shape: manifest.SchemaShapeDigest})
	if err != nil {
		return err
	}
	liveName, err := liveStoreFilename(opts.ConfigDir, opts.DBPath)
	if err != nil {
		return err
	}
	var liveDatabase uint64
	if info, err := os.Stat(filepath.Join(opts.ConfigDir, liveName)); err == nil {
		size := info.Size()
		if size < 0 {
			return fmt.Errorf("invalid live store size")
		}
		liveDatabase = uint64(size)
	} else if !os.IsNotExist(err) {
		return err
	}
	livePayload, _, err := recoveryPayloadEstimate(ctx, opts.ConfigDir, liveDatabase, false)
	if err != nil {
		return err
	}
	var sourceDatabase, largest uint64
	for _, entry := range manifest.Files {
		if entry.Size < 0 {
			return fmt.Errorf("invalid restore file size")
		}
		largest = max(largest, uint64(entry.Size))
		if entry.RelPath == storeRelPath {
			sourceDatabase = uint64(entry.Size)
		}
	}
	cloned, err := fileclone.SupportedAt(opts.ConfigDir)
	if err != nil {
		return err
	}
	extraction := inventory.PayloadBytes
	if cloned {
		largest = 0
		livePayload = liveDatabase
		if localSnapshot {
			extraction = 0
		}
	}
	if inventory.ManifestBytes < 0 {
		return fmt.Errorf("invalid manifest size")
	}
	manifestBytes := uint64(inventory.ManifestBytes)
	needed, err := sumSpace(extraction, livePayload, largest, sourceDatabase, sourceDatabase, liveDatabase, plan.ScratchBytes(), manifestBytes, manifestBytes)
	if err != nil {
		return err
	}

	return requireUpgradeSpace(needed, available)
}

func sumSpace(values ...uint64) (uint64, error) {
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return 0, fmt.Errorf("recovery space estimate overflows")
		}
		total += value
	}
	return total, nil
}

func requireUpgradeSpace(needed, available uint64) error {
	if available < needed {
		return &db.InsufficientSpaceError{NeededBytes: needed, AvailableBytes: available}
	}
	return nil
}

func recoveryPayloadEstimate(ctx context.Context, root string, databaseBytes uint64, shareBodies bool) (uint64, uint64, error) {
	files := map[string]archiveSource{}
	branches := branchCapture{all: true, roots: map[string]bool{}}
	for _, dir := range localdata.BackupRelDirs() {
		if err := collectDurableDir(ctx, root, dir, files, branches, math.MaxInt); err != nil {
			return 0, 0, err
		}
	}
	for _, rel := range localdata.BackupRelPaths() {
		if rel == storeRelPath {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return 0, 0, err
		}
		files[rel] = archiveSource{path: path}
	}
	total, metadata := databaseBytes, uint64(16<<10)
	for rel, file := range files {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		info, err := os.Lstat(file.path)
		if err != nil {
			return 0, 0, err
		}
		size := info.Size()
		if size < 0 {
			return 0, 0, fmt.Errorf("invalid retained history size")
		}
		if !shareBodies || !sharedRecoveryBodyOnVolume(root, file) {
			total, err = sumSpace(total, uint64(size))
		}
		if err != nil {
			return 0, 0, err
		}
		metadata, err = sumSpace(metadata, 2048, 12*uint64(len(rel)))
		if err != nil {
			return 0, 0, err
		}
	}
	for _, branch := range branches.names() {
		var err error
		metadata, err = sumSpace(metadata, 2048, 12*uint64(len(branch)))
		if err != nil {
			return 0, 0, err
		}
	}
	total, err := sumSpace(total, metadata)
	return total, metadata, err
}

// The retained replacement has no live writers. Its actual SQLite allocation
// is capped after capture, with separate temporary-work and sharing reservations.
func preflightRetainedUpgrade(ctx context.Context, opts CreateOpts, plan migrations.Plan, capture bool) error {
	available, known, err := availableUpgradeBytes(opts.ConfigDir)
	if err != nil || !known {
		return err
	}
	storage, err := db.InspectUpgradeStorage(ctx, opts.SQLDB, opts.DBPath, false, opts.SharedOriginal)
	if err != nil {
		return err
	}
	needed, err := storage.RequiredBytes(plan.ScratchBytes())
	if err != nil {
		return err
	}
	if capture {
		links, err := recoveryBodyLinksAvailable(opts.ConfigDir)
		if err != nil {
			return err
		}
		payload, _, err := recoveryPayloadEstimate(ctx, opts.ConfigDir, storage.Pages*storage.PageBytes, links)
		if err != nil {
			return err
		}
		snapshotClone, err := fileclone.SupportedFrom(ctx, opts.DBPath, opts.ConfigDir)
		if err != nil {
			return err
		}
		needed, err = retainedCaptureBytes(needed, payload, storage.Pages*storage.PageBytes, snapshotClone)
		if err != nil {
			return err
		}
	}
	return requireUpgradeSpace(needed, available)
}

// Snapshot-copy allocation is independent of already shared original pages.
func retainedCaptureBytes(reserved, payload, database uint64, snapshotClone bool) (uint64, error) {
	if snapshotClone {
		if payload < database {
			return 0, fmt.Errorf("recovery payload is smaller than its database")
		}
		payload -= database
	}
	return sumSpace(reserved, payload)
}
