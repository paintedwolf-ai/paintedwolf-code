package sourceledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
)

// RecoveryTreeName does not let an operation identity become a filesystem path.
func RecoveryTreeName(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}

// RecoveryTreeDirectory belongs to the recovery owner, rather than the generic
// archive inventory. Backups materialize its active claims into blob manifests.
const RecoveryTreeDirectory = "retained-trees"

// RecoveryTreeRoot is private custody for same-filesystem retained entries.
func (s *Retention) RecoveryTreeRoot() string {
	return filepath.Join(s.objects.Root(), RecoveryTreeDirectory)
}

const retainedTreeClaims = `SELECT json_extract(undo_plan_json,'$.recovery_id') FROM source_history_entries WHERE availability='available' AND json_type(undo_plan_json,'$.retained_tree')='object'
 UNION SELECT json_extract(redo_plan_json,'$.recovery_id') FROM source_history_entries WHERE availability='available' AND json_type(redo_plan_json,'$.retained_tree')='object'
 UNION SELECT json_extract(plan_json,'$.recovery_id') FROM source_mutations WHERE status IN ('prepared','file_applied','failed','diverged') AND json_type(plan_json,'$.retained_tree')='object'`

// Retained slots share the source-content capture/backup lease. A failed or
// interrupted publication remains a claim until its journal is settled.
func (s *Retention) reclaimRecoveryTrees(ctx context.Context) error {
	release, ok, err := s.objects.TryAcquireMaintenanceLease()
	if err != nil {
		return err
	}
	if !ok {
		return ErrBlobMaintenanceDeferred
	}
	defer release()
	rows, err := s.sqlDB.QueryContext(ctx, retainedTreeClaims)
	if err != nil {
		return err
	}
	retained := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		retained[RecoveryTreeName(id)] = true
	}
	scanErr := rows.Err()
	if err := errors.Join(scanErr, rows.Close()); err != nil {
		return err
	}
	return PruneRecoveryTrees(ctx, s.RecoveryTreeRoot(), retained)
}

// PruneRecoveryTrees removes unclaimed entries under the private custody root.
// Its caller must exclude new captures while references and entries are read.
func PruneRecoveryTrees(ctx context.Context, root string, retained map[string]bool) error {
	directory, err := os.Open(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if retained[entry.Name()] {
				continue
			}
			// Unknown files never become deletion capabilities.
			if len(entry.Name()) != sha256.Size*2 {
				continue
			}
			if _, err := hex.DecodeString(entry.Name()); err != nil {
				continue
			}
			identity, err := fspath.EntryIdentity(filepath.Join(root, entry.Name()))
			if err != nil {
				return err
			}
			if err := fseffect.RemoveTreeGuarded(fseffect.Location{Root: root, Rel: entry.Name()}, identity); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
