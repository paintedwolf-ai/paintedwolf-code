package privategit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func ensureViewRepoInitialized(
	ctx context.Context,
	mgr *Manager,
	view *managedView,
	repoRel string,
) error {
	repoRel = cleanSlashRel(repoRel)
	view.mu.Lock()
	defer view.mu.Unlock()

	if existing, ok := view.repos[repoRel]; ok && existing != nil && existing.privateGitDir != "" {
		return nil
	}
	view.repos[repoRel] = &initializedRepo{metrics: InitMetrics{State: StatePending}}

	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}

	repo, err := initializePrivateRepo(ctx, mgr, view, repoRel, targetWT)
	if err != nil {
		if os.IsNotExist(err) {
			delete(view.repos, repoRel)
		} else {
			view.repos[repoRel] = &initializedRepo{metrics: InitMetrics{State: StateFailed}}
		}
		return err
	}
	view.repos[repoRel] = repo

	if repoRel == "." {
		gitmodulesPath := filepath.Join(targetWT, ".gitmodules")
		if !view.materialized {
			gitmodulesPath = filepath.Join(view.trunkDir, ".gitmodules")
		}
		subRels := parseSubmodulePaths(gitmodulesPath)
		for _, subRel := range subRels {
			subWT := filepath.Join(view.worktreeDir, filepath.FromSlash(subRel))
			subRepo, sErr := initializePrivateRepo(ctx, mgr, view, subRel, subWT)
			if sErr == nil {
				view.repos[subRel] = subRepo
				modDst := filepath.Join(repo.privateGitDir, "modules", filepath.FromSlash(subRel))
				_ = copyMetadataTree(subRepo.privateGitDir, modDst)
			}
		}
	}
	return nil
}

// InitializeMaterializedSeed discovers repositories during materialized fallback seed
// traversal, skips raw source .git metadata, registers pre-existing working-tree bases,
// and installs complete private .git repositories before workspace readiness (#1671).
func (m *Manager) InitializeMaterializedSeed(
	ctx context.Context,
	cfg MaterializedSeedConfig,
) (*MaterializedSeedResult, error) {
	return initializeMaterializedSeed(ctx, m, cfg)
}

func initializeMaterializedSeed(
	ctx context.Context,
	m *Manager,
	cfg MaterializedSeedConfig,
) (*MaterializedSeedResult, error) {
	if cfg.ViewID == "" || cfg.TrunkDir == "" || cfg.TargetDir == "" {
		return nil, fmt.Errorf("privategit: ViewID, TrunkDir, and TargetDir are required")
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = cfg.ViewID
	}
	absTrunk, err := filepath.Abs(cfg.TrunkDir)
	if err != nil {
		return nil, err
	}
	absTarget, err := filepath.Abs(cfg.TargetDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absTarget, 0o755); err != nil {
		return nil, err
	}

	var entriesSeeded, bytesSeeded int64
	discoveredRepos := make(map[string]struct{})

	walkErr := filepath.WalkDir(absTrunk, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cErr := ctx.Err(); cErr != nil {
			return cErr
		}
		rel, rErr := filepath.Rel(absTrunk, path)
		if rErr != nil {
			return rErr
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if d.Name() == ".git" {
			repoRel := cleanSlashRel(filepath.ToSlash(filepath.Dir(rel)))
			discoveredRepos[repoRel] = struct{}{}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		entriesSeeded++
		if cfg.Limits.MaxEntries > 0 && entriesSeeded > cfg.Limits.MaxEntries {
			return ErrSeedLimitExceeded
		}

		dstPath := filepath.Join(absTarget, rel)
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}

		if d.Type()&fs.ModeSymlink != 0 {
			link, lErr := os.Readlink(path)
			if lErr != nil {
				return lErr
			}
			_ = os.Remove(dstPath)
			if err := os.Symlink(link, dstPath); err != nil {
				return err
			}
			return registerSeedBaseIfCore(cfg.Core, slashRel, layeredfs.EntrySymlink, info.Mode(), 0, info.ModTime(), "", link)
		}
		if d.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			return registerSeedBaseIfCore(cfg.Core, slashRel, layeredfs.EntryDirectory, info.Mode(), 0, info.ModTime(), "", "")
		}

		bytesSeeded += info.Size()
		if cfg.Limits.MaxBytes > 0 && bytesSeeded > cfg.Limits.MaxBytes {
			return ErrSeedLimitExceeded
		}
		sha, cErr := copyAndHashWorkingFile(path, dstPath, info.Mode())
		if cErr != nil {
			return cErr
		}
		return registerSeedBaseIfCore(cfg.Core, slashRel, layeredfs.EntryRegular, info.Mode(), info.Size(), info.ModTime(), sha, "")
	})
	if walkErr != nil {
		return nil, walkErr
	}

	viewBacking := filepath.Join(m.cfg.BackingDir, "views", cfg.ViewID)
	if err := os.MkdirAll(viewBacking, 0o755); err != nil {
		return nil, err
	}
	view := &managedView{
		viewID:            cfg.ViewID,
		workerID:          cfg.WorkerID,
		rootID:            "primary",
		trunkDir:          absTrunk,
		worktreeDir:       absTarget,
		materialized:      true,
		core:              cfg.Core,
		authorizedGitDirs: append([]string(nil), cfg.AuthorizedGitDirs...),
		accessOrigin:      OriginFilesystem,
		repos:             make(map[string]*initializedRepo),
	}
	registerManagedView(m, view)

	if cfg.Core != nil {
		_ = cfg.Core.RegisterLowerProvider("primary", &gitAwareLowerProvider{
			mgr:      m,
			viewID:   cfg.ViewID,
			rootID:   "primary",
			trunkDir: absTarget,
		})
	}

	var repoList []string
	for r := range discoveredRepos {
		repoList = append(repoList, r)
	}
	sort.Strings(repoList)

	metricsMap := make(map[string]InitMetrics, len(repoList))
	for _, repoRel := range repoList {
		if err := ensureViewRepoInitialized(ctx, m, view, repoRel); err != nil {
			_ = m.ReleaseView(cfg.ViewID)
			return nil, err
		}
		view.mu.Lock()
		if r := view.repos[repoRel]; r != nil {
			metricsMap[repoRel] = r.metrics
		}
		view.mu.Unlock()
	}

	return &MaterializedSeedResult{
		EntriesSeeded: entriesSeeded,
		BytesSeeded:   bytesSeeded,
		Repositories:  repoList,
		RepoMetrics:   metricsMap,
	}, nil
}
