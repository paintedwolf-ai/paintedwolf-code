package privategit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

type initializedRepo struct {
	repoRel        string
	worktreeDir    string
	privateGitDir  string
	leaseID        string
	initialHeadOID string
	initialRefs    map[string]string
	metrics        InitMetrics
}

func initializePrivateRepo(
	ctx context.Context,
	mgr *Manager,
	view *managedView,
	repoRel string,
	targetWorktreeDir string,
) (*initializedRepo, error) {
	start := time.Now()
	forbidden := forbiddenTargetDirs(mgr, view.viewID)
	layout, err := resolveSourceRepoLayout(view.trunkDir, repoRel, view.authorizedGitDirs, forbidden)
	if err != nil {
		return nil, err
	}
	if !view.materialized {
		if vErr := verifyActiveMountedView(mgr, view, layout.worktreeDir, targetWorktreeDir); vErr != nil {
			return nil, fmt.Errorf("%w: %w", layeredfs.ErrGitPending, vErr)
		}
	}

	stageBase := filepath.Join(mgr.cfg.BackingDir, "views", view.viewID, "git-staging")
	if err := os.MkdirAll(stageBase, 0o755); err != nil {
		return nil, err
	}
	stageGitDir, err := os.MkdirTemp(stageBase, "repo-*")
	if err != nil {
		return nil, err
	}
	cleanupStage := func() { _ = os.RemoveAll(stageGitDir) }

	maxRetries := mgr.cfg.MaxCaptureRetries
	if maxRetries <= 0 {
		maxRetries = 4
	}
	var captured bool
	var attempts int
	for attempt := 0; attempt < maxRetries; attempt++ {
		attempts = attempt + 1
		_ = os.RemoveAll(stageGitDir)
		if err := os.MkdirAll(stageGitDir, 0o755); err != nil {
			cleanupStage()
			return nil, err
		}
		coherent, cErr := tryCaptureSourceMetadataOnce(layout, stageGitDir, attempt, mgr.cfg.CaptureAttemptHook)
		if cErr != nil {
			cleanupStage()
			return nil, cErr
		}
		if coherent {
			captured = true
			break
		}
	}
	if !captured {
		cleanupStage()
		return nil, fmt.Errorf("%w: source git metadata changed during capture", layeredfs.ErrGitPending)
	}

	tipOIDs, initRefs, err := collectCapturedRefAndIndexOIDs(ctx, stageGitDir)
	if err != nil {
		cleanupStage()
		return nil, fmt.Errorf("%w: %w", layeredfs.ErrGitPending, err)
	}
	if mgr.cfg.FailObjectImportHook != nil {
		if hErr := mgr.cfg.FailObjectImportHook(view.viewID, repoRel); hErr != nil {
			cleanupStage()
			return nil, fmt.Errorf("%w: %w", layeredfs.ErrGitPending, hErr)
		}
	}

	importMetrics, reachableOIDs, err := mgr.store.ImportRepositoryObjects(ctx, layout.objectDirs, tipOIDs)
	if err != nil {
		cleanupStage()
		return nil, fmt.Errorf("%w: %w", layeredfs.ErrGitPending, err)
	}

	leaseID, err := mgr.store.RetainForView(view.viewID+":"+repoRel, reachableOIDs)
	if err != nil {
		cleanupStage()
		return nil, err
	}

	altPath := filepath.Join(stageGitDir, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(altPath), 0o755); err != nil {
		_ = mgr.store.ReleaseLease(leaseID)
		cleanupStage()
		return nil, err
	}
	if err := os.WriteFile(altPath, []byte(mgr.store.ObjectsDir()+"\n"), 0o644); err != nil {
		_ = mgr.store.ReleaseLease(leaseID)
		cleanupStage()
		return nil, err
	}

	kernelMounted := !view.materialized && view.kernelMounted
	finalGitDir := filepath.Join(targetWorktreeDir, ".git")
	if kernelMounted {
		finalGitDir = filepath.Join(mgr.cfg.BackingDir, "views", view.viewID, "private-git", sanitizeRepoKey(repoRel))
	}
	if err := os.MkdirAll(filepath.Dir(finalGitDir), 0o755); err != nil {
		_ = mgr.store.ReleaseLease(leaseID)
		cleanupStage()
		return nil, err
	}
	_ = os.RemoveAll(finalGitDir)
	if err := os.Rename(stageGitDir, finalGitDir); err != nil {
		_ = mgr.store.ReleaseLease(leaseID)
		cleanupStage()
		return nil, err
	}
	abortFinal := func(retErr error) (*initializedRepo, error) {
		_ = mgr.store.ReleaseLease(leaseID)
		_ = os.RemoveAll(finalGitDir)
		return nil, retErr
	}

	if layout.isLinkedWT && layout.worktreeName != "" {
		if err := setupPrivateLinkedWorktreeMetadata(finalGitDir, targetWorktreeDir, layout.worktreeName); err != nil {
			return abortFinal(fmt.Errorf("%w: %v", layeredfs.ErrGitPending, err))
		}
	}

	if !kernelMounted {
		if _, sErr := os.Stat(filepath.Join(finalGitDir, "index")); sErr == nil {
			uOut, uCode, uErr := gitexec.Run(ctx, targetWorktreeDir, []string{"update-index", "-q", "--refresh"}, gitexec.Opts{
				Profile: gitexec.ProfileHermetic,
			})
			if (uErr != nil && uCode != 1) || uCode >= 128 || uCode < 0 {
				return abortFinal(fmt.Errorf("%w: update-index refresh failed (exit %d): %v: %s", layeredfs.ErrGitPending, uCode, uErr, strings.TrimSpace(string(uOut))))
			}
		}
	}

	var initHeadOID string
	revDir := targetWorktreeDir
	revArgs := []string{"rev-parse", "--verify", "HEAD"}
	if kernelMounted {
		revDir = filepath.Dir(finalGitDir)
		revArgs = []string{"--git-dir=" + finalGitDir, "rev-parse", "--verify", "HEAD"}
	}
	if hOut, hCode, hErr := gitexec.Run(ctx, revDir, revArgs, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	}); hErr == nil && hCode == 0 {
		initHeadOID = strings.TrimSpace(string(hOut))
	}

	importMetrics.Attempts = attempts
	importMetrics.Duration = time.Since(start)
	importMetrics.State = StateReady
	return &initializedRepo{
		repoRel:        repoRel,
		worktreeDir:    targetWorktreeDir,
		privateGitDir:  finalGitDir,
		leaseID:        leaseID,
		initialHeadOID: initHeadOID,
		initialRefs:    initRefs,
		metrics:        importMetrics,
	}, nil
}

