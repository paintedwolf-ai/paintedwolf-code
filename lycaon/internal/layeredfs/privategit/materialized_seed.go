package privategit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// InitRepo ensures the private Git repository for repoRel in viewID is initialized.
func (m *Manager) InitRepo(ctx context.Context, viewID, repoRel string) (InitMetrics, error) {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return InitMetrics{}, err
	}
	if strings.TrimSpace(repoRel) == "" {
		repoRel = "."
	}
	if err := ensureViewRepoInitialized(ctx, m, view, repoRel); err != nil {
		return InitMetrics{}, err
	}
	if err := syncViewGitOverlay(view); err != nil {
		return InitMetrics{}, err
	}
	metrics, _ := m.RepoMetrics(viewID, repoRel)
	return metrics, nil
}

func syncViewGitOverlay(view *managedView) error {
	if view == nil || view.syncer == nil {
		return nil
	}
	type repoDir struct {
		repoRel string
		gitDir  string
	}
	view.mu.Lock()
	rootID := view.rootID
	var dirs []repoDir
	for rel, r := range view.repos {
		if r != nil && r.privateGitDir != "" {
			dirs = append(dirs, repoDir{repoRel: rel, gitDir: r.privateGitDir})
		}
	}
	view.mu.Unlock()
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].repoRel < dirs[j].repoRel })
	for _, d := range dirs {
		if err := view.syncer.SyncGitOverlayTo(rootID, d.repoRel, d.gitDir); err != nil {
			return err
		}
	}
	return nil
}

func ensureViewRepoInitialized(
	ctx context.Context,
	mgr *Manager,
	view *managedView,
	repoRel string,
) error {
	repoRel = cleanSlashRel(repoRel)
	view.mu.Lock()
	defer view.mu.Unlock()

	existing := view.repos[repoRel]
	durable := readDurableRepoRecord(mgr, view.viewID, repoRel)

	if (existing != nil && existing.privateGitDir != "") || durable.Status == repoStatusEstablished {
		gitDir, vErr := verifyEstablishedPrivateRepo(mgr, existing, durable)
		if vErr != nil {
			view.repos[repoRel] = &initializedRepo{repoRel: repoRel, metrics: InitMetrics{State: StateFailed}}
			return vErr
		}
		if existing == nil || existing.privateGitDir == "" {
			view.repos[repoRel] = &initializedRepo{
				repoRel:        repoRel,
				worktreeDir:    view.worktreeDir,
				privateGitDir:  gitDir,
				leaseID:        durable.LeaseID,
				initialHeadOID: durable.InitialHeadOID,
				metrics:        InitMetrics{State: StateReady},
			}
		}
		return nil
	}

	if (existing != nil && existing.noSourceRepo) || durable.Status == repoStatusNoSourceRepo {
		view.repos[repoRel] = &initializedRepo{repoRel: repoRel, noSourceRepo: true}
		return ErrNoSourceRepository
	}

	hadPriorFailure := (existing != nil && existing.metrics.State == StateFailed) || durable.Status == repoStatusFailed
	view.repos[repoRel] = &initializedRepo{repoRel: repoRel, metrics: InitMetrics{State: StatePending}}

	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}

	repo, err := initializePrivateRepo(ctx, mgr, view, repoRel, targetWT)
	if err != nil {
		if errors.Is(err, ErrNoSourceRepository) && !hadPriorFailure {
			view.repos[repoRel] = &initializedRepo{repoRel: repoRel, noSourceRepo: true}
			writeDurableRepoRecord(mgr, view.viewID, durableRepoRecord{
				RepoRel: repoRel,
				Status:  repoStatusNoSourceRepo,
			})
			return ErrNoSourceRepository
		}
		view.repos[repoRel] = &initializedRepo{repoRel: repoRel, metrics: InitMetrics{State: StateFailed}}
		writeDurableRepoRecord(mgr, view.viewID, durableRepoRecord{
			RepoRel: repoRel,
			Status:  repoStatusFailed,
		})
		if errors.Is(err, ErrNoSourceRepository) {
			return fmt.Errorf("%w: source repository missing after failed private git initialization for %q", layeredfs.ErrGitPending, repoRel)
		}
		return err
	}
	view.repos[repoRel] = repo
	writeDurableRepoRecord(mgr, view.viewID, durableRepoRecord{
		RepoRel:        repoRel,
		Status:         repoStatusEstablished,
		PrivateGitDir:  repo.privateGitDir,
		LeaseID:        repo.leaseID,
		InitialHeadOID: repo.initialHeadOID,
	})

	if repoRel == "." {
		subRels := repo.submoduleRels
		if view.materialized && len(subRels) == 0 {
			subRels = parseSubmodulePaths(filepath.Join(targetWT, ".gitmodules"))
		}
		for _, subRel := range subRels {
			subWT := filepath.Join(view.worktreeDir, filepath.FromSlash(subRel))
			subRepo, sErr := initializePrivateRepo(ctx, mgr, view, subRel, subWT)
			if sErr == nil {
				view.repos[subRel] = subRepo
				writeDurableRepoRecord(mgr, view.viewID, durableRepoRecord{
					RepoRel:        subRel,
					Status:         repoStatusEstablished,
					PrivateGitDir:  subRepo.privateGitDir,
					LeaseID:        subRepo.leaseID,
					InitialHeadOID: subRepo.initialHeadOID,
				})
				modDst := filepath.Join(repo.privateGitDir, "modules", filepath.FromSlash(subRel))
				_ = copyMetadataTree(subRepo.privateGitDir, modDst)
			}
		}
	}
	return nil
}

