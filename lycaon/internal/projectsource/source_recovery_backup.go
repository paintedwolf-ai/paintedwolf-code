package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// MaterializeRecoveryBackup changes only the private captured store and content
// directory. The caller holds the live source-content reference lease throughout
// capture. The archive keeps the existing blob-manifest representation; original
// retained entries, live journal rows and live history are never changed here.
func MaterializeRecoveryBackup(ctx context.Context, captured db.Handle, liveContent, capturedContent string, admit func(int64, int64) error) (err error) {
	memory, err := newSourceOperationMemory()
	if err != nil {
		return err
	}

	ledger := sourceledger.New(captured, capturedContent)
	defer func() { err = errors.Join(err, ledger.Snapshots.Close()) }()
	recovery := &sourceRecovery{db: captured, objects: ledger.Retention}
	materialize := func(plan *sourceMutationPlan) error {
		if plan.RetainedTree == nil {
			return nil
		}
		if plan.EntryRecoveryKey == "" || plan.EntryRecoveryKey != plan.RecoveryID {
			return ErrSourceRecoveryFailed
		}
		var count int64
		if err := captured.QueryRowContext(ctx, `SELECT count(*) FROM source_recovery_entries WHERE project_id=? AND recovery_id=?`, plan.ProjectID, plan.RecoveryID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			path := filepath.Join(liveContent, sourceledger.RecoveryTreeDirectory, sourceledger.RecoveryTreeName(plan.RecoveryID))
			if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
				// An undone tree is in its original location. It is usable only
				// while that exact entry still holds the reviewed content.
				path = plan.AbsPath
			} else if statErr != nil {
				return statErr
			}
			if err := requireSourceIdentity(path, plan.RetainedTree.Identity); err != nil {
				return &sourceeffect.RecoveryUnavailableError{Cause: ErrSourceMutationDiverged}
			}
			if err := requireSourceFingerprint(ctx, path, plan.TreeSHA, memory); err != nil {
				if errors.Is(err, ErrSourceMutationDiverged) || os.IsNotExist(err) {
					return &sourceeffect.RecoveryUnavailableError{Cause: ErrSourceMutationDiverged}
				}
				return err
			}
			contentBytes := plan.BeforeSize
			if plan.Kind == "restore" {
				contentBytes = plan.AfterSize
			}
			if plan.RecoveryID == "" || contentBytes < 0 || admit == nil {
				return ErrSourceRecoveryFailed
			}
			if err := admit(contentBytes, plan.RetainedTree.AllocatedBytes); err != nil {
				return err
			}
			// The existing writer admits every byte before storing it and
			// verifies the complete tree fingerprint before accepting capture.
			copy := *plan
			copy.Kind, copy.RootPath, copy.Path, copy.AbsPath = "delete", filepath.Dir(path), filepath.Base(path), path
			recovery.quota = func() int64 { return contentBytes }
			if err := recovery.captureRecovery(ctx, &copy, path, memory); err != nil {
				return err
			}
			if err := requireSourceIdentity(path, plan.RetainedTree.Identity); err != nil {
				return err
			}
			count = copy.RecoveryCount
		}
		check := *plan
		check.RecoveryCount = count
		digest := newSourceTreeDigest()
		if err := recovery.walkRecovery(ctx, &check, false, digest.append); err != nil {
			return err
		}
		if digest.sum() != plan.TreeSHA {
			return ErrSourceRecoveryFailed
		}
		plan.RecoveryCount, plan.RetainedTree = count, nil
		return nil
	}
	if err := materializeHistory(ctx, captured, materialize); err != nil {
		return err
	}
	return materializePending(ctx, captured, materialize)
}

func materializeHistory(ctx context.Context, captured db.Handle, materialize func(*sourceMutationPlan) error) error {
	var after int64
	for {
		var seq int64
		var undoRaw, redoRaw string
		err := captured.QueryRowContext(ctx, `SELECT seq,undo_plan_json,redo_plan_json FROM source_history_entries WHERE seq>? AND availability='available' AND (json_type(undo_plan_json,'$.retained_tree')='object' OR json_type(redo_plan_json,'$.retained_tree')='object') ORDER BY seq LIMIT 1`, after).Scan(&seq, &undoRaw, &redoRaw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		after = seq
		var undo, redo sourceMutationPlan
		if err := json.Unmarshal([]byte(undoRaw), &undo); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(redoRaw), &redo); err != nil {
			return err
		}
		if undo.RetainedTree != nil && redo.RetainedTree != nil && (*undo.RetainedTree != *redo.RetainedTree || undo.RecoveryID != redo.RecoveryID || undo.TreeSHA != redo.TreeSHA) {
			return ErrSourceRecoveryFailed
		}
		for _, plan := range []*sourceMutationPlan{&undo, &redo} {
			if err := materialize(plan); err != nil {
				var unavailable *sourceeffect.RecoveryUnavailableError
				if !errors.As(err, &unavailable) {
					return err
				}
				if _, markErr := captured.ExecContext(ctx, `UPDATE source_history_entries SET availability='unavailable' WHERE seq=?`, seq); markErr != nil {
					return markErr
				}
				break
			}
		}
		if err != nil {
			return err
		}
		if undo.RetainedTree != nil || redo.RetainedTree != nil {
			continue
		}
		undoJSON, err := json.Marshal(undo)
		if err != nil {
			return err
		}
		redoJSON, err := json.Marshal(redo)
		if err != nil {
			return err
		}
		if _, err := captured.ExecContext(ctx, `UPDATE source_history_entries SET undo_plan_json=?,redo_plan_json=? WHERE seq=?`, string(undoJSON), string(redoJSON), seq); err != nil {
			return err
		}
	}
}

func materializePending(ctx context.Context, captured db.Handle, materialize func(*sourceMutationPlan) error) error {
	var after string
	for {
		var id, raw string
		err := captured.QueryRowContext(ctx, `SELECT id,plan_json FROM source_mutations WHERE id>? AND status IN ('prepared','file_applied','failed','diverged') AND json_type(plan_json,'$.retained_tree')='object' ORDER BY id LIMIT 1`, after).Scan(&id, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		after = id
		var plan sourceMutationPlan
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			return err
		}
		// A pending journal claim cannot be rewritten as unavailable history:
		// recovery may still need it to settle an interrupted source effect.
		if err := materialize(&plan); err != nil {
			return err
		}
		encoded, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		if _, err := captured.ExecContext(ctx, `UPDATE source_mutations SET plan_json=? WHERE id=?`, string(encoded), id); err != nil {
			return err
		}
	}
}
