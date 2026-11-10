package session

import (
	"bytes"
	"context"
	"errors"
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
	// The recovery store is shared, so only the branch's own record can name its newest checkpoint.
	latest, err := taskLatestCheckpointID(task, store)
	if err != nil {
		return err
	}
	if latest != bound.CheckpointID {
		return checkpoint.ErrStalePreviewCheckpointChanged
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return promoter.RevalidatePreviewTargets(bound)
}

func taskLatestCheckpointID(task *api.WorkerTask, store *checkpoint.Store) (string, error) {
	if _, published, ok := taskBranchCheckpoint(task); ok {
		return published, nil
	}
	return store.LatestCheckpointID()
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
	promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{Store: store, TargetRoots: targetRoots})
	if err != nil {
		return nil, checkpoint.Manifest{}, nil, true, err
	}
	return store, manifest, promoter, true, nil
}

// SyncTaskCheckpointToWorkspace rehydrates task.WorkspaceRoot from the published
// checkpoint manifest, in the layout promotion reads through PromoteRoots.
func SyncTaskCheckpointToWorkspace(task *api.WorkerTask, roots []projectroot.RootRef) error {
	if task == nil {
		return nil
	}
	store, manifest, hasCP, err := OpenTaskCheckpointStore(task)
	if err != nil || !hasCP {
		return err
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		return nil
	}
	if err := os.MkdirAll(task.WorkspaceRoot, 0o755); err != nil {
		return err
	}
	replaced := checkpointNonDirectoryEntries(manifest)
	for _, entry := range manifest.Entries {
		if beneathCheckpointNonDirectory(entry.Path, replaced) {
			continue
		}
		targetDir, err := checkpointBranchDir(task, roots, entry.Path.RootID)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return err
		}
		if err := applyManifestEntryToRoot(store, targetDir, entry); err != nil {
			return err
		}
	}
	return nil
}

func checkpointBranchDir(task *api.WorkerTask, roots []projectroot.RootRef, rootID string) (string, error) {
	if len(roots) <= 1 {
		return task.WorkspaceRoot, nil
	}
	rootID = strings.TrimSpace(rootID)
	for _, root := range roots {
		alias := rootID == "primary" || rootID == "root-0" || rootID == "" || rootID == strings.TrimSpace(task.WorkspaceRootID)
		if root.ID == rootID || (root.IsPrimary && alias) {
			dir, err := projectroot.BranchDirForID(root.ID)
			if err != nil {
				return "", err
			}
			return filepath.Join(task.WorkspaceRoot, dir), nil
		}
	}
	return "", fmt.Errorf("%w: unresolved checkpoint root id %q", layeredfs.ErrInvalidRoot, rootID)
}

func checkpointNonDirectoryEntries(manifest checkpoint.Manifest) map[layeredfs.QualifiedPath]bool {
	replaced := make(map[layeredfs.QualifiedPath]bool)
	for _, entry := range manifest.Entries {
		if !entry.Deleted && (entry.Kind == layeredfs.EntrySymlink || entry.Kind == layeredfs.EntryRegular) {
			replaced[entry.Path] = true
		}
	}
	return replaced
}

// A child recorded beneath a path that became a file or symlink no longer exists
// in the final tree, and following that entry would leave the branch.
func beneathCheckpointNonDirectory(path layeredfs.QualifiedPath, replaced map[layeredfs.QualifiedPath]bool) bool {
	for parent := filepath.Dir(path.Rel); parent != "." && parent != string(filepath.Separator); parent = filepath.Dir(parent) {
		if replaced[layeredfs.QualifiedPath{RootID: path.RootID, Rel: parent}] {
			return true
		}
	}
	return false
}

func applyManifestEntryToRoot(store *checkpoint.Store, rootDir string, entry checkpoint.ManifestEntry) error {
	if !filepath.IsLocal(entry.Path.Rel) || entry.Path.Rel == "." {
		return fmt.Errorf("%w: invalid entry path %q", checkpoint.ErrInvalidManifest, entry.Path.Rel)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rel := filepath.FromSlash(entry.Path.Rel)
	abs := filepath.Join(rootDir, rel)
	if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
		return root.RemoveAll(rel)
	}
	if entry.DirectoryReplaced {
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
	}
	switch entry.Kind {
	case layeredfs.EntryDirectory:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o755
		}
		if err := root.MkdirAll(rel, mode|0o700); err != nil {
			return err
		}
		if err := root.Chmod(rel, mode); err != nil {
			return err
		}
		return checkpoint.ApplyPathXattrs(abs, entry.Final.Xattrs)
	case layeredfs.EntrySymlink:
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return err
		}
		if err := root.RemoveAll(rel); err != nil {
			return err
		}
		return root.Symlink(entry.Final.LinkTarget, rel)
	case layeredfs.EntryRegular:
		mode := entry.Final.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		raw, err := store.ReadBlob(entry.Final.SHA256)
		if err != nil {
			return err
		}
		if _, err := fseffect.Replace(fseffect.ReplaceRequest{
			Location: fseffect.Location{Root: rootDir, Rel: rel},
			Source:   bytes.NewReader(raw),
			Mode:     mode,
			DirMode:  0o755,
		}); err != nil {
			return err
		}
		return checkpoint.ApplyPathXattrs(abs, entry.Final.Xattrs)
	default:
		return fmt.Errorf("checkpoint: unsupported entry kind %q", entry.Kind)
	}
}

