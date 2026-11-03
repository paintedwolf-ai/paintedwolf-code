package observation

import (
	"sort"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// RecordTrunkChange processes a trunk mutation, updates source generation, marks affected
// trunk observations stale, and dispatches targeted adapter cache invalidations.
// Private paths and retained BaseCaptures remain immune to trunk edits.
func (t *Tracker) RecordTrunkChange(ev TrunkChangeEvent) []InvalidationEvent {
	qpath := normalizeQPath(ev.Path)
	t.mu.Lock()
	t.advanceGenerationLocked(qpath.RootID, ev.SourceGeneration)
	gen := t.rootGenLocked(qpath.RootID)
	t.populateTrunkChangeFromDiskLocked(&ev, qpath)

	t.trunkState[qpath] = recordedTrunkState{
		kind:       ev.Kind,
		sha256:     ev.NewSHA256,
		size:       ev.NewSize,
		modTimeNS:  ev.NewModTimeNS,
		ino:        ev.NewIno,
		dev:        ev.NewDev,
		generation: gen,
		deleted:    ev.Kind == TrunkDelete,
		updatedAt:  t.clock(),
	}

	isPrivate := t.isPrivateOrRetainedPathLocked(qpath)
	var dispatched []InvalidationEvent
	if !isPrivate {
		t.markTrunkPathObservationsStaleLocked(qpath, ev)
		if ev.PreviousPath != nil {
			prev := normalizeQPath(*ev.PreviousPath)
			if !t.isPrivateOrRetainedPathLocked(prev) {
				t.markTrunkPathObservationsStaleLocked(prev, ev)
			}
		}
		dispatched = t.buildTrunkInvalidationsLocked(qpath, ev, gen)
	}
	sinks := append([]InvalidationSink(nil), t.invalidationSinks...)
	t.mu.Unlock()

	for _, inv := range dispatched {
		for _, sink := range sinks {
			sink.Invalidate(inv)
		}
	}
	return dispatched
}

// RecordWorkerMutation records a worker-initiated mutation through the mount and dispatches
// immediate cache invalidations for negative lookups, same-size changes, and directory listings.
func (t *Tracker) RecordWorkerMutation(qpath layeredfs.QualifiedPath, kind TrunkChangeKind) []InvalidationEvent {
	qpath = normalizeQPath(qpath)
	t.mu.Lock()
	_, wasNegative := t.negativeLookups[qpath]
	delete(t.negativeLookups, qpath)
	gen := t.rootGenLocked(qpath.RootID)
	var events []InvalidationEvent
	if t.facts.SupportsInvalidationUpcall {
		parentDir, childName := splitParentAndName(qpath)
		if (wasNegative || kind == TrunkCreate) && t.facts.SupportsNegativeLookupInvalidation {
			events = append(events, InvalidationEvent{
				Kind:             InvalidateNegativeName,
				ViewID:           t.viewID,
				Path:             qpath,
				ParentDir:        parentDir,
				ChildName:        childName,
				SourceGeneration: gen,
				Reason:           "worker_mutation",
			})
		}
		events = append(events,
			InvalidationEvent{Kind: InvalidateAttr, ViewID: t.viewID, Path: qpath, SourceGeneration: gen, Reason: "worker_mutation"},
			InvalidationEvent{Kind: InvalidateEntry, ViewID: t.viewID, Path: qpath, ParentDir: parentDir, ChildName: childName, SourceGeneration: gen, Reason: "worker_mutation"},
		)
		if kind == TrunkCreate || kind == TrunkDelete || kind == TrunkRename || kind == TrunkAtomicReplace {
			events = append(events, InvalidationEvent{
				Kind:             InvalidateDirectory,
				ViewID:           t.viewID,
				Path:             parentDir,
				ParentDir:        parentDir,
				ChildName:        childName,
				SourceGeneration: gen,
				Reason:           "worker_directory_mutation",
			})
		}
	}
	sinks := append([]InvalidationSink(nil), t.invalidationSinks...)
	t.mu.Unlock()

	for _, inv := range events {
		for _, sink := range sinks {
			sink.Invalidate(inv)
		}
	}
	return events
}

// RecordWatcherOverflow marks affected trunk observations as uncertain due to a watcher gap
// or queue overflow and triggers adapter cache invalidation/revalidation.
func (t *Tracker) RecordWatcherOverflow(rootID string, reason string) []InvalidationEvent {
	if reason == "" {
		reason = "watcher_queue_overflow"
	}
	t.mu.Lock()
	t.advanceGenerationLocked(rootID, 0)
	gen := t.sourceGen
	targetRoots := map[string]struct{}{}
	if rootID != "" {
		targetRoots[rootID] = struct{}{}
	} else {
		for id := range t.rootsByID {
			targetRoots[id] = struct{}{}
		}
		for _, obs := range t.observations {
			targetRoots[obs.Path.RootID] = struct{}{}
		}
	}
	for rID := range targetRoots {
		t.rootGens[rID] = gen
		t.overflowByRoot[rID] = append(t.overflowByRoot[rID], reason)
	}
	for i := range t.observations {
		obs := &t.observations[i]
		if obs.Layer != layeredfs.LayerTrunk {
			continue
		}
		if _, match := targetRoots[obs.Path.RootID]; !match && rootID != "" {
			continue
		}
		if t.isPrivateOrRetainedPathLocked(obs.Path) {
			continue
		}
		obs.Uncertain = true
		obs.Provenance = ProvenanceUncertainOverflow
		obs.UncertaintyReason = reason
		t.staleObsIDs[obs.ObservationID] = StaleReasonWatcherOverflowUncertain
		t.pendingNotice[obs.Path] = struct{}{}
		t.pendingObsRefs[obs.ObservationID] = struct{}{}
	}
	var events []InvalidationEvent
	if t.facts.UntouchedVisibility == VisibilityLive && t.facts.SupportsInvalidationUpcall {
		for rID := range targetRoots {
			events = append(events, InvalidationEvent{
				Kind:             InvalidateAllUncertain,
				ViewID:           t.viewID,
				Path:             layeredfs.QualifiedPath{RootID: rID, Rel: "."},
				SourceGeneration: gen,
				Reason:           reason,
			})
		}
	}
	sinks := append([]InvalidationSink(nil), t.invalidationSinks...)
	t.mu.Unlock()

	for _, inv := range events {
		for _, sink := range sinks {
			sink.Invalidate(inv)
		}
	}
	return events
}

// PendingTrunkChangedNotice returns the structured trunk.changed notification without consuming it.
func (t *Tracker) PendingTrunkChangedNotice() (*TrunkChangedNotice, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.buildNoticeLocked()
}

// ConsumeTrunkChangedNotice returns and clears the pending trunk.changed notification for the next model request.
func (t *Tracker) ConsumeTrunkChangedNotice() (*TrunkChangedNotice, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	notice, ok := t.buildNoticeLocked()
	if !ok {
		return nil, false
	}
	t.pendingNotice = make(map[layeredfs.QualifiedPath]struct{})
	t.pendingObsRefs = make(map[string]struct{})
	return notice, true
}

func (t *Tracker) buildNoticeLocked() (*TrunkChangedNotice, bool) {
	if len(t.pendingNotice) == 0 && len(t.overflowByRoot) == 0 {
		return nil, false
	}
	paths := make([]layeredfs.QualifiedPath, 0, len(t.pendingNotice))
	for p := range t.pendingNotice {
		paths = append(paths, p)
	}
	sortQualifiedPaths(paths)

	refs := make([]string, 0, len(t.pendingObsRefs))
	for ref := range t.pendingObsRefs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var reasons []string
	for _, rList := range t.overflowByRoot {
		reasons = append(reasons, rList...)
	}
	return &TrunkChangedNotice{
		EventType:            TrunkChangedEventType,
		ViewID:               t.viewID,
		AffectedPaths:        paths,
		ObservationRefs:      refs,
		UncertainOverflow:    len(reasons) > 0,
		OverflowReasons:      reasons,
		SourceGeneration:     t.sourceGen,
		GrantsAuthority:      false,
		EstablishesMergeBase: false,
	}, true
}
