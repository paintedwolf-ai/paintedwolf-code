package adapter

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

// CaptureStats counts what one stat capture did. An unchanged file adds to EntriesWalked and nothing
// else: its stat proves it, and its content is never opened.
type CaptureStats struct {
	EntriesWalked    int64 `json:"entries_walked"`
	StatCandidates   int64 `json:"stat_candidates"`
	Removed          int64 `json:"removed"`
	ContentBytesRead int64 `json:"content_bytes_read"`
}

// statCapture holds a materialized view's capture state. manifest is immutable; current follows what
// the last capture saw, and synced holds what Core holds for each path a capture looked at.
type statCapture struct {
	manifestNS int64
	manifest   map[layeredfs.QualifiedPath]statRecord
	current    map[layeredfs.QualifiedPath]statRecord
	bases      map[layeredfs.QualifiedPath]layeredfs.BaseCapture
	synced     map[layeredfs.QualifiedPath]layeredfs.BaseCapture
	// refreshed names records a capture replaced, whose stat no longer speaks for the base.
	refreshed map[layeredfs.QualifiedPath]struct{}
	pristine  map[string]string
	parents   []layeredfs.ParentLayer
	// basesDir is where Core keeps the bases it holds. A changed file's base goes there from the
	// pristine tree, a clone where the volume allows it.
	basesDir string
	baseSeq  int
	meter    contentMeter
	stats    CaptureStats
}

func newStatCapture(takenNS int64, records []statRecord, roots []PreparedRoot, parents []layeredfs.ParentLayer, backingDir string) *statCapture {
	basesDir := filepath.Join(backingDir, "bases")
	if resolved, err := filepath.EvalSymlinks(basesDir); err == nil {
		basesDir = resolved
	}
	s := &statCapture{
		basesDir:   basesDir,
		manifestNS: takenNS,
		manifest:   make(map[layeredfs.QualifiedPath]statRecord, len(records)),
		current:    make(map[layeredfs.QualifiedPath]statRecord, len(records)),
		bases:      make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
		synced:     make(map[layeredfs.QualifiedPath]layeredfs.BaseCapture),
		refreshed:  make(map[layeredfs.QualifiedPath]struct{}),
		pristine:   make(map[string]string, len(roots)),
		parents:    parents,
	}
	for _, rec := range records {
		s.manifest[rec.qualified()] = rec
		s.current[rec.qualified()] = rec
	}
	for _, r := range roots {
		s.pristine[r.RootID] = r.PristinePath
	}
	return s
}

// statVisit is a walked entry whose stat no longer proves it unchanged.
type statVisit struct {
	qp   layeredfs.QualifiedPath
	path string
	info fs.FileInfo
}

// walkRoot lists a root's candidates in walk order and its records with nothing on disk behind them.
func (s *statCapture) walkRoot(ctx context.Context, rootID, workDir string) ([]statVisit, []statRecord, error) {
	var visits []statVisit
	seen := make(map[layeredfs.QualifiedPath]struct{})
	err := filepath.WalkDir(workDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(workDir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		qp := layeredfs.QualifiedPath{RootID: rootID, Rel: filepath.ToSlash(rel)}
		s.stats.EntriesWalked++
		rec, tracked := s.current[qp]
		if tracked && !entryKindMatchesMode(rec.Kind, info.Mode()) {
			return s.noteVisit(&visits, qp, p, info)
		}
		seen[qp] = struct{}{}
		if tracked && rec.unchangedBy(info, s.manifestNS) {
			return nil
		}
		return s.noteVisit(&visits, qp, p, info)
	})
	if err != nil {
		return nil, nil, err
	}
	var gone []statRecord
	for qp, rec := range s.current {
		if qp.RootID != rootID {
			continue
		}
		if _, ok := seen[qp]; !ok {
			gone = append(gone, rec)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		di, dj := strings.Count(gone[i].Rel, "/"), strings.Count(gone[j].Rel, "/")
		if di != dj {
			return di > dj
		}
		return gone[i].Rel > gone[j].Rel
	})
	return visits, gone, nil
}

