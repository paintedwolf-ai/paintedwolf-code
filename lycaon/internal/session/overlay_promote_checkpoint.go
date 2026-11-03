package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
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

// MaybeBindTaskCheckpointPreview binds cpPreview to jobID if preview-bind intent was marked.
func MaybeBindTaskCheckpointPreview(jobID string, cpPreview *checkpoint.PromotionPreview) {
	previewBindMu.Lock()
	if previewBindIntentJobs[jobID] {
		delete(previewBindIntentJobs, jobID)
		boundPreviewsByJob[jobID] = cpPreview
	}
	previewBindMu.Unlock()
}

// RevalidateTaskCheckpointPreview checks whether any bound PromotionPreview target observations drifted.
func RevalidateTaskCheckpointPreview(task *api.WorkerTask) error {
	if task == nil {
		return nil
	}
	previewBindMu.Lock()
	bound := boundPreviewsByJob[task.ID]
	previewBindMu.Unlock()
	if bound == nil {
		return nil
	}
	_, _, promoter, ok, err := NewTaskCheckpointPromoter(task, nil, nil)
	if err != nil || !ok {
		return err
	}
	curPreview, err := promoter.Preview(context.Background(), checkpoint.PreviewRequest{CheckpointID: bound.CheckpointID})
	if err != nil {
		return err
	}
	for k, oldObs := range bound.TargetObservations {
		curObs, exists := curPreview.TargetObservations[k]
		if !exists || curObs.Kind != oldObs.Kind || curObs.SHA256 != oldObs.SHA256 || curObs.Size != oldObs.Size {
			return fmt.Errorf("%w: target %s changed since preview", checkpoint.ErrStalePreviewTargetChanged, k)
		}
	}
	return nil
}

// TargetRootsForTaskCheckpoint resolves the target root map for a task's checkpoint manifest.
func TargetRootsForTaskCheckpoint(task *api.WorkerTask, roots []projectroot.RootRef, manifest checkpoint.Manifest) map[string]string {
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
		for _, alias := range []string{"primary", "root", ""} {
			if _, exists := targetRoots[alias]; !exists {
				targetRoots[alias] = primaryDir
			}
		}
		for _, entry := range manifest.Entries {
			if _, exists := targetRoots[entry.Path.RootID]; !exists {
				targetRoots[entry.Path.RootID] = primaryDir
			}
		}
	}
	return targetRoots
}

// DefaultCheckpointRefImporter imports checkpointed private Git refs into the target repository's refs/lycaon/workers/<worker-id>/... namespace.
func DefaultCheckpointRefImporter(targetRoots map[string]string) func(context.Context, string, string, checkpoint.PrivateGitSnapshot) (map[string]string, error) {
	return func(_ context.Context, _ string, workerID string, git checkpoint.PrivateGitSnapshot) (map[string]string, error) {
		if len(git.Refs) == 0 {
			return nil, nil
		}
		cleanWorker := filepath.Clean(strings.TrimSpace(workerID))
		if cleanWorker == "" || cleanWorker == "." || strings.Contains(cleanWorker, "..") {
			return nil, fmt.Errorf("invalid worker id %q for ref import", workerID)
		}
		primaryDir := targetRoots["primary"]
		for _, d := range targetRoots {
			if primaryDir == "" && d != "" {
				primaryDir = d
			}
		}
		imported := make(map[string]string, len(git.Refs))
		for refName, oid := range git.Refs {
			rel := strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(refName)), "/"), "refs/")
			cleanRel := filepath.ToSlash(filepath.Clean(rel))
			if cleanRel == "" || cleanRel == "." || strings.HasPrefix(cleanRel, "..") {
				continue
			}
			nsRef := "refs/lycaon/workers/" + cleanWorker + "/" + cleanRel
			imported[nsRef] = oid
			if primaryDir != "" {
				refPath := filepath.Join(primaryDir, ".git", filepath.FromSlash(nsRef))
				if err := os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
					return nil, err
				}
				tmpPath := refPath + ".tmp"
				if err := os.WriteFile(tmpPath, []byte(strings.TrimSpace(oid)+"\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.Rename(tmpPath, refPath); err != nil {
					return nil, err
				}
			}
		}
		return imported, nil
	}
}

// NewTaskCheckpointPromoter creates a checkpoint.Promoter bound to task's published checkpoint store and target roots.
func NewTaskCheckpointPromoter(
	task *api.WorkerTask,
	roots []projectroot.RootRef,
	refImporter func(context.Context, string, string, checkpoint.PrivateGitSnapshot) (map[string]string, error),
) (*checkpoint.Store, checkpoint.Manifest, *checkpoint.Promoter, bool, error) {
	store, manifest, ok, err := OpenTaskCheckpointStore(task)
	if err != nil || !ok {
		return nil, checkpoint.Manifest{}, nil, ok, err
	}
	targetRoots := TargetRootsForTaskCheckpoint(task, roots, manifest)
	if refImporter == nil {
		refImporter = DefaultCheckpointRefImporter(targetRoots)
	}
	promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{
		Store: store, TargetRoots: targetRoots, RefImporter: refImporter,
	})
	if err != nil {
		return nil, checkpoint.Manifest{}, nil, true, err
	}
	return store, manifest, promoter, true, nil
}

