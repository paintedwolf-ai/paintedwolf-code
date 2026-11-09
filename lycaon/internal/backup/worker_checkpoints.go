package backup

import (
	"context"
	"encoding/hex"
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

type workerCheckpointMeta struct {
	LastPublishedCheckpointID string   `json:"last_published_checkpoint_id"`
	CheckpointStoreDir        string   `json:"checkpoint_store_dir"`
	ParentCheckpointIDs       []string `json:"parent_checkpoint_ids"`
}

func branchTreeExistsOnDisk(configDir, branchRel string) bool {
	info, err := os.Lstat(filepath.Join(configDir, filepath.FromSlash(branchRel)))
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func readWorkerCheckpointMetaOnDisk(configDir, branchRel string) (workerCheckpointMeta, bool) {
	metaPath := filepath.Join(configDir, filepath.FromSlash(branchRel+".meta/state.json"))
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return workerCheckpointMeta{}, false
	}
	var meta workerCheckpointMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return workerCheckpointMeta{}, false
	}
	return meta, true
}

func branchDeclaresPublishedCheckpoint(configDir, branchRel string) bool {
	meta, ok := readWorkerCheckpointMetaOnDisk(configDir, branchRel)
	return ok && strings.TrimSpace(meta.LastPublishedCheckpointID) != ""
}

func candidateCheckpointStoreDirsOnDisk(configDir string, declared string) []string {
	var out []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		for _, existing := range out {
			if existing == dir {
				return
			}
		}
		out = append(out, dir)
	}
	add(declared)
	if strings.TrimSpace(configDir) != "" {
		add(filepath.Join(configDir, enginepaths.WorkerRecoveryDirName, "checkpoints"))
		add(filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", "recovery", "checkpoints"))
		add(filepath.Join(configDir, enginepaths.WorkerBaselinesDirName, ".task-checkpoints"))
	}
	return out
}

func hasVerifiedPublishedCheckpoint(configDir, branchRel string) bool {
	meta, ok := readWorkerCheckpointMetaOnDisk(configDir, branchRel)
	if !ok || strings.TrimSpace(meta.LastPublishedCheckpointID) == "" {
		return false
	}
	for _, storeDir := range candidateCheckpointStoreDirsOnDisk(configDir, meta.CheckpointStoreDir) {
		if verifyCheckpointChainOnDisk(configDir, branchRel, storeDir, meta.LastPublishedCheckpointID, map[string]bool{}) == nil {
			return true
		}
	}
	return false
}

func verifyCheckpointChainOnDisk(configDir, branchRel, storeDir, checkpointID string, visited map[string]bool) error {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return fmt.Errorf("empty checkpoint id")
	}
	if visited[checkpointID] {
		return nil
	}
	visited[checkpointID] = true

	m, err := readCheckpointManifestOnDisk(storeDir, checkpointID)
	if err != nil {
		return err
	}
	for _, entry := range m.Entries {
		if !entry.Deleted && entry.Kind == layeredfs.EntryRegular {
			if err := verifyBlobOnDisk(storeDir, entry.Final.SHA256, entry.Final.Size); err != nil {
				return err
			}
		}
		if entry.Base.Kind == layeredfs.EntryRegular && entry.Base.SHA256 != "" {
			if err := verifyBlobOnDisk(storeDir, entry.Base.SHA256, entry.Base.Size); err != nil {
				return err
			}
		}
		for _, cb := range entry.CandidateBases {
			if cb.Kind == layeredfs.EntryRegular && cb.SHA256 != "" {
				if err := verifyBlobOnDisk(storeDir, cb.SHA256, cb.Size); err != nil {
					return err
				}
			}
		}
	}
	for _, oid := range collectRequiredGitOIDs(m.PrivateGit) {
		if !gitObjectExistsOnDisk(configDir, branchRel, storeDir, checkpointID, oid) {
			return fmt.Errorf("missing retained git object %s for checkpoint %s", oid, checkpointID)
		}
	}
	for _, parentID := range m.ParentCheckpoints {
		if err := verifyCheckpointChainOnDisk(configDir, branchRel, storeDir, parentID, visited); err != nil {
			return err
		}
	}
	return nil
}

func readCheckpointManifestOnDisk(storeDir, checkpointID string) (checkpoint.Manifest, error) {
	candidates := []string{
		filepath.Join(storeDir, "checkpoints", checkpointID, "manifest.json"),
		filepath.Join(storeDir, "checkpoints", checkpointID+".json"),
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m checkpoint.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return checkpoint.Manifest{}, err
		}
		if m.FormatVersion != checkpoint.FormatVersion || m.CheckpointID != checkpointID {
			return checkpoint.Manifest{}, fmt.Errorf("invalid checkpoint manifest %s", checkpointID)
		}
		return m, nil
	}
	return checkpoint.Manifest{}, fmt.Errorf("checkpoint manifest %s not found", checkpointID)
}

func verifyBlobOnDisk(storeDir, sha string, expectedSize int64) error {
	if len(sha) != 64 {
		return fmt.Errorf("invalid blob sha256 %q", sha)
	}
	candidates := []string{
		filepath.Join(storeDir, "blobs", sha),
		filepath.Join(storeDir, "blobs", sha[:2], sha+".blob"),
	}
	for _, p := range candidates {
		actual, size, err := sha256File(p)
		if err != nil {
			continue
		}
		if actual != sha || (expectedSize >= 0 && size != expectedSize) {
			return fmt.Errorf("corrupt blob %s", sha)
		}
		return nil
	}
	return fmt.Errorf("missing blob %s", sha)
}

