package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// StackedPromotionRequest specifies parameters for resolving and promoting a stacked child checkpoint.
// Transaction is the promotion it joins; the caller commits it at its own commit fact.
type StackedPromotionRequest struct {
	ChildCheckpointID string
	TargetRoots       map[string]string
	Promoter          *checkpoint.Promoter
	Transaction       *checkpoint.Transaction
	Resolutions       map[layeredfs.QualifiedPath]checkpoint.ConflictResolution
	ImportGitRefs     bool
	TransactionID     string
}

// StackedPromotionPlan contains the composed effective manifest and three-way preview for a stacked checkpoint.
type StackedPromotionPlan struct {
	ChildCheckpointID     string
	EffectiveCheckpointID string
	ResolvedChain         []string
	LandedAncestors       []string
	UnlandedAncestors     []string
	EffectiveManifest     checkpoint.Manifest
	Preview               *checkpoint.PromotionPreview
}

// ResolveStackedPromotionPlan resolves the complete parent checkpoint chain (with deletions, whiteouts,
// and replacements), checks recorded landing receipts + target state to avoid double-applying landed
// parents, and computes a deterministic PromotionPreview relative to trunk bases.
func (c *Coordinator) ResolveStackedPromotionPlan(ctx context.Context, req StackedPromotionRequest) (*StackedPromotionPlan, error) {
	return resolveStackedPromotionPlan(ctx, c, req)
}

func resolveStackedPromotionPlan(ctx context.Context, c *Coordinator, req StackedPromotionRequest) (*StackedPromotionPlan, error) {
	if strings.TrimSpace(req.ChildCheckpointID) == "" || req.Promoter == nil {
		return nil, fmt.Errorf("recovery: ChildCheckpointID and Promoter are required")
	}
	_, newestToOldest, err := c.OpenCheckpointChainLayers(ctx, []string{req.ChildCheckpointID})
	if err != nil {
		return nil, err
	}
	chain := slices.Clone(newestToOldest)
	slices.Reverse(chain)

	landedSet := make(map[string]bool)
	var resolvedIDs, landedIDs, unlandedIDs []string
	for i, m := range chain {
		resolvedIDs = append(resolvedIDs, m.CheckpointID)
		if i == len(chain)-1 {
			continue
		}
		if isAncestorLandedOnTarget(c, m, req.TargetRoots) {
			landedSet[m.CheckpointID] = true
			landedIDs = append(landedIDs, m.CheckpointID)
		} else {
			unlandedIDs = append(unlandedIDs, m.CheckpointID)
		}
	}

	childManifest := chain[len(chain)-1]
	effective := composeEffectiveStackedManifest(childManifest, chain, landedSet)
	if err := saveManifestToCheckpointStore(c, effective); err != nil {
		return nil, err
	}

	preview, err := req.Promoter.Preview(ctx, checkpoint.PreviewRequest{
		CheckpointID:      effective.CheckpointID,
		ExpectedWorkerID:  effective.WorkerID,
		ExpectedTaskID:    effective.TaskID,
		ExpectedProjectID: effective.ProjectID,
		Resolutions:       req.Resolutions,
	})
	if err != nil {
		return nil, err
	}
	return &StackedPromotionPlan{
		ChildCheckpointID:     req.ChildCheckpointID,
		EffectiveCheckpointID: effective.CheckpointID,
		ResolvedChain:         resolvedIDs,
		LandedAncestors:       landedIDs,
		UnlandedAncestors:     unlandedIDs,
		EffectiveManifest:     effective,
		Preview:               preview,
	}, nil
}

// PromoteStackedCheckpoint resolves the stacked promotion plan and applies its file and ref changes
// through the one promotion transaction, with landing receipts for the child and any folded ancestors
// as records of that transaction. A failure restores the original target and leaves no receipt.
func (c *Coordinator) PromoteStackedCheckpoint(ctx context.Context, req StackedPromotionRequest) (*checkpoint.PromotionJournalRecord, error) {
	if req.Transaction == nil {
		return nil, fmt.Errorf("recovery: a stacked promotion joins a transaction")
	}
	if err := injectFault(c, PhaseFilePromotion, "promote_start"); err != nil {
		return nil, errors.Join(err, req.Transaction.Rollback(context.WithoutCancel(ctx)))
	}
	plan, err := c.ResolveStackedPromotionPlan(ctx, req)
	if err != nil {
		return nil, errors.Join(err, req.Transaction.Rollback(context.WithoutCancel(ctx)))
	}
	workerID := plan.EffectiveManifest.WorkerID
	if err := recordPromotionTransition(c, workerID, StateBegin, req.ChildCheckpointID, "promote_stacked"); err != nil {
		return nil, errors.Join(err, req.Transaction.Rollback(context.WithoutCancel(ctx)))
	}
	// The claim is durable before any target changes, and only settlement releases it, so a failed
	// promotion's whole chain stays available for repair.
	if err := c.AcquireClaim(RetentionClaim{
		ClaimID:       stackedPromotionClaim(req.ChildCheckpointID),
		Kind:          ClaimPromotion,
		HolderID:      req.ChildCheckpointID,
		WorkerID:      workerID,
		CheckpointIDs: plan.ResolvedChain,
	}); err != nil {
		return nil, errors.Join(err, req.Transaction.Rollback(context.WithoutCancel(ctx)))
	}
	txID := req.TransactionID
	if txID == "" {
		txID = "tx-" + req.ChildCheckpointID
	}
	rec, err := req.Transaction.ApplyCheckpoint(ctx, req.Promoter, checkpoint.ApplyRequest{
		TransactionID: txID,
		Preview:       plan.Preview,
		ImportGitRefs: req.ImportGitRefs,
		FaultInjector: func(stage checkpoint.ApplyFaultStage) error {
			if stage == checkpoint.ApplyFaultMidFileApply {
				return injectFault(c, PhaseFilePromotion, "mid_file_apply")
			}
			if stage == checkpoint.ApplyFaultAfterFilesBeforeRefs {
				return injectFault(c, PhaseRefImport, "before_ref_import")
			}
			return nil
		},
	})
	if err != nil {
		return nil, errors.Join(err, recordPromotionTransition(c, workerID, StateFailed, req.ChildCheckpointID, "promote_stacked"))
	}
	if err := c.recordStackedLandings(req, plan, rec); err != nil {
		return nil, errors.Join(err, req.Transaction.Rollback(context.WithoutCancel(ctx)), recordPromotionTransition(c, workerID, StateFailed, req.ChildCheckpointID, "promote_stacked"))
	}
	return rec, nil
}

