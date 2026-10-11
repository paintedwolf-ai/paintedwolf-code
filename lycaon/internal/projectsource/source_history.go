package projectsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
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
	Unavailable bool
	Seq         int64
	ID          string
	ProjectID   string
	Kind        string
	State       string
	UndoLabel   string
	RedoLabel   string
	UndoPlan    sourceMutationPlan
	RedoPlan    sourceMutationPlan
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
		if errors.Is(err, ErrSourceTrashUnavailable) {
			err = errors.Join(err, s.History.markUnavailable(context.WithoutCancel(ctx), p.SourceID(), expectedEntryID, direction))
		}
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
		if original.NativeTrash != nil {
			return nativeCreationHistory(original)
		}
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
			sourceMutationContent:     sourceMutationContent{Before: original.After, BaseSHA256: original.AfterSHA, BeforeSize: original.AfterSize},
			sourceMutationRecovery:    sourceMutationRecovery{RecoveryID: original.RecoveryID, RecoveryCount: original.RecoveryCount, TreeSHA: fingerprint, Disposal: sourceDisposalTrash},
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "delete",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   targetAbs,
			EntryKind:                 original.EntryKind,
			Changed:                   true,
		}
		redo := sourceMutationPlan{
			sourceMutationContent:     sourceMutationContent{After: original.After, AfterSHA: original.AfterSHA, AfterSize: original.AfterSize},
			sourceMutationRecovery:    sourceMutationRecovery{RecoveryID: original.RecoveryID, RecoveryCount: original.RecoveryCount, TreeSHA: fingerprint},
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "restore",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   targetAbs,
			EntryKind:                 original.EntryKind,
			Changed:                   true,
		}
		kind := original.Kind
		return sourceHistoryPlans{
			undo: historyPlan(undo), redo: historyPlan(redo), kind: kind,
			undoLabel: "Undo " + kind + " of " + name, redoLabel: "Redo " + kind + " of " + name,
		}, nil
	case "delete":
		undo := sourceMutationPlan{
			sourceMutationContent:     sourceMutationContent{After: original.Before, AfterSHA: original.BaseSHA256, AfterSize: original.BeforeSize},
			sourceMutationRecovery:    sourceMutationRecovery{NativeTrash: original.NativeTrash, RecoveryID: original.RecoveryID, RecoveryCount: original.RecoveryCount, TreeSHA: original.TreeSHA},
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
			Kind:                      "restore",
			RootID:                    original.RootID,
			RootPath:                  original.RootPath,
			Path:                      original.Path,
			AbsPath:                   original.AbsPath,
			EntryKind:                 original.EntryKind,
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
	if plan.NativeTrash != nil {
		copy := *plan.NativeTrash
		plan.NativeTrash = &copy
	}
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

func nativeCreationHistory(original sourceMutationPlan) (sourceHistoryPlans, error) {
	target := original.AbsPath
	if target == "" {
		target = original.ToAbs
	}
	identity, err := fspath.EntryIdentity(target)
	if err != nil {
		return sourceHistoryPlans{}, err
	}
	undo := sourceMutationPlan{
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: original.ProjectID, WorkspaceID: original.WorkspaceID, BranchID: original.BranchID},
		sourceMutationPublication: sourceMutationPublication{EntryIdentity: identity},
		sourceMutationRecovery:    sourceMutationRecovery{NativeTrash: &sourceTrashRecovery{}, Disposal: sourceDisposalTrash},
		Kind:                      "delete", RootID: original.RootID, RootPath: original.RootPath, Path: original.Path, AbsPath: target, EntryKind: original.EntryKind, Changed: true,
	}
	redo := undo
	redo.Kind = "restore"
	name := filepath.Base(filepath.FromSlash(original.Path))
	return sourceHistoryPlans{undo: historyPlan(undo), redo: historyPlan(redo), kind: original.Kind,
		undoLabel: "Undo " + original.Kind + " of " + name, redoLabel: "Redo " + original.Kind + " of " + name}, nil
}
