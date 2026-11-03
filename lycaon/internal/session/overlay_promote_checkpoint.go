package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	previewBindMu         sync.Mutex
	boundPreviewsByJob    = map[string]*checkpoint.PromotionPreview{}
	previewBindIntentJobs = map[string]bool{}
)

// MarkTaskCheckpointPreviewBindIntent marks jobID so the next checkpoint assessment binds its PromotionPreview.
func MarkTaskCheckpointPreviewBindIntent(jobID string) {
	previewBindMu.Lock()
	previewBindIntentJobs[jobID] = true
	delete(boundPreviewsByJob, jobID)
	previewBindMu.Unlock()
}

// ClearTaskCheckpointPreview clears any bound PromotionPreview for jobID.
func ClearTaskCheckpointPreview(jobID string) {
	previewBindMu.Lock()
	delete(boundPreviewsByJob, jobID)
	delete(previewBindIntentJobs, jobID)
	previewBindMu.Unlock()
}

// MaybeBindTaskCheckpointPreview retains the selected checkpoint until a new preview begins.
func MaybeBindTaskCheckpointPreview(jobID string, cpPreview *checkpoint.PromotionPreview) {
	previewBindMu.Lock()
	if previewBindIntentJobs[jobID] || boundPreviewsByJob[jobID] == nil {
		delete(previewBindIntentJobs, jobID)
		boundPreviewsByJob[jobID] = cpPreview
	}
	previewBindMu.Unlock()
}

func boundTaskCheckpointID(jobID string) string {
	previewBindMu.Lock()
	defer previewBindMu.Unlock()
	if preview := boundPreviewsByJob[jobID]; preview != nil {
		return preview.CheckpointID
	}
	return ""
}

// RevalidateTaskCheckpointPreview checks whether any bound PromotionPreview target observations drifted.
func RevalidateTaskCheckpointPreview(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) error {
	if task == nil {
		return nil
	}
	previewBindMu.Lock()
	bound := boundPreviewsByJob[task.ID]
	previewBindMu.Unlock()
	if bound == nil {
		return nil
	}
	store, _, promoter, ok, err := NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return err
	}
	// A worker has one durable promotion completion. Refresh review before applying
	// a superseded checkpoint rather than marking newer, unreviewed work merged.
	latest, err := store.LatestCheckpointID()
	if err != nil {
		return err
	}
	if latest != bound.CheckpointID {
		return checkpoint.ErrStalePreviewCheckpointChanged
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return promoter.RevalidatePreviewTargets(bound, nil)
}

// TargetRootsForTaskCheckpoint resolves the target root map for a task's checkpoint manifest and rejects unmapped RootIDs.
func TargetRootsForTaskCheckpoint(task *api.WorkerTask, roots []projectroot.RootRef, manifest checkpoint.Manifest) (map[string]string, error) {
	targetRoots := make(map[string]string, len(roots)+4)
	primaryDir := ""
	if task != nil {
		primaryDir = strings.TrimSpace(task.WorkspacePath)
	}
	for _, r := range roots {
		if r.ID != "" && r.Path != "" {
			targetRoots[r.ID] = r.Path
		}
		if r.IsPrimary && r.Path != "" {
			primaryDir = r.Path
		}
	}
	if primaryDir != "" {
		aliases := []string{"primary", "root-0", ""}
		if task != nil && strings.TrimSpace(task.WorkspaceRootID) != "" {
			aliases = append(aliases, strings.TrimSpace(task.WorkspaceRootID))
		}
		for _, alias := range aliases {
			if _, exists := targetRoots[alias]; !exists {
				targetRoots[alias] = primaryDir
			}
		}
	}
	for _, obs := range manifest.SourceObservations {
		if strings.TrimSpace(targetRoots[obs.Path.RootID]) == "" {
			return nil, fmt.Errorf("%w: unresolved checkpoint source root id %q", layeredfs.ErrInvalidRoot, obs.Path.RootID)
		}
	}
	for _, entry := range manifest.Entries {
		if strings.TrimSpace(targetRoots[entry.Path.RootID]) == "" {
			return nil, fmt.Errorf("%w: unresolved checkpoint root id %q", layeredfs.ErrInvalidRoot, entry.Path.RootID)
		}
	}
	return targetRoots, nil
}

