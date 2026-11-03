package privategit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// MountedClaimWithWorktree binds a mounted layeredfs.Core view and associates it with
// mountWorktreeDir (the active mounted worktree path, or a hardlinked projection directory
// when mountWorktreeDir is empty) without scanning or initializing repositories at claim time (#1671).
func (m *Manager) MountedClaimWithWorktree(
	viewID, workerID, rootID, trunkDir, mountWorktreeDir string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if viewID == "" || trunkDir == "" || core == nil {
		return fmt.Errorf("privategit: viewID, trunkDir, and core are required")
	}
	if rootID == "" {
		rootID = "primary"
	}
	if workerID == "" {
		workerID = viewID
	}
	absTrunk, err := filepath.Abs(trunkDir)
	if err != nil {
		return err
	}
	worktreeDir := strings.TrimSpace(mountWorktreeDir)
	if worktreeDir == "" {
		worktreeDir = filepath.Join(m.cfg.BackingDir, "views", viewID, "worktree")
	}
	absWT, err := filepath.Abs(worktreeDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absWT, 0o755); err != nil {
		return err
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

// BindMountedWorktree updates the mounted worktree directory bound to viewID (#1671).
func (m *Manager) BindMountedWorktree(viewID, mountWorktreeDir string) error {
	if strings.TrimSpace(mountWorktreeDir) == "" {
		return fmt.Errorf("privategit: mountWorktreeDir is required")
	}
	absWT, err := filepath.Abs(mountWorktreeDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absWT, 0o755); err != nil {
		return err
	}
	view, err := getManagedView(m, viewID)
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

// projectMountedWorktreeFromTrunk projects working-tree entries from srcWorktreeDir into
// targetWorktreeDir using hardlinks for regular files (0 bytes copied, 0 SHA-256 passes)
// while preserving .git metadata isolation and source index state (#1671).
func projectMountedWorktreeFromTrunk(srcWorktreeDir, targetWorktreeDir string) (map[string]uint64, error) {
	inodes := make(map[string]uint64)
	err := filepath.WalkDir(srcWorktreeDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rErr := filepath.Rel(srcWorktreeDir, path)
		if rErr != nil || rel == "." {
			return rErr
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		dstPath := filepath.Join(targetWorktreeDir, rel)
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, lErr := os.Readlink(path)
			if lErr != nil {
				return lErr
			}
			if curLink, cErr := os.Readlink(dstPath); cErr == nil && curLink == link {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(dstPath)
			return os.Symlink(link, dstPath)
		}
		if d.IsDir() {
			return os.MkdirAll(dstPath, info.Mode().Perm()|0o700)
		}
		ino := fileInode(info)
		inodes[slashRel] = ino
		return linkOrCopyTrunkFile(path, dstPath, info, ino)
	})
	if err != nil {
		return nil, err
	}
	return inodes, nil
}

func linkOrCopyTrunkFile(srcPath, dstPath string, srcInfo fs.FileInfo, srcIno uint64) error {
	if dstInfo, err := os.Lstat(dstPath); err == nil {
		if srcIno != 0 && fileInode(dstInfo) == srcIno {
			return nil
		}
		_ = os.RemoveAll(dstPath)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	if err := os.Link(srcPath, dstPath); err == nil {
		return nil
	}
	return copyWorkingTreeFile(srcPath, dstPath, srcInfo.Mode())
}

func fileInode(info fs.FileInfo) uint64 {
	if info == nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return uint64(st.Ino)
	}
	return 0
}

func syncTrunkInodesToWorktree(view *managedView) error {
	if view == nil || view.materialized {
		return nil
	}
	coreTouched := make(map[string]bool)
	if view.core != nil {
		if changes, err := view.core.EnumerateChanges(false); err == nil {
			for _, ce := range changes {
				if ce.Path.RootID == view.rootID {
					coreTouched[cleanSlashRel(ce.Path.Rel)] = true
				}
			}
		}
	}
	view.mu.Lock()
	repos := make([]*initializedRepo, 0, len(view.repos))
	for _, r := range view.repos {
		if r != nil && r.privateGitDir != "" {
			repos = append(repos, r)
		}
	}
	view.mu.Unlock()

	for _, r := range repos {
		srcDir := view.trunkDir
		if r.repoRel != "." && r.repoRel != "" {
			srcDir = filepath.Join(view.trunkDir, filepath.FromSlash(r.repoRel))
		}
		if r.trunkInodes == nil {
			r.trunkInodes = make(map[string]uint64)
		}
		_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return nil
			}
			rel, rErr := filepath.Rel(srcDir, path)
			if rErr != nil || rel == "." {
				return nil
			}
			if d.Name() == ".git" {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			slashRel := filepath.ToSlash(rel)
			fullRel := slashRel
			if r.repoRel != "." && r.repoRel != "" {
				fullRel = cleanSlashRel(r.repoRel + "/" + slashRel)
			}
			if coreTouched[fullRel] {
				return nil
			}
			info, iErr := d.Info()
			if iErr != nil {
				return nil
			}
			ino := fileInode(info)
			prevIno, seen := r.trunkInodes[slashRel]
			if !seen || (ino != 0 && prevIno != ino) {
				dstPath := filepath.Join(r.worktreeDir, rel)
				_ = linkOrCopyTrunkFile(path, dstPath, info, ino)
				r.trunkInodes[slashRel] = ino
			}
			return nil
		})
	}
	return nil
}
