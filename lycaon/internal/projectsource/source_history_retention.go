package projectsource

import (
	"context"
	"database/sql"
	"math"
	"sort"
)

// A history entry can retain two different snapshots. A shared recovery ID is
// counted once; native Trash receipts retain no source-blob content.
type historyRecoveryUsage struct {
	id    string
	bytes int64
}

func recoveryUsage(plan sourceMutationPlan) historyRecoveryUsage {
	if plan.NativeTrash != nil || plan.RecoveryCount == 0 {
		return historyRecoveryUsage{}
	}
	return historyRecoveryUsage{id: plan.RecoveryID, bytes: max(plan.BeforeSize, plan.AfterSize)}
}

func retainedRecoveryBytes(undo, redo historyRecoveryUsage) int64 {
	if undo.id == "" {
		undo.bytes = 0
	}
	if redo.id == "" {
		redo.bytes = 0
	}
	if undo.id == redo.id {
		return max(undo.bytes, redo.bytes)
	}
	if undo.bytes > math.MaxInt64-redo.bytes {
		return math.MaxInt64
	}
	return undo.bytes + redo.bytes
}

func pruneSourceHistoryRecoveryQuotaTx(ctx context.Context, tx *sql.Tx, projectID string, quotaBytes int64) error {
	if quotaBytes <= 0 {
		quotaBytes = DefaultSourceHistoryRecoveryQuotaBytes
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,
 COALESCE(json_extract(undo_plan_json,'$.recovery_id'),''),
 CASE WHEN COALESCE(json_type(undo_plan_json,'$.native_trash'),'null')='null' AND COALESCE(json_extract(undo_plan_json,'$.recovery_count'),0)>0 THEN MAX(COALESCE(json_extract(undo_plan_json,'$.before_size'),0),COALESCE(json_extract(undo_plan_json,'$.after_size'),0)) ELSE 0 END,
 COALESCE(json_extract(redo_plan_json,'$.recovery_id'),''),
 CASE WHEN COALESCE(json_type(redo_plan_json,'$.native_trash'),'null')='null' AND COALESCE(json_extract(redo_plan_json,'$.recovery_count'),0)>0 THEN MAX(COALESCE(json_extract(redo_plan_json,'$.before_size'),0),COALESCE(json_extract(redo_plan_json,'$.after_size'),0)) ELSE 0 END
 FROM source_history_entries WHERE project_id=? ORDER BY seq DESC`, projectID)
	if err != nil {
		return err
	}
	var total int64
	var evict []string
	newest := true
	for rows.Next() {
		var id string
		var undo, redo historyRecoveryUsage
		if err := rows.Scan(&id, &undo.id, &undo.bytes, &redo.id, &redo.bytes); err != nil {
			_ = rows.Close()
			return err
		}
		size := retainedRecoveryBytes(undo, redo)
		// An already-applied operation may be recovered under a lower quota.
		// Preserve its promised newest Undo; new captures cannot exceed quota.
		if !newest && size > 0 && size > max(0, quotaBytes-total) {
			evict = append(evict, id)
		} else {
			total += size
		}
		newest = false
	}
	scanErr := rows.Err()
	if err := rows.Close(); err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	for _, id := range evict {
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_history_entries WHERE project_id=? AND id=?`, projectID, id); err != nil {
			return err
		}
	}
	return nil
}

// The caller holds stateMu, including the quota snapshot and pruning decision.
func (s *SourceHistory) pruneSourceHistoryMemory(projectID string) {
	var entries []*sourceHistoryEntry
	for _, entry := range s.history {
		if entry.ProjectID == projectID {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq > entries[j].Seq })
	quota := s.quotaBytes
	if quota <= 0 {
		quota = DefaultSourceHistoryRecoveryQuotaBytes
	}
	var total int64
	for i, entry := range entries {
		size := retainedRecoveryBytes(recoveryUsage(entry.UndoPlan), recoveryUsage(entry.RedoPlan))
		if i >= sourceHistoryLimit || (i > 0 && size > 0 && size > max(0, quota-total)) {
			delete(s.history, entry.ID)
		} else {
			total += size
		}
	}
}

// Memory reclamation observes history and pending receipts only after all
// captures have published or released their references.
func (s *SourceMutationService) reclaimMemoryRecovery() {
	if !s.recovery.lifecycle.TryLock() {
		return
	}
	defer s.recovery.lifecycle.Unlock()
	retained := map[string]bool{}
	s.History.stateMu.Lock()
	for _, entry := range s.History.history {
		retained[entry.UndoPlan.RecoveryID], retained[entry.RedoPlan.RecoveryID] = true, true
	}
	s.History.stateMu.Unlock()
	s.Journal.stateMu.Lock()
	for _, row := range s.Journal.memory {
		if row.Status != sourceMutationCommitted {
			retained[row.Plan.RecoveryID] = true
		}
	}
	s.Journal.stateMu.Unlock()
	s.recovery.stateMu.Lock()
	defer s.recovery.stateMu.Unlock()
	objects := map[string]bool{}
	for id, entries := range s.recovery.recoveryEntries {
		if !retained[id] {
			delete(s.recovery.recoveryEntries, id)
			continue
		}
		for _, entry := range entries {
			objects[entry.SHA] = true
		}
	}
	for sha := range s.recovery.recoveryBytes {
		if !objects[sha] {
			delete(s.recovery.recoveryBytes, sha)
		}
	}
}
