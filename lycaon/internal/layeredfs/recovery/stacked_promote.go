package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
type StackedPromotionRequest struct {
	ChildCheckpointID string
	TargetRoots       map[string]string
	Promoter          *checkpoint.Promoter
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

// PromoteStackedCheckpoint resolves the stacked promotion plan, applies file and ref changes
// transactionally, and records explicit landing receipts for the child and any folded ancestors.
func (c *Coordinator) PromoteStackedCheckpoint(ctx context.Context, req StackedPromotionRequest) (*checkpoint.PromotionJournalRecord, error) {
	return promoteStackedCheckpoint(ctx, c, req)
}

func promoteStackedCheckpoint(ctx context.Context, c *Coordinator, req StackedPromotionRequest) (*checkpoint.PromotionJournalRecord, error) {
	if err := injectFault(c, PhaseFilePromotion, "promote_start"); err != nil {
		return nil, err
	}
	plan, err := c.ResolveStackedPromotionPlan(ctx, req)
	if err != nil {
		return nil, err
	}
	workerID := plan.EffectiveManifest.WorkerID
	_ = c.RecordTransition(workerID, "", PhaseFilePromotion, StateBegin, req.ChildCheckpointID, "promote_stacked", nil, false)

	claimID := "claim-promote-" + req.ChildCheckpointID
	_ = c.AcquireClaim(RetentionClaim{
		ClaimID:       claimID,
		Kind:          ClaimPromotion,
		HolderID:      req.ChildCheckpointID,
		WorkerID:      workerID,
		CheckpointIDs: plan.ResolvedChain,
	})

	txID := req.TransactionID
	if txID == "" {
		txID = "tx-" + req.ChildCheckpointID
	}
	rec, err := req.Promoter.Apply(ctx, checkpoint.ApplyRequest{
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
		_ = c.RecordTransition(workerID, "", PhaseFilePromotion, StateFailed, req.ChildCheckpointID, err.Error(), nil, false)
		return nil, err
	}

	_ = c.ReleaseClaim(claimID)
	promotedDigests := buildPromotedTargetDigests(plan.EffectiveManifest, req.TargetRoots)
	for _, landedCP := range append(append([]string(nil), plan.UnlandedAncestors...), req.ChildCheckpointID) {
		_ = c.RecordCheckpointLanded(LandingReceipt{
			FormatVersion:     FormatVersion,
			CheckpointID:      landedCP,
			TransactionID:     rec.TransactionID,
			WorkerID:          workerID,
			TaskID:            plan.EffectiveManifest.TaskID,
			ProjectID:         plan.EffectiveManifest.ProjectID,
			ParentCheckpoints: plan.EffectiveManifest.ParentCheckpoints,
			PromotedDigests:   promotedDigests,
			ImportedRefs:      rec.ImportedRefs,
			LandedAt:          time.Now().UTC(),
		})
	}
	_ = c.RecordTransition(workerID, "", PhaseFilePromotion, StateCommitted, req.ChildCheckpointID, "promote_stacked", nil, false)
	return rec, nil
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
