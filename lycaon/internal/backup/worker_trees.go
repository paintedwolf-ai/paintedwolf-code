package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/workerplacement"
)

type branchCapture struct {
	all   bool
	roots map[string]bool
}

func unsealedBranchTrees(ctx context.Context, configDir string, database db.DBTX) (branchCapture, error) {
	ctx, closeClosureSession := ensureGitClosureSession(ctx)
	defer closeClosureSession()
	selection := branchCapture{roots: map[string]bool{}}
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT workspace_relpath FROM worker_jobs WHERE workspace_relpath IS NOT NULL AND workspace_relpath != '' AND (workspace_overlay_id IS NULL OR workspace_overlay_id = '')`)
	if err != nil {
		return selection, err
	}
	defer func() { _ = rows.Close() }()
	visitState := make(map[string]uint8)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return selection, err
		}
		if branchTreeRoot(root) != root {
			return selection, fmt.Errorf("invalid retained worker branch root")
		}
		if configDir != "" {
			if hasVerifiedPublishedCheckpointWithState(ctx, configDir, root, visitState) {
				continue
			}
			if !branchTreeExistsOnDisk(configDir, root) && branchDeclaresPublishedCheckpoint(configDir, root) {
				continue
			}
		}
		selection.roots[root] = true
	}
	return selection, rows.Err()
}

func unsealedBranchTreesInFiles(ctx context.Context, configDir string, database db.DBTX, files map[string]archiveSource) (branchCapture, error) {
	ctx, closeClosureSession := ensureGitClosureSession(ctx)
	defer closeClosureSession()
	selection := branchCapture{roots: map[string]bool{}}
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT workspace_relpath FROM worker_jobs WHERE workspace_relpath IS NOT NULL AND workspace_relpath != '' AND (workspace_overlay_id IS NULL OR workspace_overlay_id = '')`)
	if err != nil {
		return selection, err
	}
	defer func() { _ = rows.Close() }()
	visitState := sessionFileVisitState(ctx)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return selection, err
		}
		if branchTreeRoot(root) != root {
			return selection, fmt.Errorf("invalid retained worker branch root")
		}
		if configDir != "" {
			if cid := readBranchPublishedCheckpointIDInFiles(root, files); cid != "" {
				if verifyCheckpointChainInFiles(ctx, root, cid, files, visitState) == nil {
					continue
				}
				if !branchTreeExistsOnDisk(configDir, root) {
					continue
				}
			}
		}
		selection.roots[root] = true
	}
	return selection, rows.Err()
}

func readBranchPublishedCheckpointIDInFiles(branchRel string, files map[string]archiveSource) string {
	metaSrc, ok := files[branchRel+".meta/state.json"]
	if !ok || metaSrc.kind != fileKindRegular {
		return ""
	}
	raw, err := os.ReadFile(metaSrc.path)
	if err != nil {
		return ""
	}
	var meta workerCheckpointMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.LastPublishedCheckpointID)
}

