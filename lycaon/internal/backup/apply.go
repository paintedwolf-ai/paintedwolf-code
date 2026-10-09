package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fssync"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const fileMode = 0o600

type markerPublishedError struct{ cause error }

func (e *markerPublishedError) Error() string { return e.cause.Error() }
func (e *markerPublishedError) Unwrap() error { return e.cause }

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
func ApplyPending(configDir string) error {
	restore, err := loadPendingRestore(configDir)
	if err != nil {
		return unapplicableRestore(err)
	}
	if restore == nil {
		return nil
	}
	release, err := editoroutbox.Acquire(context.Background(), configDir)
	if err != nil {
		return unapplicableRestore(err)
	}
	defer release()
	if err := restore.apply(configDir); err != nil {
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

func (r *pendingRestore) apply(configDir string) error {
	if err := r.prepareFileDestinations(configDir); err != nil {
		return err
	}
	if err := r.installFiles(configDir); err != nil {
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

func ensureDirectoryPath(root, leaf string) error {
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, filepath.Clean(leaf))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		switch {
		case os.IsNotExist(statErr):
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return err
			}
		case statErr != nil:
			return statErr
		case info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			continue
		default:
			if err := os.RemoveAll(current); err != nil {
				return err
			}
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(current)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *pendingRestore) installFiles(configDir string) error {
	for _, fe := range r.marker.Files {
		if _, done := r.appliedSet[fe.RelPath]; done {
			continue
		}
		src := r.stagedFiles[fe.RelPath]
		dest := filepath.Join(configDir, filepath.FromSlash(r.marker.liveRelPath(fe.RelPath)))
		if err := installFileAtomic(configDir, src, dest, fe); err != nil {
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

func (r *pendingRestore) recordApplied(rel string) error {
	r.applied = append(r.applied, rel)
	r.appliedSet[rel] = struct{}{}
	r.marker.Applied = r.applied
	return writeMarker(r.markerPath, r.marker)
}

// The failure marker allows an explicit recovery action to replace the transaction.
func (r *pendingRestore) recordFailure(cause error) {
	r.marker.Applied = r.applied
	r.marker.FailedAt = time.Now().UTC().Format(time.RFC3339)
	r.marker.FailureDetail = cause.Error()
	_ = writeMarker(r.markerPath, r.marker)
}

func loadPendingRestore(configDir string) (*pendingRestore, error) {
	markerPath := PendingMarkerPath(configDir)
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pending marker: %w", err)
	}

	var marker PendingMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil, fmt.Errorf("parse pending marker: %w", err)
	}
	name, err := liveStoreFilename(configDir, filepath.Join(configDir, marker.LiveStoreFilename))
	if err != nil || name != marker.LiveStoreFilename {
		return nil, fmt.Errorf("invalid configured store filename in restore marker")
	}
	if marker.StagingDir == "" || marker.RecoveryDir == "" {
		return nil, fmt.Errorf("pending marker missing transaction directories")
	}
	stagingDir, stagingID, err := validateRestoreTransactionDir(configDir, marker.StagingDir, localdata.RestoreStagingDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("invalid staging directory: %w", err)
	}
	recoveryDir, recoveryID, err := validateRestoreTransactionDir(configDir, marker.RecoveryDir, localdata.RestorePreImageDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("invalid recovery directory: %w", err)
	}
	if stagingID != recoveryID {
		return nil, fmt.Errorf("staging and recovery transaction ids differ")
	}
	marker.StagingDir = stagingDir
	marker.RecoveryDir = recoveryDir
	if err := validateBranchTreeList(marker.RetainedBranchTrees); err != nil {
		return nil, err
	}

	pendingSet := make(map[string]struct{}, len(marker.Files))
	stagedFiles := make(map[string]string, len(marker.Files))
	for _, fe := range marker.Files {
		validKind := fe.Kind == fileKindRegular || fe.Kind == fileKindSymlink
		validMode := fe.Mode&^uint32(0o777) == 0
		if !validKind || !validMode || sandbox.HasParentTraversal(fe.RelPath) || !restorableArchivePath(fe.RelPath, marker.RetainedBranchTrees) {
			return nil, fmt.Errorf("refusing marker path %q — not a restorable durable file", fe.RelPath)
		}
		if _, duplicate := pendingSet[fe.RelPath]; duplicate {
			return nil, fmt.Errorf("refusing duplicate marker path %q", fe.RelPath)
		}
		pendingSet[fe.RelPath] = struct{}{}
		src, srcErr := validateStagedRestoreFile(stagingDir, fe.RelPath)
		if srcErr != nil {
			return nil, fmt.Errorf("validate staged %s: %w", fe.RelPath, srcErr)
		}
		sum, size, sumErr := sha256File(src)
		if sumErr != nil {
			return nil, fmt.Errorf("hash staged %s: %w", fe.RelPath, sumErr)
		}
		if sum != fe.SHA256 || size != fe.Size {
			return nil, fmt.Errorf("staged file mismatch for %s", fe.RelPath)
		}
		stagedFiles[fe.RelPath] = src
	}

	dirSet := make(map[string]struct{}, len(marker.ReplaceRelDirs))
	for _, dir := range marker.ReplaceRelDirs {
		if sandbox.HasParentTraversal(dir) || !RestorableRelDir(dir) {
			return nil, fmt.Errorf("refusing directory %q — not a restorable durable directory", dir)
		}
		if _, duplicate := dirSet[dir]; duplicate {
			return nil, fmt.Errorf("refusing duplicate directory %q", dir)
		}
		dirSet[dir] = struct{}{}
	}
	if len(marker.RetainedBranchTrees) > 0 {
		if _, ok := dirSet[enginepaths.WorkerBranchesDirName]; !ok {
			return nil, fmt.Errorf("retained worker trees are outside restore scope")
		}
	}

	deleteSet := make(map[string]struct{}, len(marker.DeleteRelPaths))
	for _, rel := range marker.DeleteRelPaths {
		if sandbox.HasParentTraversal(rel) || !RestorableRelPath(rel) || rel == storeRelPath {
			return nil, fmt.Errorf("refusing delete path %q — not an absent restorable durable file", rel)
		}
		if _, installs := pendingSet[rel]; installs {
			return nil, fmt.Errorf("refusing path %q listed for both install and delete", rel)
		}
		if _, duplicate := deleteSet[rel]; duplicate {
			return nil, fmt.Errorf("refusing duplicate delete path %q", rel)
		}
		deleteSet[rel] = struct{}{}
	}
	if err := validatePendingOperation(marker, pendingSet, deleteSet); err != nil {
		return nil, err
	}

	applied := append([]string{}, marker.Applied...)
	appliedSet := make(map[string]struct{}, len(applied))
	for _, rel := range applied {
		if _, installs := pendingSet[rel]; !installs {
			if _, deletes := deleteSet[rel]; !deletes {
				return nil, fmt.Errorf("refusing applied path %q — not a pending operation", rel)
			}
		}
		if _, duplicate := appliedSet[rel]; duplicate {
			return nil, fmt.Errorf("refusing duplicate applied path %q", rel)
		}
		appliedSet[rel] = struct{}{}
	}

	return &pendingRestore{
		markerPath:  markerPath,
		marker:      marker,
		pendingSet:  pendingSet,
		stagedFiles: stagedFiles,
		applied:     applied,
		appliedSet:  appliedSet,
	}, nil
}

func validatePendingOperation(marker PendingMarker, pendingSet, deleteSet map[string]struct{}) error {
	switch marker.Operation {
	case PendingOperationRestore:
		return nil
	case PendingOperationFreshStart:
	default:
		return fmt.Errorf("unknown pending operation %q", marker.Operation)
	}
	if len(pendingSet) != 1 {
		return fmt.Errorf("fresh-start marker must install only store.db")
	}
	if _, ok := pendingSet[storeRelPath]; !ok {
		return fmt.Errorf("fresh-start marker does not install store.db")
	}
	onboarding := localdata.FirstRunOnboardingRelPath()
	if len(deleteSet) != 1 {
		return fmt.Errorf("fresh-start marker must reset only the onboarding latch")
	}
	if _, ok := deleteSet[onboarding]; !ok {
		return fmt.Errorf("fresh-start marker does not reset onboarding")
	}
	if len(marker.ReplaceRelDirs) != 0 {
		return fmt.Errorf("fresh-start marker cannot replace durable directories")
	}
	return nil
}

func validateRestoreTransactionDir(configDir, candidate, prefix string) (string, uuid.UUID, error) {
	root, err := filepath.Abs(configDir)
	if err != nil {
		return "", uuid.Nil, err
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", uuid.Nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", uuid.Nil, err
	}
	// Resolve parent aliases while preserving the transaction directory's symlink check.
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", uuid.Nil, err
	}
	if parent != root {
		return "", uuid.Nil, fmt.Errorf("must be a direct child of the config root")
	}
	candidate = filepath.Join(parent, filepath.Base(candidate))
	id := strings.TrimPrefix(filepath.Base(candidate), prefix+"-")
	if id == filepath.Base(candidate) {
		return "", uuid.Nil, fmt.Errorf("must use the %s-<transaction-id> name", prefix)
	}
	transactionID, err := uuid.Parse(id)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("invalid transaction id: %w", err)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", uuid.Nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", uuid.Nil, fmt.Errorf("transaction path is not a directory")
	}
	return candidate, transactionID, nil
}

func validateStagedRestoreFile(root, rel string) (string, error) {
	dest, err := joinUnder(root, rel)
	if err != nil {
		return "", err
	}
	current := root
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	for i, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("staged path contains a symbolic link")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("staged path parent is not a directory")
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("staged path is not a regular file")
		}
	}
	return dest, nil
}

