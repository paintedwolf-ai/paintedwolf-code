package privategit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// MountedClaimWithWorktree binds a mounted layeredfs.Core view and associates it with
// an already-mounted host-owned workspace directory (mountWorktreeDir) without scanning,
// copying, or hardlinking trunk repositories at claim time (#1671).
func (m *Manager) MountedClaimWithWorktree(
	viewID, workerID, rootID, trunkDir, mountWorktreeDir string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if viewID == "" || trunkDir == "" || core == nil {
		return fmt.Errorf("privategit: viewID, trunkDir, and core are required")
	}
	absTrunk, err := filepath.Abs(trunkDir)
	if err != nil {
		return err
	}
	absWT, err := validateMountedWorktreeDir(absTrunk, mountWorktreeDir)
	if err != nil {
		return err
	}
	return registerMountedClaim(m, viewID, workerID, rootID, absTrunk, absWT, core, authorizedGitDirs...)
}

func registerMountedClaim(
	m *Manager,
	viewID, workerID, rootID, absTrunk, absWT string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if rootID == "" {
		rootID = "primary"
	}
	if workerID == "" {
		workerID = viewID
	}
	view := &managedView{
		viewID:            viewID,
		workerID:          workerID,
		rootID:            rootID,
		trunkDir:          absTrunk,
		worktreeDir:       absWT,
		materialized:      false,
		core:              core,
		authorizedGitDirs: append([]string(nil), authorizedGitDirs...),
		accessOrigin:      OriginFilesystem,
		repos:             make(map[string]*initializedRepo),
	}
	registerManagedView(m, view)

	return core.RegisterLowerProvider(rootID, &gitAwareLowerProvider{
		mgr:      m,
		viewID:   viewID,
		rootID:   rootID,
		trunkDir: absTrunk,
	})
}

// BindMountedWorktree binds or updates the active host-owned mounted workspace directory
// for viewID after verifying that it is mounted and isolated from raw trunk inodes (#1671).
func (m *Manager) BindMountedWorktree(viewID, mountWorktreeDir string) error {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return err
	}
	absWT, err := validateMountedWorktreeDir(view.trunkDir, mountWorktreeDir)
	if err != nil {
		return err
	}
	unregisterActiveView(view)
	m.mu.Lock()
	if oldWT := canonicalCleanPath(view.worktreeDir); oldWT != "" {
		delete(m.viewsByWorktree, oldWT)
	}
	view.worktreeDir = absWT
	if newWT := canonicalCleanPath(absWT); newWT != "" {
		m.viewsByWorktree[newWT] = view
	}
	m.mu.Unlock()
	registerActiveView(m, view)
	return nil
}

func validateMountedWorktreeDir(trunkDir, mountWorktreeDir string) (string, error) {
	if strings.TrimSpace(mountWorktreeDir) == "" {
		return "", fmt.Errorf("%w: mounted worktree path is empty", ErrMountedWorktreeUnavailable)
	}
	absWT, err := filepath.Abs(mountWorktreeDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	wtInfo, err := os.Lstat(absWT)
	if err != nil || !wtInfo.IsDir() {
		return "", fmt.Errorf("%w: %q is not an accessible mounted directory", ErrMountedWorktreeUnavailable, absWT)
	}
	cleanWT := canonicalCleanPath(absWT)
	cleanTrunk := canonicalCleanPath(trunkDir)
	if cleanWT == "" || cleanTrunk == "" {
		return "", fmt.Errorf("%w: invalid trunk or mounted worktree path", ErrMountedWorktreeUnavailable)
	}
	if cleanWT == cleanTrunk || isUnderAny(cleanWT, []string{filepath.Join(cleanTrunk, ".git")}) {
		return "", fmt.Errorf("%w: mounted worktree %q aliases canonical trunk %q", ErrMountedWorktreeUnavailable, absWT, trunkDir)
	}

	trunkEntries, err := os.ReadDir(cleanTrunk)
	if err != nil {
		return "", fmt.Errorf("%w: read trunk dir: %v", ErrMountedWorktreeUnavailable, err)
	}
	wtEntries, err := os.ReadDir(absWT)
	if err != nil {
		return "", fmt.Errorf("%w: read mounted worktree dir: %v", ErrMountedWorktreeUnavailable, err)
	}

	wtNonGit := 0
	for _, we := range wtEntries {
		if we.Name() != ".git" {
			wtNonGit++
		}
	}
	trunkNonGit := 0
	for _, te := range trunkEntries {
		if te.Name() == ".git" {
			continue
		}
		trunkNonGit++
		mInfo, mErr := os.Lstat(filepath.Join(absWT, te.Name()))
		if mErr != nil {
			continue
		}
		tInfo, tErr := te.Info()
		if tErr == nil && !tInfo.IsDir() && !mInfo.IsDir() && os.SameFile(tInfo, mInfo) {
			return "", fmt.Errorf("%w: mounted worktree %q shares raw trunk inode for %q", ErrMountedWorktreeUnavailable, absWT, te.Name())
		}
	}
	if trunkNonGit > 0 && wtNonGit == 0 {
		return "", fmt.Errorf("%w: mounted worktree %q is unmounted or unpopulated", ErrMountedWorktreeUnavailable, absWT)
	}
	return absWT, nil
}