func branchTreeRoot(rel string) string {
	if rel != filepath.ToSlash(filepath.Clean(rel)) || strings.Contains(rel, "\\") {
		return ""
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || parts[0] != enginepaths.WorkerBranchesDirName || !workerMetadataPath(strings.Join(parts[:2], "/"), true) {
		return ""
	}
	if parts[2] == "" || parts[2] == "." || parts[2] == ".." || enginepaths.IsJobMetaDirName(parts[2]) {
		return ""
	}
	return strings.Join(parts[:3], "/")
}

// materializedBaseJobID names the branch whose exact base rel belongs to: an unsealed materialized worker's
// changes can only be derived again from its pristine tree and stat manifest, so they go with its tree.
func materializedBaseJobID(rel string) string {
	if rel != filepath.ToSlash(filepath.Clean(rel)) {
		return ""
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 4 || parts[0] != enginepaths.WorkerBranchesDirName || !enginepaths.IsJobMetaDirName(parts[2]) ||
		parts[2] == enginepaths.JobMetaDirSuffix || !workerMetadataPath(strings.Join(parts[:2], "/"), true) {
		return ""
	}
	switch parts[3] {
	case "pristine", "stat-manifest":
		return strings.Join(append(parts[:2:2], strings.TrimSuffix(parts[2], enginepaths.JobMetaDirSuffix)), "/")
	}
	return ""
}

func (s branchCapture) includes(rel string, directory bool) bool {
	if rel == enginepaths.WorkerRecoveryDirName || strings.HasPrefix(rel, enginepaths.WorkerRecoveryDirName+"/") {
		return durableWorkerRecoveryPath(rel, directory)
	}
	if rel == enginepaths.WorkerBranchesDirName+"/.adapter" || strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/.adapter/") {
		return durableWorkerAdapterPath(rel, directory)
	}
	if workerMetadataPath(rel, directory) {
		return true
	}
	if jobID := materializedBaseJobID(rel); jobID != "" {
		return s.all || s.roots[jobID]
	}
	root := branchTreeRoot(rel)
	if root == "" {
		return false
	}
	if s.all {
		if s.roots != nil {
			s.roots[root] = true
		}
		return true
	}
	return s.roots[root]
}

func (s branchCapture) names() []string {
	roots := make([]string, 0, len(s.roots))
	for root := range s.roots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

// placedRoots lists selected trees that live in a placement folder on this device.
func (s branchCapture) placedRoots(dataDir string) (map[string]string, error) {
	placed := map[string]string{}
	for root := range s.roots {
		physical, err := workerplacement.PhysicalFromLogical(dataDir, root)
		if err != nil {
			return nil, err
		}
		if physical != filepath.Join(dataDir, filepath.FromSlash(root)) {
			placed[root] = physical
		}
	}
	return placed, nil
}

// collectPlacedBranchTrees adds placed unsealed trees under their logical address,
// where restore expects them. Their real layout record replaces the state-folder
// one, which always says the tree is absent.
func collectPlacedBranchTrees(ctx context.Context, dataDir string, files map[string]archiveSource, branches branchCapture, entryLimit int) error {
	placed, err := branches.placedRoots(dataDir)
	if err != nil {
		return err
	}
	for root, physical := range placed {
		err := filepath.WalkDir(physical, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			kind := fileKindRegular
			if info.Mode()&os.ModeSymlink != 0 {
				kind = fileKindSymlink
			} else if !info.Mode().IsRegular() {
				return fmt.Errorf("durable path is not a regular file: %s", path)
			}
			if len(files)+1 >= entryLimit {
				return fmt.Errorf("archive contains too many entries")
			}
			rel, err := filepath.Rel(physical, path)
			if err != nil {
				return err
			}
			files[root+"/"+filepath.ToSlash(rel)] = archiveSource{path: path, kind: kind, mode: info.Mode().Perm()}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%w: walk placed worker tree: %w", ErrCaptureIncomplete, err)
		}
		meta := enginepaths.MetaDirForBranchRoot(physical) + "/state.json"
		info, err := os.Lstat(meta)
		if err != nil {
			return fmt.Errorf("%w: placed worker layout: %w", ErrCaptureIncomplete, err)
		}
		files[root+enginepaths.JobMetaDirSuffix+"/state.json"] = archiveSource{path: meta, kind: fileKindRegular, mode: info.Mode().Perm()}
	}
	return nil
}

// withoutPlaced drops trees outside the state folder. A store upgrade neither
// changes nor needs them, so an absent volume must not stop one.
func (s branchCapture) withoutPlaced(dataDir string) (branchCapture, error) {
	placed, err := s.placedRoots(dataDir)
	if err != nil {
		return s, err
	}
	for root := range placed {
		delete(s.roots, root)
	}
	return s, nil
}

func (s branchCapture) verifyPresent(dataDir string) error {
	for root := range s.roots {
		physical, err := workerplacement.PhysicalFromLogical(dataDir, root)
		if err != nil {
			return err
		}
		info, err := os.Lstat(physical)
		if err != nil {
			return fmt.Errorf("%w: retained worker tree %s: %w", ErrCaptureIncomplete, root, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: retained worker tree is not a directory", ErrCaptureIncomplete)
		}
	}
	return nil
}

func validateBranchTreeList(trees []string) error {
	seen := map[string]bool{}
	for _, root := range trees {
		if branchTreeRoot(root) != root || seen[root] {
			return &InvalidError{Detail: "invalid or duplicate retained worker tree"}
		}
		seen[root] = true
	}
	return nil
}

func restorableArchivePath(rel string, trees []string) bool {
	if RestorableRelPath(rel) {
		return true
	}
	root := branchTreeRoot(rel)
	if root == "" || rel == root {
		return false
	}
	for _, tree := range trees {
		if root == tree {
			return true
		}
	}
	return false
}

// validateRequiredBranchTrees requires every unsealed tree that no published checkpoint under
// capturedDir rebuilds. A tree absent from the manifest is accepted only when this device's registry,
// under configDir, places it outside the state folder, as an upgrade recovery point leaves it.
func validateRequiredBranchTrees(ctx context.Context, capturedDir string, database db.DBTX, manifest Manifest, configDir string) error {
	required, err := unsealedBranchTrees(ctx, capturedDir, database)
	if err != nil {
		return err
	}
	for _, root := range manifest.RetainedBranchTrees {
		delete(required.roots, root)
	}
	if required, err = required.withoutPlaced(configDir); err != nil {
		return err
	}
	if len(required.roots) != 0 {
		return &InvalidError{Detail: "archive omits an unsealed worker tree"}
	}
	return nil
}
