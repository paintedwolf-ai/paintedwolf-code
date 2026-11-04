package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// StackedParents is the immutable checkpoint chain a stacked child starts from, newest first.
type StackedParents struct {
	CheckpointIDs []string
	Manifests     []checkpoint.Manifest
	Layers        []layeredfs.ParentLayer
}

// StackedViewRequest specifies a stacked child worker view. Prepare builds the view over the parents,
// through whichever claim path the worker's adapter takes, and Release undoes it if a later step fails.
type StackedViewRequest struct {
	ChildWorkerID      string
	ChildViewID        string
	TaskID             string
	ProjectID          string
	ParentCheckpointID string
	AdapterID          adapter.AdapterID
	Prepare            func(context.Context, StackedParents) (*adapter.PreparedView, error)
	Release            func(context.Context, *adapter.PreparedView) error
}

// StackedWorkerSession holds the prepared child worker view and its pinned parent chain claims.
type StackedWorkerSession struct {
	ChildWorkerID    string
	ChildViewID      string
	Parents          StackedParents
	PreparedView     *adapter.PreparedView
	RetentionClaimID string
	GitLeaseID       string
}

// PrepareStackedWorkerView verifies the parent checkpoint chain is reconstructible, pins retention
// claims and private Git object leases for the stacked child, opens the immutable chain as read-only
// layers over live trunk, and prepares the child worker view.
func (c *Coordinator) PrepareStackedWorkerView(ctx context.Context, req StackedViewRequest) (*StackedWorkerSession, error) {
	if strings.TrimSpace(req.ChildWorkerID) == "" || strings.TrimSpace(req.ParentCheckpointID) == "" || req.Prepare == nil {
		return nil, fmt.Errorf("recovery: ChildWorkerID, ParentCheckpointID, and Prepare are required")
	}
	if err := injectFault(c, PhasePreparation, "stacked_prepare_start"); err != nil {
		return nil, err
	}
	layers, manifests, err := c.OpenCheckpointChainLayers(ctx, []string{req.ParentCheckpointID})
	if err != nil {
		return nil, err
	}
	parents := StackedParents{Manifests: manifests, Layers: layers}
	blobSet := make(map[string]bool)
	gitObjSet := make(map[string]bool)
	for _, m := range manifests {
		parents.CheckpointIDs = append(parents.CheckpointIDs, m.CheckpointID)
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
		ClaimID: claimID, Kind: ClaimStackedChild, HolderID: req.ChildWorkerID, WorkerID: req.ChildWorkerID, ViewID: req.ChildViewID,
		CheckpointIDs: parents.CheckpointIDs, Blobs: blobs, GitObjects: gitObjs, GitLeaseID: gitLeaseID,
	}); err != nil {
		return nil, err
	}
	view, err := req.Prepare(ctx, parents)
	if err != nil {
		return nil, errors.Join(err, c.ReleaseClaim(claimID))
	}
	abort := func(cause error) (*StackedWorkerSession, error) {
		var released error
		if req.Release != nil {
			released = req.Release(ctx, view)
		}
		return nil, errors.Join(cause, released, c.ReleaseClaim(claimID))
	}
	if len(manifests) > 0 {
		if err := applyPrivateGitSnapshotToView(ctx, c, view, req.ChildWorkerID, manifests[0].PrivateGit); err != nil {
			return abort(err)
		}
	}
	// A materialized child has no view to rebuild; its claim alone keeps the chain retained.
	if view.Facts.PreparationKind == adapter.PreparationMounted {
		if err := c.recordStackedWorker(req, parents, view); err != nil {
			return abort(err)
		}
	}
	return &StackedWorkerSession{
		ChildWorkerID: req.ChildWorkerID, ChildViewID: view.ViewID, Parents: parents, PreparedView: view,
		RetentionClaimID: claimID, GitLeaseID: gitLeaseID,
	}, nil
}

// recordStackedWorker keeps what a claim already recorded for this worker and adds its parent chain,
// which garbage collection and rebuild both read.
func (c *Coordinator) recordStackedWorker(req StackedViewRequest, parents StackedParents, view *adapter.PreparedView) error {
	journal, err := c.LoadWorkerJournal(req.ChildWorkerID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	journal.FormatVersion, journal.WorkerID, journal.ViewID = FormatVersion, req.ChildWorkerID, view.ViewID
	journal.AdapterID, journal.BootID, journal.UpdatedAt = req.AdapterID, c.cfg.CurrentBootID, time.Now().UTC()
	journal.ParentCheckpointIDs = parents.CheckpointIDs
	journal.ProjectID, journal.TaskID = req.ProjectID, req.TaskID
	if len(parents.Manifests) > 0 {
		if journal.ProjectID == "" {
			journal.ProjectID = parents.Manifests[0].ProjectID
		}
		if journal.TaskID == "" {
			journal.TaskID = parents.Manifests[0].TaskID
		}
	}
	return c.SaveWorkerJournal(journal)
}

// OverlayParentChainOnMaterializedView writes a parent chain into a materialized view's own tree,
// oldest first, reading only immutable checkpoint content.
func (c *Coordinator) OverlayParentChainOnMaterializedView(view *adapter.PreparedView, newestToOldest []checkpoint.Manifest) error {
	for i := len(newestToOldest) - 1; i >= 0; i-- {
		for _, e := range newestToOldest[i].Entries {
			matPath := materializedPathForRoot(view, e.Path)
			if matPath == "" {
				continue
			}
			if err := c.overlayParentEntry(matPath, e); err != nil {
				return fmt.Errorf("overlay parent entry %s: %w", e.Path.Rel, err)
			}
		}
	}
	return nil
}

func (c *Coordinator) overlayParentEntry(matPath string, e checkpoint.ManifestEntry) error {
	if e.Deleted || e.Kind == layeredfs.EntryAbsent {
		return os.RemoveAll(matPath)
	}
	if e.DirectoryReplaced {
		if err := os.RemoveAll(matPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(matPath), 0o755); err != nil {
		return err
	}
	switch e.Kind {
	case layeredfs.EntryDirectory:
		return os.MkdirAll(matPath, e.Final.Mode.Perm()|0o700)
	case layeredfs.EntrySymlink:
		if err := os.RemoveAll(matPath); err != nil {
			return err
		}
		return os.Symlink(e.Final.LinkTarget, matPath)
	case layeredfs.EntryRegular:
		raw, err := c.cfg.CheckpointStore.ReadBlob(e.Final.SHA256)
		if err != nil {
			return err
		}
		mode := e.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.RemoveAll(matPath); err != nil {
			return err
		}
		return os.WriteFile(matPath, raw, mode)
	}
	return nil
}
