package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// SealedOverlay names a finished materialized worker's immutable result.
type SealedOverlay struct {
	JobID        string
	ProjectID    string
	BaselinePath string
	OverlayPath  string
	Roots        []projectroot.RootRef
	// ParentCheckpointID names the checkpoint this worker was itself stacked on, if any.
	ParentCheckpointID string
}

// CheckpointCoordinator returns the recovery coordinator this Manager's checkpoints live under.
func (m *Manager) CheckpointCoordinator() (*recovery.Coordinator, error) {
	services, err := m.layered()
	if err != nil {
		return nil, err
	}
	return services.recovery, nil
}

// PublishSealedOverlayCheckpoint states a sealed overlay as a checkpoint, so a stacked child starts
// from immutable content however its parent ran. It is idempotent per overlay. An overlay whose
// unchanged-size base was never pinned has no exact base to record, and refuses.
func (m *Manager) PublishSealedOverlayCheckpoint(ctx context.Context, sealed SealedOverlay) (string, error) {
	services, err := m.layered()
	if err != nil {
		return "", err
	}
	store := services.recovery.CheckpointStore()
	checkpointID := "sealed-" + filepath.Base(sealed.OverlayPath)
	if _, err := store.LoadManifest(checkpointID); err == nil {
		return checkpointID, nil
	} else if !errors.Is(err, checkpoint.ErrCheckpointNotFound) {
		return "", err
	}
	manifest := checkpoint.Manifest{
		CheckpointID: checkpointID, WorkerID: sealed.JobID, TaskID: sealed.JobID, ProjectID: sealed.ProjectID,
		SourceGeneration: "1", Trigger: checkpoint.TriggerWorkerCompletion,
	}
	var parentLayers []layeredfs.ParentLayer
	if sealed.ParentCheckpointID != "" {
		manifest.ParentCheckpoints = []string{sealed.ParentCheckpointID}
		layers, _, lErr := services.recovery.OpenCheckpointChainLayers(ctx, manifest.ParentCheckpoints)
		if lErr != nil {
			return "", lErr
		}
		parentLayers = layers
	}
	for _, root := range sealed.Roots {
		manifest.SelectedRoots = append(manifest.SelectedRoots, layeredfs.RootSpec{ID: root.ID, Label: root.Label, TrunkPath: root.Path, IsPrimary: root.IsPrimary})
	}
	err = workspacebaseline.EachOverlayChange(ctx, sealed.BaselinePath, sealed.OverlayPath, sealed.Roots, func(change workspacebaseline.OverlayChange) error {
		entry, err := sealedManifestEntry(store, change)
		if err != nil {
			return err
		}
		fromParent, err := hasExplicitParentEntry(parentLayers, entry.Path)
		if err != nil {
			return err
		}
		if fromParent {
			entry.Base.Layer = layeredfs.LayerParent
		}
		manifest.Entries = append(manifest.Entries, entry)
		return nil
	})
	if err != nil {
		return "", err
	}
	retention := func(ctx context.Context, blobs, gitObjects []string) error {
		return services.recovery.CommitCheckpointRetention(ctx, sealed.JobID, checkpointID, blobs, gitObjects, manifest.ParentCheckpoints)
	}
	if err := store.PublishComposedManifest(ctx, manifest, retention); err != nil {
		return "", err
	}
	return checkpointID, nil
}

func sealedManifestEntry(store *checkpoint.Store, change workspacebaseline.OverlayChange) (checkpoint.ManifestEntry, error) {
	path := layeredfs.QualifiedPath{RootID: change.RootID, Rel: change.Rel}
	entry := checkpoint.ManifestEntry{Path: path, ContentChanged: true}
	entry.Base = layeredfs.BaseCapture{Path: path, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryAbsent}
	if change.BaseExists {
		mode := fs.FileMode(change.Base.Mode)
		entry.Base.Kind, entry.Base.Mode, entry.Base.Size = sealedEntryKind(mode), mode, change.Base.Size
		entry.Base.LinkTarget = change.Base.LinkTarget
		if entry.Base.Kind == layeredfs.EntryRegular {
			if change.Base.Opaque {
				return entry, fmt.Errorf("sealed overlay base of %q was not pinned", change.Rel)
			}
			body, err := change.Body(change.Base.SHA256)
			if err != nil {
				return entry, err
			}
			if entry.Base.SHA256, _, err = store.WriteBlobBytes(body); err != nil {
				return entry, err
			}
		}
	}
	if change.Final.Deleted {
		entry.Deleted, entry.Kind = true, layeredfs.EntryAbsent
		return entry, nil
	}
	mode := fs.FileMode(change.Final.Mode)
	entry.Kind = sealedEntryKind(mode)
	entry.Final = layeredfs.ChangeFinalAttr{Mode: mode, Size: change.Final.Size, LinkTarget: change.Final.LinkTarget}
	if entry.Kind == layeredfs.EntryRegular {
		body, err := change.Body(change.Final.SHA256)
		if err != nil {
			return entry, err
		}
		if entry.Final.SHA256, _, err = store.WriteBlobBytes(body); err != nil {
			return entry, err
		}
	}
	return entry, nil
}