func checkpointManifestChangedPaths(manifest checkpoint.Manifest, roots []projectroot.RootRef) []string {
	primary, hasPrimary := projectroot.PrimaryRoot(roots)
	byID := make(map[string]projectroot.RootRef, len(roots))
	for _, r := range roots {
		byID[r.ID] = r
	}
	changed := make([]string, 0, len(manifest.Entries))
	for _, e := range manifest.Entries {
		rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(e.Path.Rel)))
		if rel == "" || rel == "." {
			continue
		}
		if hasPrimary == nil && len(roots) > 1 {
			if r, ok := byID[e.Path.RootID]; ok {
				rel = projectroot.Qualify(primary, r, filepath.Join(r.Path, filepath.FromSlash(rel)))
			}
		}
		changed = append(changed, rel)
	}
	if len(roots) > 0 {
		return FilterOverlayPromoteCandidatePathsForRoots(roots, changed)
	}
	return promotionstate.FilterOverlayPromoteCandidatePaths(changed)
}

// SyncTaskCheckpointToWorkspace rehydrates task.WorkspaceRoot from the published checkpoint manifest.
func SyncTaskCheckpointToWorkspace(task *api.WorkerTask) {
	if task == nil {
		return
	}
	store, manifest, hasCP, err := OpenTaskCheckpointStore(task)
	if err != nil || !hasCP {
		return
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		if cpDir := CheckpointStoreDirForTask(task); cpDir != "" {
			task.WorkspaceRoot = filepath.Join(cpDir, ".workspace-rehydrated")
		}
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		return
	}
	_ = os.MkdirAll(task.WorkspaceRoot, 0o755)
	for _, entry := range manifest.Entries {
		applyManifestEntryToRoot(store, task.WorkspaceRoot, entry, true)
	}
}

// ApplyTaskCheckpointAttributes applies checkpoint file modes, symlinks, directories, deletions, and xattrs to target roots.
func ApplyTaskCheckpointAttributes(task *api.WorkerTask, roots []projectroot.RootRef) {
	store, manifest, hasCP, err := OpenTaskCheckpointStore(task)
	if err != nil || !hasCP {
		return
	}
	targetRoots := TargetRootsForTaskCheckpoint(task, roots, manifest)
	for _, entry := range manifest.Entries {
		if rootDir := targetRoots[entry.Path.RootID]; rootDir != "" {
			applyManifestEntryToRoot(store, rootDir, entry, false)
		}
	}
}

func applyManifestEntryToRoot(store *checkpoint.Store, rootDir string, entry checkpoint.ManifestEntry, writeRegularBytes bool) {
	abs := filepath.Join(rootDir, filepath.FromSlash(entry.Path.Rel))
	if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
		_ = os.RemoveAll(abs)
		return
	}
	if entry.DirectoryReplaced {
		_ = os.RemoveAll(abs)
	}
	switch entry.Kind {
	case layeredfs.EntryDirectory:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o755
		}
		_ = os.MkdirAll(abs, mode)
		_ = os.Chmod(abs, mode)
	case layeredfs.EntrySymlink:
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		_ = os.RemoveAll(abs)
		_ = os.Symlink(entry.Final.LinkTarget, abs)
	case layeredfs.EntryRegular:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if writeRegularBytes {
			if raw, rErr := store.ReadBlob(entry.Final.SHA256); rErr == nil {
				_ = os.MkdirAll(filepath.Dir(abs), 0o755)
				_ = os.WriteFile(abs, raw, mode)
			}
		}
		if writeRegularBytes || entry.Final.Mode != 0 {
			_ = os.Chmod(abs, mode)
		}
		for k, v := range entry.Final.Xattrs {
			_ = unix.Setxattr(abs, k, v, 0)
		}
	}
}

// CommitTaskCheckpointJournal records or recovers a task's checkpoint promotion journal and #1671 ref import.
func CommitTaskCheckpointJournal(
	ctx context.Context,
	task *api.WorkerTask,
	roots []projectroot.RootRef,
	importer func(context.Context, string, string, checkpoint.PrivateGitSnapshot) (map[string]string, error),
) error {
	ApplyTaskCheckpointAttributes(task, roots)
	store, manifest, promoter, ok, err := NewTaskCheckpointPromoter(task, roots, importer)
	if err != nil || !ok {
		return err
	}
	txID := "tx-" + manifest.CheckpointID
	if rec, exists, lErr := store.LoadPromotionRecord(txID); lErr == nil && exists && rec.State == checkpoint.PromotionStateApplied {
		return nil
	}
	cpPreview, err := promoter.Preview(ctx, checkpoint.PreviewRequest{CheckpointID: manifest.CheckpointID})
	if err != nil {
		return err
	}
	cpPreview.PlannedChanges, cpPreview.Conflicts = nil, nil
	_, err = promoter.Apply(ctx, checkpoint.ApplyRequest{TransactionID: txID, Preview: cpPreview, ImportGitRefs: true})
	return err
}
