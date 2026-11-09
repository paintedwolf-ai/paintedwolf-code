package backup

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

func hasVerifiedPublishedCheckpoint(ctx context.Context, configDir, branchRel string) bool {
	meta, ok := readWorkerCheckpointMetaOnDisk(configDir, branchRel)
	if !ok || strings.TrimSpace(meta.LastPublishedCheckpointID) == "" {
		return false
	}
	for _, storeDir := range candidateCheckpointStoreDirsOnDisk(configDir, meta.CheckpointStoreDir) {
		if verifyCheckpointChainOnDisk(ctx, configDir, branchRel, storeDir, meta.LastPublishedCheckpointID, map[string]bool{}) == nil {
			return true
		}
	}
	return false
}

func verifyCheckpointChainOnDisk(ctx context.Context, configDir, branchRel, storeDir, checkpointID string, visited map[string]bool) error {
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
	if err := verifyCheckpointGitClosureOnDisk(ctx, configDir, branchRel, storeDir, checkpointID, m.PrivateGit); err != nil {
		return err
	}
	for _, parentID := range m.ParentCheckpoints {
		if err := verifyCheckpointChainOnDisk(ctx, configDir, branchRel, storeDir, parentID, visited); err != nil {
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

func collectRequiredGitOIDs(pg checkpoint.PrivateGitSnapshot) ([]string, []string, error) {
	seenOIDs := make(map[string]bool)
	seenPacks := make(map[string]bool)
	var requiredOIDs []string
	var requiredPacks []string

	addOID := func(oid string) {
		if !seenOIDs[oid] {
			seenOIDs[oid] = true
			requiredOIDs = append(requiredOIDs, oid)
		}
	}
	if head := strings.ToLower(strings.TrimSpace(pg.HeadRef)); head != "" && !strings.HasPrefix(head, "refs/") {
		if !isGitOID(head) {
			return nil, nil, fmt.Errorf("invalid git head_ref %q", pg.HeadRef)
		}
		addOID(head)
	}

	refNames := make([]string, 0, len(pg.Refs))
	for ref := range pg.Refs {
		refNames = append(refNames, ref)
	}
	sort.Strings(refNames)
	for _, ref := range refNames {
		rawOID := pg.Refs[ref]
		oid := strings.ToLower(strings.TrimSpace(rawOID))
		if !isGitOID(oid) {
			return nil, nil, fmt.Errorf("invalid git ref %q target %q", ref, rawOID)
		}
		addOID(oid)
	}

	for _, raw := range pg.RetainedObjects {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "pack:") {
			packBase := strings.TrimSpace(strings.TrimPrefix(trimmed, "pack:"))
			packBase = strings.TrimSuffix(strings.TrimSuffix(packBase, ".pack"), ".idx")
			if !isValidGitPackBase(packBase) {
				return nil, nil, fmt.Errorf("invalid retained git pack %q", raw)
			}
			if !seenPacks[packBase] {
				seenPacks[packBase] = true
				requiredPacks = append(requiredPacks, packBase)
			}
			continue
		}
		oid := strings.ToLower(trimmed)
		if !isGitOID(oid) {
			return nil, nil, fmt.Errorf("invalid retained git object id %q", raw)
		}
		addOID(oid)
	}
	return requiredOIDs, requiredPacks, nil
}

func isGitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func isHexOnly(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func isValidGitPackBase(base string) bool {
	if !strings.HasPrefix(base, "pack-") || strings.ContainsAny(base, `/\.`) {
		return false
	}
	return isHexOnly(strings.TrimPrefix(base, "pack-"))
}

func branchProjKey(branchRel string) string {
	parts := strings.Split(filepath.ToSlash(branchRel), "/")
	if len(parts) >= 2 && parts[0] == enginepaths.WorkerBranchesDirName && isLowerHex16(parts[1]) {
		return parts[1]
	}
	return ""
}

func verifyCheckpointGitClosureOnDisk(ctx context.Context, configDir, branchRel, storeDir, checkpointID string, pg checkpoint.PrivateGitSnapshot) error {
	const syntheticStorePrefix = "worker-recovery/checkpoints"
	diskDirs := []struct{ dir, prefix string }{
		{filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", "privategit", "retained-store", "objects"), enginepaths.WorkerBranchesDirName + "/.adapter/privategit/retained-store/objects"},
		{filepath.Join(storeDir, "objects"), syntheticStorePrefix + "/objects"},
		{filepath.Join(storeDir, "checkpoints", checkpointID, "git-objects"), syntheticStorePrefix + "/checkpoints/" + checkpointID + "/git-objects"},
	}
	if projKey := branchProjKey(branchRel); projKey != "" {
		diskDirs = append(diskDirs, struct{ dir, prefix string }{
			filepath.Join(configDir, enginepaths.WorkerBranchesDirName, ".adapter", projKey, "retained-git-store", "objects"),
			enginepaths.WorkerBranchesDirName + "/.adapter/" + projKey + "/retained-git-store/objects",
		})
	}
	files := make(map[string]archiveSource)
	for _, item := range diskDirs {
		_ = filepath.WalkDir(item.dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			rel, rErr := filepath.Rel(item.dir, path)
			info, iErr := entry.Info()
			if rErr != nil || iErr != nil {
				return nil
			}
			kind := fileKindRegular
			if info.Mode()&os.ModeSymlink != 0 {
				kind = fileKindSymlink
			}
			files[item.prefix+"/"+filepath.ToSlash(rel)] = archiveSource{path: path, kind: kind, mode: info.Mode().Perm()}
			return nil
		})
	}
	return verifyCheckpointGitClosureInFiles(ctx, branchRel, syntheticStorePrefix, checkpointID, pg, files)
}