func sealedEntryKind(mode fs.FileMode) layeredfs.EntryKind {
	if mode&fs.ModeSymlink != 0 {
		return layeredfs.EntrySymlink
	}
	return layeredfs.EntryRegular
}

func hasExplicitParentEntry(layers []layeredfs.ParentLayer, path layeredfs.QualifiedPath) (bool, error) {
	for _, pl := range layers {
		if pl == nil {
			continue
		}
		pe, ok, err := pl.Lookup(path)
		if err != nil {
			return false, err
		}
		if ok && !pe.Synthetic {
			return true, nil
		}
	}
	return false, nil
}

func openBranchParentLayers(ctx context.Context, services *layeredServices, branchRoot string, meta JobMeta, jobID string) ([]layeredfs.ParentLayer, []string, error) {
	parentIDs := append([]string(nil), meta.ParentCheckpointIDs...)
	if len(parentIDs) == 0 && services != nil && jobID != "" {
		if j, err := services.recovery.LoadWorkerJournal(jobID); err == nil && len(j.ParentCheckpointIDs) > 0 {
			parentIDs = append([]string(nil), j.ParentCheckpointIDs...)
		}
	}
	if len(parentIDs) == 0 {
		return nil, nil, nil
	}
	if services != nil {
		layers, _, err := services.recovery.OpenCheckpointChainLayers(ctx, parentIDs)
		return layers, parentIDs, err
	}
	storeDir := findBranchCheckpointStoreDir(branchRoot, meta, parentIDs[0])
	if storeDir == "" {
		return nil, parentIDs, nil
	}
	store, err := checkpoint.NewStore(storeDir)
	if err != nil {
		return nil, parentIDs, err
	}
	layers, err := openStoreParentChain(ctx, store, parentIDs)
	return layers, parentIDs, err
}

func recoveredParentCheckpointIDs(services *layeredServices, branchRoot, jobID string) []string {
	if meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot)); err == nil && len(meta.ParentCheckpointIDs) > 0 {
		return append([]string(nil), meta.ParentCheckpointIDs...)
	}
	if services != nil && jobID != "" {
		if j, err := services.recovery.LoadWorkerJournal(jobID); err == nil && len(j.ParentCheckpointIDs) > 0 {
			return append([]string(nil), j.ParentCheckpointIDs...)
		}
	}
	return nil
}

func findBranchCheckpointStoreDir(branchRoot string, meta JobMeta, checkpointID string) string {
	if checkpointManifestExists(meta.CheckpointStoreDir, checkpointID) {
		return meta.CheckpointStoreDir
	}
	workerBranchesDir := filepath.Dir(filepath.Dir(filepath.Clean(branchRoot)))
	configDir := filepath.Dir(workerBranchesDir)
	for _, cand := range []string{
		filepath.Join(configDir, enginepaths.WorkerRecoveryDirName, "checkpoints"),
		filepath.Join(workerBranchesDir, adapterRuntimeDirName, "recovery", "checkpoints"),
		filepath.Join(configDir, enginepaths.WorkerBaselinesDirName, ".task-checkpoints"),
	} {
		if checkpointManifestExists(cand, checkpointID) {
			return cand
		}
	}
	return ""
}

func openStoreParentChain(ctx context.Context, store *checkpoint.Store, ids []string) ([]layeredfs.ParentLayer, error) {
	seen := make(map[string]bool)
	var layers []layeredfs.ParentLayer
	var walk func([]string) error
	walk = func(cids []string) error {
		for _, id := range cids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			m, err := store.LoadManifest(id)
			if err != nil {
				return err
			}
			pl, err := checkpoint.NewCheckpointParentLayer(store, m)
			if err != nil {
				return err
			}
			layers = append(layers, pl)
			if err := walk(m.ParentCheckpoints); err != nil {
				return err
			}
		}
		return nil
	}
	return layers, walk(ids)
}
