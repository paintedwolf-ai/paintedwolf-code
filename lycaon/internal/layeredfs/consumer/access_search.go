package consumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

// LookupPath resolves a named path directly through the worker mount even when the source index
// has not observed it or holds stale metadata.
func (e *Engine) LookupPath(_ context.Context, qpath layeredfs.QualifiedPath) (InventoryEntry, error) {
	nq, err := normalizeQualifiedPath(qpath)
	if err != nil {
		return InventoryEntry{}, err
	}
	if _, ok := e.rootsByID[nq.RootID]; !ok {
		return InventoryEntry{}, ErrUnknownRoot
	}
	if _, err := e.ResolveMountedPath(nq); err != nil && !errors.Is(err, ErrMountNotConfigured) {
		return InventoryEntry{}, err
	}
	if e.core != nil {
		attr, err := e.core.Lstat(nq)
		if e.tracker != nil {
			e.tracker.ObserveLookupResult(nq, attr, err)
		}
		if err != nil {
			return InventoryEntry{}, err
		}
		return e.attrToInventoryEntry(nq, attr), nil
	}
	absPath, err := e.ResolveMountedPath(nq)
	if err != nil {
		return InventoryEntry{}, err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		if e.tracker != nil && errors.Is(err, fs.ErrNotExist) {
			e.tracker.RecordNegativeLookup(nq)
		}
		return InventoryEntry{}, err
	}
	attr := layeredfs.EntryAttr{
		Layer: layeredfs.LayerTrunk, Kind: entryKindFromMode(info.Mode()),
		Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime(),
	}
	if e.tracker != nil {
		e.tracker.ObserveLookupResult(nq, attr, nil)
	}
	return e.attrToInventoryEntry(nq, attr), nil
}