// AdoptMaterializedView installs repositories a materialized seed already prepared as each worktree's
// .git and manages the view. The seed's own lease keeps retaining their base objects until it settles.
func (m *Manager) AdoptMaterializedView(ctx context.Context, b MaterializedViewBinding) error {
	if b.ViewID == "" || b.TrunkDir == "" || b.WorktreeDir == "" {
		return fmt.Errorf("privategit: ViewID, TrunkDir, and WorktreeDir are required")
	}
	absTrunk, err := filepath.Abs(b.TrunkDir)
	if err != nil {
		return err
	}
	absWorktree, err := filepath.Abs(b.WorktreeDir)
	if err != nil {
		return err
	}
	if b.RootID == "" {
		b.RootID = "primary"
	}
	view := &managedView{
		viewID: b.ViewID, workerID: b.WorkerID, rootID: b.RootID, coreViewID: b.CoreViewID,
		trunkDir: absTrunk, worktreeDir: absWorktree, materialized: true, core: b.Core, syncer: b.Core,
		accessOrigin: OriginFilesystem, repos: make(map[string]*initializedRepo, len(b.Repos)),
	}
	for _, seeded := range b.Repos {
		repoRel := cleanSlashRel(seeded.RepoRel)
		target := absWorktree
		if repoRel != "." {
			target = filepath.Join(absWorktree, filepath.FromSlash(repoRel))
		}
		finalGitDir := filepath.Join(target, ".git")
		if err := os.Rename(seeded.PrivateGitDir, finalGitDir); err != nil {
			return fmt.Errorf("privategit: install seeded repository %q: %w", repoRel, err)
		}
		if _, sErr := os.Stat(filepath.Join(finalGitDir, "index")); sErr == nil {
			out, code, rErr := gitexec.Run(ctx, target, []string{"update-index", "-q", "--refresh"}, gitexec.Opts{Profile: gitexec.ProfileHermetic})
			if (rErr != nil && code != 1) || code >= 128 || code < 0 {
				return fmt.Errorf("privategit: refresh seeded index (exit %d): %v: %s", code, rErr, strings.TrimSpace(string(out)))
			}
		}
		_, initialRefs, err := collectCapturedRefAndIndexOIDs(ctx, finalGitDir)
		if err != nil {
			return err
		}
		var initialHead string
		if out, code, hErr := gitexec.Run(ctx, target, []string{"rev-parse", "--verify", "HEAD"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); hErr == nil && code == 0 {
			initialHead = strings.TrimSpace(string(out))
		}
		metrics := seeded.Metrics
		metrics.State = StateReady
		view.repos[repoRel] = &initializedRepo{
			repoRel: repoRel, worktreeDir: target, privateGitDir: finalGitDir,
			initialHeadOID: initialHead, initialRefs: initialRefs, metrics: metrics,
		}
		writeDurableRepoRecord(m, b.ViewID, durableRepoRecord{
			RepoRel:        repoRel,
			Status:         repoStatusEstablished,
			PrivateGitDir:  finalGitDir,
			InitialHeadOID: initialHead,
		})
	}
	for _, at := range b.Installed {
		repoRel := cleanSlashRel(at.RepoRel)
		target := absWorktree
		if repoRel != "." {
			target = filepath.Join(absWorktree, filepath.FromSlash(repoRel))
		}
		finalGitDir := filepath.Join(target, ".git")
		view.repos[repoRel] = &initializedRepo{
			repoRel: repoRel, worktreeDir: target, privateGitDir: finalGitDir,
			initialHeadOID: at.InitialHead, initialRefs: at.InitialRefs, metrics: at.Metrics,
		}
		writeDurableRepoRecord(m, b.ViewID, durableRepoRecord{
			RepoRel:        repoRel,
			Status:         repoStatusEstablished,
			PrivateGitDir:  finalGitDir,
			InitialHeadOID: at.InitialHead,
		})
	}
	registerManagedView(m, view)
	return nil
}

// MaterializedReposOf reports the repositories of a materialized view in a form a later process can
// adopt again without losing what they started from.
func (m *Manager) MaterializedReposOf(viewID string) []MaterializedRepoAt {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return nil
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	out := make([]MaterializedRepoAt, 0, len(view.repos))
	for repoRel, repo := range view.repos {
		out = append(out, MaterializedRepoAt{
			RepoRel: repoRel, InitialHead: repo.initialHeadOID, InitialRefs: repo.initialRefs, Metrics: repo.metrics,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoRel < out[j].RepoRel })
	return out
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
		syncer:            cfg.Core,
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
