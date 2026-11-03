package projectsource

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
)

// A reclaim flag becomes durable only when mutation and history agree.
func persistHistoryTrashState(ctx context.Context, database db.Handle, entry *historyReclaimEntry) error {
	dbCtx := context.WithoutCancel(ctx)
	undoJSON, err := json.Marshal(entry.undoPlan)
	if err != nil {
		return err
	}
	redoJSON, err := json.Marshal(entry.redoPlan)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(dbCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(dbCtx, `UPDATE source_history_entries SET undo_plan_json=?, redo_plan_json=? WHERE id=?`, string(undoJSON), string(redoJSON), entry.id); err != nil {
		return err
	}
	if entry.undoPlan.NativeTrash != nil {
		trashJSON, err := json.Marshal(entry.undoPlan.NativeTrash)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(dbCtx, `UPDATE source_mutations SET plan_json=json_set(plan_json,'$.native_trash',json(?)) WHERE id=? AND json_type(plan_json,'$.native_trash')='object'`, string(trashJSON), entry.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func persistMutationTrashState(ctx context.Context, database db.Handle, id string, trash *sourceTrashRecovery) error {
	if trash == nil {
		return nil
	}
	dbCtx := context.WithoutCancel(ctx)
	trashJSON, err := json.Marshal(trash)
	if err != nil {
		return err
	}
	tx, err := database.BeginTx(dbCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(dbCtx, `UPDATE source_mutations SET plan_json=json_set(plan_json,'$.native_trash',json(?)) WHERE id=? AND json_type(plan_json,'$.native_trash')='object'`, string(trashJSON), id); err != nil {
		return err
	}
	if trash.Reclaimed {
		if _, err := tx.ExecContext(dbCtx, `UPDATE source_history_entries SET undo_plan_json=json_set(undo_plan_json,'$.native_trash',json(?)) WHERE id=? AND json_type(undo_plan_json,'$.native_trash')='object'`, string(trashJSON), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(dbCtx, `UPDATE source_history_entries SET redo_plan_json=json_set(redo_plan_json,'$.native_trash',json(?)) WHERE id=? AND json_type(redo_plan_json,'$.native_trash')='object'`, string(trashJSON), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