// CheckpointKeepTargetResolutions builds ResolutionKeepTarget entries for any Stage-1 resolved
// promote paths so Stage-2 checkpoint promotion preserves the bytes Stage-1 already wrote or kept.
func CheckpointKeepTargetResolutions(task *api.WorkerTask, manifest checkpoint.Manifest, roots []projectroot.RootRef, resolvedPaths []string) map[layeredfs.QualifiedPath]checkpoint.ConflictResolution {
	if len(resolvedPaths) == 0 {
		return nil
	}
	resolvedSet := make(map[string]struct{}, len(resolvedPaths))
	for _, p := range resolvedPaths {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
		if clean != "" && clean != "." {
			resolvedSet[clean] = struct{}{}
		}
	}
	if len(resolvedSet) == 0 {
		return nil
	}
	out := make(map[layeredfs.QualifiedPath]checkpoint.ConflictResolution, len(resolvedSet)*2)
	add := func(qp layeredfs.QualifiedPath) {
		if qp.Rel == "" || qp.Rel == "." {
			return
		}
		out[qp] = checkpoint.ConflictResolution{
			Path:   qp,
			Choice: checkpoint.ResolutionKeepTarget,
		}
	}
	for _, entry := range manifest.Entries {
		rel := QualifyCheckpointPath(entry.Path, roots)
		if _, ok := resolvedSet[rel]; ok {
			add(entry.Path)
			continue
		}
		if _, ok := resolvedSet[entry.Path.Rel]; ok {
			add(entry.Path)
		}
	}
	primaryAliases := []string{"primary", "root-0", ""}
	if task != nil && strings.TrimSpace(task.WorkspaceRootID) != "" {
		primaryAliases = append(primaryAliases, strings.TrimSpace(task.WorkspaceRootID))
	}
	for _, r := range roots {
		if r.IsPrimary && r.ID != "" {
			primaryAliases = append(primaryAliases, r.ID)
		}
	}
	for rel := range resolvedSet {
		if len(roots) > 0 {
			if abs, rootRef, err := projectroot.ResolveAbs(roots, "", rel); err == nil {
				cleanSub := filepath.ToSlash(filepath.Clean(projectroot.ScopeRel(rootRef, abs)))
				add(layeredfs.QualifiedPath{RootID: rootRef.ID, Rel: cleanSub})
				if rootRef.IsPrimary {
					for _, alias := range primaryAliases {
						add(layeredfs.QualifiedPath{RootID: alias, Rel: cleanSub})
					}
				}
				continue
			}
		}
		for _, alias := range primaryAliases {
			add(layeredfs.QualifiedPath{RootID: alias, Rel: rel})
		}
	}
	return out
}

// ApplyTaskCheckpoint joins a task's checkpoint effects and #1671 ref import to the promotion's transaction.
func ApplyTaskCheckpoint(ctx context.Context, txn *checkpoint.Transaction, task *api.WorkerTask, roots []projectroot.RootRef, resolvedPaths ...string) error {
	store, manifest, promoter, ok, err := NewTaskCheckpointPromoter(task, roots)
	if err != nil || !ok {
		return err
	}
	txID := "tx-" + manifest.CheckpointID
	if rec, exists, lErr := store.LoadPromotionRecord(txID); lErr != nil || (exists && rec.State == checkpoint.PromotionStateApplied) {
		return lErr
	}
	resolutions := CheckpointKeepTargetResolutions(task, manifest, roots, resolvedPaths)
	cpPreview, err := promoter.Preview(ctx, checkpoint.PreviewRequest{
		CheckpointID:      manifest.CheckpointID,
		ExpectedWorkerID:  manifest.WorkerID,
		ExpectedProjectID: task.ProjectID,
		Resolutions:       resolutions,
	})
	if err != nil {
		return errors.Join(err, txn.Rollback(context.WithoutCancel(ctx)))
	}
	_, err = txn.ApplyCheckpoint(ctx, promoter, checkpoint.ApplyRequest{TransactionID: txID, Preview: cpPreview, ImportGitRefs: true, SkipTargetRevalidate: true})
	return err
}
