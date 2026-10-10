package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/sandbox"
)

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