func isKernelMountedWorktree(worktreeDir string) bool {
	if strings.TrimSpace(worktreeDir) == "" {
		return false
	}
	wtInfo, err := os.Lstat(worktreeDir)
	if err != nil {
		return false
	}
	parentInfo, pErr := os.Lstat(filepath.Dir(worktreeDir))
	if pErr != nil {
		return false
	}
	wtDev := fileInfoToLowerEntry(wtInfo).Dev
	parentDev := fileInfoToLowerEntry(parentInfo).Dev
	return wtDev != 0 && parentDev != 0 && wtDev != parentDev
}

func sanitizeRepoKey(repoRel string) string {
	clean := cleanSlashRel(repoRel)
	if clean == "." || clean == "" {
		return "root"
	}
	return strings.ReplaceAll(clean, "/", "__")
}

func retainNewPrivateObjects(ctx context.Context, mgr *Manager, view *managedView, repoRel string) error {
	if mgr == nil || view == nil {
		return nil
	}
	repoRel = cleanSlashRel(repoRel)
	if mgr.cfg.FailObjectRetentionHook != nil {
		if hErr := mgr.cfg.FailObjectRetentionHook(view.viewID, repoRel); hErr != nil {
			return fmt.Errorf("%w: %w", ErrCorruptObjectStore, hErr)
		}
	}
	view.mu.Lock()
	repo := view.repos[repoRel]
	view.mu.Unlock()
	if repo == nil || repo.privateGitDir == "" {
		return nil
	}
	tipOIDs, _, err := collectCapturedRefAndIndexOIDs(ctx, repo.privateGitDir)
	if err != nil {
		return err
	}
	if len(tipOIDs) == 0 {
		return nil
	}
	objDirs := []string{filepath.Join(repo.privateGitDir, "objects"), mgr.store.ObjectsDir()}
	_, reachable, err := mgr.store.ImportRepositoryObjects(ctx, objDirs, tipOIDs)
	if err != nil {
		return err
	}
	newLeaseID, err := mgr.store.RetainForView(view.viewID+":"+repoRel, reachable)
	if err != nil {
		return err
	}
	view.mu.Lock()
	oldLease := repo.leaseID
	repo.leaseID = newLeaseID
	view.mu.Unlock()
	if oldLease != "" && oldLease != newLeaseID {
		_ = mgr.store.ReleaseLease(oldLease)
	}
	return nil
}
