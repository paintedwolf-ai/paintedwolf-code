package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// EffectStatus tracks whether a distinct promotion effect (files vs Git refs) has completed.
type EffectStatus string

// PromotionState identifies the durable state of a promotion record.
type PromotionState string

// ApplyFaultStage identifies a boundary inside a checkpoint promotion.
type ApplyFaultStage string

const (
	EffectCompleted                EffectStatus    = "completed"
	EffectSkipped                  EffectStatus    = "skipped"
	PromotionStateApplied          PromotionState  = "applied"
	PromotionStateRejected         PromotionState  = "rejected"
	ApplyFaultMidFileApply         ApplyFaultStage = "mid_file_apply"
	ApplyFaultAfterFilesBeforeRefs ApplyFaultStage = "after_files_before_refs"
)

// PromotionJournalRecord records a promotion outcome. An applied record exists only after every
// effect happened, and rollback removes it with them.
type PromotionJournalRecord struct {
	TransactionID       string                    `json:"transaction_id"`
	CheckpointID        string                    `json:"checkpoint_id"`
	PreviewID           string                    `json:"preview_id"`
	State               PromotionState            `json:"state"`
	RejectionReason     string                    `json:"rejection_reason,omitempty"`
	FilePromotionStatus EffectStatus              `json:"file_promotion_status"`
	RefImportStatus     EffectStatus              `json:"ref_import_status"`
	ImportedRefs        map[string]string         `json:"imported_refs,omitempty"`
	AppliedPaths        []layeredfs.QualifiedPath `json:"applied_paths,omitempty"`
	UpdatedAt           time.Time                 `json:"updated_at"`
}

// ApplyRequest specifies a validated PromotionPreview to apply.
type ApplyRequest struct {
	TransactionID string
	Preview       *PromotionPreview
	ImportGitRefs bool
	FaultInjector func(stage ApplyFaultStage) error
}

// ApplyCheckpoint revalidates the preview's target observations and applies its file and ref effects.
// Any failure restores the original target before returning.
func (txn *Transaction) ApplyCheckpoint(ctx context.Context, pr *Promoter, req ApplyRequest) (*PromotionJournalRecord, error) {
	pr.applyMu.Lock()
	defer pr.applyMu.Unlock()
	rec, err := txn.applyCheckpoint(ctx, pr, req)
	if err != nil {
		return nil, errors.Join(err, txn.rollback(context.WithoutCancel(ctx)))
	}
	return rec, nil
}

