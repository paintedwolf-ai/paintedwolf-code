package workspace

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
)

func readoptMaterializedRepos(ctx context.Context, services *layeredServices, branchRoot string, core *layeredfs.Core) error {
	recordPath := materializedGitRecordPath(branchRoot)
	if info, err := os.Lstat(recordPath); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: materialized Git record is a symlink: %s", recovery.ErrIncompleteCheckpoint, recordPath)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		return err
	}
	var record materializedGitRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("%w: decode materialized Git record: %w", recovery.ErrIncompleteCheckpoint, err)
	}
	if record.Format != materializedGitFormat {
		return fmt.Errorf("%w: unsupported materialized Git record format %d", recovery.ErrIncompleteCheckpoint, record.Format)
	}
	for _, gitView := range record.Views {
		resolvedWT, _, _, err := resolveConfinedRepoGitDir(branchRoot, gitView.WorktreeDir, gitView.WorktreeRel, ".")
		if err != nil {
			return err
		}
		gitView.WorktreeDir = resolvedWT
		if gitView.BranchDir != "" {
			gitView.BranchDir = branchRoot
		}
		if err := refreshMaterializedRepoAlternates(ctx, services, branchRoot, gitView); err != nil {
			return err
		}
		if err := adoptGitView(ctx, services, gitView, core, nil, gitView.Repos); err != nil {
			return fmt.Errorf("%w: %w", recovery.ErrIncompleteCheckpoint, err)
		}
	}
	return nil
}

func isLocalOrDotRel(rel string) bool {
	return !strings.Contains(rel, "\\") && (rel == "." || filepath.IsLocal(filepath.FromSlash(rel)))
}

func resolveConfinedRepoGitDir(branchRoot, worktreeDir, worktreeRel, repoRel string) (targetDir, gitDir, altPath string, err error) {
	branchRoot = filepath.Clean(branchRoot)
	if info, statErr := os.Lstat(branchRoot); statErr != nil {
		return "", "", "", fmt.Errorf("%w: stat branch root %s: %w", recovery.ErrIncompleteCheckpoint, branchRoot, statErr)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", "", fmt.Errorf("%w: branch root %s is not a confined directory", recovery.ErrIncompleteCheckpoint, branchRoot)
	}
	if worktreeRel != "" {
		if !isLocalOrDotRel(worktreeRel) {
			return "", "", "", fmt.Errorf("%w: invalid materialized worktree_rel %q", recovery.ErrIncompleteCheckpoint, worktreeRel)
		}
		worktreeDir = filepath.Join(branchRoot, filepath.FromSlash(worktreeRel))
	}
	if strings.TrimSpace(worktreeDir) == "" {
		return "", "", "", fmt.Errorf("%w: empty materialized worktree directory", recovery.ErrIncompleteCheckpoint)
	}
	worktreeDir = filepath.Clean(worktreeDir)
	if relWT, relErr := filepath.Rel(branchRoot, worktreeDir); relErr != nil || !isLocalOrDotRel(filepath.ToSlash(relWT)) {
		return "", "", "", fmt.Errorf("%w: materialized worktree %s escapes branch root %s", recovery.ErrIncompleteCheckpoint, worktreeDir, branchRoot)
	}
	targetDir = worktreeDir
	if repoRel != "" && repoRel != "." {
		if !isLocalOrDotRel(repoRel) {
			return "", "", "", fmt.Errorf("%w: invalid materialized repo_rel %q", recovery.ErrIncompleteCheckpoint, repoRel)
		}
		targetDir = filepath.Join(worktreeDir, filepath.FromSlash(repoRel))
	}
	targetDir = filepath.Clean(targetDir)
	if relTarget, relErr := filepath.Rel(branchRoot, targetDir); relErr != nil || !isLocalOrDotRel(filepath.ToSlash(relTarget)) {
		return "", "", "", fmt.Errorf("%w: materialized repo %s escapes branch root %s", recovery.ErrIncompleteCheckpoint, targetDir, branchRoot)
	}
	gitDir = filepath.Join(targetDir, ".git")
	altPath = filepath.Join(gitDir, "objects", "info", "alternates")
	relAlt, relErr := filepath.Rel(branchRoot, altPath)
	if relErr != nil || !isLocalOrDotRel(filepath.ToSlash(relAlt)) {
		return "", "", "", fmt.Errorf("%w: alternates path %s escapes branch root %s", recovery.ErrIncompleteCheckpoint, altPath, branchRoot)
	}
	cur := branchRoot
	for _, seg := range strings.Split(relAlt, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		info, lstatErr := os.Lstat(cur)
		if errors.Is(lstatErr, os.ErrNotExist) {
			break
		}
		if lstatErr != nil {
			return "", "", "", fmt.Errorf("%w: inspect %s: %w", recovery.ErrIncompleteCheckpoint, cur, lstatErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", "", fmt.Errorf("%w: symlink confinement violation at %s", recovery.ErrIncompleteCheckpoint, cur)
		}
	}
	return targetDir, gitDir, altPath, nil
}

func isHexGitOID(s string) bool {
	_, err := hex.DecodeString(s)
	return (len(s) == 40 || len(s) == 64) && err == nil
}

func collectRepoCurrentTips(gitDir string) ([]string, error) {
	seen := make(map[string]bool)
	var tips []string
	addOID := func(candidate string) {
		oid := strings.ToLower(strings.TrimSpace(candidate))
		if isHexGitOID(oid) && !seen[oid] {
			seen[oid] = true
			tips = append(tips, oid)
		}
	}
	packedPath := filepath.Join(gitDir, "packed-refs")
	if info, err := os.Lstat(packedPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink packed-refs in %s", gitDir)
		}
		if raw, rErr := os.ReadFile(packedPath); rErr == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					addOID(fields[0])
				}
			}
		}
	}
	refsRoot := filepath.Join(gitDir, "refs")
	if info, err := os.Lstat(refsRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink refs dir in %s", gitDir)
		}
		if walkErr := filepath.WalkDir(refsRoot, func(path string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink ref %s", path)
			}
			if d.IsDir() {
				return nil
			}
			raw, rErr := os.ReadFile(path)
			if rErr != nil {
				return rErr
			}
			val := strings.TrimSpace(string(raw))
			if !strings.HasPrefix(val, "ref:") {
				addOID(val)
			}
			return nil
		}); walkErr != nil {
			return nil, walkErr
		}
	}
	headPath := filepath.Join(gitDir, "HEAD")
	if info, err := os.Lstat(headPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink HEAD in %s", gitDir)
		}
		if raw, rErr := os.ReadFile(headPath); rErr == nil {
			val := strings.TrimSpace(string(raw))
			if strings.HasPrefix(val, "ref:") {
				refName := strings.TrimSpace(strings.TrimPrefix(val, "ref:"))
				if filepath.IsLocal(filepath.FromSlash(refName)) {
					refPath := filepath.Join(gitDir, filepath.FromSlash(refName))
					if rInfo, lErr := os.Lstat(refPath); lErr == nil && rInfo.Mode()&os.ModeSymlink == 0 {
						if refRaw, rfErr := os.ReadFile(refPath); rfErr == nil {
							addOID(string(refRaw))
						}
					}
				}
			} else {
				addOID(val)
			}
		}
	}
	return tips, nil
}

