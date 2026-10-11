package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/localdata"
)

type pendingRestore struct {
	markerPath  string
	marker      PendingMarker
	pendingSet  map[string]struct{}
	stagedFiles map[string]string
	applied     []string
	appliedSet  map[string]struct{}
}

// ApplyPending resumes a staged restore before the store opens.
// Failures retain recovery state and enter recovery mode.
func ApplyPending(ctx context.Context, configDir string) error {
	restore, err := loadPendingRestore(configDir)
	if err != nil {
		return unapplicableRestore(err)
	}
	if restore == nil {
		return nil
	}
	release, err := editoroutbox.Acquire(ctx, configDir)
	if err != nil {
		return unapplicableRestore(err)
	}
	defer release()
	if err := restore.apply(ctx, configDir); err != nil {
		restore.recordFailure(err)
		return unapplicableRestore(err)
	}
	return nil
}

// unapplicableRestore keeps recovery actions available after a failed apply.
func unapplicableRestore(err error) error {
	return &db.StoreIncompatibleError{
		Reason: db.RecoveryReasonIntegrityFailed,
		Detail: fmt.Sprintf("a staged restore could not be applied: %v", err),
	}
}

func (r *pendingRestore) apply(ctx context.Context, configDir string) error {
	if err := r.prepareFileDestinations(configDir); err != nil {
		return err
	}
	if err := r.installFiles(ctx, configDir); err != nil {
		return err
	}
	if err := r.deleteFiles(configDir); err != nil {
		return err
	}
	if err := r.replaceDirectories(configDir); err != nil {
		return err
	}
	if r.marker.Operation == PendingOperationFreshStart {
		if err := localdata.ResetStoreDependents(filepath.Join(configDir, r.marker.liveRelPath(storeRelPath))); err != nil {
			return fmt.Errorf("clear replaced-store dependents: %w", err)
		}
	}
	return r.finish()
}

func (r *pendingRestore) prepareFileDestinations(configDir string) error {
	for _, rel := range r.marker.RetainedBranchTrees {
		if err := ensureDirectoryPath(configDir, filepath.Join(configDir, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	for _, fe := range r.marker.Files {
		dest, err := joinUnder(configDir, r.marker.liveRelPath(fe.RelPath))
		if err != nil {
			return fmt.Errorf("resolve %s: %w", fe.RelPath, err)
		}
		if err := ensureDirectoryPath(configDir, filepath.Dir(dest)); err != nil {
			return fmt.Errorf("prepare parent for %s: %w", fe.RelPath, err)
		}
		info, err := os.Lstat(dest)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", fe.RelPath, err)
		}
		if info.IsDir() {
			if err := os.RemoveAll(dest); err != nil {
				return fmt.Errorf("replace directory at %s: %w", fe.RelPath, err)
			}
			if err := syncDirectory(filepath.Dir(dest)); err != nil {
				return fmt.Errorf("sync prepared %s: %w", fe.RelPath, err)
			}
		}
	}
	return nil
}

func (r *pendingRestore) installFiles(ctx context.Context, configDir string) error {
	for _, fe := range r.marker.Files {
		if _, done := r.appliedSet[fe.RelPath]; done {
			continue
		}
		src := r.stagedFiles[fe.RelPath]
		dest := filepath.Join(configDir, filepath.FromSlash(r.marker.liveRelPath(fe.RelPath)))
		if err := installFileAtomic(ctx, configDir, src, dest, fe); err != nil {
			return fmt.Errorf("install %s: %w", fe.RelPath, err)
		}
		// Remove database journals before recording progress.
		if err := clearSQLiteSidecars(dest, fe.RelPath == storeRelPath); err != nil {
			return fmt.Errorf("clear stale sidecars for %s: %w", fe.RelPath, err)
		}
		sum, size, err := installedDigest(dest, fe.Kind)
		if err != nil {
			return fmt.Errorf("verify %s: %w", fe.RelPath, err)
		}
		if sum != fe.SHA256 || size != fe.Size {
			return fmt.Errorf("hash mismatch after install %s", fe.RelPath)
		}
		if err := r.recordApplied(fe.RelPath); err != nil {
			return fmt.Errorf("record progress: %w", err)
		}
	}
	return nil
}

func (r *pendingRestore) deleteFiles(configDir string) error {
	for _, rel := range r.marker.DeleteRelPaths {
		if _, done := r.appliedSet[rel]; done {
			continue
		}
		dest, err := joinUnder(configDir, rel)
		if err != nil {
			return fmt.Errorf("resolve delete %s: %w", rel, err)
		}
		removeErr := os.Remove(dest)
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("delete %s: %w", rel, removeErr)
		}
		if removeErr == nil {
			if err := syncDirectory(filepath.Dir(dest)); err != nil {
				return fmt.Errorf("sync delete %s: %w", rel, err)
			}
		}
		if err := r.recordApplied(rel); err != nil {
			return fmt.Errorf("record delete progress: %w", err)
		}
	}
	return nil
}

func (r *pendingRestore) replaceDirectories(configDir string) error {
	for _, dir := range r.marker.ReplaceRelDirs {
		liveDir := filepath.Join(configDir, filepath.FromSlash(dir))
		if err := r.reconcileDirectory(liveDir, dir); err != nil {
			return fmt.Errorf("reconcile %s: %w", dir, err)
		}
	}
	return nil
}

func (r *pendingRestore) reconcileDirectory(liveDir, relDir string) error {
	entries, err := os.ReadDir(liveDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		rel := relDir + "/" + entry.Name()
		path := filepath.Join(liveDir, entry.Name())
		if !r.keepsDirectoryEntry(rel) {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("clear %s: %w", rel, err)
			}
			removed = true
			continue
		}
		if entry.IsDir() {
			if err := r.reconcileDirectory(path, rel); err != nil {
				return err
			}
		}
	}
	if removed {
		return syncDirectory(liveDir)
	}
	return nil
}

func (r *pendingRestore) keepsDirectoryEntry(rel string) bool {
	for _, root := range r.marker.RetainedBranchTrees {
		if rel == root || strings.HasPrefix(root, rel+"/") {
			return true
		}
	}
	if _, keeps := r.pendingSet[rel]; keeps {
		return true
	}
	prefix := rel + "/"
	for pending := range r.pendingSet {
		if strings.HasPrefix(pending, prefix) {
			return true
		}
	}
	return false
}

func (r *pendingRestore) finish() error {
	if err := os.Remove(r.markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear marker: %w", err)
	}
	if err := syncDirectory(filepath.Dir(r.markerPath)); err != nil {
		return fmt.Errorf("sync cleared marker: %w", err)
	}
	if err := os.RemoveAll(r.marker.StagingDir); err != nil {
		return fmt.Errorf("clear staging: %w", err)
	}
	return nil
}
