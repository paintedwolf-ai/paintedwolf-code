package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

func pinWorkerRecoveryClosure(ctx context.Context, tmpDir string, files map[string]archiveSource) error {
	pinnedRoot := filepath.Join(tmpDir, "pinned")
	for rel, src := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if src.kind != fileKindRegular {
			continue
		}
		if !strings.HasPrefix(rel, enginepaths.WorkerRecoveryDirName+"/") &&
			!strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/.adapter/") &&
			!workerMetadataPath(rel, false) {
			continue
		}
		dst := filepath.Join(pinnedRoot, filepath.FromSlash(rel))
		if _, err := copySnapshotRegular(ctx, src, dst); err != nil {
			return fmt.Errorf("%w: pin %s: %w", ErrCaptureIncomplete, rel, err)
		}
		src.path = dst
		files[rel] = src
	}
	return nil
}

func validateWorkerCheckpoints(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
	ctx, closeClosureSession := ensureGitClosureSession(ctx)
	defer closeClosureSession()
	rows, err := snapshot.QueryContext(ctx, `SELECT DISTINCT workspace_relpath FROM worker_jobs WHERE workspace_relpath IS NOT NULL AND workspace_relpath != '' AND (workspace_overlay_id IS NULL OR workspace_overlay_id = '')`)
	if err != nil {
		return fmt.Errorf("backup: query worker jobs for checkpoint validation: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var roots []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return err
		}
		if branchTreeRoot(root) != root {
			return &InvalidError{Detail: "invalid retained worker branch root"}
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	visitState := sessionFileVisitState(ctx)
	for _, root := range roots {
		metaRel := root + ".meta/state.json"
		metaSrc, ok := files[metaRel]
		if !ok || metaSrc.kind != fileKindRegular {
			return &InvalidError{Detail: "retained worker metadata is missing: " + metaRel}
		}
		raw, err := os.ReadFile(metaSrc.path)
		if err != nil {
			return &InvalidError{Detail: "retained worker metadata is unreadable: " + metaRel}
		}
		var meta workerCheckpointMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			return &InvalidError{Detail: "retained worker metadata is invalid: " + metaRel}
		}
		cid := strings.TrimSpace(meta.LastPublishedCheckpointID)
		if cid == "" {
			if !archiveHasBranchTreeFiles(root, files) {
				return &InvalidError{Detail: "archive omits unsealed worker tree and published checkpoint: " + root}
			}
			continue
		}
		if err := verifyCheckpointChainInFiles(ctx, root, cid, files, visitState); err != nil {
			return err
		}
	}
	return nil
}

func archiveHasBranchTreeFiles(root string, files map[string]archiveSource) bool {
	prefix := root + "/"
	for rel := range files {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

var archiveCheckpointStoreRelPrefixes = []string{
	enginepaths.WorkerRecoveryDirName + "/checkpoints",
	enginepaths.WorkerBranchesDirName + "/.adapter/recovery/checkpoints",
	enginepaths.WorkerBaselinesDirName + "/.task-checkpoints",
}

func storeIdentityInFiles(storePrefix, checkpointID string, files map[string]archiveSource) string {
	candidates := []string{
		storePrefix + "/checkpoints/" + checkpointID + "/manifest.json",
		storePrefix + "/checkpoints/" + checkpointID + ".json",
	}
	for _, rel := range candidates {
		if src, ok := files[rel]; ok {
			suffix := filepath.FromSlash(rel)
			if cleaned := filepath.Clean(src.path); strings.HasSuffix(cleaned, suffix) {
				root := filepath.Clean(strings.TrimSuffix(cleaned, suffix))
				if realRoot, err := filepath.EvalSymlinks(root); err == nil {
					root = realRoot
				}
				return root + "\x00" + storePrefix
			}
		}
	}
	return storePrefix
}

func verifyCheckpointChainInFiles(ctx context.Context, branchRel, checkpointID string, files map[string]archiveSource, visitState map[string]uint8) error {
	ctx, closeClosureSession := ensureGitClosureSession(ctx)
	defer closeClosureSession()
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return &InvalidError{Detail: "worker metadata names an empty checkpoint id"}
	}
	if visitState == nil {
		visitState = make(map[string]uint8)
	}
	storePrefix, m, err := findCheckpointManifestInFiles(checkpointID, files)
	if err != nil {
		return err
	}
	visitKey := storeIdentityInFiles(storePrefix, checkpointID, files) + "\x00" + branchProjKey(branchRel) + "\x00" + checkpointID
	switch visitState[visitKey] {
	case checkpointVisitActive:
		return &InvalidError{Detail: fmt.Sprintf("cyclic checkpoint ancestry detected at %s", checkpointID)}
	case checkpointVisitVerified:
		return nil
	}
	visitState[visitKey] = checkpointVisitActive

	for _, entry := range m.Entries {
		if !entry.Deleted && entry.Kind == layeredfs.EntryRegular {
			if err := verifyBlobInFiles(ctx, storePrefix, entry.Final.SHA256, entry.Final.Size, files); err != nil {
				delete(visitState, visitKey)
				return err
			}
		}
		if entry.Base.Kind == layeredfs.EntryRegular && entry.Base.SHA256 != "" {
			if err := verifyBlobInFiles(ctx, storePrefix, entry.Base.SHA256, entry.Base.Size, files); err != nil {
				delete(visitState, visitKey)
				return err
			}
		}
		for _, cb := range entry.CandidateBases {
			if cb.Kind == layeredfs.EntryRegular && cb.SHA256 != "" {
				if err := verifyBlobInFiles(ctx, storePrefix, cb.SHA256, cb.Size, files); err != nil {
					delete(visitState, visitKey)
					return err
				}
			}
		}
	}
	if err := verifyCheckpointGitClosureInFiles(ctx, branchRel, storePrefix, checkpointID, m.PrivateGit, files); err != nil {
		delete(visitState, visitKey)
		return err
	}
	for _, parentID := range m.ParentCheckpoints {
		if err := verifyCheckpointChainInFiles(ctx, branchRel, parentID, files, visitState); err != nil {
			delete(visitState, visitKey)
			return err
		}
	}
	visitState[visitKey] = checkpointVisitVerified
	return nil
}

func findCheckpointManifestInFiles(checkpointID string, files map[string]archiveSource) (string, checkpoint.Manifest, error) {
	for _, prefix := range archiveCheckpointStoreRelPrefixes {
		candidates := []string{
			prefix + "/checkpoints/" + checkpointID + "/manifest.json",
			prefix + "/checkpoints/" + checkpointID + ".json",
		}
		for _, rel := range candidates {
			src, ok := files[rel]
			if !ok || src.kind != fileKindRegular {
				continue
			}
			raw, err := os.ReadFile(src.path)
			if err != nil {
				return "", checkpoint.Manifest{}, &InvalidError{Detail: "checkpoint manifest is unreadable: " + rel}
			}
			var m checkpoint.Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return "", checkpoint.Manifest{}, &InvalidError{Detail: "checkpoint manifest is invalid: " + rel}
			}
			if m.FormatVersion != checkpoint.FormatVersion || m.CheckpointID != checkpointID {
				return "", checkpoint.Manifest{}, &InvalidError{Detail: "checkpoint manifest version or id mismatch: " + rel}
			}
			return prefix, m, nil
		}
	}
	return "", checkpoint.Manifest{}, &InvalidError{Detail: "retained worker checkpoint manifest is missing: " + checkpointID}
}

func verifyBlobInFiles(ctx context.Context, storePrefix, sha string, expectedSize int64, files map[string]archiveSource) error {
	if len(sha) != 64 {
		return &InvalidError{Detail: "checkpoint manifest names invalid blob sha256: " + sha}
	}
	candidates := []string{
		storePrefix + "/blobs/" + sha,
		storePrefix + "/blobs/" + sha[:2] + "/" + sha + ".blob",
	}
	for _, rel := range candidates {
		src, ok := files[rel]
		if !ok || src.kind != fileKindRegular {
			continue
		}
		actual, size, err := sha256FileInSession(ctx, src.path)
		if err != nil {
			return &InvalidError{Detail: "checkpoint blob is unreadable: " + rel}
		}
		if actual != sha || (expectedSize >= 0 && size != expectedSize) {
			return &InvalidError{Detail: "checkpoint blob digest or size mismatch: " + rel}
		}
		return nil
	}
	return &InvalidError{Detail: "retained checkpoint blob is missing: " + sha}
}