// clearSQLiteSidecars removes journals beside an installed database.
func clearSQLiteSidecars(dest string, mainStore bool) error {
	if !mainStore && !strings.HasSuffix(strings.ToLower(filepath.Base(dest)), ".db") {
		return nil
	}
	if err := db.RemoveStoreSidecars(dest); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dest))
}

func writeMarker(path string, marker PendingMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw),
		Mode:     fileMode,
		DirMode:  0o700,
	})
	return err
}

func writeMarkerExclusive(path string, marker PendingMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-marker-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := fssync.File(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return &markerPublishedError{cause: err}
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return &markerPublishedError{cause: err}
	}
	return nil
}

// installFileAtomic installs the entry and syncs directories through the restore root.
func installFileAtomic(root, src, dest string, entry PendingFile) error {
	if entry.Kind == fileKindSymlink {
		return installSymlinkAtomic(root, src, dest, entry.RelPath)
	}
	tmp := filepath.Join(filepath.Dir(dest), ".restore-file-"+uuid.NewString())
	defer func() { _ = os.Remove(tmp) }()
	if _, err := copySnapshotRegular(context.Background(), src, tmp, os.FileMode(entry.Mode)); err != nil {
		return fmt.Errorf("copy staged file: %w", err)
	}
	tempRel, err := filepath.Rel(root, tmp)
	if err != nil {
		return err
	}
	destRel, err := filepath.Rel(root, dest)
	if err != nil {
		return err
	}
	if err := fseffect.Rename(root, tempRel, destRel); err != nil {
		return fmt.Errorf("install staged file: %w", err)
	}

	return syncDirectoryTree(root, filepath.Dir(dest))
}