func (s *statCapture) noteVisit(visits *[]statVisit, qp layeredfs.QualifiedPath, p string, info fs.FileInfo) error {
	s.stats.StatCandidates++
	*visits = append(*visits, statVisit{qp: qp, path: p, info: info})
	return nil
}

// capture reconciles what a worker did in a materialized tree into Core: only entries whose stat
// differs are looked at, and only those read. A byte copy captures the same way as a clone.
func (s *statCapture) capture(ctx context.Context, view *PreparedView) error {
	s.stats = CaptureStats{}
	before := s.meter.read.Load()
	defer func() { s.stats.ContentBytesRead = s.meter.read.Load() - before }()
	startedNS := time.Now().UnixNano()
	for _, r := range view.Roots {
		visits, gone, err := s.walkRoot(ctx, r.RootID, r.WorkspacePath)
		if err != nil {
			return err
		}
		for _, rec := range gone {
			if err := s.removeEntry(ctx, view.Core, rec); err != nil {
				return err
			}
		}
		for _, v := range visits {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.applyVisit(ctx, view.Core, v, startedNS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *statCapture) removeEntry(ctx context.Context, core *layeredfs.Core, rec statRecord) error {
	qp := rec.qualified()
	if _, err := s.baseFor(ctx, core, qp); err != nil {
		return err
	}
	rmdir := rec.Kind == layeredfs.EntryDirectory
	if err := core.Unlink(qp, rmdir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.stats.Removed++
	delete(s.current, qp)
	delete(s.synced, qp)
	return nil
}

func (s *statCapture) applyVisit(ctx context.Context, core *layeredfs.Core, v statVisit, startedNS int64) error {
	base, err := s.baseFor(ctx, core, v.qp)
	if err != nil {
		return err
	}
	var rec statRecord
	switch {
	case v.info.IsDir():
		err = syncDirEntry(core, v.qp, v.path, v.info, s.synced)
		rec = recordStat(v.qp.RootID, v.qp.Rel, layeredfs.EntryDirectory, v.info)
	case v.info.Mode()&os.ModeSymlink != 0:
		err = syncSymlinkEntry(core, v.qp, v.path, v.info, s.synced)
		rec = recordStat(v.qp.RootID, v.qp.Rel, layeredfs.EntrySymlink, v.info)
		rec.LinkTarget = s.synced[v.qp].LinkTarget
	case v.info.Mode().IsRegular():
		err = syncRegularEntry(ctx, core, &s.meter, v.qp, v.path, v.info, base, s.synced)
		rec = recordStat(v.qp.RootID, v.qp.Rel, layeredfs.EntryRegular, v.info)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	rec.recordedNS = startedNS
	s.current[v.qp] = rec
	s.refreshed[v.qp] = struct{}{}
	return nil
}

// baseFor reads a path's exact base from the pristine tree the first time a capture needs it and
// hands it to Core, which holds it from then on. A path the manifest does not name has no such base,
// and one a parent layer supplies keeps the parent's: Core captures both of those itself.
func (s *statCapture) baseFor(ctx context.Context, core *layeredfs.Core, qp layeredfs.QualifiedPath) (layeredfs.BaseCapture, error) {
	if base, ok := s.bases[qp]; ok {
		return base, nil
	}
	rec, ok := s.manifest[qp]
	if !ok {
		return layeredfs.BaseCapture{}, nil
	}
	if s.heldByParent(qp) {
		return layeredfs.BaseCapture{}, nil
	}
	base, err := s.readPristineBase(ctx, rec)
	if err != nil {
		return layeredfs.BaseCapture{}, err
	}
	if err := core.RegisterSeedBase(base); err != nil {
		return layeredfs.BaseCapture{}, err
	}
	s.bases[qp] = base
	if _, known := s.synced[qp]; !known {
		s.synced[qp] = cloneBaseCaptureRecord(base)
	}
	return base, nil
}

func (s *statCapture) heldByParent(qp layeredfs.QualifiedPath) bool {
	for _, parent := range s.parents {
		if _, ok, err := parent.Lookup(qp); err == nil && ok {
			return true
		}
	}
	return false
}

func (s *statCapture) readPristineBase(ctx context.Context, rec statRecord) (layeredfs.BaseCapture, error) {
	full := filepath.Join(s.pristine[rec.Root], filepath.FromSlash(rec.Rel))
	xattrs, err := readPathXattrs(full)
	// An archive keeps files, so a restored pristine tree can lack an empty directory; its mode is here.
	if err != nil && !(rec.Kind == layeredfs.EntryDirectory && errors.Is(err, fs.ErrNotExist)) {
		return layeredfs.BaseCapture{}, err
	}
	base := layeredfs.BaseCapture{
		Path: rec.qualified(), Layer: layeredfs.LayerTrunk, Kind: rec.Kind, Mode: rec.fileMode(),
		ModTime: rec.modTime(), LinkTarget: rec.LinkTarget, Xattrs: xattrs, CapturedAt: time.Now(),
	}
	if rec.Kind == layeredfs.EntryRegular {
		sha, size, err := s.meter.hashFile(ctx, full)
		if err != nil {
			return layeredfs.BaseCapture{}, err
		}
		held, err := s.holdBaseBytes(ctx, full)
		if err != nil {
			return layeredfs.BaseCapture{}, err
		}
		base.SHA256, base.Size, base.BackingFile = sha, size, held
	}
	return base, nil
}

func (s *statCapture) holdBaseBytes(ctx context.Context, pristinePath string) (string, error) {
	s.baseSeq++
	held := filepath.Join(s.basesDir, fmt.Sprintf("pristine-base-%08d", s.baseSeq))
	cloned, err := fileclone.Clone(pristinePath, held)
	if err != nil {
		return "", err
	}
	if !cloned {
		if _, err := s.meter.copyFile(ctx, pristinePath, held, 0o600, 0); err != nil {
			return "", err
		}
	}
	// A clone keeps the pristine tree's read-only mode, and Core clones this file to write to it.
	return held, os.Chmod(held, 0o600)
}

// changedPaths lists what differs from the exact base right now, without touching Core. Unchanged
// stats answer for themselves here too.
func (s *statCapture) changedPaths(ctx context.Context, roots []PreparedRoot) ([]layeredfs.QualifiedPath, error) {
	s.stats = CaptureStats{}
	before := s.meter.read.Load()
	defer func() { s.stats.ContentBytesRead = s.meter.read.Load() - before }()
	var out []layeredfs.QualifiedPath
	for _, r := range roots {
		visits, gone, err := s.walkRoot(ctx, r.RootID, r.WorkspacePath)
		if err != nil {
			return nil, err
		}
		for _, rec := range gone {
			if _, inManifest := s.manifest[rec.qualified()]; inManifest {
				out = append(out, rec.qualified())
			}
		}
		visited := make(map[layeredfs.QualifiedPath]struct{}, len(visits))
		for _, v := range visits {
			visited[v.qp] = struct{}{}
			differs, err := s.differsFromBase(ctx, v)
			if err != nil {
				return nil, err
			}
			if differs {
				out = append(out, v.qp)
			}
		}
		captured, err := s.capturedDifferences(ctx, r, visited)
		if err != nil {
			return nil, err
		}
		out = append(out, captured...)
	}
	slices.SortFunc(out, func(a, b layeredfs.QualifiedPath) int {
		return cmp.Or(cmp.Compare(a.RootID, b.RootID), cmp.Compare(a.Rel, b.Rel))
	})
	return slices.Compact(out), nil
}

// capturedDifferences answers for paths an earlier capture took in, whose stat now matches the
// capture rather than the base, and for bases it removed.
func (s *statCapture) capturedDifferences(ctx context.Context, r PreparedRoot, visited map[layeredfs.QualifiedPath]struct{}) ([]layeredfs.QualifiedPath, error) {
	var out []layeredfs.QualifiedPath
	for qp := range s.refreshed {
		if _, done := visited[qp]; done || qp.RootID != r.RootID {
			continue
		}
		full := filepath.Join(r.WorkspacePath, filepath.FromSlash(qp.Rel))
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		differs, err := s.differsFromBase(ctx, statVisit{qp: qp, path: full, info: info})
		if err != nil {
			return nil, err
		}
		if differs {
			out = append(out, qp)
		}
	}
	for qp := range s.manifest {
		if _, tracked := s.current[qp]; tracked || qp.RootID != r.RootID {
			continue
		}
		if _, err := os.Lstat(filepath.Join(r.WorkspacePath, filepath.FromSlash(qp.Rel))); err != nil {
			out = append(out, qp)
		}
	}
	return out, nil
}

func (s *statCapture) differsFromBase(ctx context.Context, v statVisit) (bool, error) {
	rec, inManifest := s.manifest[v.qp]
	if !inManifest || !entryKindMatchesMode(rec.Kind, v.info.Mode()) {
		return true, nil
	}
	switch rec.Kind {
	case layeredfs.EntrySymlink:
		target, err := os.Readlink(v.path)
		return target != rec.LinkTarget, err
	case layeredfs.EntryDirectory:
		return v.info.Mode().Perm() != rec.fileMode().Perm(), nil
	}
	if v.info.Mode().Perm() != rec.fileMode().Perm() {
		return true, nil
	}
	diskSHA, _, err := s.meter.hashFile(ctx, v.path)
	if err != nil {
		return false, err
	}
	baseSHA, _, err := s.meter.hashFile(ctx, filepath.Join(s.pristine[rec.Root], filepath.FromSlash(rec.Rel)))
	if err != nil {
		return false, err
	}
	return diskSHA != baseSHA, nil
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
	if hasSynced && cur.Kind == layeredfs.EntrySymlink && cur.LinkTarget != target {
		if err := core.Unlink(qp, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(synced, qp)
		hasSynced = false
	}
	if !hasSynced || cur.Kind != layeredfs.EntrySymlink {
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

// syncRegularEntry looks at one file whose stat differs. A hash that matches what Core already holds
// stops here, so a touched file costs a read and no write.
func syncRegularEntry(
	ctx context.Context,
	core *layeredfs.Core,
	meter *contentMeter,
	qp layeredfs.QualifiedPath,
	fullPath string,
	info fs.FileInfo,
	base layeredfs.BaseCapture,
	synced map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
) error {
	cur, hasSynced := synced[qp]
	wasDifferentFromBase := !hasSynced ||
		cur.Kind != layeredfs.EntryRegular ||
		cur.SHA256 != base.SHA256 ||
		cur.Mode.Perm() != base.Mode.Perm() ||
		!xattrsEqual(cur.Xattrs, base.Xattrs)

	diskSHA, _, err := meter.hashFile(ctx, fullPath)
	if err != nil {
		return err
	}
	if !hasSynced || cur.Kind != layeredfs.EntryRegular || diskSHA != cur.SHA256 {
		raw, err := meter.readFile(fullPath)
		if err != nil {
			return err
		}
		// A background writer may have moved on since the hash; Core records what it was handed.
		diskSHA = sha256Hex(raw)
		if _, err := core.WriteFile(qp, raw, info.Mode().Perm()); err != nil {
			return err
		}
		if !hasSynced || cur.Kind != layeredfs.EntryRegular {
			cur = layeredfs.BaseCapture{
				Path: qp,
				Kind: layeredfs.EntryRegular,
				Mode: info.Mode().Perm(),
			}
		}
		cur.Size, cur.SHA256 = int64(len(raw)), diskSHA
		synced[qp] = cur
	}

	if err := syncEntryMetadataIfChanged(core, qp, fullPath, info, synced); err != nil {
		return err
	}

	after := synced[qp]
	matchesSeedBase := base.Kind == layeredfs.EntryRegular &&
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