// ReadEvidence reads actual file content through the worker mount (enforcing the access boundary
// guard against direct backing/lower reads), preserves private overrides, and records a structured
// content observation on the view's observation.Tracker.
func (e *Engine) ReadEvidence(_ context.Context, qpath layeredfs.QualifiedPath) (EvidenceReadResult, error) {
	nq, err := normalizeQualifiedPath(qpath)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	if _, ok := e.rootsByID[nq.RootID]; !ok {
		return EvidenceReadResult{}, ErrUnknownRoot
	}
	absMountPath, err := e.ResolveMountedPath(nq)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	data, layer, mode, modTime, ino, dev, err := e.readMountedOrCore(nq, absMountPath)
	if err != nil {
		if e.tracker != nil && errors.Is(err, fs.ErrNotExist) {
			e.tracker.RecordNegativeLookup(nq)
		}
		return EvidenceReadResult{}, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	var obs observation.SourceObservation
	if e.tracker != nil {
		obs = e.tracker.RecordServerFSObservation(layeredfs.ObservationEvent{
			ViewID: e.viewID, Path: nq, Layer: layer, Kind: layeredfs.ObserveRead,
			Size: int64(len(data)), ModTimeNS: modTime, SHA256: digest, Ino: ino, Dev: dev,
		})
	}
	return EvidenceReadResult{
		Path: nq, Content: data, SHA256: digest, Size: int64(len(data)),
		Mode: mode, Layer: layer, Source: AccessSourceMountedView,
		StoredInCheckpoint: false, VisibilityMode: e.VisibilityFacts().UntouchedVisibility, Observation: obs,
	}, nil
}

func (e *Engine) readMountedOrCore(nq layeredfs.QualifiedPath, absMountPath string) ([]byte, layeredfs.LayerSource, fs.FileMode, int64, uint64, uint64, error) {
	if e.core != nil {
		attr, sErr := e.core.Lstat(nq)
		if sErr != nil {
			if errors.Is(sErr, fs.ErrNotExist) {
				return nil, layeredfs.LayerTrunk, 0, 0, 0, 0, sErr
			}
		} else if attr.Kind != layeredfs.EntryRegular {
			return nil, attr.Layer, attr.Mode, 0, 0, 0, fs.ErrInvalid
		} else if attr.Layer == layeredfs.LayerPrivate {
			data, err := e.core.ReadFile(nq)
			return data, attr.Layer, attr.Mode, attr.ModTime.UnixNano(), 0, 0, err
		} else {
			if info, mErr := os.Lstat(absMountPath); mErr == nil && info.Mode().IsRegular() {
				if data, rErr := os.ReadFile(absMountPath); rErr == nil {
					ino, dev := statInoDev(info)
					return data, attr.Layer, info.Mode(), info.ModTime().UnixNano(), ino, dev, nil
				}
			}
			data, err := e.core.ReadFile(nq)
			return data, attr.Layer, attr.Mode, attr.ModTime.UnixNano(), 0, 0, err
		}
	}
	info, err := os.Lstat(absMountPath)
	if err != nil {
		return nil, layeredfs.LayerTrunk, 0, 0, 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, layeredfs.LayerTrunk, info.Mode(), 0, 0, 0, fs.ErrInvalid
	}
	data, err := os.ReadFile(absMountPath)
	if err != nil {
		return nil, layeredfs.LayerTrunk, 0, 0, 0, 0, err
	}
	ino, dev := statInoDev(info)
	return data, layeredfs.LayerTrunk, info.Mode(), info.ModTime().UnixNano(), ino, dev, nil
}

func statInoDev(info fs.FileInfo) (uint64, uint64) {
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil {
		return uint64(sys.Ino), uint64(sys.Dev)
	}
	return 0, 0
}

// ReadCheckpointEntry inspects a file in a published checkpoint. Changed files stored in the
// checkpoint are read from immutable checkpoint blobs; untouched paths are either rejected or,
// when allowLiveFallback is true, read from the live mount and explicitly labeled AccessSourceLiveTrunk
// with StoredInCheckpoint=false.
func (e *Engine) ReadCheckpointEntry(ctx context.Context, checkpointID string, qpath layeredfs.QualifiedPath, allowLiveFallback bool) (EvidenceReadResult, error) {
	if e.ckptStore == nil {
		return EvidenceReadResult{}, ErrCheckpointNotConfigured
	}
	nq, err := normalizeQualifiedPath(qpath)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	m, err := e.ckptStore.LoadManifest(strings.TrimSpace(checkpointID))
	if err != nil {
		return EvidenceReadResult{}, err
	}
	for _, entry := range m.Entries {
		if entry.Path == nq {
			if entry.Deleted || entry.Kind != layeredfs.EntryRegular {
				return EvidenceReadResult{}, fs.ErrNotExist
			}
			data, err := e.ckptStore.ReadBlob(entry.Final.SHA256)
			if err != nil {
				return EvidenceReadResult{}, err
			}
			return EvidenceReadResult{
				Path: nq, Content: data, SHA256: entry.Final.SHA256, Size: entry.Final.Size,
				Mode: entry.Final.Mode, ModTime: entry.Final.ModTime, Layer: layeredfs.LayerPrivate,
				Source: AccessSourceCheckpointStore, StoredInCheckpoint: true, VisibilityMode: observation.VisibilityFixed,
			}, nil
		}
		if (entry.DirectoryReplaced || entry.Deleted) && entry.Path.RootID == nq.RootID && isDescendantRel(nq.Rel, entry.Path.Rel) {
			return EvidenceReadResult{}, fs.ErrNotExist
		}
	}
	if !allowLiveFallback {
		return EvidenceReadResult{}, fs.ErrNotExist
	}
	liveRes, err := e.ReadEvidence(ctx, nq)
	if err != nil {
		return EvidenceReadResult{}, err
	}
	liveRes.Source = AccessSourceLiveTrunk
	liveRes.StoredInCheckpoint = false
	liveRes.Layer = layeredfs.LayerTrunk
	return liveRes, nil
}

// SearchWorkspace searches indexed and privately modified files (confirming matches through the
// worker mount or checkpoint store) and, when AllowScopedTraversal is true, performs a bounded,
// cancelable traversal of the declared scope with explicit resource accounting.
func (e *Engine) SearchWorkspace(ctx context.Context, req SearchRequest) (SearchResult, error) {
	roots, allowed, err := e.resolveRequestedRoots(req.RootIDs)
	if err != nil {
		return SearchResult{}, err
	}
	prefix := strings.Trim(strings.TrimSpace(req.PathPrefix), "/")
	ckptID := strings.TrimSpace(req.CheckpointID)
	res := SearchResult{Coverage: CoverageComplete, VisibilityFacts: e.VisibilityFacts()}
	var idxRes SourceIndexResult
	if e.index != nil {
		idxRes, err = e.index.QueryIndex(ctx, SourceIndexQuery{RootIDs: roots, PathPrefix: prefix})
		if err != nil {
			return SearchResult{}, err
		}
		if idxRes.Coverage != "" {
			res.Coverage = idxRes.Coverage
		}
		if idxRes.Coverage != CoverageComplete || idxRes.Truncated {
			res.IncompleteScope = true
		}
	} else {
		res.Coverage = CoverageAbsent
		res.IncompleteScope = true
	}

	delta, err := e.collectChangeDelta(ckptID, allowed, prefix)
	if err != nil {
		return SearchResult{}, err
	}
	candidates, _ := e.mergeIndexAndDelta(idxRes, delta, allowed, prefix, 0)
	inspected := make(map[layeredfs.QualifiedPath]bool, len(candidates))

	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			res.Canceled, res.IncompleteScope = true, true
			return res, err
		}
		if cand.Kind != layeredfs.EntryRegular {
			continue
		}
		if cand.IsIndexDerived {
			res.Accounting.IndexCandidates++
		} else {
			res.Accounting.PrivateCandidates++
		}
		if req.MaxFilesInspected > 0 && res.Accounting.FilesInspected >= req.MaxFilesInspected {
			res.IncompleteScope = true
			return res, nil
		}
		inspected[cand.Path] = true
		res.Accounting.FilesInspected++
		e.inspectSearchCandidate(ctx, cand.Path, cand.IsIndexDerived, ckptID, req.Query, &res)
	}
	if req.AllowScopedTraversal && ckptID == "" {
		if err := e.traverseDeclaredSearchScope(ctx, roots, prefix, req, inspected, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (e *Engine) inspectSearchCandidate(ctx context.Context, qpath layeredfs.QualifiedPath, isIndexDerived bool, ckptID, query string, res *SearchResult) {
	var ev EvidenceReadResult
	var err error
	if ckptID != "" {
		ev, err = e.ReadCheckpointEntry(ctx, ckptID, qpath, true)
	} else {
		res.Accounting.MountReads++
		ev, err = e.ReadEvidence(ctx, qpath)
	}
	if err != nil {
		return
	}
	obsCopy := ev.Observation
	if query == "" {
		res.Matches = append(res.Matches, SearchMatch{
			Path: qpath, Line: 1, Layer: ev.Layer, Source: ev.Source,
			IsIndexDerived: isIndexDerived, ConfirmedViaMount: ckptID == "", Observation: &obsCopy,
		})
		return
	}
	qBytes := []byte(query)
	for i, line := range bytes.Split(ev.Content, []byte("\n")) {
		if bytes.Contains(line, qBytes) {
			res.Matches = append(res.Matches, SearchMatch{
				Path: qpath, Line: i + 1, Snippet: strings.TrimSpace(string(line)),
				Layer: ev.Layer, Source: ev.Source, IsIndexDerived: isIndexDerived,
				ConfirmedViaMount: ckptID == "", Observation: &obsCopy,
			})
		}
	}
}

func (e *Engine) traverseDeclaredSearchScope(ctx context.Context, roots []string, prefix string, req SearchRequest, inspected map[layeredfs.QualifiedPath]bool, res *SearchResult) error {
	startRel := "."
	if prefix != "" {
		startRel = path.Clean(prefix)
	}
	for _, rootID := range roots {
		queue := []string{startRel}
		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				res.Canceled, res.IncompleteScope = true, true
				return err
			}
			if req.MaxDirsVisited > 0 && res.Accounting.DirsVisited >= req.MaxDirsVisited {
				res.IncompleteScope = true
				return nil
			}
			dirRel := queue[0]
			queue = queue[1:]
			if IsInternalStoragePath(dirRel) {
				continue
			}
			res.Accounting.DirsVisited++
			children, err := e.ListDirectory(ctx, layeredfs.QualifiedPath{RootID: rootID, Rel: dirRel}, "")
			if err != nil {
				continue
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					res.Canceled, res.IncompleteScope = true, true
					return err
				}
				if IsInternalStoragePath(child.Path.Rel) {
					continue
				}
				if child.Kind == layeredfs.EntryDirectory {
					queue = append(queue, child.Path.Rel)
				} else if child.Kind == layeredfs.EntryRegular && !inspected[child.Path] {
					if req.MaxFilesInspected > 0 && res.Accounting.FilesInspected >= req.MaxFilesInspected {
						res.IncompleteScope = true
						return nil
					}
					inspected[child.Path] = true
					res.Accounting.FilesInspected++
					e.inspectSearchCandidate(ctx, child.Path, false, "", req.Query, res)
				}
			}
		}
	}
	res.IncompleteScope = false
	res.Coverage = CoverageComplete
	return nil
}
