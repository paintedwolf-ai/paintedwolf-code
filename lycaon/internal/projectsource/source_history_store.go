package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/db"
	"sort"
	"time"
)

func commitSourceHistoryTx(ctx context.Context, tx *sql.Tx, plan sourceMutationPlan, entry *sourceHistoryEntry, now time.Time) error {
	if plan.HistoryEntryID != "" {
		from, to := historyTransitionStates(plan.HistoryTransition)
		result, err := tx.ExecContext(ctx, `UPDATE source_history_entries SET state=?, updated_at=? WHERE id=? AND state=?`,
			to, now.Format(time.RFC3339Nano), plan.HistoryEntryID, from)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrSourceHistoryChanged
		}
		if plan.NativeTrash != nil {
			column := "redo_plan_json"
			if plan.HistoryTransition == "redo" {
				column = "undo_plan_json"
			}
			receipt, marshalErr := json.Marshal(plan.NativeTrash)
			if marshalErr != nil {
				return marshalErr
			}
			_, err = tx.ExecContext(ctx, `UPDATE source_history_entries SET `+column+`=json_set(`+column+`,'$.native_trash',json(?),'$.entry_identity',?) WHERE id=?`, string(receipt), plan.DestinationIdentity, plan.HistoryEntryID)
			return err
		}
		if plan.Kind == "rename" && plan.CrossVolume {
			column := "redo_plan_json"
			if plan.HistoryTransition == "redo" {
				column = "undo_plan_json"
			}
			_, err = tx.ExecContext(ctx, `UPDATE source_history_entries SET `+column+`=json_set(`+column+`,'$.entry_identity',?) WHERE id=?`, plan.DestinationIdentity, plan.HistoryEntryID)
			return err
		}
		return nil
	}
	if entry == nil {
		return nil
	}
	undoJSON, err := json.Marshal(entry.UndoPlan)
	if err != nil {
		return err
	}
	redoJSON, err := json.Marshal(entry.RedoPlan)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_history_entries WHERE project_id=? AND state='undone'`, entry.ProjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO source_history_entries(id, project_id, kind, state, undo_label, redo_label, undo_plan_json, redo_plan_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		entry.ID, entry.ProjectID, entry.Kind, entry.State, entry.UndoLabel, entry.RedoLabel,
		string(undoJSON), string(redoJSON), entry.CreatedAt.Format(time.RFC3339Nano), entry.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM source_history_entries WHERE project_id=? AND seq NOT IN (SELECT seq FROM source_history_entries WHERE project_id=? ORDER BY seq DESC LIMIT ?)`,
		entry.ProjectID, entry.ProjectID, sourceHistoryLimit)
	if err != nil {
		return err
	}
	return pruneSourceRecoveryRows(ctx, tx, entry.ProjectID)
}

func (s *SourceHistory) commitSourceHistoryMemory(plan sourceMutationPlan, entry *sourceHistoryEntry) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if plan.HistoryEntryID != "" {
		held := s.history[plan.HistoryEntryID]
		from, to := historyTransitionStates(plan.HistoryTransition)
		if held == nil || held.State != from {
			return ErrSourceHistoryChanged
		}
		held.State, held.UpdatedAt = to, time.Now().UTC()
		if plan.NativeTrash != nil {
			opposite := &held.RedoPlan
			if plan.HistoryTransition == "redo" {
				opposite = &held.UndoPlan
			}
			copy := *plan.NativeTrash
			opposite.NativeTrash = &copy
			if plan.Kind == "restore" {
				opposite.EntryIdentity = plan.DestinationIdentity
			}
		}
		if plan.Kind == "rename" && plan.CrossVolume {
			if plan.HistoryTransition == "redo" {
				held.UndoPlan.EntryIdentity = plan.DestinationIdentity
			} else {
				held.RedoPlan.EntryIdentity = plan.DestinationIdentity
			}
		}
		return nil
	}
	if entry == nil {
		return nil
	}
	for id, held := range s.history {
		if held.ProjectID == entry.ProjectID && held.State == "undone" {
			delete(s.history, id)
		}
	}
	s.historySeq++
	copy := *entry
	copy.Seq = s.historySeq
	s.history[copy.ID] = &copy
	s.pruneSourceHistoryMemory(copy.ProjectID)
	return nil
}

func (s *SourceHistory) pruneSourceHistoryMemory(projectID string) {
	entries := make([]*sourceHistoryEntry, 0)
	for _, entry := range s.history {
		if entry.ProjectID == projectID {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq > entries[j].Seq })
	if len(entries) <= sourceHistoryLimit {
		return
	}
	for _, entry := range entries[sourceHistoryLimit:] {
		delete(s.history, entry.ID)
	}
}

func historyTransitionStates(transition string) (string, string) {
	if transition == "redo" {
		return "undone", "applied"
	}
	return "applied", "undone"
}

func (s *SourceHistory) State(ctx context.Context, projectID string) (SourceHistoryState, error) {
	return s.readSourceHistory(ctx, projectID)
}

func (s *SourceHistory) readSourceHistory(ctx context.Context, projectID string) (SourceHistoryState, error) {
	undo, err := s.historyEntry(ctx, projectID, "applied", "DESC")
	if err != nil {
		return SourceHistoryState{}, err
	}
	redo, err := s.historyEntry(ctx, projectID, "undone", "ASC")
	if err != nil {
		return SourceHistoryState{}, err
	}
	state := SourceHistoryState{}
	if undo != nil {
		action := undo.action("undo")
		state.Undo = &action
	}
	if redo != nil {
		action := redo.action("redo")
		state.Redo = &action
	}
	return state, nil
}

func (s *SourceHistory) historyEntry(ctx context.Context, projectID, state, order string) (*sourceHistoryEntry, error) {
	if s.db == nil {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		var selected *sourceHistoryEntry
		for _, entry := range s.history {
			if entry.ProjectID != projectID || entry.State != state {
				continue
			}
			if selected == nil || (order == "DESC" && entry.Seq > selected.Seq) || (order == "ASC" && entry.Seq < selected.Seq) {
				copy := *entry
				selected = &copy
			}
		}
		return selected, nil
	}
	query := `SELECT seq,id,project_id,kind,state,undo_label,redo_label,undo_plan_json,redo_plan_json,created_at,updated_at FROM source_history_entries WHERE project_id=? AND state=? ORDER BY seq ` + order + ` LIMIT 1`
	entry := &sourceHistoryEntry{}
	var undoJSON, redoJSON, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, query, projectID, state).Scan(
		&entry.Seq, &entry.ID, &entry.ProjectID, &entry.Kind, &entry.State,
		&entry.UndoLabel, &entry.RedoLabel, &undoJSON, &redoJSON, &createdAt, &updatedAt)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(undoJSON), &entry.UndoPlan); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(redoJSON), &entry.RedoPlan); err != nil {
		return nil, err
	}
	entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	entry.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return entry, nil
}

func (entry sourceHistoryEntry) action(direction string) SourceHistoryAction {
	plan, label := entry.UndoPlan, entry.UndoLabel
	if direction == "redo" {
		plan, label = entry.RedoPlan, entry.RedoLabel
	}
	return SourceHistoryAction{
		ID: entry.ID, Label: label, Kind: entry.Kind, RootID: plan.RootID,
		Path: plan.Path, FromPath: plan.FromPath, ToPath: plan.ToPath,
		IsDir: plan.EntryKind == SourceEntryFolder, RemovesPath: plan.Kind == "delete" || plan.Kind == "rename",
	}
}
