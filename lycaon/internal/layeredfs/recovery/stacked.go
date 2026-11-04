package recovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// StackedViewRequest specifies parameters for preparing a stacked child worker view
// over an immutable parent checkpoint chain and live trunk roots.
type StackedViewRequest struct {
	ChildWorkerID      string
	ChildViewID        string
	TaskID             string
	ProjectID          string
	ParentCheckpointID string
	Adapter            adapter.Adapter
	PrepareReq         adapter.PrepareRequest
}

// StackedWorkerSession holds the prepared child worker view and its pinned parent chain claims.
type StackedWorkerSession struct {
	ChildWorkerID       string
	ChildViewID         string
	ParentCheckpointIDs []string
	ParentManifests     []checkpoint.Manifest
	ParentLayers        []layeredfs.ParentLayer
	PreparedView        *adapter.PreparedView
	RetentionClaimID    string
	GitLeaseID          string
}

// PrepareStackedWorkerView verifies the parent checkpoint chain is reconstructible, pins retention
// claims and private Git object leases for the stacked child, opens the immutable parent checkpoint
// chain as read-only ParentLayers over live trunk, and prepares the child worker view.
func (c *Coordinator) PrepareStackedWorkerView(ctx context.Context, req StackedViewRequest) (*StackedWorkerSession, error) {
	return prepareStackedWorkerView(ctx, c, req)
}

func prepareStackedWorkerView(ctx context.Context, c *Coordinator, req StackedViewRequest) (*StackedWorkerSession, error) {
	if strings.TrimSpace(req.ChildWorkerID) == "" || strings.TrimSpace(req.ParentCheckpointID) == "" || req.Adapter == nil {
		return nil, fmt.Errorf("recovery: ChildWorkerID, ParentCheckpointID, and Adapter are required")
	}
	if err := injectFault(c, PhasePreparation, "stacked_prepare_start"); err != nil {
		return nil, err
	}

	parentLayers, parentManifests, err := c.OpenCheckpointChainLayers(ctx, []string{req.ParentCheckpointID})
	if err != nil {
		return nil, err
	}
	var parentIDs []string
	blobSet := make(map[string]bool)
	gitObjSet := make(map[string]bool)
	for _, m := range parentManifests {
		parentIDs = append(parentIDs, m.CheckpointID)
		collectManifestBlobs(m, blobSet)
		for _, oid := range m.PrivateGit.RetainedObjects {
			if oid != "" {
				gitObjSet[oid] = true
			}
		}
	}

	var blobs, gitObjs []string
	for b := range blobSet {
		blobs = append(blobs, b)
	}
	for g := range gitObjSet {
		gitObjs = append(gitObjs, g)
	}

	var gitLeaseID string
	if c.cfg.GitStore != nil && len(gitObjs) > 0 {
		lid, lErr := c.cfg.GitStore.RetainForStackedChild(req.ChildWorkerID, req.ParentCheckpointID, gitObjs)
		if lErr != nil {
			return nil, lErr
		}
		gitLeaseID = lid
	}

	claimID := fmt.Sprintf("claim-stacked-%s-%s", req.ChildWorkerID, req.ParentCheckpointID)
	if err := c.AcquireClaim(RetentionClaim{
		ClaimID:       claimID,
		Kind:          ClaimStackedChild,
		HolderID:      req.ChildWorkerID,
		WorkerID:      req.ChildWorkerID,
		ViewID:        req.ChildViewID,
		CheckpointIDs: parentIDs,
		Blobs:         blobs,
		GitObjects:    gitObjs,
		GitLeaseID:    gitLeaseID,
	}); err != nil {
		return nil, err
	}

	prepReq := req.PrepareReq
	if req.ChildViewID != "" {
		prepReq.ViewID = req.ChildViewID
	}
	if prepReq.ViewID == "" {
		prepReq.ViewID = fmt.Sprintf("view-stacked-%s-%d", req.ChildWorkerID, c.seq.Add(1))
	}
	prepReq.ParentLayers = parentLayers
	if c.cfg.GitManager != nil && prepReq.GitInterceptor == nil {
		prepReq.GitInterceptor = c.cfg.GitManager
	}

	view, err := req.Adapter.Prepare(ctx, prepReq)
	if err != nil {
		_ = c.ReleaseClaim(claimID)
		return nil, err
	}
	if binder, ok := req.Adapter.(PrivateGitBinder); ok && c.cfg.GitManager != nil {
		if bErr := binder.BindPrivateGit(view.ViewID, req.ChildWorkerID, c.cfg.GitManager); bErr != nil {
			_, _ = req.Adapter.Release(ctx, view.ViewID)
			_ = c.ReleaseClaim(claimID)
			return nil, bErr
		}
	}

	if view.Facts.PreparationKind == adapter.PreparationMaterialized {
		if err := overlayParentChainOnMaterializedRoots(c, view, parentManifests); err != nil {
			_, _ = req.Adapter.Release(ctx, view.ViewID)
			_ = c.ReleaseClaim(claimID)
			return nil, err
		}
	}

	if len(parentManifests) > 0 {
		newestParent := parentManifests[0]
		if err := applyPrivateGitSnapshotToView(ctx, c, view, req.ChildWorkerID, newestParent.PrivateGit); err != nil {
			_, _ = req.Adapter.Release(ctx, view.ViewID)
			_ = c.ReleaseClaim(claimID)
			return nil, err
		}
	}

	projectID := req.ProjectID
	if projectID == "" && len(parentManifests) > 0 {
		projectID = parentManifests[0].ProjectID
	}
	taskID := req.TaskID
	if taskID == "" && len(parentManifests) > 0 {
		taskID = parentManifests[0].TaskID
	}
	if err := c.SaveWorkerJournal(WorkerLifecycleJournal{
		FormatVersion:       FormatVersion,
		WorkerID:            req.ChildWorkerID,
		ViewID:              view.ViewID,
		TaskID:              taskID,
		ProjectID:           projectID,
		AdapterID:           req.Adapter.ID(),
		BootID:              c.cfg.CurrentBootID,
		ParentCheckpointIDs: parentIDs,
		UpdatedAt:           time.Now().UTC(),
	}); err != nil {
		_, _ = req.Adapter.Release(ctx, view.ViewID)
		_ = c.ReleaseClaim(claimID)
		return nil, err
	}

	if err := recordPreparedViewIdentity(c, req.ChildWorkerID, req.Adapter, view); err != nil {
		_, _ = req.Adapter.Release(ctx, view.ViewID)
		_ = c.ReleaseClaim(claimID)
		return nil, err
	}

	return &StackedWorkerSession{
		ChildWorkerID:       req.ChildWorkerID,
		ChildViewID:         view.ViewID,
		ParentCheckpointIDs: parentIDs,
		ParentManifests:     parentManifests,
		ParentLayers:        parentLayers,
		PreparedView:        view,
		RetentionClaimID:    claimID,
		GitLeaseID:          gitLeaseID,
	}, nil
}

