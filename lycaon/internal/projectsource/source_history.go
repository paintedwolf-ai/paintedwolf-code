package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

type SourceHistory struct {
	db         db.Handle
	recovery   *sourceRecovery
	stateMu    sync.Mutex
	history    map[string]*sourceHistoryEntry
	historySeq int64
}

const sourceHistoryLimit = 100

var ErrSourceHistoryChanged = errors.New("source history changed")

type SourceHistoryAction struct {
	ID          string
	Label       string
	Kind        string
	RootID      string
	Path        string
	FromPath    string
	ToPath      string
	IsDir       bool
	RemovesPath bool
}

type SourceHistoryState struct {
	Undo *SourceHistoryAction
	Redo *SourceHistoryAction
}

type SourceHistoryMutationRequest struct {
	ExpectedEntryID string
	SessionID       string
	Turn            int
	Prepare         func(context.Context, SourceRenamePlan) error
}

type SourceHistoryMutationResult struct {
	EntryID  string
	RootID   string
	Path     string
	FromPath string
	Op       string
	IsDir    bool
}

type sourceHistoryEntry struct {
	Seq       int64
	ID        string
	ProjectID string
	Kind      string
	State     string
	UndoLabel string
	RedoLabel string
	UndoPlan  sourceMutationPlan
	RedoPlan  sourceMutationPlan
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *SourceHistory) buildSourceHistoryEntry(ctx context.Context, operationID string, plan *sourceMutationPlan) (*sourceHistoryEntry, error) {
	if plan == nil || plan.AgentEffect != nil || plan.HistoryEntryID != "" || !plan.Changed {
		return nil, nil
	}
	if plan.Kind != "create" && plan.Kind != "rename" && plan.Kind != "copy" && plan.Kind != "delete" {
		return nil, nil
	}
	plans, err := s.planSourceHistory(ctx, operationID, *plan)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &sourceHistoryEntry{
		ID: operationID, ProjectID: plan.ProjectID, Kind: plans.kind, State: "applied",
		UndoLabel: plans.undoLabel, RedoLabel: plans.redoLabel, UndoPlan: plans.undo, RedoPlan: plans.redo,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

type sourceHistoryPlans struct {
	undo, redo                 sourceMutationPlan
	kind, undoLabel, redoLabel string
}

func (s *SourceHistory) planSourceHistory(ctx context.Context, operationID string, original sourceMutationPlan) (sourceHistoryPlans, error) {
	name := filepath.Base(filepath.FromSlash(original.Path))
	switch original.Kind {
	case "rename":
		kind := "move"
		if sourceParent(original.FromPath) == sourceParent(original.ToPath) {
			kind = "rename"
		}
		if original.CrossVolume {
			original.EntryIdentity = original.DestinationIdentity
		}
		undo := original
		undo.Path, undo.FromPath, undo.ToPath = original.FromPath, original.ToPath, original.FromPath
		undo.FromAbs, undo.ToAbs = original.ToAbs, original.FromAbs
		redo := original
		return sourceHistoryPlans{
			undo: historyPlan(undo), redo: historyPlan(redo), kind: kind,
			undoLabel: "Undo " + kind + " of " + name, redoLabel: "Redo " + kind + " of " + name,
		}, nil
	case "create", "copy":
		targetAbs := original.AbsPath
		if targetAbs == "" {
			targetAbs = original.ToAbs
		}
		fingerprint := original.TreeSHA
		if fingerprint == "" {
			var err error
			fingerprint, err = sourceTreeFingerprint(ctx, targetAbs)
			if err != nil {
				return sourceHistoryPlans{}, err
			}
		}
		original.RecoveryID, original.TreeSHA = operationID, fingerprint
		if original.RecoveryCount == 0 {
			if err := s.recovery.captureRecovery(ctx, &original, targetAbs); err != nil {
				return sourceHistoryPlans{}, err
			}
		}
		undo := sourceMutationPlan{
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "delete",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   targetAbs,
			RecoveryID:                original.RecoveryID,
			RecoveryCount:             original.RecoveryCount,
			TreeSHA:                   fingerprint,
			EntryKind:                 original.EntryKind,
			Before:                    original.After,
			BaseSHA256:                original.AfterSHA,
			BeforeSize:                original.AfterSize,
			Disposal:                  sourceDisposalTrash,
			Changed:                   true,
		}
		redo := sourceMutationPlan{
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "restore",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   targetAbs,
			RecoveryID:                original.RecoveryID,
			RecoveryCount:             original.RecoveryCount,
			TreeSHA:                   fingerprint,
			EntryKind:                 original.EntryKind,
			After:                     original.After,
			AfterSHA:                  original.AfterSHA,
			AfterSize:                 original.AfterSize,
			Changed:                   true,
		}
		kind := original.Kind
		return sourceHistoryPlans{
			undo: historyPlan(undo), redo: historyPlan(redo), kind: kind,
			undoLabel: "Undo " + kind + " of " + name, redoLabel: "Redo " + kind + " of " + name,
		}, nil
	case "delete":
		undo := sourceMutationPlan{
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "restore",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   original.AbsPath,
			RecoveryID:                original.RecoveryID,
			RecoveryCount:             original.RecoveryCount,
			TreeSHA:                   original.TreeSHA,
			EntryKind:                 original.EntryKind,
			After:                     original.Before,
			AfterSHA:                  original.BaseSHA256,
			AfterSize:                 original.BeforeSize,
			Changed:                   true,
		}
		redo := original
		plans := sourceHistoryPlans{undo: historyPlan(undo), redo: historyPlan(redo)}
		if original.Disposal == sourceDisposalDiscard {
			plans.kind = "delete"
			plans.undoLabel, plans.redoLabel = "Undo deletion of "+name, "Redo deletion of "+name
		} else {
			plans.kind = "trash"
			plans.undoLabel, plans.redoLabel = "Undo move of "+name+" to Trash", "Redo move of "+name+" to Trash"
		}
		return plans, nil
	default:
		return sourceHistoryPlans{}, nil
	}
}

func historyPlan(plan sourceMutationPlan) sourceMutationPlan {
	plan.Response = nil
	plan.EffectStarted = false
	plan.CrossVolume = false
	plan.HoldAbs, plan.DestinationIdentity = "", ""
	plan.HoldStarted = false
	plan.MoveCleanupStarted = false
	if plan.Kind == "rename" {
		plan.TreeSHA = ""
		plan.RecoveryID = ""
		plan.RecoveryCount = 0
	}
	plan.StageIdentity = ""
	plan.PublicationMode = 0
	plan.StageAbs, plan.DeleteStarted, plan.DeleteIdentity = "", false, ""
	// Undo and redo receive fresh actor attribution.
	plan.SessionID, plan.Turn, plan.Agent = "", 0, nil
	plan.Cause = "source_history"
	return plan
}

func sourceParent(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

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

func (s *SourceMutationService) Undo(ctx context.Context, operationID string, p ProjectSource, req SourceHistoryMutationRequest) (*SourceHistoryMutationResult, error) {
	return s.applyHistory(ctx, operationID, p, req, "undo")
}

func (s *SourceMutationService) Redo(ctx context.Context, operationID string, p ProjectSource, req SourceHistoryMutationRequest) (*SourceHistoryMutationResult, error) {
	return s.applyHistory(ctx, operationID, p, req, "redo")
}

func (s *SourceMutationService) applyHistory(ctx context.Context, operationID string, p ProjectSource, req SourceHistoryMutationRequest, direction string) (*SourceHistoryMutationResult, error) {
	if p == nil || len(p.SourceRoots()) == 0 {
		return nil, ErrSourceNoRoot
	}
	releaseHistory, lockErr := s.Paths.lockSourceHistory(ctx, p.SourceID())
	if lockErr != nil {
		return nil, lockErr
	}
	defer releaseHistory()
	if err := s.validateLifecycleReplay(ctx, operationID, p); err != nil {
		return nil, err
	}
	expectedEntryID := strings.TrimSpace(req.ExpectedEntryID)
	digest, err := sourceMutationDigest(struct {
		ProjectID, Direction, EntryID, SessionID string
		Turn                                     int
	}{p.SourceID(), direction, expectedEntryID, strings.TrimSpace(req.SessionID), req.Turn})
	if err != nil {
		return nil, err
	}
	if err := s.validatePendingHistoryHead(ctx, operationID, p.SourceID(), expectedEntryID, direction, digest); err != nil {
		return nil, err
	}
	encoded, err := s.execute(ctx, operationID, p.SourceID(), digest, func() (*sourceMutationPlan, error) {
		state, order := "applied", "DESC"
		if direction == "redo" {
			state, order = "undone", "ASC"
		}
		entry, historyErr := s.History.historyEntry(ctx, p.SourceID(), state, order)
		if historyErr != nil {
			return nil, historyErr
		}
		if entry == nil || expectedEntryID == "" || entry.ID != expectedEntryID {
			return nil, ErrSourceHistoryChanged
		}
		plan := entry.UndoPlan
		if direction == "redo" {
			plan = entry.RedoPlan
		}
		if validateErr := validateSourceHistoryPlan(p, plan); validateErr != nil {
			return nil, fmt.Errorf("validate source history plan: %w", validateErr)
		}
		if plan.Kind == "restore" && sourceMutationPathExists(plan.AbsPath) {
			return nil, ErrSourceMutationDiverged
		}
		if plan.Kind == "rename" && req.Prepare != nil {
			if prepareErr := req.Prepare(ctx, SourceRenamePlan{OperationID: operationID, RootID: plan.RootID, From: plan.FromPath, To: plan.ToPath}); prepareErr != nil {
				return nil, prepareErr
			}
		}
		plan.SessionID, plan.Turn = strings.TrimSpace(req.SessionID), req.Turn
		plan.HistoryEntryID, plan.HistoryTransition = entry.ID, direction
		plan.Cause = "source_history_" + direction
		response, marshalErr := json.Marshal(historyMutationResult(entry.ID, plan))
		if marshalErr != nil {
			return nil, marshalErr
		}
		plan.Response = response
		return &plan, nil
	})
	if err != nil {
		return nil, fmt.Errorf("apply source history %s: %w", direction, err)
	}
	var committed SourceHistoryMutationResult
	if err := json.Unmarshal(encoded, &committed); err != nil {
		return nil, err
	}
	return &committed, nil
}

func historyMutationResult(entryID string, plan sourceMutationPlan) SourceHistoryMutationResult {
	op := plan.Kind
	if op == "restore" {
		op = "create"
	}
	return SourceHistoryMutationResult{
		EntryID: entryID, RootID: plan.RootID, Path: plan.Path, FromPath: plan.FromPath,
		Op: op, IsDir: plan.EntryKind == SourceEntryFolder,
	}
}

func validateSourceHistoryPlan(p ProjectSource, plan sourceMutationPlan) error {
	root, ok := rootByID(p.SourceRoots(), plan.RootID)
	if !ok {
		return fmt.Errorf("root no longer exists: %w", ErrSourceHistoryChanged)
	}
	if filepath.Clean(root.Path) != filepath.Clean(plan.RootPath) {
		return fmt.Errorf("root path changed: %w", ErrSourceHistoryChanged)
	}
	if plan.AbsPath != "" && !historyPathMatches(root.Path, plan.Path, plan.AbsPath) {
		return fmt.Errorf("item path changed: %w", ErrSourceHistoryChanged)
	}
	if plan.Kind == "rename" {
		if !historyPathMatches(root.Path, plan.FromPath, plan.FromAbs) {
			return fmt.Errorf("rename source path changed: %w", ErrSourceHistoryChanged)
		}
		if !historyPathMatches(root.Path, plan.ToPath, plan.ToAbs) {
			return fmt.Errorf("rename destination path changed: %w", ErrSourceHistoryChanged)
		}
	}
	return nil
}

func historyPathMatches(root, rel, abs string) bool {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	want := filepath.Join(root, rel)
	got, err := filepath.Abs(abs)
	return err == nil && filepath.Clean(got) == filepath.Clean(want)
}

func (s *SourceMutationService) validatePendingHistoryHead(ctx context.Context, operationID, projectID, expected, direction, digest string) error {
	row, found, err := s.Journal.load(ctx, operationID)
	if err != nil {
		return err
	}
	if found && row.InputDigest != digest {
		return ErrSourceMutationConflict
	}
	if !found || row.Status == sourceMutationCommitted || row.Plan.EffectStarted {
		return nil
	}
	state, order := "applied", "DESC"
	if direction == "redo" {
		state, order = "undone", "ASC"
	}
	entry, err := s.History.historyEntry(ctx, projectID, state, order)
	if err != nil {
		return err
	}
	if entry == nil || entry.ID != expected {
		return ErrSourceHistoryChanged
	}
	return nil
}
