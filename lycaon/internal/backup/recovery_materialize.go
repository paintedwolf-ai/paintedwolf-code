package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectsource"
)

// The captured database and the publication lease, rather than a walk of the
// live history, determine which retained trees must enter this backup.
func materializeRecovery(ctx context.Context, configDir, snapshotPath, capturedDir string) (err error) {
	read, err := db.OpenReadOnly(ctx, snapshotPath)
	if err != nil {
		return err
	}
	var revision int
	if err := read.QueryRowContext(ctx, "PRAGMA user_version").Scan(&revision); err != nil {
		return errors.Join(err, read.Close())
	}
	if revision < 7 {
		return read.Close()
	}
	var claims int
	err = read.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM source_history_entries WHERE availability='available' AND (json_type(undo_plan_json,'$.retained_tree')='object' OR json_type(redo_plan_json,'$.retained_tree')='object')) +
 (SELECT count(*) FROM source_mutations WHERE status IN ('prepared','file_applied','failed','diverged') AND json_type(plan_json,'$.retained_tree')='object')`).Scan(&claims)
	err = errors.Join(err, read.Close())
	if err != nil || claims == 0 {
		return err
	}
	captured, err := db.OpenWithOptions(ctx, snapshotPath, db.UpgradeHooks{}, db.StoreOptions{})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, captured.Close()) }()
	return projectsource.MaterializeRecoveryBackup(ctx, captured,
		filepath.Join(configDir, enginepaths.SourceContentDirName),
		filepath.Join(capturedDir, enginepaths.SourceContentDirName),
		func(content, allocated int64) error {
			if content < 0 || allocated < 0 {
				return fmt.Errorf("invalid retained recovery size")
			}
			available, known, err := availableUpgradeBytes(capturedDir)
			if err != nil {
				return err
			}
			if !known {
				return fmt.Errorf("cannot admit recovery capture without destination storage facts")
			}
			// Admit against the actual destination volume before capture. The
			// writer separately bounds plaintext bytes and propagates storage
			// failures; a partial capture is never published as a backup.
			needed, err := sumSpace(uint64(content), uint64(content), uint64(allocated))
			if err != nil {
				return err
			}
			return requireUpgradeSpace(needed, available)
		})
}
