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

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// VerifyCheckpointReconstructible verifies that a checkpoint manifest is fully published (not a half-written .tmp)
// and that all referenced content blobs, base blobs, candidate bases, parent layers, and private Git objects exist.
func (c *Coordinator) VerifyCheckpointReconstructible(ctx context.Context, checkpointID string) (checkpoint.Manifest, error) {
	return verifyChainReconstructible(ctx, c, checkpointID, make(map[string]bool))
}

func verifyChainReconstructible(
	ctx context.Context,
	c *Coordinator,
	checkpointID string,
	visiting map[string]bool,
) (checkpoint.Manifest, error) {
	if strings.TrimSpace(checkpointID) == "" {
		return checkpoint.Manifest{}, fmt.Errorf("%w: empty checkpoint_id", ErrIncompleteCheckpoint)
	}
	if visiting[checkpointID] {
		return checkpoint.Manifest{}, fmt.Errorf("%w: cyclic parent chain at %s", ErrCorruptRecoveryMetadata, checkpointID)
	}
	visiting[checkpointID] = true
	defer delete(visiting, checkpointID)

	cpDir := filepath.Join(c.cfg.CheckpointStore.RootDir(), "checkpoints", checkpointID)
	finalPath := filepath.Join(cpDir, "manifest.json")
	tmpPath := filepath.Join(cpDir, "manifest.json.tmp")
	if _, err := os.Stat(finalPath); err != nil {
		if _, tErr := os.Stat(tmpPath); tErr == nil {
			return checkpoint.Manifest{}, fmt.Errorf("%w: %s has only uncommitted manifest.json.tmp", ErrIncompleteCheckpoint, checkpointID)
		}
		return checkpoint.Manifest{}, fmt.Errorf("%w: %s: %w", ErrIncompleteCheckpoint, checkpointID, err)
	}

	m, err := c.cfg.CheckpointStore.LoadManifest(checkpointID)
	if err != nil {
		if errors.Is(err, checkpoint.ErrMissingBaseBlob) || errors.Is(err, checkpoint.ErrCorruptBlob) {
			return checkpoint.Manifest{}, fmt.Errorf("%w: %w", ErrMissingRetainedObject, err)
		}
		return checkpoint.Manifest{}, fmt.Errorf("%w: %w", ErrIncompleteCheckpoint, err)
	}

	for _, e := range m.Entries {
		for _, cb := range e.CandidateBases {
			if cb.Kind == layeredfs.EntryRegular && cb.SHA256 != "" {
				if _, bErr := c.cfg.CheckpointStore.VerifyBlob(cb.SHA256, cb.Size); bErr != nil {
					return checkpoint.Manifest{}, fmt.Errorf("%w: candidate base %s: %w", ErrMissingRetainedObject, cb.SHA256, bErr)
				}
			}
		}
	}

	if err := verifyCheckpointGitObjects(ctx, c, m.PrivateGit); err != nil {
		return checkpoint.Manifest{}, err
	}

	for _, parentID := range m.ParentCheckpoints {
		if _, pErr := verifyChainReconstructible(ctx, c, parentID, visiting); pErr != nil {
			return checkpoint.Manifest{}, fmt.Errorf("%w: parent %s: %w", ErrParentCheckpointNotFound, parentID, pErr)
		}
	}
	return m, nil
}

func verifyCheckpointGitObjects(ctx context.Context, c *Coordinator, pg checkpoint.PrivateGitSnapshot) error {
	if len(pg.RetainedObjects) == 0 && len(pg.Refs) == 0 && pg.HeadRef == "" {
		return nil
	}
	if c.cfg.GitStore != nil {
		for _, oid := range pg.RetainedObjects {
			if !c.cfg.GitStore.HasObject(oid) {
				return fmt.Errorf("%w: missing git object %s", ErrMissingRetainedObject, oid)
			}
		}
	}
	if c.cfg.GitManager != nil && len(pg.Refs) > 0 {
		headOID := pg.Refs[pg.HeadRef]
		if headOID == "" {
			headOID = pg.Refs["HEAD"]
		}
		st := &privategit.CheckpointGitState{
			HeadSymbolic: pg.HeadRef,
			HeadOID:      headOID,
			Refs:         pg.Refs,
		}
		if err := c.cfg.GitManager.VerifyCheckpointHistory(ctx, st); err != nil {
			return fmt.Errorf("%w: git history unreachable: %w", ErrMissingRetainedObject, err)
		}
	}
	return nil
}

// OpenCheckpointChainLayers resolves a parent checkpoint chain (newest to oldest), verifies each
// checkpoint is reconstructible, and opens each as an immutable read-only ParentLayer.
func (c *Coordinator) OpenCheckpointChainLayers(ctx context.Context, checkpointIDs []string) ([]layeredfs.ParentLayer, []checkpoint.Manifest, error) {
	return openCheckpointChainLayers(ctx, c, checkpointIDs)
}

