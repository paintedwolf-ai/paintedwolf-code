package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (t *Tracker) advanceGenerationLocked(rootID string, requestedGen uint64) {
	t.sourceGen++
	if requestedGen > t.sourceGen {
		t.sourceGen = requestedGen
	}
	if rootID != "" {
		t.rootGens[rootID] = t.sourceGen
	}
}

func (t *Tracker) populateTrunkChangeFromDiskLocked(ev *TrunkChangeEvent, qpath layeredfs.QualifiedPath) {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = t.clock()
	}
	if ev.Kind == TrunkDelete {
		return
	}
	rootSpec, ok := t.rootsByID[qpath.RootID]
	if !ok || rootSpec.TrunkPath == "" {
		return
	}
	absPath := filepath.Join(rootSpec.TrunkPath, filepath.FromSlash(qpath.Rel))
	info, err := os.Lstat(absPath)
	if err != nil {
		return
	}
	if ev.NewSize == 0 {
		ev.NewSize = info.Size()
	}
	if ev.NewModTimeNS == 0 {
		ev.NewModTimeNS = info.ModTime().UnixNano()
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok && sys != nil && ev.NewIno == 0 {
		ev.NewIno = uint64(sys.Ino)
		ev.NewDev = uint64(sys.Dev)
	}
	if ev.NewSHA256 == "" && info.Mode().IsRegular() {
		if data, rErr := os.ReadFile(absPath); rErr == nil {
			sum := sha256.Sum256(data)
			ev.NewSHA256 = hex.EncodeToString(sum[:])
			ev.NewSize = int64(len(data))
		}
	}
}

func (t *Tracker) isPrivateOrRetainedPathLocked(qpath layeredfs.QualifiedPath) bool {
	if t.core != nil {
		if _, hasCapturedBase := t.core.CapturedBase(qpath); hasCapturedBase {
			return true
		}
	}
	indices := t.obsByPath[qpath]
	for i := len(indices) - 1; i >= 0; i-- {
		if t.observations[indices[i]].Layer == layeredfs.LayerPrivate {
			return true
		}
	}
	return false
}

// markTrunkPathObservationsStaleLocked checks prior trunk observations for qpath against ev.
// It never uses timestamp equality alone as proof that an observed version is unchanged.
func (t *Tracker) markTrunkPathObservationsStaleLocked(qpath layeredfs.QualifiedPath, ev TrunkChangeEvent) {
	indices := t.obsByPath[qpath]
	for _, idx := range indices {
		obs := t.observations[idx]
		if obs.Layer != layeredfs.LayerTrunk {
			continue
		}
		if !observationAffectedByTrunkChange(obs, ev) {
			continue
		}
		t.staleObsIDs[obs.ObservationID] = StaleReasonObservedDependencyChanged
		t.pendingNotice[qpath] = struct{}{}
		t.pendingObsRefs[obs.ObservationID] = struct{}{}
	}
}

func observationAffectedByTrunkChange(obs SourceObservation, ev TrunkChangeEvent) bool {
	if ev.Kind == TrunkDelete || ev.Kind == TrunkRename || ev.Kind == TrunkAtomicReplace || ev.Kind == TrunkCreate {
		return true
	}
	if obs.SHA256 != "" && ev.NewSHA256 != "" {
		return obs.SHA256 != ev.NewSHA256
	}
	if obs.Ino != 0 && ev.NewIno != 0 && obs.Ino != ev.NewIno {
		return true
	}
	if obs.Size != ev.NewSize {
		return true
	}
	// Never treat identical timestamps without matching content digest as proof of unchanged version.
	return true
}

func (t *Tracker) buildTrunkInvalidationsLocked(
	qpath layeredfs.QualifiedPath,
	ev TrunkChangeEvent,
	gen uint64,
) []InvalidationEvent {
	if t.facts.UntouchedVisibility != VisibilityLive || !t.facts.SupportsInvalidationUpcall {
		return nil
	}
	parentDir, childName := splitParentAndName(qpath)
	_, wasNegative := t.negativeLookups[qpath]
	delete(t.negativeLookups, qpath)

	var events []InvalidationEvent
	if (wasNegative || ev.Kind == TrunkCreate) && t.facts.SupportsNegativeLookupInvalidation {
		events = append(events, InvalidationEvent{
			Kind:             InvalidateNegativeName,
			ViewID:           t.viewID,
			Path:             qpath,
			ParentDir:        parentDir,
			ChildName:        childName,
			SourceGeneration: gen,
			Reason:           string(ev.Kind),
		})
	}
	events = append(events,
		InvalidationEvent{
			Kind:             InvalidateAttr,
			ViewID:           t.viewID,
			Path:             qpath,
			SourceGeneration: gen,
			Reason:           string(ev.Kind),
		},
		InvalidationEvent{
			Kind:             InvalidateEntry,
			ViewID:           t.viewID,
			Path:             qpath,
			ParentDir:        parentDir,
			ChildName:        childName,
			SourceGeneration: gen,
			Reason:           string(ev.Kind),
		},
	)
	if ev.PreviousPath != nil {
		prev := normalizeQPath(*ev.PreviousPath)
		prevParent, prevChild := splitParentAndName(prev)
		events = append(events, InvalidationEvent{
			Kind:             InvalidateEntry,
			ViewID:           t.viewID,
			Path:             prev,
			ParentDir:        prevParent,
			ChildName:        prevChild,
			SourceGeneration: gen,
			Reason:           string(ev.Kind),
		})
	}
	if ev.Kind == TrunkCreate || ev.Kind == TrunkDelete || ev.Kind == TrunkRename || ev.Kind == TrunkAtomicReplace {
		events = append(events, InvalidationEvent{
			Kind:             InvalidateDirectory,
			ViewID:           t.viewID,
			Path:             parentDir,
			ParentDir:        parentDir,
			ChildName:        childName,
			SourceGeneration: gen,
			Reason:           string(ev.Kind),
		})
	}
	return events
}

func splitParentAndName(qpath layeredfs.QualifiedPath) (layeredfs.QualifiedPath, string) {
	if qpath.Rel == "." || qpath.Rel == "" {
		return layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: "."}, "."
	}
	slash := strings.LastIndexByte(qpath.Rel, '/')
	if slash < 0 {
		return layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: "."}, qpath.Rel
	}
	return layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: qpath.Rel[:slash]}, qpath.Rel[slash+1:]
}

func sortQualifiedPaths(paths []layeredfs.QualifiedPath) {
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].RootID != paths[j].RootID {
			return paths[i].RootID < paths[j].RootID
		}
		return paths[i].Rel < paths[j].Rel
	})
}

func (t *Tracker) rootGenLocked(rootID string) uint64 {
	if gen, ok := t.rootGens[rootID]; ok && gen > 0 {
		return gen
	}
	return t.sourceGen
}

func normalizeQPath(qp layeredfs.QualifiedPath) layeredfs.QualifiedPath {
	rel := filepath.ToSlash(strings.TrimSpace(qp.Rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "."
	} else {
		rel = filepath.ToSlash(filepath.Clean(rel))
	}
	return layeredfs.QualifiedPath{RootID: qp.RootID, Rel: rel}
}

