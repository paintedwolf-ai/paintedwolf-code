package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// syncMaterializedTreeToCore scans the materialized seed directories and records any direct
// filesystem modifications, additions, deletions, replacements, or metadata changes into Core's journal.
func syncMaterializedTreeToCore(view *PreparedView, bases map[layeredfs.QualifiedPath]layeredfs.BaseCapture) error {
	if view == nil || view.Core == nil {
		return nil
	}
	for _, r := range view.Roots {
		if err := syncSingleMaterializedRoot(view.Core, r.RootID, r.WorkspacePath, bases); err != nil {
			return err
		}
	}
	return nil
}

func syncSingleMaterializedRoot(
	core *layeredfs.Core,
	rootID, seedDir string,
	bases map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	// Phase 1: Unlink any seeded entry that was removed on disk or replaced with a different
	// entry kind (or retargeted symlink), in reverse depth order (children before parents).
	if err := deleteRemovedOrReplacedEntries(core, rootID, seedDir, bases); err != nil {
		return err
	}

	// Phase 2: Walk current on-disk tree top-down to apply additions, replacements, content
	// modifications, and metadata changes.
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
			return syncDirEntry(core, qp, p, info, base, hasBase)
		case info.Mode()&os.ModeSymlink != 0:
			return syncSymlinkEntry(core, qp, p, base, hasBase)
		case info.Mode().IsRegular():
			return syncRegularEntry(core, qp, p, info, base, hasBase)
		default:
			return nil
		}
	})
}

func deleteRemovedOrReplacedEntries(
	core *layeredfs.Core,
	rootID, seedDir string,
	bases map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	type removalCandidate struct {
		qp    layeredfs.QualifiedPath
		kind  layeredfs.EntryKind
		depth int
	}
	var candidates []removalCandidate
	for qp, base := range bases {
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
		} else if !entryKindMatchesMode(base.Kind, info.Mode()) {
			needRemove = true
		} else if base.Kind == layeredfs.EntrySymlink {
			target, linkErr := os.Readlink(full)
			if linkErr != nil {
				return linkErr
			}
			if target != base.LinkTarget {
				needRemove = true
			}
		}
		if needRemove {
			candidates = append(candidates, removalCandidate{
				qp:    qp,
				kind:  base.Kind,
				depth: strings.Count(qp.Rel, "/"),
			})
		}
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
	}
	return nil
}

func isMissingOnDisk(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR)
}

func entryKindMatchesMode(kind layeredfs.EntryKind, mode fs.FileMode) bool {
	switch kind {
	case layeredfs.EntryDirectory:
		return mode.IsDir()
	case layeredfs.EntrySymlink:
		return mode&os.ModeSymlink != 0
	case layeredfs.EntryRegular:
		return mode.IsRegular()
	default:
		return false
	}
}

func syncDirEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	base layeredfs.BaseCapture,
	hasBase bool,
) error {
	if !hasBase || base.Kind != layeredfs.EntryDirectory {
		if _, err := core.Mkdir(qp, info.Mode().Perm()); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		return syncEntryMetadataIfChanged(core, qp, fullPath, info, layeredfs.BaseCapture{}, false)
	}
	return syncEntryMetadataIfChanged(core, qp, fullPath, info, base, true)
}

func syncSymlinkEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	base layeredfs.BaseCapture,
	hasBase bool,
) error {
	target, err := os.Readlink(fullPath)
	if err != nil {
		return err
	}
	if hasBase && base.Kind == layeredfs.EntrySymlink && base.LinkTarget == target {
		return nil
	}
	_, err = core.Symlink(target, qp)
	return err
}

func syncRegularEntry(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	base layeredfs.BaseCapture,
	hasBase bool,
) error {
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	if hasBase && base.Kind == layeredfs.EntryRegular {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) == base.SHA256 {
			return syncEntryMetadataIfChanged(core, qp, fullPath, info, base, true)
		}
	}
	if _, err := core.WriteFile(qp, raw, info.Mode().Perm()); err != nil {
		return err
	}
	return syncEntryMetadataIfChanged(core, qp, fullPath, info, base, hasBase && base.Kind == layeredfs.EntryRegular)
}

func syncEntryMetadataIfChanged(
	core *layeredfs.Core,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	base layeredfs.BaseCapture,
	compareBase bool,
) error {
	if compareBase && info.Mode().Perm() != base.Mode.Perm() {
		if _, err := core.Chmod(qp, info.Mode().Perm()); err != nil {
			return err
		}
	}
	diskXattrs, _ := readPathXattrs(fullPath)
	if compareBase && xattrsEqual(diskXattrs, base.Xattrs) {
		return nil
	}
	if !compareBase && len(diskXattrs) == 0 {
		return nil
	}
	existingNames, err := core.ListXattr(qp, false)
	if err != nil {
		return err
	}
	existingSet := make(map[string]struct{}, len(existingNames))
	for _, name := range existingNames {
		existingSet[name] = struct{}{}
		if _, keep := diskXattrs[name]; !keep {
			if err := core.RemoveXattr(qp, name, false); err != nil {
				return err
			}
		}
	}
	for name, val := range diskXattrs {
		if _, had := existingSet[name]; had {
			oldVal, gErr := core.GetXattr(qp, name, false)
			if gErr == nil && bytes.Equal(oldVal, val) {
				continue
			}
		}
		if err := core.SetXattr(qp, name, val, false); err != nil {
			return err
		}
	}
	return nil
}

func xattrsEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}