func collectRequiredGitOIDs(pg checkpoint.PrivateGitSnapshot) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(oid string) {
		oid = strings.ToLower(strings.TrimSpace(oid))
		if !isGitOID(oid) || seen[oid] {
			return
		}
		seen[oid] = true
		out = append(out, oid)
	}
	for _, oid := range pg.RetainedObjects {
		add(oid)
	}
	for _, oid := range pg.Refs {
		add(oid)
	}
	return out
}

func isGitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func branchProjKey(branchRel string) string {
	parts := strings.Split(filepath.ToSlash(branchRel), "/")
	if len(parts) >= 2 && parts[0] == enginepaths.WorkerBranchesDirName && isLowerHex16(parts[1]) {
		return parts[1]
	}
	return ""
}

func gitObjectExistsOnDisk(configDir, branchRel, storeDir, checkpointID, oid string) bool {
	if len(oid) < 3 {
		return false
	}
	objectDirs := []string{
		filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", "privategit", "retained-store", "objects"),
		filepath.Join(storeDir, "objects"),
		filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects"),
	}
	if projKey := branchProjKey(branchRel); projKey != "" {
		objectDirs = append(objectDirs, filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", projKey, "retained-git-store", "objects"))
	}
	for _, objDir := range objectDirs {
		loose := filepath.Join(objDir, oid[:2], oid[2:])
		if info, err := os.Stat(loose); err == nil && info.Mode().IsRegular() {
			return true
		}
		packDir := filepath.Join(objDir, "pack")
		if entries, err := os.ReadDir(packDir); err == nil {
			hasPack, hasIdx := false, false
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if strings.HasSuffix(e.Name(), ".pack") {
					hasPack = true
				} else if strings.HasSuffix(e.Name(), ".idx") {
					hasIdx = true
				}
			}
			if hasPack && hasIdx {
				return true
			}
		}
	}
	return false
}

func validateWorkerCheckpoints(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
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
		if err := verifyCheckpointChainInFiles(root, cid, files, map[string]bool{}); err != nil {
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

func verifyCheckpointChainInFiles(branchRel, checkpointID string, files map[string]archiveSource, visited map[string]bool) error {
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return &InvalidError{Detail: "worker metadata names an empty checkpoint id"}
	}
	if visited[checkpointID] {
		return nil
	}
	visited[checkpointID] = true

	storePrefix, m, err := findCheckpointManifestInFiles(checkpointID, files)
	if err != nil {
		return err
	}
	for _, entry := range m.Entries {
		if !entry.Deleted && entry.Kind == layeredfs.EntryRegular {
			if err := verifyBlobInFiles(storePrefix, entry.Final.SHA256, entry.Final.Size, files); err != nil {
				return err
			}
		}
		if entry.Base.Kind == layeredfs.EntryRegular && entry.Base.SHA256 != "" {
			if err := verifyBlobInFiles(storePrefix, entry.Base.SHA256, entry.Base.Size, files); err != nil {
				return err
			}
		}
		for _, cb := range entry.CandidateBases {
			if cb.Kind == layeredfs.EntryRegular && cb.SHA256 != "" {
				if err := verifyBlobInFiles(storePrefix, cb.SHA256, cb.Size, files); err != nil {
					return err
				}
			}
		}
	}
	for _, oid := range collectRequiredGitOIDs(m.PrivateGit) {
		if !gitObjectExistsInFiles(branchRel, storePrefix, checkpointID, oid, files) {
			return &InvalidError{Detail: fmt.Sprintf("retained checkpoint git object is missing: %s (checkpoint %s)", oid, checkpointID)}
		}
	}
	for _, parentID := range m.ParentCheckpoints {
		if err := verifyCheckpointChainInFiles(branchRel, parentID, files, visited); err != nil {
			return err
		}
	}
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

func verifyBlobInFiles(storePrefix, sha string, expectedSize int64, files map[string]archiveSource) error {
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
		actual, size, err := sha256File(src.path)
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

func gitObjectExistsInFiles(branchRel, storePrefix, checkpointID, oid string, files map[string]archiveSource) bool {
	if len(oid) < 3 {
		return false
	}
	objectPrefixes := []string{
		enginepaths.WorkerBranchesDirName + "/.adapter/privategit/retained-store/objects",
		storePrefix + "/objects",
		storePrefix + "/checkpoints/" + checkpointID + "/git-objects",
	}
	if projKey := branchProjKey(branchRel); projKey != "" {
		objectPrefixes = append(objectPrefixes, enginepaths.WorkerBranchesDirName+"/.adapter/"+projKey+"/retained-git-store/objects")
	}
	for _, objPrefix := range objectPrefixes {
		looseRel := objPrefix + "/" + oid[:2] + "/" + oid[2:]
		if src, ok := files[looseRel]; ok && src.kind == fileKindRegular {
			return true
		}
		packPrefix := objPrefix + "/pack/"
		hasPack, hasIdx := false, false
		for rel, src := range files {
			if src.kind != fileKindRegular || !strings.HasPrefix(rel, packPrefix) {
				continue
			}
			if strings.HasSuffix(rel, ".pack") {
				hasPack = true
			} else if strings.HasSuffix(rel, ".idx") {
				hasIdx = true
			}
		}
		if hasPack && hasIdx {
			return true
		}
	}
	return false
}
