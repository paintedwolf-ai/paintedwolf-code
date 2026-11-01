package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceeffect"
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

func addSatInt64(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
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
	defer func() { _ = rows.Close() }()
	var total int64
	var evict []string
	newest := true
	for rows.Next() {
		var id string
		var undo, redo historyRecoveryUsage
		if err := rows.Scan(&id, &undo.id, &undo.bytes, &redo.id, &redo.bytes); err != nil {
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

func isActiveMovedAsidePlan(state string, plan sourceMutationPlan) bool {
	return state == "applied" && plan.Disposal == sourceDisposalDiscard && plan.NativeTrash != nil && !plan.NativeTrash.Reclaiming && !plan.NativeTrash.Reclaimed
}

func syncMovedAsideTrashPlans(undo, redo *sourceMutationPlan) {
	if undo.NativeTrash == nil || redo.NativeTrash == nil {
		return
	}
	redo.NativeTrash.AllocatedBytes = undo.NativeTrash.AllocatedBytes
	redo.NativeTrash.SizeMeasured = undo.NativeTrash.SizeMeasured
	redo.NativeTrash.Reclaiming = undo.NativeTrash.Reclaiming
	redo.NativeTrash.Reclaimed = undo.NativeTrash.Reclaimed
}

func removeRecoverySlot(slotPath, expectedIdentity string) error {
	clean := filepath.Clean(slotPath)
	parent := filepath.Dir(clean)
	base := filepath.Base(clean)
	if base == "." || base == "/" || filepath.Base(parent) != settingsoverlay.RecoveryDirName {
		return fmt.Errorf("refusing to remove recovery slot outside recovery directory: %s", slotPath)
	}
	identity, err := fspath.EntryIdentity(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expectedIdentity != "" && identity != expectedIdentity {
		return nil
	}
	err = fseffect.RemoveTreeGuarded(fseffect.Location{Root: parent, Rel: base}, identity)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func measureEntryAllocation(ctx context.Context, abs string, memory sourceeffect.Memory) (int64, error) {
	_, size, err := measureRemovalMetadata(ctx, abs, memory)
	if err != nil {
		return 0, err
	}
	return size.AllocatedBytes, nil
}

func sweepRecoveryRootOrphans(rootPath string, liveSlotPaths map[string]bool) error {
	if rootPath == "" {
		return nil
	}
	recoveryDir := settingsoverlay.RecoveryDir(rootPath)
	entries, err := os.ReadDir(recoveryDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		candidate := filepath.Clean(filepath.Join(recoveryDir, entry.Name()))
		if liveSlotPaths[candidate] {
			continue
		}
		if err := removeRecoverySlot(candidate, ""); err != nil {
			return err
		}
	}
	return nil
}

func isReclaimedHistoryOperation(ctx context.Context, s *SourceMutationService, projectID, operationID string) bool {
	if s == nil || operationID == "" {
		return false
	}
	if s.History.db == nil {
		s.History.stateMu.Lock()
		if entry := s.History.history[operationID]; entry != nil && entry.ProjectID == projectID {
			if entry.Unavailable || (entry.UndoPlan.NativeTrash != nil && (entry.UndoPlan.NativeTrash.Reclaiming || entry.UndoPlan.NativeTrash.Reclaimed)) {
				s.History.stateMu.Unlock()
				return true
			}
		}
		s.History.stateMu.Unlock()
		s.Journal.stateMu.Lock()
		defer s.Journal.stateMu.Unlock()
		row := s.Journal.memory[operationID]
		return row != nil && row.ProjectID == projectID && row.Plan.Disposal == sourceDisposalDiscard && row.Plan.NativeTrash != nil && (row.Plan.NativeTrash.Reclaiming || row.Plan.NativeTrash.Reclaimed)
	}
	var availability, undoJSON string
	err := s.History.db.QueryRowContext(ctx, `SELECT availability, undo_plan_json FROM source_history_entries WHERE project_id=? AND id=?`, projectID, operationID).Scan(&availability, &undoJSON)
	if err == nil {
		if availability == "unavailable" {
			return true
		}
		var undo sourceMutationPlan
		if json.Unmarshal([]byte(undoJSON), &undo) == nil && undo.NativeTrash != nil && (undo.NativeTrash.Reclaiming || undo.NativeTrash.Reclaimed) {
			return true
		}
	} else if !db.IsNoRows(err) {
		return false
	}
	row, found, err := s.Journal.load(ctx, operationID)
	if err != nil || !found || row.ProjectID != projectID {
		return false
	}
	return row.Plan.Disposal == sourceDisposalDiscard && row.Plan.NativeTrash != nil && (row.Plan.NativeTrash.Reclaiming || row.Plan.NativeTrash.Reclaimed)
}

type historyReclaimEntry struct {
	seq          int64
	id           string
	projectID    string
	state        string
	availability string
	undoPlan     sourceMutationPlan
	redoPlan     sourceMutationPlan
}

func reclaimDiscardedMutationSlots(ctx context.Context, database db.Handle, recoveryRoots, liveSlotPaths map[string]bool) error {
	type mutationDiscardRow struct {
		id   string
		plan sourceMutationPlan
	}
	mutRows, err := database.QueryContext(ctx, `SELECT id, plan_json FROM source_mutations WHERE status NOT IN ('prepared', 'file_applied') AND json_extract(plan_json,'$.disposal')='discard' AND json_type(plan_json,'$.native_trash')='object' ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return err
	}
	defer func() { _ = mutRows.Close() }()
	var discards []mutationDiscardRow
	for mutRows.Next() {
		var id, planJSON string
		if err := mutRows.Scan(&id, &planJSON); err != nil {
			return err
		}
		var plan sourceMutationPlan
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return err
		}
		discards = append(discards, mutationDiscardRow{id: id, plan: plan})
	}
	if err := errors.Join(mutRows.Err(), mutRows.Close()); err != nil {
		return err
	}

	for i := range discards {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := &discards[i]
		if row.plan.RootPath != "" {
			recoveryRoots[filepath.Clean(row.plan.RootPath)] = true
		}
		if row.plan.NativeTrash == nil || row.plan.NativeTrash.Reclaimed {
			continue
		}
		slotPath := filepath.Clean(row.plan.NativeTrash.Receipt.Path)
		if (slotPath == "" || slotPath == ".") && row.plan.RootPath != "" {
			slotPath = filepath.Clean(filepath.Join(settingsoverlay.RecoveryDir(row.plan.RootPath), recoverySlotName(row.id)))
		}
		if liveSlotPaths[slotPath] {
			continue
		}
		if !row.plan.NativeTrash.Reclaiming {
			row.plan.NativeTrash.Reclaiming = true
			if err := persistMutationTrashState(ctx, database, row.id, row.plan.NativeTrash); err != nil {
				return err
			}
		}
		if slotPath != "" && slotPath != "." {
			if err := removeRecoverySlot(slotPath, row.plan.NativeTrash.Receipt.Identity); err != nil {
				return err
			}
		}
		row.plan.NativeTrash.Reclaimed = true
		if err := persistMutationTrashState(ctx, database, row.id, row.plan.NativeTrash); err != nil {
			return err
		}
	}

	return nil
}

func loadHistoryReclaimEntries(ctx context.Context, history *SourceHistory) ([]*historyReclaimEntry, error) {
	rows, err := history.db.QueryContext(ctx, `SELECT seq, id, project_id, state, availability, undo_plan_json, redo_plan_json FROM source_history_entries ORDER BY seq ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []*historyReclaimEntry
	for rows.Next() {
		var e historyReclaimEntry
		var undoJSON, redoJSON string
		if err := rows.Scan(&e.seq, &e.id, &e.projectID, &e.state, &e.availability, &undoJSON, &redoJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(undoJSON), &e.undoPlan); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(redoJSON), &e.redoPlan); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return entries, nil
}

func reclaimMovedAsideRecovery(ctx context.Context, s *SourceMutationService) error {
	if s == nil || s.History.db == nil {
		return nil
	}
	s.recovery.lifecycle.Lock()
	defer s.recovery.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := loadHistoryReclaimEntries(ctx, s.History)
	if err != nil {
		return err
	}

	memory, err := newSourceOperationMemory()
	if err != nil {
		return err
	}
	byProject := make(map[string][]*historyReclaimEntry)
	recoveryRoots := make(map[string]bool)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.undoPlan.RootPath != "" {
			recoveryRoots[filepath.Clean(entry.undoPlan.RootPath)] = true
		}
		if isActiveMovedAsidePlan(entry.state, entry.undoPlan) && !entry.undoPlan.NativeTrash.SizeMeasured {
			slotPath := entry.undoPlan.NativeTrash.Receipt.Path
			var allocated int64
			if slotPath != "" {
				measured, measureErr := measureEntryAllocation(ctx, slotPath, memory)
				if measureErr != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if !errors.Is(measureErr, os.ErrNotExist) {
						return measureErr
					}
				} else {
					allocated = measured
				}
			}
			entry.undoPlan.NativeTrash.AllocatedBytes = allocated
			entry.undoPlan.NativeTrash.SizeMeasured = true
			syncMovedAsideTrashPlans(&entry.undoPlan, &entry.redoPlan)
			if err := persistHistoryTrashState(ctx, s.History.db, entry); err != nil {
				return err
			}
		}
		byProject[entry.projectID] = append(byProject[entry.projectID], entry)
	}

	quota := s.History.recoveryQuotaBytes()
	for _, projectEntries := range byProject {
		var total int64
		for _, entry := range projectEntries {
			if isActiveMovedAsidePlan(entry.state, entry.undoPlan) {
				total = addSatInt64(total, entry.undoPlan.NativeTrash.AllocatedBytes)
			} else {
				total = addSatInt64(total, retainedRecoveryBytes(recoveryUsage(entry.undoPlan), recoveryUsage(entry.redoPlan)))
			}
		}
		for _, entry := range projectEntries {
			if total <= quota {
				break
			}
			if !isActiveMovedAsidePlan(entry.state, entry.undoPlan) {
				continue
			}
			entry.undoPlan.NativeTrash.Reclaiming = true
			syncMovedAsideTrashPlans(&entry.undoPlan, &entry.redoPlan)
			total = max(0, total-entry.undoPlan.NativeTrash.AllocatedBytes)
			if err := persistHistoryTrashState(ctx, s.History.db, entry); err != nil {
				return err
			}
		}
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.undoPlan.Disposal == sourceDisposalDiscard && entry.undoPlan.NativeTrash != nil && entry.undoPlan.NativeTrash.Reclaiming && !entry.undoPlan.NativeTrash.Reclaimed {
			slotPath := filepath.Clean(entry.undoPlan.NativeTrash.Receipt.Path)
			if slotPath != "" && slotPath != "." {
				if err := removeRecoverySlot(slotPath, entry.undoPlan.NativeTrash.Receipt.Identity); err != nil {
					return err
				}
			}
			entry.undoPlan.NativeTrash.Reclaimed = true
			syncMovedAsideTrashPlans(&entry.undoPlan, &entry.redoPlan)
			if err := persistHistoryTrashState(ctx, s.History.db, entry); err != nil {
				return err
			}
		}
	}

	liveSlotPaths := make(map[string]bool)
	for _, entry := range entries {
		if isActiveMovedAsidePlan(entry.state, entry.undoPlan) && entry.undoPlan.NativeTrash.Receipt.Path != "" {
			liveSlotPaths[filepath.Clean(entry.undoPlan.NativeTrash.Receipt.Path)] = true
		}
	}

	pendingRows, err := s.History.db.QueryContext(ctx, `SELECT id, plan_json FROM source_mutations WHERE status IN ('prepared', 'file_applied')`)
	if err != nil {
		return err
	}
	defer func() { _ = pendingRows.Close() }()
	for pendingRows.Next() {
		var id, planJSON string
		if err := pendingRows.Scan(&id, &planJSON); err != nil {
			return err
		}
		var plan sourceMutationPlan
		if json.Unmarshal([]byte(planJSON), &plan) == nil {
			if plan.RootPath != "" {
				recoveryRoots[filepath.Clean(plan.RootPath)] = true
				liveSlotPaths[filepath.Clean(filepath.Join(settingsoverlay.RecoveryDir(plan.RootPath), recoverySlotName(id)))] = true
			}
			if plan.NativeTrash != nil && plan.NativeTrash.Receipt.Path != "" {
				liveSlotPaths[filepath.Clean(plan.NativeTrash.Receipt.Path)] = true
			}
		}
	}
	if err := errors.Join(pendingRows.Err(), pendingRows.Close()); err != nil {
		return err
	}

	if err := reclaimDiscardedMutationSlots(ctx, s.History.db, recoveryRoots, liveSlotPaths); err != nil {
		return err
	}

	for rootPath := range recoveryRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sweepRecoveryRootOrphans(rootPath, liveSlotPaths); err != nil {
			return err
		}
	}
	return nil
}

// Memory reclamation observes history and pending receipts only after all
// captures have published or released their references.
func measureMemoryRecoverySlots(ctx context.Context, history *SourceHistory) (map[string]int64, error) {
	type unmeasuredSlot struct {
		id   string
		path string
	}
	var toMeasure []unmeasuredSlot
	history.stateMu.Lock()
	for _, entry := range history.history {
		if isActiveMovedAsidePlan(entry.State, entry.UndoPlan) && !entry.UndoPlan.NativeTrash.SizeMeasured {
			toMeasure = append(toMeasure, unmeasuredSlot{id: entry.ID, path: entry.UndoPlan.NativeTrash.Receipt.Path})
		}
	}
	history.stateMu.Unlock()

	measuredBytes := make(map[string]int64, len(toMeasure))
	memory, err := newSourceOperationMemory()
	if err != nil {
		return nil, err
	}
	for _, item := range toMeasure {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var allocated int64
		if item.path != "" {
			bytes, err := measureEntryAllocation(ctx, item.path, memory)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			} else {
				allocated = bytes
			}
		}
		measuredBytes[item.id] = allocated
	}

	return measuredBytes, nil
}

func reclaimMemoryRecovery(ctx context.Context, s *SourceMutationService) error {
	s.recovery.lifecycle.Lock()
	defer s.recovery.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	measuredBytes, err := measureMemoryRecoverySlots(ctx, s.History)
	if err != nil {
		return err
	}

	retained := map[string]bool{}
	liveSlotPaths := map[string]bool{}
	recoveryRoots := map[string]bool{}

	s.History.stateMu.Lock()
	byProject := make(map[string][]*sourceHistoryEntry)
	for _, entry := range s.History.history {
		if entry.UndoPlan.RootPath != "" {
			recoveryRoots[filepath.Clean(entry.UndoPlan.RootPath)] = true
		}
		if bytes, ok := measuredBytes[entry.ID]; ok && isActiveMovedAsidePlan(entry.State, entry.UndoPlan) && !entry.UndoPlan.NativeTrash.SizeMeasured {
			entry.UndoPlan.NativeTrash.AllocatedBytes = bytes
			entry.UndoPlan.NativeTrash.SizeMeasured = true
			syncMovedAsideTrashPlans(&entry.UndoPlan, &entry.RedoPlan)
		}
		byProject[entry.ProjectID] = append(byProject[entry.ProjectID], entry)
	}
	quota := s.History.quotaBytes
	if quota <= 0 {
		quota = DefaultSourceHistoryRecoveryQuotaBytes
	}
	for _, projectEntries := range byProject {
		sort.Slice(projectEntries, func(i, j int) bool { return projectEntries[i].Seq < projectEntries[j].Seq })
		var total int64
		for _, entry := range projectEntries {
			if isActiveMovedAsidePlan(entry.State, entry.UndoPlan) {
				total = addSatInt64(total, entry.UndoPlan.NativeTrash.AllocatedBytes)
			} else {
				total = addSatInt64(total, retainedRecoveryBytes(recoveryUsage(entry.UndoPlan), recoveryUsage(entry.RedoPlan)))
			}
		}
		for _, entry := range projectEntries {
			if total <= quota {
				break
			}
			if !isActiveMovedAsidePlan(entry.State, entry.UndoPlan) {
				continue
			}
			entry.UndoPlan.NativeTrash.Reclaiming = true
			syncMovedAsideTrashPlans(&entry.UndoPlan, &entry.RedoPlan)
			total = max(0, total-entry.UndoPlan.NativeTrash.AllocatedBytes)
		}
	}
	for _, entry := range s.History.history {
		retained[entry.UndoPlan.RecoveryID], retained[entry.RedoPlan.RecoveryID] = true, true
		if isActiveMovedAsidePlan(entry.State, entry.UndoPlan) && entry.UndoPlan.NativeTrash.Receipt.Path != "" {
			liveSlotPaths[filepath.Clean(entry.UndoPlan.NativeTrash.Receipt.Path)] = true
		}
	}
	s.History.stateMu.Unlock()

	type slotToDelete struct {
		id       string
		path     string
		identity string
	}
	var deleteQueue []slotToDelete
	s.Journal.stateMu.Lock()
	var rows []*sourceMutationRow
	for _, row := range s.Journal.memory {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	for _, row := range rows {
		if row.Plan.RootPath != "" {
			recoveryRoots[filepath.Clean(row.Plan.RootPath)] = true
		}
		if row.Status != sourceMutationCommitted {
			retained[row.Plan.RecoveryID] = true
			if row.Plan.RootPath != "" {
				liveSlotPaths[filepath.Clean(filepath.Join(settingsoverlay.RecoveryDir(row.Plan.RootPath), recoverySlotName(row.ID)))] = true
			}
			if row.Plan.NativeTrash != nil && row.Plan.NativeTrash.Receipt.Path != "" {
				liveSlotPaths[filepath.Clean(row.Plan.NativeTrash.Receipt.Path)] = true
			}
			continue
		}
		if row.Plan.Disposal == sourceDisposalDiscard && row.Plan.NativeTrash != nil && !row.Plan.NativeTrash.Reclaimed {
			slotPath := filepath.Clean(row.Plan.NativeTrash.Receipt.Path)
			if !liveSlotPaths[slotPath] {
				row.Plan.NativeTrash.Reclaiming = true
				deleteQueue = append(deleteQueue, slotToDelete{id: row.ID, path: slotPath, identity: row.Plan.NativeTrash.Receipt.Identity})
			}
		}
	}
	s.Journal.stateMu.Unlock()

	for _, item := range deleteQueue {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.path != "" && item.path != "." {
			if err := removeRecoverySlot(item.path, item.identity); err != nil {
				return err
			}
		}
		s.History.stateMu.Lock()
		if entry := s.History.history[item.id]; entry != nil && entry.UndoPlan.NativeTrash != nil {
			entry.UndoPlan.NativeTrash.Reclaimed = true
			syncMovedAsideTrashPlans(&entry.UndoPlan, &entry.RedoPlan)
		}
		s.History.stateMu.Unlock()
		s.Journal.stateMu.Lock()
		if row := s.Journal.memory[item.id]; row != nil && row.Plan.NativeTrash != nil {
			row.Plan.NativeTrash.Reclaimed = true
		}
		s.Journal.stateMu.Unlock()
	}

	for rootPath := range recoveryRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sweepRecoveryRootOrphans(rootPath, liveSlotPaths); err != nil {
			return err
		}
	}

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
	return nil
}