func installSymlinkAtomic(root, src, dest, rel string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read staged symlink: %w", err)
	}
	if len(raw) == 0 || int64(len(raw)) > maxSymlinkTargetBytes || bytes.IndexByte(raw, 0) >= 0 {
		return fmt.Errorf("staged symlink target is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("mkdir symlink destination: %w", err)
	}
	tmpRel := filepath.ToSlash(filepath.Join(filepath.Dir(rel), ".restore-link-"+uuid.NewString()))
	tmp, err := joinUnder(root, tmpRel)
	if err != nil {
		return err
	}
	if err := os.Symlink(string(raw), tmp); err != nil {
		return fmt.Errorf("stage symlink: %w", err)
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := fseffect.Rename(root, filepath.FromSlash(tmpRel), filepath.FromSlash(rel)); err != nil {
		return fmt.Errorf("install staged symlink: %w", err)
	}
	return syncDirectoryTree(root, filepath.Dir(dest))
}

func installedDigest(path, kind string) (string, int64, error) {
	if kind == fileKindSymlink {
		target, err := os.Readlink(path)
		if err != nil {
			return "", 0, err
		}
		return sha256Hex([]byte(target)), int64(len(target)), nil
	}
	return sha256File(path)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}

func syncDirectoryTree(root, leaf string) error {
	root = filepath.Clean(root)
	current := filepath.Clean(leaf)
	rel, err := filepath.Rel(root, current)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
	}
	for {
		if err := syncDirectory(current); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
		}
		rel, err := filepath.Rel(root, parent)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("directory %s is outside restore root %s", leaf, root)
		}
		current = parent
	}
}