// DefaultCheckpointRefImporter imports and validates checkpointed private Git objects and refs into the target repository.
func DefaultCheckpointRefImporter(store *checkpoint.Store, manifest checkpoint.Manifest, targetRoots map[string]string, authorizedGitDirs ...string) func(context.Context, string, string, checkpoint.PrivateGitSnapshot) (map[string]string, error) {
	storeDir := ""
	if store != nil {
		storeDir = store.RootDir()
	}
	return func(ctx context.Context, _ string, workerID string, git checkpoint.PrivateGitSnapshot) (map[string]string, error) {
		if strings.TrimSpace(workerID) == "" {
			workerID = manifest.WorkerID
		}
		return checkpoint.ImportAndValidateCheckpointGitRefs(ctx, storeDir, manifest.CheckpointID, workerID, git, targetRoots, authorizedGitDirs...)
	}
}

// NewTaskCheckpointPromoter creates a checkpoint.Promoter bound to task's published checkpoint store and target roots.
func NewTaskCheckpointPromoter(task *api.WorkerTask, roots []projectroot.RootRef) (*checkpoint.Store, checkpoint.Manifest, *checkpoint.Promoter, bool, error) {
	store, manifest, ok, err := OpenTaskCheckpointStore(task)
	if err != nil || !ok {
		return nil, checkpoint.Manifest{}, nil, ok, err
	}
	targetRoots, err := TargetRootsForTaskCheckpoint(task, roots, manifest)
	if err != nil {
		return nil, checkpoint.Manifest{}, nil, true, err
	}
	refImporter := DefaultCheckpointRefImporter(store, manifest, targetRoots)
	promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{Store: store, TargetRoots: targetRoots, RefImporter: refImporter})
	if err != nil {
		return nil, checkpoint.Manifest{}, nil, true, err
	}
	return store, manifest, promoter, true, nil
}

// SyncTaskCheckpointToWorkspace rehydrates task.WorkspaceRoot from the published checkpoint manifest.
func SyncTaskCheckpointToWorkspace(task *api.WorkerTask) error {
	if task == nil {
		return nil
	}
	store, manifest, hasCP, err := OpenTaskCheckpointStore(task)
	if err != nil || !hasCP {
		return err
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		if cpDir := CheckpointStoreDirForTask(task); cpDir != "" {
			task.WorkspaceRoot = filepath.Join(cpDir, ".workspace-rehydrated")
		}
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		return nil
	}
	if err := os.MkdirAll(task.WorkspaceRoot, 0o755); err != nil {
		return err
	}
	multiRoot := len(manifest.SelectedRoots) > 1
	if !multiRoot {
		for _, entry := range manifest.Entries {
			id := strings.TrimSpace(entry.Path.RootID)
			if id != "" && id != "primary" && id != "root-0" && (task == nil || id != strings.TrimSpace(task.WorkspaceRootID)) {
				multiRoot = true
				break
			}
		}
	}
	for _, entry := range manifest.Entries {
		targetDir := task.WorkspaceRoot
		if multiRoot {
			branchDir, bErr := projectroot.BranchDirForID(entry.Path.RootID)
			if bErr != nil {
				return bErr
			}
			targetDir = filepath.Join(task.WorkspaceRoot, branchDir)
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return err
			}
		}
		if err := applyManifestEntryToRoot(store, targetDir, entry, true); err != nil {
			return err
		}
	}
	return nil
}