func overlayParentChainOnMaterializedRoots(
	c *Coordinator,
	view *adapter.PreparedView,
	newestToOldest []checkpoint.Manifest,
) error {
	for i := len(newestToOldest) - 1; i >= 0; i-- {
		m := newestToOldest[i]
		for _, e := range m.Entries {
			matPath := materializedPathForRoot(view, e.Path)
			if matPath == "" {
				continue
			}
			if e.Deleted || e.Kind == layeredfs.EntryAbsent {
				_ = os.RemoveAll(matPath)
				continue
			}
			if e.DirectoryReplaced {
				_ = os.RemoveAll(matPath)
			}
			_ = os.MkdirAll(filepath.Dir(matPath), 0o755)
			switch e.Kind {
			case layeredfs.EntryDirectory:
				_ = os.MkdirAll(matPath, e.Final.Mode.Perm()|0o700)
			case layeredfs.EntrySymlink:
				_ = os.RemoveAll(matPath)
				if err := os.Symlink(e.Final.LinkTarget, matPath); err != nil {
					return err
				}
			case layeredfs.EntryRegular:
				raw, err := c.cfg.CheckpointStore.ReadBlob(e.Final.SHA256)
				if err != nil {
					return err
				}
				mode := e.Final.Mode.Perm()
				if mode == 0 {
					mode = 0o644
				}
				if err := os.WriteFile(matPath, raw, mode); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