func refreshMaterializedRepoAlternates(ctx context.Context, services *layeredServices, branchRoot string, gitView materializedGitView) error {
	if services == nil || services.privateGit == nil || services.privateGit.Store() == nil {
		return fmt.Errorf("%w: private Git store is unavailable", recovery.ErrIncompleteCheckpoint)
	}
	store := services.privateGit.Store()
	workerBranchesDir := filepath.Dir(filepath.Dir(branchRoot))
	projKey := filepath.Base(filepath.Dir(branchRoot))
	adapterRetainedObjects := filepath.Join(workerBranchesDir, adapterRuntimeDirName, projKey, "retained-git-store", "objects")

	for _, at := range gitView.Repos {
		target, gitDir, altPath, err := resolveConfinedRepoGitDir(branchRoot, gitView.WorktreeDir, gitView.WorktreeRel, at.RepoRel)
		if err != nil {
			return err
		}
		if info, statErr := os.Lstat(gitDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: materialized repository .git missing or invalid at %s: %v", recovery.ErrIncompleteCheckpoint, gitDir, statErr)
		}
		rawAlt, err := os.ReadFile(altPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: read materialized repository alternates %s: %w", recovery.ErrIncompleteCheckpoint, altPath, err)
		}
		objectsDir := filepath.Join(gitDir, "objects")
		srcDirs := []string{
			store.ObjectsDir(),
			adapterRetainedObjects,
			objectsDir,
		}
		for _, line := range strings.Split(string(rawAlt), "\n") {
			dir := strings.TrimSpace(line)
			if dir == "" {
				continue
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Clean(filepath.Join(objectsDir, dir))
			}
			if dir != store.ObjectsDir() {
				srcDirs = append(srcDirs, dir)
			}
		}
		if gitView.TrunkDir != "" {
			trunk := gitView.TrunkDir
			if at.RepoRel != "" && at.RepoRel != "." {
				trunk = filepath.Join(trunk, filepath.FromSlash(at.RepoRel))
			}
			srcDirs = append(srcDirs, filepath.Join(trunk, ".git", "objects"))
		}

		currentTips, err := collectRepoCurrentTips(gitDir)
		if err != nil {
			return fmt.Errorf("%w: read materialized repository refs in %s: %w", recovery.ErrIncompleteCheckpoint, gitDir, err)
		}
		seenTips := make(map[string]bool)
		var tips []string
		addTip := func(oid string) {
			oid = strings.ToLower(strings.TrimSpace(oid))
			if isHexGitOID(oid) && !seenTips[oid] {
				seenTips[oid] = true
				tips = append(tips, oid)
			}
		}
		addTip(at.InitialHead)
		for _, oid := range at.InitialRefs {
			addTip(oid)
		}
		for _, oid := range currentTips {
			addTip(oid)
		}
		if len(tips) > 0 {
			if _, _, err := store.ImportRepositoryObjects(ctx, srcDirs, tips); err != nil {
				return fmt.Errorf("%w: materialized repository objects unavailable for view %s: %w", recovery.ErrIncompleteCheckpoint, gitView.ViewID, err)
			}
		}
		if strings.TrimSpace(string(rawAlt)) != store.ObjectsDir() {
			if _, err := fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.PathLocation(altPath),
				Source:   strings.NewReader(store.ObjectsDir() + "\n"),
				Mode:     0o644,
				DirMode:  0o700,
			}); err != nil {
				return fmt.Errorf("%w: update alternates %s: %w", recovery.ErrIncompleteCheckpoint, altPath, err)
			}
		}
		for _, tip := range tips {
			out, code, rErr := gitexec.Run(ctx, target, []string{"rev-list", "--objects", "-n", "1", tip}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
			if rErr != nil || code != 0 {
				return fmt.Errorf("%w: incomplete git object graph for %s in view %s (exit %d): %v: %s",
					recovery.ErrIncompleteCheckpoint, tip, gitView.ViewID, code, rErr, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}
