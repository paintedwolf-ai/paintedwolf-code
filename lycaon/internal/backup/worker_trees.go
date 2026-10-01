package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

type branchCapture struct {
	all   bool
	roots map[string]bool
}

func unsealedBranchTrees(ctx context.Context, database db.DBTX) (branchCapture, error) {
	selection := branchCapture{roots: map[string]bool{}}
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT workspace_relpath FROM worker_jobs WHERE workspace_relpath IS NOT NULL AND workspace_relpath != '' AND (workspace_overlay_id IS NULL OR workspace_overlay_id = '')`)
	if err != nil {
		return selection, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return selection, err
		}
		if branchTreeRoot(root) != root {
			return selection, fmt.Errorf("invalid retained worker branch root")
		}
		selection.roots[root] = true
	}
	return selection, rows.Err()
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

func (s branchCapture) includes(rel string, directory bool) bool {
	if workerMetadataPath(rel, directory) {
		return true
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

func (s branchCapture) verifyPresent(dataDir string) error {
	for root := range s.roots {
		info, err := os.Lstat(filepath.Join(dataDir, filepath.FromSlash(root)))
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

func validateRequiredBranchTrees(ctx context.Context, database db.DBTX, manifest Manifest) error {
	required, err := unsealedBranchTrees(ctx, database)
	if err != nil {
		return err
	}
	for _, root := range manifest.RetainedBranchTrees {
		delete(required.roots, root)
	}
	if len(required.roots) != 0 {
		return &InvalidError{Detail: "archive omits an unsealed worker tree"}
	}
	return nil
}
