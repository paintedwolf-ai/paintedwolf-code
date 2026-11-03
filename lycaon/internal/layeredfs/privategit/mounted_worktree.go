package privategit

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

func initializeMountedSparseWorktree(
	ctx context.Context,
	srcWorktreeDir, targetWorktreeDir, privateGitDir string,
) error {
	dirtyPaths := make(map[string]bool)
	statusOut, code, err := gitexec.Run(
		ctx,
		srcWorktreeDir,
		[]string{
			"--git-dir=" + privateGitDir,
			"--work-tree=" + srcWorktreeDir,
			"--no-optional-locks",
			"status",
			"--porcelain=v1",
			"-z",
			"--untracked-files=all",
		},
		gitexec.Opts{
			Profile:   gitexec.ProfileHermetic,
			IndexFile: filepath.Join(privateGitDir, "index"),
		},
	)
	if err == nil && code == 0 {
		for _, p := range parsePorcelainZPaths(statusOut) {
			dirtyPaths[p] = true
		}
	}
	if _, sErr := os.Lstat(filepath.Join(srcWorktreeDir, ".gitmodules")); sErr == nil {
		dirtyPaths[".gitmodules"] = true
	}
	for rel := range dirtyPaths {
		if err := copySingleTrunkPath(srcWorktreeDir, targetWorktreeDir, rel); err != nil {
			return err
		}
	}
	lsOut, lsCode, lsErr := gitexec.Run(ctx, targetWorktreeDir, []string{"ls-files", "-z"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if lsErr == nil && lsCode == 0 {
		var cleanTracked []string
		for _, tok := range bytes.Split(lsOut, []byte{0}) {
			if p := string(tok); p != "" && !dirtyPaths[p] {
				cleanTracked = append(cleanTracked, p)
			}
		}
		if len(cleanTracked) > 0 {
			_ = updateIndexSkipWorktree(ctx, targetWorktreeDir, "--skip-worktree", cleanTracked)
		}
	}
	_, _, _ = gitexec.Run(ctx, targetWorktreeDir, []string{"update-index", "-q", "--refresh"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	return nil
}

func parsePorcelainZPaths(raw []byte) []string {
	tokens := bytes.Split(raw, []byte{0})
	var out []string
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 4 {
			continue
		}
		xy, path := string(tok[:2]), string(tok[3:])
		if path != "" {
			out = append(out, path)
		}
		if (strings.Contains(xy, "R") || strings.Contains(xy, "C")) && i+1 < len(tokens) && len(tokens[i+1]) > 0 {
			out = append(out, string(tokens[i+1]))
			i++
		}
	}
	return out
}

func updateIndexSkipWorktree(ctx context.Context, worktreeDir, flag string, paths []string) error {
	lsOut, lsCode, lsErr := gitexec.Run(ctx, worktreeDir, []string{"ls-files", "-z"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if lsErr != nil || lsCode != 0 {
		return nil
	}
	inIndex := make(map[string]bool)
	for _, tok := range bytes.Split(lsOut, []byte{0}) {
		if len(tok) > 0 {
			inIndex[string(tok)] = true
		}
	}
	var filtered []string
	for _, p := range paths {
		if inIndex[p] {
			filtered = append(filtered, p)
		}
	}
	const batchSize = 128
	for i := 0; i < len(filtered); i += batchSize {
		end := min(i+batchSize, len(filtered))
		args := append([]string{"update-index", flag}, filtered[i:end]...)
		_, _, _ = gitexec.Run(ctx, worktreeDir, args, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	}
	return nil
}

func copySingleTrunkPath(srcRoot, dstRoot, slashRel string) error {
	clean := cleanSlashRel(slashRel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return nil
	}
	srcPath := filepath.Join(srcRoot, filepath.FromSlash(clean))
	dstPath := filepath.Join(dstRoot, filepath.FromSlash(clean))
	info, err := os.Lstat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		link, lErr := os.Readlink(srcPath)
		if lErr != nil {
			return lErr
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		_ = os.RemoveAll(dstPath)
		return os.Symlink(link, dstPath)
	}
	if info.IsDir() {
		return filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if d.Name() == ".git" {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel, rErr := filepath.Rel(srcRoot, p)
			if rErr != nil {
				return rErr
			}
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
			}
			return copySingleTrunkPath(srcRoot, dstRoot, filepath.ToSlash(rel))
		})
	}
	_, err = copyAndHashWorkingFile(srcPath, dstPath, info.Mode())
	return err
}

func materializeMountedPaths(ctx context.Context, view *managedView, repoRel string, relPaths []string) error {
	if view == nil || view.materialized || len(relPaths) == 0 {
		return nil
	}
	repoRel = cleanSlashRel(repoRel)
	srcDir, dstDir := view.trunkDir, view.worktreeDir
	if repoRel != "." {
		srcDir = filepath.Join(view.trunkDir, filepath.FromSlash(repoRel))
		dstDir = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}
	var toUnskip []string
	seen := make(map[string]bool, len(relPaths))
	for _, rawRel := range relPaths {
		clean := cleanSlashRel(rawRel)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".git" || strings.HasPrefix(clean, ".git/") || seen[clean] {
			continue
		}
		seen[clean] = true
		if _, err := os.Lstat(filepath.Join(dstDir, filepath.FromSlash(clean))); os.IsNotExist(err) {
			if cErr := copySingleTrunkPath(srcDir, dstDir, clean); cErr != nil {
				return cErr
			}
		}
		toUnskip = append(toUnskip, clean)
	}
	if len(toUnskip) > 0 {
		_ = updateIndexSkipWorktree(ctx, dstDir, "--no-skip-worktree", toUnskip)
		_, _, _ = gitexec.Run(ctx, dstDir, []string{"update-index", "-q", "--refresh"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	}
	return nil
}

func materializeCandidateRefDiffs(ctx context.Context, view *managedView, repoRel string) error {
	if view == nil || view.materialized {
		return nil
	}
	repoRel = cleanSlashRel(repoRel)
	dstDir := view.worktreeDir
	if repoRel != "." {
		dstDir = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}
	headOut, hCode, hErr := gitexec.Run(ctx, dstDir, []string{"rev-parse", "--verify", "HEAD"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if hErr != nil || hCode != 0 {
		return nil
	}
	headOID := strings.TrimSpace(string(headOut))
	refsOut, rCode, rErr := gitexec.Run(ctx, dstDir, []string{"for-each-ref", "--format=%(objectname)", "refs/heads", "refs/stash"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if rErr != nil || rCode != 0 {
		return nil
	}
	candidates := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(refsOut)), "\n") {
		if oid := strings.TrimSpace(line); oid != "" && oid != headOID {
			candidates[oid] = true
		}
	}
	view.mu.Lock()
	if r := view.repos[repoRel]; r != nil && r.initialHeadOID != "" && r.initialHeadOID != headOID {
		candidates[r.initialHeadOID] = true
		candidates["HEAD^"] = true
	}
	view.mu.Unlock()

	var diffPaths []string
	for other := range candidates {
		dtOut, dtCode, dtErr := gitexec.Run(ctx, dstDir, []string{"diff-tree", "-r", "--name-only", "-z", headOID, other}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
		if dtErr == nil && dtCode == 0 {
			for _, tok := range bytes.Split(dtOut, []byte{0}) {
				if len(tok) > 0 {
					diffPaths = append(diffPaths, string(tok))
				}
			}
		}
	}
	return materializeMountedPaths(ctx, view, repoRel, diffPaths)
}

func materializeCommandTargets(ctx context.Context, view *managedView, repoRel string, args []string) error {
	if view == nil || view.materialized || len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "checkout", "switch", "stash", "merge", "rebase":
		_ = materializeCandidateRefDiffs(ctx, view, repoRel)
	case "revert", "cherry-pick":
		dstDir := view.worktreeDir
		if repoRel != "." {
			dstDir = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
		}
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") || a == "" {
				continue
			}
			for _, spec := range []string{a + "^", a} {
				if dtOut, dtCode, dtErr := gitexec.Run(ctx, dstDir, []string{"diff-tree", "-r", "--name-only", "-z", "HEAD", spec}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); dtErr == nil && dtCode == 0 {
					var paths []string
					for _, tok := range bytes.Split(dtOut, []byte{0}) {
						if len(tok) > 0 {
							paths = append(paths, string(tok))
						}
					}
					_ = materializeMountedPaths(ctx, view, repoRel, paths)
				}
			}
		}
	}
	srcDir := view.trunkDir
	if repoRel != "." {
		srcDir = filepath.Join(view.trunkDir, filepath.FromSlash(repoRel))
	}
	var explicit []string
	afterDash := false
	for _, a := range args[1:] {
		if a == "--" {
			afterDash = true
			continue
		}
		if !afterDash && strings.HasPrefix(a, "-") {
			continue
		}
		clean := cleanSlashRel(a)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			continue
		}
		if afterDash {
			explicit = append(explicit, clean)
		} else if _, err := os.Lstat(filepath.Join(srcDir, filepath.FromSlash(clean))); err == nil {
			explicit = append(explicit, clean)
		}
	}
	return materializeMountedPaths(ctx, view, repoRel, explicit)
}
