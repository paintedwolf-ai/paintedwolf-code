package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// syncMaterializedTreeToCore scans the materialized seed directories and reconciles any direct
// filesystem modifications, additions, deletions, replacements, or metadata changes against the
// currently synchronized Core state while preserving original immutable seed base records.
func syncMaterializedTreeToCore(
	view *PreparedView,
	bases map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	if view == nil || view.Core == nil {
		return nil
	}
	if synced == nil {
		synced = make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture, len(bases))
		for qp, b := range bases {
			synced[qp] = cloneBaseCaptureRecord(b)
		}
	}
	for _, r := range view.Roots {
		if err := syncSingleMaterializedRoot(view.Core, r.RootID, r.WorkspacePath, bases, synced); err != nil {
			return err
		}
	}
	return nil
}

func syncSingleMaterializedRoot(
	core *layeredfs.Core,
	rootID, seedDir string,
	bases map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	// Phase 1: Unlink any previously synchronized entry (from seed or prior captures) that was
	// removed on disk or replaced with a different entry kind (or retargeted symlink), in reverse
	// depth order so children are unlinked before parent directories.
	if err := deleteRemovedOrReplacedEntries(core, rootID, seedDir, synced); err != nil {
		return err
	}

	// Phase 2: Walk current on-disk tree top-down to apply additions, replacements, content
	// modifications, reverts, and metadata changes relative to the synchronized Core state.
	return filepath.WalkDir(seedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(seedDir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		qp := layeredfs.QualifiedPath{RootID: rootID, Rel: relSlash}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		base, hasBase := bases[qp]
		switch {
		case info.IsDir():
			return syncDirEntry(core, qp, p, info, synced)
		case info.Mode()&os.ModeSymlink != 0:
			return syncSymlinkEntry(core, qp, p, info, synced)
		case info.Mode().IsRegular():
			return syncRegularEntry(core, qp, p, info, base, hasBase, synced)
		default:
			return nil
		}
	})
}

func deleteRemovedOrReplacedEntries(
	core *layeredfs.Core,
	rootID, seedDir string,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	type removalCandidate struct {
		qp    layeredfs.QualifiedPath
		kind  layeredfs.EntryKind
		depth int
	}
	marked := make(map[layeredfs.QualifiedPath]layeredfs.EntryKind)
	removedDirs := make([]string, 0)

	for qp, cur := range synced {
		if qp.RootID != rootID {
			continue
		}
		full := filepath.Join(seedDir, filepath.FromSlash(qp.Rel))
		info, err := os.Lstat(full)
		needRemove := false
		if err != nil {
			if isMissingOnDisk(err) {
				needRemove = true
			} else {
				return err
			}
		} else if !entryKindMatchesMode(cur.Kind, info.Mode()) {
			needRemove = true
		} else if cur.Kind == layeredfs.EntrySymlink {
			target, linkErr := os.Readlink(full)
			if linkErr != nil {
				return linkErr
			}
			if target != cur.LinkTarget {
				needRemove = true
			}
		}
		if needRemove {
			marked[qp] = cur.Kind
			if cur.Kind == layeredfs.EntryDirectory {
				removedDirs = append(removedDirs, qp.Rel+"/")
			}
		}
	}

	if len(removedDirs) > 0 {
		for qp, cur := range synced {
			if qp.RootID != rootID {
				continue
			}
			if _, already := marked[qp]; already {
				continue
			}
			for _, prefix := range removedDirs {
				if strings.HasPrefix(qp.Rel, prefix) {
					marked[qp] = cur.Kind
					break
				}
			}
		}
	}

	candidates := make([]removalCandidate, 0, len(marked))
	for qp, kind := range marked {
		candidates = append(candidates, removalCandidate{
			qp:    qp,
			kind:  kind,
			depth: strings.Count(qp.Rel, "/"),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].depth != candidates[j].depth {
			return candidates[i].depth > candidates[j].depth
		}
		return candidates[i].qp.Rel > candidates[j].qp.Rel
	})
	for _, c := range candidates {
		rmdir := c.kind == layeredfs.EntryDirectory
		if err := core.Unlink(c.qp, rmdir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(synced, c.qp)
	}
	return nil
}

func syncDirEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	cur, hasSynced := synced[qp]
	if !hasSynced || cur.Kind != layeredfs.EntryDirectory {
		if _, err := core.Mkdir(qp, info.Mode().Perm()); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		synced[qp] = layeredfs.BaseCapture{
			Path: qp,
			Kind: layeredfs.EntryDirectory,
			Mode: fs.ModeDir | info.Mode().Perm(),
		}
	}
	return syncEntryMetadataIfChanged(core, qp, fullPath, info, synced)
}

func syncSymlinkEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	target, err := os.Readlink(fullPath)
	if err != nil {
		return err
	}
	cur, hasSynced := synced[qp]
	if !hasSynced || cur.Kind != layeredfs.EntrySymlink || cur.LinkTarget != target {
		if _, err := core.Symlink(target, qp); err != nil {
			return err
		}
		synced[qp] = layeredfs.BaseCapture{
			Path:       qp,
			Kind:       layeredfs.EntrySymlink,
			Mode:       fs.ModeSymlink | 0o777,
			Size:       int64(len(target)),
			LinkTarget: target,
		}
	}
	return syncEntryMetadataIfChanged(core, qp, fullPath, info, synced)
}

func syncRegularEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	base layeredfs.BaseCapture,
	hasBase bool,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	diskSHA := hex.EncodeToString(sum[:])

	cur, hasSynced := synced[qp]
	wasDifferentFromBase := !hasSynced ||
		cur.Kind != layeredfs.EntryRegular ||
		cur.SHA256 != base.SHA256 ||
		cur.Mode.Perm() != base.Mode.Perm() ||
		!xattrsEqual(cur.Xattrs, base.Xattrs)

	if !hasSynced || cur.Kind != layeredfs.EntryRegular || diskSHA != cur.SHA256 {
		if _, err := core.WriteFile(qp, raw, info.Mode().Perm()); err != nil {
			return err
		}
		if !hasSynced || cur.Kind != layeredfs.EntryRegular {
			cur = layeredfs.BaseCapture{
				Path:   qp,
				Kind:   layeredfs.EntryRegular,
				Mode:   info.Mode().Perm(),
				Size:   int64(len(raw)),
				SHA256: diskSHA,
			}
		} else {
			cur.Size = int64(len(raw))
			cur.SHA256 = diskSHA
		}
		synced[qp] = cur
	}

	if err := syncEntryMetadataIfChanged(core, qp, fullPath, info, synced); err != nil {
		return err
	}

	after := synced[qp]
	matchesSeedBase := hasBase &&
		base.Kind == layeredfs.EntryRegular &&
		after.SHA256 == base.SHA256 &&
		after.Mode.Perm() == base.Mode.Perm() &&
		xattrsEqual(after.Xattrs, base.Xattrs)
	if matchesSeedBase && wasDifferentFromBase {
		if _, err := core.Chtimes(qp, base.ModTime, base.ModTime, true); err != nil {
			return err
		}
		after.ModTime = base.ModTime
		synced[qp] = after
	}
	return nil
}

func syncEntryMetadataIfChanged(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	diskXattrs, err := readPathXattrs(fullPath)
	if err != nil {
		return err
	}
	cur := synced[qp]
	if info.Mode()&os.ModeSymlink == 0 && (info.Mode().Perm() != cur.Mode.Perm() || info.Mode().Perm() == 0) {
		if _, err := core.Chmod(qp, info.Mode().Perm()); err != nil {
			return err
		}
		cur.Mode = (cur.Mode & fs.ModeType) | info.Mode().Perm()
		synced[qp] = cur
	}
	if !xattrsEqual(diskXattrs, cur.Xattrs) {
		if err := applyXattrDiff(core, qp, diskXattrs); err != nil {
			return err
		}
		cur.Xattrs = cloneXattrMap(diskXattrs)
		synced[qp] = cur
	}
	return nil
}
