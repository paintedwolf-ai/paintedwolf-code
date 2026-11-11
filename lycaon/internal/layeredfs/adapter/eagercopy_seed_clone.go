package adapter

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// Where the volume clones whole directories, each top-level child of a root is cloned in one call,
// the pristine tree is cloned from that clone, and the walk that builds the stat manifest adopts what
// the clones made instead of creating it. Both paths share one repair, because a clone and a
// per-file copy both drop ACLs and hard-link identity: the walk copies each entry's ACL and links
// every later name of a source file to the first one's copy.

// DirectoryCloner clones a whole tree, reporting false where the volume cannot.
type DirectoryCloner func(src, dst string) (bool, error)

type seedFileID struct{ dev, ino uint64 }

type seededLink struct{ seed, pristine string }

// cloneChildren clones each top-level child of srcRoot into both trees and names those it cloned.
// A child it cannot clone is seeded entry by entry; a volume that cannot clone stops the attempts.
func (a *seedAccumulator) cloneChildren(srcRoot, seedDir, pristineDir string) (map[string]bool, error) {
	if a.dirClone == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return nil, nil
	}
	cloned := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		src := filepath.Join(srcRoot, name)
		if name == ".git" || name == ".lycaon" || (entry.IsDir() && (a.hidden(src) || unlistable(entry))) {
			continue
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			continue
		}
		seed, pristine := filepath.Join(seedDir, name), filepath.Join(pristineDir, name)
		ok, err := a.dirClone(src, seed)
		if err != nil || !ok {
			if err == nil {
				return cloned, nil
			}
			continue
		}
		if ok, err := a.dirClone(seed, pristine); err != nil || !ok {
			if rmErr := removeClonedTree(seed); rmErr != nil {
				return cloned, rmErr
			}
			continue
		}
		cloned[name] = true
		if entry.IsDir() {
			a.out.stats.ClonedDirectories++
		}
	}
	return cloned, nil
}

// clonedChild reports whether rel lies under a child the clones made.
func (a *seedAccumulator) clonedChild(rel string) bool {
	top, _, _ := strings.Cut(rel, "/")
	return a.cloned[top]
}

// adoptClonedEntry records what the clones made for one source entry, settling it as a per-file seed
// would: an unreadable file and an entry of another type are taken out, the working tree keeps the
// source modes, and the pristine tree is read-only files in traversable directories.
func (a *seedAccumulator) adoptClonedEntry(rootID, rel, srcPath, dstPath, pristinePath string) error {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		if err := a.chargeEntries(rootID, 1); err != nil {
			return err
		}
		for path, perm := range map[string]fs.FileMode{dstPath: mode.Perm() | 0o700, pristinePath: 0o700} {
			if err := os.Chmod(path, perm); err != nil {
				return err
			}
		}
		if err := a.repairEntry(srcPath, dstPath, pristinePath, info); err != nil {
			return err
		}
		a.seededDirs = append(a.seededDirs, seededDirMode{dstPath: dstPath, rootID: rootID, rel: rel, perm: mode.Perm(), modTime: info.ModTime()})
		return nil
	case mode&os.ModeSymlink != 0:
		if err := a.chargeEntries(rootID, 1); err != nil {
			return err
		}
		if err := a.repairEntry(srcPath, dstPath, pristinePath, info); err != nil {
			return err
		}
		final, err := os.Lstat(dstPath)
		if err != nil {
			return err
		}
		rec := recordStat(rootID, rel, layeredfs.EntrySymlink, final)
		if rec.LinkTarget, err = os.Readlink(dstPath); err != nil {
			return err
		}
		a.out.records = append(a.out.records, rec)
		return nil
	case mode.IsRegular() && mode.Perm()&0o400 != 0:
		if err := a.chargeEntries(rootID, 1); err != nil {
			return err
		}
		size := max(info.Size(), 0)
		a.out.logicalBytes += size
		a.out.stats.ClonedFiles++
		a.out.stats.ClonedBytes += size
		if err := os.Chmod(pristinePath, 0o400); err != nil {
			return err
		}
		if err := a.repairEntry(srcPath, dstPath, pristinePath, info); err != nil {
			return err
		}
		final, err := os.Lstat(dstPath)
		if err != nil {
			return err
		}
		a.out.records = append(a.out.records, recordStat(rootID, rel, layeredfs.EntryRegular, final))
		return nil
	default:
		if mode.IsRegular() {
			a.out.stats.UnreadableEntries++
		}
		return a.dropCloned(dstPath, pristinePath)
	}
}

// dropCloned takes an entry the clones made out of both trees.
func (a *seedAccumulator) dropCloned(dstPath, pristinePath string) error {
	if err := removeClonedTree(dstPath); err != nil {
		return err
	}
	return removeClonedTree(pristinePath)
}

// removeClonedTree removes a cloned entry, opening directories its mode would keep closed.
func removeClonedTree(path string) error {
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// repairEntry restores what a clone or a copy leaves behind: the entry's ACL, and the identity a
// source file shares with its other names.
func (a *seedAccumulator) repairEntry(srcPath, dstPath, pristinePath string, info fs.FileInfo) error {
	for _, path := range []string{dstPath, pristinePath} {
		if err := fileclone.CopyACL(srcPath, path); err != nil {
			return err
		}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink < 2 {
		return nil
	}
	id := seedFileID{dev: uint64(st.Dev), ino: uint64(st.Ino)}
	first, seen := a.links[id]
	if !seen {
		a.links[id] = seededLink{seed: dstPath, pristine: pristinePath}
		return nil
	}
	for _, pair := range [][2]string{{first.seed, dstPath}, {first.pristine, pristinePath}} {
		if err := os.Remove(pair[1]); err != nil {
			return err
		}
		if err := os.Link(pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}