// ApplyTaskCheckpointAttributes applies checkpoint file modes, symlinks, directories, deletions, and xattrs to target roots.
func ApplyTaskCheckpointAttributes(task *api.WorkerTask, roots []projectroot.RootRef) error {
	store, manifest, hasCP, err := OpenTaskCheckpointStore(task)
	if err != nil || !hasCP {
		return err
	}
	if rec, exists, lErr := store.LoadPromotionRecord("tx-" + manifest.CheckpointID); lErr != nil || (exists && rec.State == checkpoint.PromotionStateApplied) {
		return lErr
	}
	targetRoots, err := TargetRootsForTaskCheckpoint(task, roots, manifest)
	if err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		if err := applyManifestEntryToRoot(store, targetRoots[entry.Path.RootID], entry, false); err != nil {
			return err
		}
	}
	return nil
}

func applyManifestEntryToRoot(store *checkpoint.Store, rootDir string, entry checkpoint.ManifestEntry, writeRegularBytes bool) error {
	if !filepath.IsLocal(entry.Path.Rel) || entry.Path.Rel == "." {
		return fmt.Errorf("%w: invalid entry path %q", checkpoint.ErrInvalidManifest, entry.Path.Rel)
	}
	abs := filepath.Join(rootDir, filepath.FromSlash(entry.Path.Rel))
	if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
		return os.RemoveAll(abs)
	}
	if entry.DirectoryReplaced {
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
	}
	switch entry.Kind {
	case layeredfs.EntryDirectory:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o755
		}
		if err := os.MkdirAll(abs, mode|0o700); err != nil {
			return err
		}
		if err := os.Chmod(abs, mode); err != nil {
			return err
		}
		return checkpoint.ApplyPathXattrs(abs, entry.Final.Xattrs)
	case layeredfs.EntrySymlink:
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
		return os.Symlink(entry.Final.LinkTarget, abs)
	case layeredfs.EntryRegular:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if writeRegularBytes {
			raw, rErr := store.ReadBlob(entry.Final.SHA256)
			if rErr != nil {
				return rErr
			}
			if _, err := fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.Location{Root: rootDir, Rel: filepath.FromSlash(entry.Path.Rel)},
				Source:   bytes.NewReader(raw),
				Mode:     mode,
				DirMode:  0o755,
			}); err != nil {
				return err
			}
		} else if entry.Final.Mode != 0 {
			if err := os.Chmod(abs, mode); err != nil {
				return err
			}
		}
		return checkpoint.ApplyPathXattrs(abs, entry.Final.Xattrs)
	default:
		return fmt.Errorf("checkpoint: unsupported entry kind %q", entry.Kind)
	}
}

// CommitTaskCheckpointJournal transactionally applies and journals a task's checkpoint promotion and #1671 ref import.
func CommitTaskCheckpointJournal(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) error {
	store, manifest, promoter, ok, err := NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return err
	}
	txID := "tx-" + manifest.CheckpointID
	if rec, exists, lErr := store.LoadPromotionRecord(txID); lErr != nil || (exists && rec.State == checkpoint.PromotionStateApplied) {
		return lErr
	}
	cpPreview, err := promoter.Preview(ctx, checkpoint.PreviewRequest{CheckpointID: manifest.CheckpointID, ExpectedWorkerID: manifest.WorkerID, ExpectedProjectID: task.ProjectID})
	if err != nil {
		return err
	}
	_, err = checkpoint.NewHostPromotion(promoter).Apply(ctx, checkpoint.ApplyRequest{TransactionID: txID, Preview: cpPreview, ImportGitRefs: true})
	return err
}

// RollbackTaskCheckpointJournal unwinds checkpoint effects before ordinary host file undo.
func RollbackTaskCheckpointJournal(ctx context.Context, task *api.WorkerTask, roots []projectroot.RootRef) error {
	_, _, promoter, ok, err := NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return err
	}
	return checkpoint.NewHostPromotion(promoter).Rollback(ctx)
}