func openCheckpointChainLayers(ctx context.Context, c *Coordinator, checkpointIDs []string) ([]layeredfs.ParentLayer, []checkpoint.Manifest, error) {
	seen := make(map[string]bool)
	var layers []layeredfs.ParentLayer
	var manifests []checkpoint.Manifest

	var walk func(ids []string) error
	walk = func(ids []string) error {
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			m, err := c.VerifyCheckpointReconstructible(ctx, id)
			if err != nil {
				return err
			}
			layer, err := c.cfg.CheckpointStore.OpenParentLayer(id)
			if err != nil {
				return err
			}
			layers = append(layers, layer)
			manifests = append(manifests, m)
			if err := walk(m.ParentCheckpoints); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(checkpointIDs); err != nil {
		return nil, nil, err
	}
	return layers, manifests, nil
}

// ApplyCheckpointToView reconstructs a validated checkpoint's entries, bases, and private Git state
// into a newly prepared worker view.
func (c *Coordinator) ApplyCheckpointToView(ctx context.Context, view *adapter.PreparedView, m checkpoint.Manifest) error {
	return applyCheckpointToView(ctx, c, view, m)
}

func applyCheckpointToView(ctx context.Context, c *Coordinator, view *adapter.PreparedView, m checkpoint.Manifest) error {
	if view == nil || view.Core == nil {
		return fmt.Errorf("recovery: PreparedView with Core is required")
	}
	for _, e := range m.Entries {
		if e.Base.Path.RootID != "" && e.Base.Path.Rel != "" {
			base := e.Base
			base.BackingFile = ""
			if err := view.Core.RegisterSeedBase(base); err != nil {
				return err
			}
		}
	}

	entries := slices.Clone(m.Entries)
	slices.SortFunc(entries, compareEntriesForReconstruction)

	groupPrimary := make(map[string]layeredfs.QualifiedPath)
	for _, e := range entries {
		if err := applySingleEntryToView(c, view, e, groupPrimary); err != nil {
			return err
		}
	}
	return restorePrivateGitToView(ctx, c, view, m)
}

func compareEntriesForReconstruction(a, b checkpoint.ManifestEntry) int {
	aDel := a.Deleted || a.Kind == layeredfs.EntryAbsent
	bDel := b.Deleted || b.Kind == layeredfs.EntryAbsent
	if aDel != bDel {
		if aDel {
			return -1
		}
		return 1
	}
	da := strings.Count(a.Path.Rel, "/")
	db := strings.Count(b.Path.Rel, "/")
	if aDel && da != db {
		return db - da
	}
	if da != db {
		return da - db
	}
	return strings.Compare(a.Path.RootID+":"+a.Path.Rel, b.Path.RootID+":"+b.Path.Rel)
}

func applySingleEntryToView(
	c *Coordinator,
	view *adapter.PreparedView,
	e checkpoint.ManifestEntry,
	groupPrimary map[string]layeredfs.QualifiedPath,
) error {
	core := view.Core
	matPath := materializedPathForRoot(view, e.Path)

	if e.Deleted || e.Kind == layeredfs.EntryAbsent {
		_ = core.Unlink(e.Path, e.Base.Kind == layeredfs.EntryDirectory)
		_ = core.Unlink(e.Path, false)
		if matPath != "" {
			_ = os.RemoveAll(matPath)
		}
		return nil
	}
	if e.DirectoryReplaced {
		_ = core.Unlink(e.Path, true)
		_ = core.Unlink(e.Path, false)
		if matPath != "" {
			_ = os.RemoveAll(matPath)
		}
	}
	ensureParentDirsInCore(core, e.Path, matPath)

	switch e.Kind {
	case layeredfs.EntryDirectory:
		mode := e.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o755
		}
		if _, err := core.Mkdir(e.Path, mode); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if matPath != "" {
			if err := os.MkdirAll(matPath, mode|0o700); err != nil {
				return err
			}
		}
	case layeredfs.EntrySymlink:
		_ = core.Unlink(e.Path, false)
		if _, err := core.Symlink(e.Final.LinkTarget, e.Path); err != nil {
			return err
		}
		if matPath != "" {
			_ = os.RemoveAll(matPath)
			if err := os.Symlink(e.Final.LinkTarget, matPath); err != nil {
				return err
			}
		}
	case layeredfs.EntryRegular:
		if err := applyRegularEntryToView(c, core, e, matPath, groupPrimary); err != nil {
			return err
		}
	}
	for k, v := range e.Final.Xattrs {
		_ = core.SetXattr(e.Path, k, v, false)
	}
	return nil
}

func applyRegularEntryToView(
	c *Coordinator,
	core *layeredfs.Core,
	e checkpoint.ManifestEntry,
	matPath string,
	groupPrimary map[string]layeredfs.QualifiedPath,
) error {
	if grp := e.Final.HardLinkGroup; grp != "" {
		if src, ok := groupPrimary[grp]; ok && src.RootID == e.Path.RootID {
			_ = core.Unlink(e.Path, false)
			if _, err := core.Link(src, e.Path); err == nil {
				if matPath != "" {
					srcMat := filepath.Join(filepath.Dir(matPath), mustRel(filepath.Dir(e.Path.Rel), src.Rel))
					_ = os.RemoveAll(matPath)
					_ = os.Link(srcMat, matPath)
				}
				return nil
			}
		} else {
			groupPrimary[grp] = e.Path
		}
	}
	raw, err := c.cfg.CheckpointStore.ReadBlob(e.Final.SHA256)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMissingRetainedObject, err)
	}
	mode := e.Final.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if _, err := core.WriteFile(e.Path, raw, mode); err != nil {
		return err
	}
	if matPath != "" {
		if err := os.WriteFile(matPath, raw, mode); err != nil {
			return err
		}
	}
	return nil
}