func (txn *Transaction) applyCheckpoint(ctx context.Context, pr *Promoter, req ApplyRequest) (*PromotionJournalRecord, error) {
	if txn.closed {
		return nil, errors.New("checkpoint: promotion transaction is closed")
	}
	if req.Preview == nil || len(req.Preview.Conflicts) > 0 {
		return nil, ErrUnresolvedConflict
	}
	m, err := pr.cfg.Store.LoadManifest(req.Preview.CheckpointID)
	if err != nil {
		return nil, err
	}
	txID := req.TransactionID
	if txID == "" {
		txID = "tx-" + m.CheckpointID
	}
	existing, found, err := pr.loadPromotionRecord(txID)
	if err != nil {
		return nil, err
	}
	if found && existing.State == PromotionStateApplied {
		return &existing, nil
	}
	if err := pr.RevalidatePreviewTargets(req.Preview); err != nil {
		return nil, err
	}
	if err := txn.addRoots(pr.cfg.TargetRoots); err != nil {
		return nil, err
	}
	txn.targets.store = pr.cfg.Store
	storeDir, err := filepath.Abs(pr.cfg.Store.RootDir())
	if err != nil {
		return nil, err
	}
	txn.journal.Checkpoint = &journalCheckpoint{StoreDir: storeDir, ID: m.CheckpointID, WorkerID: m.WorkerID}
	refStatus := EffectSkipped
	if req.ImportGitRefs && m.PrivateGit.HasRefs() {
		refStatus = EffectCompleted
		if txn.journal.Refs, err = txn.prepareRefs(m, pr.cfg.TargetRoots); err != nil {
			return nil, err
		}
	}
	changes := txn.canonicalChanges(req.Preview.PlannedChanges)
	if err := txn.addTargets(changes); err != nil {
		return nil, err
	}
	promotions, err := filepath.Abs(pr.cfg.Store.promotionsDir)
	if err != nil {
		return nil, err
	}
	records := []string{filepath.Join(promotions, txID+".json"), filepath.Join(promotions, gitImportRecordName(m.WorkerID, m.CheckpointID, ""))}
	for rootID := range m.PrivateGit.Roots {
		records = append(records, filepath.Join(promotions, gitImportRecordName(m.WorkerID, m.CheckpointID, rootID)))
	}
	if err := txn.JoinRecords(records...); err != nil {
		return nil, err
	}
	if err := txn.mutateTargets(ctx, changes, req.FaultInjector); err != nil {
		return nil, err
	}
	if err := txn.reach(StepFilesApplied, ""); err != nil {
		return nil, err
	}
	if req.FaultInjector != nil {
		if err := req.FaultInjector(ApplyFaultAfterFilesBeforeRefs); err != nil {
			return nil, err
		}
	}
	rec := PromotionJournalRecord{
		TransactionID: txID, CheckpointID: m.CheckpointID, PreviewID: req.Preview.PreviewID, State: PromotionStateApplied,
		FilePromotionStatus: EffectCompleted, RefImportStatus: refStatus, UpdatedAt: time.Now().UTC(),
	}
	for _, change := range req.Preview.PlannedChanges {
		rec.AppliedPaths = append(rec.AppliedPaths, change.Path)
	}
	if refStatus == EffectCompleted {
		rec.ImportedRefs, err = ImportAndValidateCheckpointGitRefs(ctx, pr.cfg.Store.RootDir(), m.CheckpointID, m.WorkerID, m.PrivateGit, pr.cfg.TargetRoots, txn.cfg.AuthorizedGitDirs...)
		if err != nil {
			return nil, err
		}
	}
	if err := pr.savePromotionRecord(rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// Reject records a rejected checkpoint outcome while preserving the checkpoint and its blobs in the store.
func (pr *Promoter) Reject(_ context.Context, checkpointID, reason string) (*PromotionJournalRecord, error) {
	if _, err := pr.cfg.Store.LoadManifest(checkpointID); err != nil {
		return nil, err
	}
	rec := PromotionJournalRecord{
		TransactionID: "tx-" + checkpointID, CheckpointID: checkpointID, State: PromotionStateRejected,
		RejectionReason: reason, FilePromotionStatus: EffectSkipped, RefImportStatus: EffectSkipped,
		UpdatedAt: time.Now().UTC(),
	}
	if err := pr.savePromotionRecord(rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// RevalidatePreviewTargets checks the bound target facts still hold.
func (pr *Promoter) RevalidatePreviewTargets(preview *PromotionPreview) error {
	for _, obs := range preview.TargetObservations {
		cur, err := pr.targets.observeTarget(obs.Path)
		if err != nil {
			return err
		}
		if !sameTargetObservation(obs, cur) {
			return fmt.Errorf("%w: path %s:%s changed before apply", ErrStalePreviewTargetChanged, obs.Path.RootID, obs.Path.Rel)
		}
	}
	return nil
}

func sameTargetObservation(a, b TargetObservation) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case layeredfs.EntryAbsent:
		return true
	case layeredfs.EntryRegular:
		return a.SHA256 == b.SHA256 && a.Size == b.Size && a.Mode.Perm() == b.Mode.Perm()
	case layeredfs.EntrySymlink:
		return a.LinkTarget == b.LinkTarget
	case layeredfs.EntryDirectory:
		return a.DirEmpty == b.DirEmpty && a.Mode.Perm() == b.Mode.Perm() && a.DirectorySHA256 == b.DirectorySHA256
	}
	return false
}
