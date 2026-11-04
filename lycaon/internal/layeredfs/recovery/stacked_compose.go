package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

func composeEffectiveStackedManifest(
	child checkpoint.Manifest,
	oldestToNewest []checkpoint.Manifest,
	landedSet map[string]bool,
) checkpoint.Manifest {
	if len(oldestToNewest) <= 1 {
		return child
	}
	landedFinalByPath := make(map[layeredfs.QualifiedPath]checkpoint.ManifestEntry)
	effectiveByPath := make(map[layeredfs.QualifiedPath]checkpoint.ManifestEntry)
	gitObjects := make(map[string]bool)
	mergedRefs := make(map[string]string)

	for _, m := range oldestToNewest {
		if landedSet[m.CheckpointID] {
			for _, e := range m.Entries {
				landedFinalByPath[e.Path] = e
			}
			continue
		}
		for _, oid := range m.PrivateGit.RetainedObjects {
			if oid != "" {
				gitObjects[oid] = true
			}
		}
		for ref, oid := range m.PrivateGit.Refs {
			mergedRefs[ref] = oid
		}
		for _, e := range m.Entries {
			mergeEntryIntoStackedMap(e, landedFinalByPath, effectiveByPath)
		}
	}

	var entries []checkpoint.ManifestEntry
	for _, e := range effectiveByPath {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b checkpoint.ManifestEntry) int {
		return strings.Compare(a.Path.RootID+":"+a.Path.Rel, b.Path.RootID+":"+b.Path.Rel)
	})

	var retainedOIDs []string
	for oid := range gitObjects {
		retainedOIDs = append(retainedOIDs, oid)
	}
	slices.Sort(retainedOIDs)

	var chainKey strings.Builder
	for _, m := range oldestToNewest {
		chainKey.WriteString(m.CheckpointID)
		if landedSet[m.CheckpointID] {
			chainKey.WriteString(":L;")
		} else {
			chainKey.WriteString(":U;")
		}
	}
	sum := sha256.Sum256([]byte(chainKey.String()))
	effID := "eff-" + child.CheckpointID + "-" + hex.EncodeToString(sum[:6])

	out := child
	out.CheckpointID = effID
	out.Entries = entries
	out.PrivateGit = checkpoint.PrivateGitSnapshot{
		HeadRef:         child.PrivateGit.HeadRef,
		Refs:            mergedRefs,
		RetainedObjects: retainedOIDs,
	}
	return out
}

func mergeEntryIntoStackedMap(
	e checkpoint.ManifestEntry,
	landedFinalByPath map[layeredfs.QualifiedPath]checkpoint.ManifestEntry,
	effectiveByPath map[layeredfs.QualifiedPath]checkpoint.ManifestEntry,
) {
	prev, exists := effectiveByPath[e.Path]
	if !exists {
		merged := e
		if landed, ok := landedFinalByPath[e.Path]; ok {
			merged.Base = baseCaptureFromEntryFinal(landed)
		} else {
			merged.Base.Layer = layeredfs.LayerTrunk
		}
		if merged.Base.Kind == layeredfs.EntryAbsent && (merged.Deleted || merged.Kind == layeredfs.EntryAbsent) {
			return
		}
		effectiveByPath[e.Path] = merged
		return
	}

	rootBase := prev.Base
	rootBase.Layer = layeredfs.LayerTrunk
	if rootBase.Kind == layeredfs.EntryAbsent && (e.Deleted || e.Kind == layeredfs.EntryAbsent) {
		delete(effectiveByPath, e.Path)
		return
	}

	merged := e
	merged.Base = rootBase
	merged.DirectoryReplaced = prev.DirectoryReplaced || e.DirectoryReplaced ||
		(rootBase.Kind == layeredfs.EntryDirectory && !e.Deleted && e.Kind != layeredfs.EntryDirectory) ||
		(rootBase.Kind != layeredfs.EntryAbsent && rootBase.Kind != layeredfs.EntryDirectory && e.Kind == layeredfs.EntryDirectory)
	merged.CandidateBases = append(append([]layeredfs.BaseCapture(nil), prev.CandidateBases...), e.CandidateBases...)
	effectiveByPath[e.Path] = merged
}

func baseCaptureFromEntryFinal(e checkpoint.ManifestEntry) layeredfs.BaseCapture {
	if e.Deleted || e.Kind == layeredfs.EntryAbsent {
		return layeredfs.BaseCapture{
			Path:       e.Path,
			Layer:      layeredfs.LayerTrunk,
			Kind:       layeredfs.EntryAbsent,
			CapturedAt: time.Now().UTC(),
		}
	}
	return layeredfs.BaseCapture{
		Path:       e.Path,
		Layer:      layeredfs.LayerTrunk,
		Kind:       e.Kind,
		Mode:       e.Final.Mode,
		Size:       e.Final.Size,
		ModTime:    e.Final.ModTime,
		SHA256:     e.Final.SHA256,
		LinkTarget: e.Final.LinkTarget,
		Xattrs:     maps.Clone(e.Final.Xattrs),
		CapturedAt: time.Now().UTC(),
	}
}

func buildPromotedTargetDigests(m checkpoint.Manifest, targetRoots map[string]string) map[string]checkpoint.TargetObservation {
	out := make(map[string]checkpoint.TargetObservation, len(m.Entries))
	for _, e := range m.Entries {
		rootDir := targetRoots[e.Path.RootID]
		if rootDir == "" {
			continue
		}
		full := filepath.Join(rootDir, filepath.FromSlash(e.Path.Rel))
		obs := checkpoint.TargetObservation{Path: e.Path, Kind: layeredfs.EntryAbsent}
		if info, err := os.Lstat(full); err == nil {
			obs.Mode = info.Mode()
			obs.ModTime = info.ModTime()
			switch {
			case info.IsDir():
				obs.Kind = layeredfs.EntryDirectory
			case info.Mode()&os.ModeSymlink != 0:
				obs.Kind = layeredfs.EntrySymlink
				obs.LinkTarget, _ = os.Readlink(full)
			case info.Mode().IsRegular():
				obs.Kind = layeredfs.EntryRegular
				obs.Size = info.Size()
				if raw, rErr := os.ReadFile(full); rErr == nil {
					sum := sha256.Sum256(raw)
					obs.SHA256 = hex.EncodeToString(sum[:])
				}
			}
		}
		out[e.Path.RootID+":"+e.Path.Rel] = obs
	}
	return out
}
