package privategit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

type initializedRepo struct {
	repoRel       string
	worktreeDir   string
	privateGitDir string
	leaseID       string
	initialRefs   map[string]string
	metrics       InitMetrics
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
		return nil, fmt.Errorf("%w: %v", layeredfs.ErrGitPending, err)
	}
	if mgr.cfg.FailObjectImportHook != nil {
		if hErr := mgr.cfg.FailObjectImportHook(view.viewID, repoRel); hErr != nil {
			cleanupStage()
			return nil, fmt.Errorf("%w: %v", layeredfs.ErrGitPending, hErr)
		}
	}

	importMetrics, reachableOIDs, err := mgr.store.ImportRepositoryObjects(ctx, layout.objectDirs, tipOIDs)
	if err != nil {
		cleanupStage()
		return nil, fmt.Errorf("%w: %v", layeredfs.ErrGitPending, err)
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

	finalGitDir := filepath.Join(targetWorktreeDir, ".git")
	if err := os.MkdirAll(targetWorktreeDir, 0o755); err != nil {
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

	if layout.isLinkedWT && layout.worktreeName != "" {
		_ = setupPrivateLinkedWorktreeMetadata(finalGitDir, targetWorktreeDir, layout.worktreeName)
	}

	// Refresh stat cache in private index without modifying staged/unstaged blob OIDs.
	_, _, _ = gitexec.Run(ctx, targetWorktreeDir, []string{"update-index", "-q", "--refresh"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	})

	importMetrics.Attempts = attempts
	importMetrics.Duration = time.Since(start)
	importMetrics.State = StateReady
	return &initializedRepo{
		repoRel:       repoRel,
		worktreeDir:   targetWorktreeDir,
		privateGitDir: finalGitDir,
		leaseID:       leaseID,
		initialRefs:   initRefs,
		metrics:       importMetrics,
	}, nil
}
