package backup

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/localdata"
)

func preflightUpgrade(ctx context.Context, opts CreateOpts, scratch uint64, capture bool) error {
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
	needed, err := sumSpace(databaseBytes, databaseBytes, scratch)
	if err != nil {
		return err
	}
	if capture {
		payload, metadata, err := recoveryPayloadEstimate(ctx, opts.ConfigDir, databaseBytes)
		if err != nil {
			return err
		}
		cloned, err := recoveryCloneAvailable(opts.ConfigDir)
		if err != nil {
			return err
		}
		snapshotBytes := payload
		if cloned {
			snapshotBytes, err = sumSpace(databaseBytes, metadata)
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
	livePayload, _, err := recoveryPayloadEstimate(ctx, opts.ConfigDir, liveDatabase)
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
	cloned, err := recoveryCloneAvailable(opts.ConfigDir)
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
		return fmt.Errorf("recovery and database work need approximately %.1f GiB free; %.1f GiB is available", float64(needed)/(1<<30), float64(available)/(1<<30))
	}
	return nil
}

func recoveryPayloadEstimate(ctx context.Context, root string, databaseBytes uint64) (uint64, uint64, error) {
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
		total, err = sumSpace(total, uint64(size))
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
