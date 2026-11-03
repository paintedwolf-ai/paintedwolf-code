package checkpoint

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// EffectStatus tracks whether a distinct promotion effect (files vs Git refs) has completed.
type EffectStatus string

// PromotionState identifies the durable state of a promotion transaction in the journal.
type PromotionState string

// ApplyFaultStage identifies a fault-injection boundary during promotion Apply.
type ApplyFaultStage string

const (
	EffectPending                  EffectStatus    = "pending"
	EffectCompleted                EffectStatus    = "completed"
	EffectSkipped                  EffectStatus    = "skipped"
	PromotionStateIntent           PromotionState  = "intent"
	PromotionStateApplying         PromotionState  = "applying"
	PromotionStateApplied          PromotionState  = "applied"
	PromotionStateRejected         PromotionState  = "rejected"
	ApplyFaultNone                 ApplyFaultStage = ""
	ApplyFaultMidFileApply         ApplyFaultStage = "mid_file_apply"
	ApplyFaultAfterFilesBeforeRefs ApplyFaultStage = "after_files_before_refs"
)

// PromotionJournalRecord durably records a promotion transaction for idempotent retry.
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

// ApplyRequest specifies a validated PromotionPreview to apply transactionally.
type ApplyRequest struct {
	TransactionID string
	Preview       *PromotionPreview
	ImportGitRefs bool
	FaultInjector func(stage ApplyFaultStage) error
}

// Apply revalidates target observations, journals intent, and executes file promotion and ref import idempotently.
func (pr *Promoter) Apply(ctx context.Context, req ApplyRequest) (*PromotionJournalRecord, error) {
	if req.Preview == nil {
		return nil, fmt.Errorf("checkpoint: Preview is required")
	}
	if len(req.Preview.Conflicts) > 0 {
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

	rec, exists, err := pr.loadPromotionRecord(txID)
	if err != nil {
		return nil, err
	}
	if exists && rec.State == PromotionStateApplied {
		return &rec, nil
	}

	if err := pr.RevalidatePreviewTargets(req.Preview, rec.AppliedPaths); err != nil {
		return nil, err
	}

	if !exists {
		refStatus := EffectSkipped
		if req.ImportGitRefs && (len(m.PrivateGit.Refs) > 0 || m.PrivateGit.HeadRef != "") {
			refStatus = EffectPending
		}
		rec = PromotionJournalRecord{
			TransactionID: txID, CheckpointID: m.CheckpointID, PreviewID: req.Preview.PreviewID,
			State: PromotionStateIntent, FilePromotionStatus: EffectPending, RefImportStatus: refStatus,
			UpdatedAt: time.Now().UTC(),
		}
		if err := pr.savePromotionRecord(rec); err != nil {
			return nil, err
		}
	}

	rec.State, rec.UpdatedAt = PromotionStateApplying, time.Now().UTC()
	if err := pr.savePromotionRecord(rec); err != nil {
		return nil, err
	}

	if rec.FilePromotionStatus != EffectCompleted {
		for _, ch := range req.Preview.PlannedChanges {
			if hasPath(rec.AppliedPaths, ch.Path) {
				continue
			}
			if err := pr.applyPlannedChange(ch); err != nil {
				return nil, err
			}
			rec.AppliedPaths = append(rec.AppliedPaths, ch.Path)
			rec.UpdatedAt = time.Now().UTC()
			if err := pr.savePromotionRecord(rec); err != nil {
				return nil, err
			}
			if req.FaultInjector != nil {
				if err := req.FaultInjector(ApplyFaultMidFileApply); err != nil {
					return nil, err
				}
			}
		}
		rec.FilePromotionStatus, rec.UpdatedAt = EffectCompleted, time.Now().UTC()
		if err := pr.savePromotionRecord(rec); err != nil {
			return nil, err
		}
	}

	if req.FaultInjector != nil {
		if err := req.FaultInjector(ApplyFaultAfterFilesBeforeRefs); err != nil {
			return nil, err
		}
	}

	if rec.RefImportStatus == EffectPending {
		if pr.cfg.RefImporter != nil {
			imported, err := pr.cfg.RefImporter(ctx, txID, m.WorkerID, m.PrivateGit)
			if err != nil {
				return nil, err
			}
			rec.ImportedRefs = imported
		}
		rec.RefImportStatus, rec.UpdatedAt = EffectCompleted, time.Now().UTC()
		if err := pr.savePromotionRecord(rec); err != nil {
			return nil, err
		}
	}

	rec.State, rec.UpdatedAt = PromotionStateApplied, time.Now().UTC()
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

// RevalidatePreviewTargets checks the bound target facts, excluding journaled applied paths on retry.
func (pr *Promoter) RevalidatePreviewTargets(preview *PromotionPreview, applied []layeredfs.QualifiedPath) error {
	for _, obs := range preview.TargetObservations {
		if hasPath(applied, obs.Path) {
			continue
		}
		cur, err := pr.observeTarget(obs.Path)
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

func hasPath(list []layeredfs.QualifiedPath, target layeredfs.QualifiedPath) bool {
	for _, p := range list {
		if p == target {
			return true
		}
	}
	return false
}