func (c *Coordinator) recordStackedLandings(req StackedPromotionRequest, plan *StackedPromotionPlan, rec *checkpoint.PromotionJournalRecord) error {
	landed := append(append([]string(nil), plan.UnlandedAncestors...), req.ChildCheckpointID)
	paths := make([]string, 0, len(landed))
	for _, id := range landed {
		path, err := filepath.Abs(filepath.Join(c.landingsDir, id+".json"))
		if err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if err := req.Transaction.JoinRecords(paths...); err != nil {
		return err
	}
	promotedDigests := buildPromotedTargetDigests(plan.EffectiveManifest, req.TargetRoots)
	for _, landedCP := range landed {
		if err := c.RecordCheckpointLanded(LandingReceipt{
			FormatVersion:     FormatVersion,
			CheckpointID:      landedCP,
			TransactionID:     rec.TransactionID,
			WorkerID:          plan.EffectiveManifest.WorkerID,
			TaskID:            plan.EffectiveManifest.TaskID,
			ProjectID:         plan.EffectiveManifest.ProjectID,
			ParentCheckpoints: plan.EffectiveManifest.ParentCheckpoints,
			PromotedDigests:   promotedDigests,
			ImportedRefs:      rec.ImportedRefs,
			LandedAt:          time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// SettleStackedPromotion closes a stacked promotion once its commit fact is durable.
func (c *Coordinator) SettleStackedPromotion(childCheckpointID string) error {
	m, err := c.cfg.CheckpointStore.LoadManifest(childCheckpointID)
	if err != nil {
		return err
	}
	if err := c.ReleaseClaim(stackedPromotionClaim(childCheckpointID)); err != nil {
		return err
	}
	return recordPromotionTransition(c, m.WorkerID, StateCommitted, childCheckpointID, "promote_stacked")
}

func stackedPromotionClaim(childCheckpointID string) string {
	return "claim-promote-" + childCheckpointID
}

// recordPromotionTransition journals a promotion of a worker that has a journal; a checkpoint can
// outlive its worker's journal once that worker's own result was released.
func recordPromotionTransition(c *Coordinator, workerID string, state TransitionState, checkpointID, op string) error {
	err := c.RecordTransition(workerID, "", PhaseFilePromotion, state, checkpointID, op, nil, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func isAncestorLandedOnTarget(c *Coordinator, anc checkpoint.Manifest, targetRoots map[string]string) bool {
	_, hasReceipt, rErr := c.LoadLandingReceipt(anc.CheckpointID)
	promRec, hasProm, pErr := c.cfg.CheckpointStore.LoadPromotionRecord("tx-" + anc.CheckpointID)
	hasDurableLandingFact := (rErr == nil && hasReceipt) || (pErr == nil && hasProm && promRec.State == checkpoint.PromotionStateApplied)
	if !hasDurableLandingFact {
		return false
	}
	for _, e := range anc.Entries {
		rootDir := targetRoots[e.Path.RootID]
		if rootDir == "" {
			continue
		}
		full := filepath.Join(rootDir, filepath.FromSlash(e.Path.Rel))
		info, err := os.Lstat(full)
		if e.Deleted || e.Kind == layeredfs.EntryAbsent {
			if err == nil {
				return false
			}
			continue
		}
		if err != nil {
			return false
		}
		if e.Kind == layeredfs.EntryRegular && e.Base.Kind == layeredfs.EntryRegular && e.Base.SHA256 != e.Final.SHA256 {
			if raw, rErr := os.ReadFile(full); rErr == nil {
				sum := sha256.Sum256(raw)
				curSHA := hex.EncodeToString(sum[:])
				if curSHA == e.Base.SHA256 {
					return false
				}
			}
		}
		_ = info
	}
	return true
}

func saveManifestToCheckpointStore(c *Coordinator, m checkpoint.Manifest) error {
	cpDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints", m.CheckpointID)
	if err := os.MkdirAll(cpDir, 0o700); err != nil {
		return err
	}
	return writeAtomicJSON(c, cpDir, "manifest.json", m)
}
