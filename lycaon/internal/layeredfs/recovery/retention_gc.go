package recovery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ClaimKind classifies the holder of a durable retention claim.
type ClaimKind string

const (
	ClaimActiveView           ClaimKind = "active_view"
	ClaimCheckpoint           ClaimKind = "checkpoint"
	ClaimStackedChild         ClaimKind = "stacked_child"
	ClaimPromotion            ClaimKind = "promotion"
	ClaimUnresolvedSettlement ClaimKind = "unresolved_settlement"
)

// RetentionClaim pins checkpoint manifests, parent chains, blobs, and private Git objects.
type RetentionClaim struct {
	FormatVersion int       `json:"format_version"`
	ClaimID       string    `json:"claim_id"`
	Kind          ClaimKind `json:"kind"`
	HolderID      string    `json:"holder_id"`
	WorkerID      string    `json:"worker_id"`
	ViewID        string    `json:"view_id,omitempty"`
	CheckpointIDs []string  `json:"checkpoint_ids,omitempty"`
	Blobs         []string  `json:"blobs,omitempty"`
	GitObjects    []string  `json:"git_objects,omitempty"`
	GitLeaseID    string    `json:"git_lease_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// EvictionReport summarizes disposable working material removed after checkpoint verification.
type EvictionReport struct {
	WorkerID       string
	ViewID         string
	CheckpointID   string
	ReclaimedBytes int64
	RemovedPaths   []string
}

// GarbageCollectionReport summarizes retained and collected objects across checkpoint and Git stores.
type GarbageCollectionReport struct {
	RetainedCheckpoints        []string
	RetainedBlobs              []string
	CollectedBlobs             []string
	CollectedStagedCheckpoints []string
	CollectedGitObjects        int
}

// CommitCheckpointRetention publishes durable retention claims atomically with checkpoint publication.
func (c *Coordinator) CommitCheckpointRetention(ctx context.Context, workerID, checkpointID string, blobs, gitObjects, parentCheckpoints []string) error {
	return commitCheckpointRetention(ctx, c, workerID, checkpointID, blobs, gitObjects, parentCheckpoints)
}

func commitCheckpointRetention(_ context.Context, c *Coordinator, workerID, checkpointID string, blobs, gitObjects, parentCheckpoints []string) error {
	if err := injectFault(c, PhasePublication, "on_retention_commit"); err != nil {
		return err
	}
	var gitLease string
	if c.cfg.GitStore != nil && len(gitObjects) > 0 {
		lid, err := c.cfg.GitStore.RetainForCheckpoint(checkpointID, gitObjects)
		if err != nil {
			return err
		}
		gitLease = lid
	}
	cpIDs := append([]string{checkpointID}, parentCheckpoints...)
	claim := RetentionClaim{
		FormatVersion: FormatVersion,
		ClaimID:       "claim-cp-" + checkpointID,
		Kind:          ClaimCheckpoint,
		HolderID:      checkpointID,
		WorkerID:      workerID,
		CheckpointIDs: cpIDs,
		Blobs:         append([]string(nil), blobs...),
		GitObjects:    append([]string(nil), gitObjects...),
		GitLeaseID:    gitLease,
		CreatedAt:     time.Now().UTC(),
	}
	if err := c.AcquireClaim(claim); err != nil {
		return err
	}
	cpPath := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints", checkpointID)
	return c.UpsertInventoryEntry(InventoryEntry{
		ID:           "inv-checkpoint-" + checkpointID,
		Bucket:       BucketRetainedWorkerResults,
		WorkerID:     workerID,
		CheckpointID: checkpointID,
		Path:         cpPath,
		PinnedBy:     []string{claim.ClaimID},
	})
}

// AcquireClaim persists a durable retention claim.
func (c *Coordinator) AcquireClaim(claim RetentionClaim) error {
	return acquireClaim(c, claim)
}

func acquireClaim(c *Coordinator, claim RetentionClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(claim.ClaimID) == "" || claim.Kind == "" {
		return fmt.Errorf("%w: claim_id and kind are required", ErrCorruptRecoveryMetadata)
	}
	claim.FormatVersion = FormatVersion
	if claim.CreatedAt.IsZero() {
		claim.CreatedAt = time.Now().UTC()
	}
	return writeAtomicJSON(c, c.retentionDir, claim.ClaimID+".json", claim)
}

// ReleaseClaim removes a settled retention claim and releases its Git store lease if held.
func (c *Coordinator) ReleaseClaim(claimID string) error {
	return releaseClaim(c, claimID)
}

func releaseClaim(c *Coordinator, claimID string) error {
	c.mu.Lock()
	p := filepath.Join(c.retentionDir, claimID+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		c.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var claim RetentionClaim
	if dErr := decodeVersionedJSON(raw, &claim); dErr != nil {
		c.mu.Unlock()
		return dErr
	}
	if rErr := os.Remove(p); rErr != nil && !errors.Is(rErr, fs.ErrNotExist) {
		c.mu.Unlock()
		return rErr
	}
	c.mu.Unlock()
	if claim.GitLeaseID != "" && c.cfg.GitStore != nil {
		_ = c.cfg.GitStore.ReleaseLease(claim.GitLeaseID)
	}
	return nil
}

// ListClaims returns all active retention claims.
func (c *Coordinator) ListClaims() ([]RetentionClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return listClaimsLocked(c)
}

func listClaimsLocked(c *Coordinator) ([]RetentionClaim, error) {
	entries, err := os.ReadDir(c.retentionDir)
	if err != nil {
		return nil, err
	}
	var out []RetentionClaim
	for _, d := range entries {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.retentionDir, d.Name()))
		if err != nil {
			return nil, err
		}
		var cl RetentionClaim
		if err := decodeVersionedJSON(raw, &cl); err != nil {
			return nil, err
		}
		out = append(out, cl)
	}
	slices.SortFunc(out, func(a, b RetentionClaim) int { return strings.Compare(a.ClaimID, b.ClaimID) })
	return out, nil
}

// EvictDisposableWorkingMaterial removes disposable working upper/runtime storage ONLY when a
// complete retained checkpoint can rebuild the worker's result and no unsealed mutations exist.
func (c *Coordinator) EvictDisposableWorkingMaterial(ctx context.Context, workerID, viewID, backingDir string) (*EvictionReport, error) {
	return evictDisposableWorkingMaterial(ctx, c, workerID, viewID, backingDir)
}

func evictDisposableWorkingMaterial(ctx context.Context, c *Coordinator, workerID, viewID, backingDir string) (*EvictionReport, error) {
	j, err := c.LoadWorkerJournal(workerID)
	if err != nil {
		return nil, err
	}
	if j.LastPublishedCheckpointID == "" || j.ActivePhase != "" {
		return nil, fmt.Errorf("%w: worker=%s checkpoint=%q active_phase=%q",
			ErrEvictUnsealedWorkRefused, workerID, j.LastPublishedCheckpointID, j.ActivePhase)
	}
	claims, err := c.ListClaims()
	if err != nil {
		return nil, err
	}
	for _, cl := range claims {
		if cl.Kind == ClaimUnresolvedSettlement && (cl.WorkerID == workerID || (viewID != "" && cl.ViewID == viewID)) {
			return nil, fmt.Errorf("%w: unresolved settlement claim %s", ErrEvictUnsealedWorkRefused, cl.ClaimID)
		}
	}
	manifest, err := c.VerifyCheckpointReconstructible(ctx, j.LastPublishedCheckpointID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEvictUnsealedWorkRefused, err)
	}
	if hasUnsealedMutationsBeyondCheckpoint(backingDir, manifest) {
		return nil, fmt.Errorf("%w: backing dir has unsealed mutations beyond %s", ErrEvictUnsealedWorkRefused, manifest.CheckpointID)
	}

	var reclaimed int64
	var removed []string
	for _, sub := range []string{"working", "bases"} {
		target := filepath.Join(backingDir, sub)
		reclaimed += dirSizeBytes(target)
		if err := clearDirEntries(target); err != nil {
			return nil, err
		}
		removed = append(removed, target)
	}
	if viewID != "" {
		_ = c.RemoveInventoryEntry("inv-runtime-" + viewID)
	}
	return &EvictionReport{
		WorkerID:       workerID,
		ViewID:         viewID,
		CheckpointID:   manifest.CheckpointID,
		ReclaimedBytes: reclaimed,
		RemovedPaths:   removed,
	}, nil
}

// EvictCheckpoint removes an unreferenced checkpoint only if no active claim or stacked child references it.
func (c *Coordinator) EvictCheckpoint(ctx context.Context, checkpointID string) error {
	return evictCheckpoint(ctx, c, checkpointID)
}

func evictCheckpoint(ctx context.Context, c *Coordinator, checkpointID string) error {
	referenced, _, err := collectReferencedSets(ctx, c)
	if err != nil {
		return err
	}
	if referenced[checkpointID] {
		return fmt.Errorf("%w: %s", ErrEvictReferencedCheckpoint, checkpointID)
	}
	cpDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints", checkpointID)
	if err := os.RemoveAll(cpDir); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(c.cfg.CheckpointStore.RootDir(), "retention", checkpointID+".json"))
	return c.RemoveInventoryEntry("inv-checkpoint-" + checkpointID)
}
