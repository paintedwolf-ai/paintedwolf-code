package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func resolveCommandDir(ctx context.Context, p Prepared, stderr io.Writer, write bool) (string, int, error) {
	dir := p.paths.Cwd().Abs
	for _, next := range p.command.dirs {
		if filepath.IsAbs(next) {
			dir = next
		} else {
			dir = filepath.Join(dir, next)
		}
		bound, err := p.paths.Bind(ctx, dir, write && filepath.Clean(dir) != filepath.Clean(p.paths.Cwd().Abs))
		if err != nil {
			return "", 0, err
		}
		if info, err := os.Stat(bound.Resolved.Abs); err != nil || !info.IsDir() {
			reason := "No such file or directory"
			if err == nil {
				reason = "Not a directory"
			}
			_, _ = fmt.Fprintf(stderr, "fatal: cannot change to '%s': %s\n", next, reason)
			return "", 128, nil
		}
		dir = bound.Resolved.Abs
	}
	return dir, 0, nil
}

func resolveCommandRepo(ctx context.Context, p Prepared, stderr io.Writer, write bool) (string, gitrepo.Repo, int, error) {
	dir, status, err := resolveCommandDir(ctx, p, stderr, false)
	if err != nil || status != 0 {
		return "", gitrepo.Repo{}, status, err
	}
	repo, ok := gitrepo.Discover(dir)
	if !ok {
		_, _ = io.WriteString(stderr, "fatal: not a git repository (or any of the parent directories): .git\n")
		return "", gitrepo.Repo{}, 128, nil
	}
	boundRepo, err := p.paths.Bind(ctx, repo.Root, false)
	if err != nil {
		return "", gitrepo.Repo{}, 0, err
	}
	repo.Root = boundRepo.Resolved.Abs
	return dir, repo, 0, nil
}

func repoRelPaths(ctx context.Context, p Prepared, dir, repoRoot string, rawPaths []string) ([]string, error) {
	if len(rawPaths) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(rawPaths))
	cleanRepo := filepath.Clean(repoRoot)
	for _, raw := range rawPaths {
		target := raw
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if filepath.Clean(target) == cleanRepo {
			out = append(out, ".")
			continue
		}
		bound, err := p.paths.Bind(ctx, target, true)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(cleanRepo, bound.Resolved.Abs)
		if err != nil || sandbox.HasParentTraversal(rel) {
			return nil, &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": raw, "tool": p.command.spec.Name}}
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

func expandRestorePaths(ctx context.Context, p Prepared, dir, repoRoot string, rawPaths []string, source string) ([]string, error) {
	rels, err := repoRelPaths(ctx, p, dir, repoRoot, rawPaths)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if rel != "" && rel != "." && !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	for _, rel := range rels {
		full := filepath.Join(repoRoot, filepath.FromSlash(rel))
		info, statErr := os.Stat(full)
		if rel != "." && (statErr != nil || !info.IsDir()) {
			add(rel)
			continue
		}
		changed, err := p.owners.Git.ChangedPaths(ctx, repoRoot)
		if err != nil {
			return nil, err
		}
		for _, c := range changed {
			if matchesDirPrefix(c, rel) {
				add(c)
			}
		}
		if strings.TrimSpace(source) != "" {
			for _, diffArgs := range [][]string{{"diff", "--name-only", source}, {"diff", "--cached", "--name-only", source}} {
				var buf bytes.Buffer
				args := append([]string{}, diffArgs...)
				if rel != "." {
					args = append(args, "--", rel)
				}
				if _, code, err := (git.CommandReader{}).ReadCommand(ctx, repoRoot, args, nil, &buf); err == nil && code == 0 {
					for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
						add(strings.TrimSpace(line))
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func expandStashPaths(ctx context.Context, p Prepared, dir, repoRoot string, rawPaths []string, includeUntracked bool) ([]string, error) {
	rels, err := repoRelPaths(ctx, p, dir, repoRoot, rawPaths)
	if err != nil {
		return nil, err
	}
	status, err := p.owners.Git.Status(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, f := range status.Files {
		if strings.HasPrefix(f.Status, "!") {
			continue
		}
		if !includeUntracked && f.Status == "??" {
			continue
		}
		dirty = append(dirty, filepath.ToSlash(f.Path))
	}
	if len(rels) == 0 {
		return dirty, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, rel := range rels {
		if rel == "." {
			for _, d := range dirty {
				if !seen[d] {
					seen[d] = true
					out = append(out, d)
				}
			}
			continue
		}
		full := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if info, statErr := os.Stat(full); statErr == nil && info.IsDir() {
			for _, d := range dirty {
				if matchesDirPrefix(d, rel) && !seen[d] {
					seen[d] = true
					out = append(out, d)
				}
			}
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out, nil
}

func matchesDirPrefix(path, dirRel string) bool {
	if dirRel == "." || dirRel == "" {
		return true
	}
	path = filepath.ToSlash(path)
	dirRel = strings.TrimSuffix(filepath.ToSlash(dirRel), "/")
	return path == dirRel || strings.HasPrefix(path, dirRel+"/")
}

func withRepoRoot(tc tools.ToolContext, repoRoot string) tools.ToolContext {
	if filepath.Clean(tc.ActiveRootPath()) == filepath.Clean(repoRoot) {
		return tc
	}
	roots := append([]projectroot.RootRef(nil), tc.Source.Roots...)
	updated := false
	for i := range roots {
		if roots[i].ID == tc.Source.ActiveRootID || (tc.Source.ActiveRootID == "" && roots[i].IsPrimary) {
			roots[i].Path = repoRoot
			updated = true
		}
	}
	if !updated && len(roots) > 0 {
		roots[0].Path = repoRoot
	}
	tc.Source.Roots = roots
	if tc.Source.WorkerBranchRoot != "" {
		tc.Source.WorkerBranchRoot = repoRoot
	}
	return tc
}
