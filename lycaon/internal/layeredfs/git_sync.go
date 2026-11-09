package layeredfs

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type gitOverlayItem struct {
	qp   QualifiedPath
	node *privateNode
}

func matchRepoGitMetadataRel(rel, repoRel string) (subRel string, isRepoRoot, ok bool) {
	rel = cleanRelPath(rel)
	repoRel = cleanRelPath(repoRel)
	gitPrefix := ".git"
	if repoRel != "" && repoRel != "." {
		gitPrefix = repoRel + "/.git"
	}
	if rel == gitPrefix {
		return ".", true, true
	}
	if strings.HasPrefix(rel, gitPrefix+"/") {
		return strings.TrimPrefix(rel, gitPrefix+"/"), false, true
	}
	return "", false, false
}

// SyncGitOverlayTo flushes and materializes any private upper-layer `.git` mutations
// under (rootID, repoRel) into privGitDir, removing them from Core's private overlay so
// privGitDir remains the single authority for private Git state.
func (c *Core) SyncGitOverlayTo(rootID, repoRel, privGitDir string) error {
	if c == nil || privGitDir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.released {
		return nil
	}
	if rootID == "" {
		rootID = c.roots.activeRootID
	}

	for _, h := range c.state.handles {
		if h == nil || h.closed || h.file == nil {
			continue
		}
		if _, _, ok := matchRepoGitMetadataRel(h.qpath.Rel, repoRel); h.qpath.RootID == rootID && ok {
			_ = syncFD(int(h.file.Fd()))
		}
	}

	for qp := range c.state.deletedPaths {
		subRel, isRepoRoot, ok := matchRepoGitMetadataRel(qp.Rel, repoRel)
		if qp.RootID != rootID || !ok {
			continue
		}
		if !isRepoRoot {
			_ = os.RemoveAll(filepath.Join(privGitDir, filepath.FromSlash(subRel)))
		}
		delete(c.state.deletedPaths, qp)
		delete(c.state.transient, qp)
		if base, ok := c.state.basesByPath[qp]; ok {
			if base.BackingFile != "" && c.backing.baseDirFile != nil {
				_ = unlinkBackingFile(int(c.backing.baseDirFile.Fd()), filepath.Base(base.BackingFile))
				delete(c.state.dirtyBackingFiles, base.BackingFile)
			}
			delete(c.state.basesByPath, qp)
		}
	}

	var items []gitOverlayItem
	for qp, nid := range c.state.pathIndex {
		if _, _, ok := matchRepoGitMetadataRel(qp.Rel, repoRel); qp.RootID != rootID || !ok {
			continue
		}
		items = append(items, gitOverlayItem{
			qp:   qp,
			node: c.state.nodesByID[nid],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if len(items[i].qp.Rel) != len(items[j].qp.Rel) {
			return len(items[i].qp.Rel) < len(items[j].qp.Rel)
		}
		return items[i].qp.Rel < items[j].qp.Rel
	})

	for _, item := range items {
		qp := item.qp
		node := item.node
		if node == nil {
			delete(c.state.pathIndex, qp)
			continue
		}
		subRel, isRepoRoot, _ := matchRepoGitMetadataRel(qp.Rel, repoRel)
		if isRepoRoot {
			delete(c.state.pathIndex, qp)
			delete(c.state.transient, qp)
			if base, ok := c.state.basesByPath[qp]; ok {
				if base.BackingFile != "" && c.backing.baseDirFile != nil {
					_ = unlinkBackingFile(int(c.backing.baseDirFile.Fd()), filepath.Base(base.BackingFile))
					delete(c.state.dirtyBackingFiles, base.BackingFile)
				}
				delete(c.state.basesByPath, qp)
			}
			delete(node.paths, qp)
			if len(node.paths) == 0 {
				node.flags.unlinked = true
				reclaimNodeLocked(c, node)
			}
			continue
		}

		target := filepath.Join(privGitDir, filepath.FromSlash(subRel))
		switch node.kind {
		case EntryDirectory:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case EntrySymlink:
			_ = os.RemoveAll(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(node.flags.linkTarget, target); err != nil {
				return err
			}
		default:
			if node.flags.backingPath != "" {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				perm := node.mode.Perm()
				if perm == 0 {
					perm = 0o644
				}
				if err := copyGitOverlayFile(node.flags.backingPath, target, perm); err != nil {
					return err
				}
			}
		}

		delete(c.state.pathIndex, qp)
		delete(c.state.transient, qp)
		if base, ok := c.state.basesByPath[qp]; ok {
			if base.BackingFile != "" && c.backing.baseDirFile != nil {
				_ = unlinkBackingFile(int(c.backing.baseDirFile.Fd()), filepath.Base(base.BackingFile))
				delete(c.state.dirtyBackingFiles, base.BackingFile)
			}
			delete(c.state.basesByPath, qp)
		}
		delete(node.paths, qp)
		if len(node.paths) == 0 {
			node.flags.unlinked = true
			if !hasOpenHandlesForNodeLocked(c, node.id) {
				if node.flags.backingName != "" && c.backing.workDirFile != nil {
					_ = unlinkBackingFile(int(c.backing.workDirFile.Fd()), node.flags.backingName)
				} else if node.flags.backingPath != "" {
					_ = os.Remove(node.flags.backingPath)
				}
				if node.flags.backingPath != "" {
					delete(c.state.dirtyBackingFiles, node.flags.backingPath)
				}
				reclaimNodeLocked(c, node)
			}
		}
	}
	return nil
}

func copyGitOverlayFile(src, dst string, perm fs.FileMode) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	_ = os.RemoveAll(dst)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(dst, perm)
}
