package observation

import (
	"sort"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// RecordTrunkChange processes a trunk mutation, updates source generation, marks affected
// trunk observations stale, and dispatches targeted adapter cache invalidations.
// Private paths and retained BaseCaptures remain immune to trunk edits.
func (c *CoherenceTracker) RecordTrunkChange(ev TrunkChangeEvent) []InvalidationEvent {
	t := c.tr
	qpath := normalizeQPath(ev.Path)
	t.mu.Lock()
	advanceGenerationLocked(t, qpath.RootID, ev.SourceGeneration)
	gen := rootGenLocked(t, qpath.RootID)
	populateTrunkChangeFromDiskLocked(t, &ev, qpath)

	t.coherence.trunkState[qpath] = recordedTrunkState{
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

	isPrivate := isPrivateOrRetainedPathLocked(t, qpath)
	var dispatched []InvalidationEvent
	if !isPrivate {
		markTrunkPathObservationsStaleLocked(t, qpath, ev)
		if ev.PreviousPath != nil {
			prev := normalizeQPath(*ev.PreviousPath)
			if !isPrivateOrRetainedPathLocked(t, prev) {
				markTrunkPathObservationsStaleLocked(t, prev, ev)
			}
		}
		dispatched = buildTrunkInvalidationsLocked(t, qpath, ev, gen)
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
func (c *CoherenceTracker) RecordWorkerMutation(qpath layeredfs.QualifiedPath, kind TrunkChangeKind) []InvalidationEvent {
	t := c.tr
	qpath = normalizeQPath(qpath)
	t.mu.Lock()
	_, wasNegative := t.store.negativeLookups[qpath]
	delete(t.store.negativeLookups, qpath)
	gen := rootGenLocked(t, qpath.RootID)
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
func (c *CoherenceTracker) RecordWatcherOverflow(rootID string, reason string) []InvalidationEvent {
	t := c.tr
	if reason == "" {
		reason = "watcher_queue_overflow"
	}
	t.mu.Lock()
	advanceGenerationLocked(t, rootID, 0)
	gen := t.store.sourceGen
	targetRoots := map[string]struct{}{}
	if rootID != "" {
		targetRoots[rootID] = struct{}{}
	} else {
		for id := range t.rootsByID {
			targetRoots[id] = struct{}{}
		}
		for _, obs := range t.store.observations {
			targetRoots[obs.Path.RootID] = struct{}{}
		}
	}
	for rID := range targetRoots {
		t.store.rootGens[rID] = gen
		t.coherence.overflowByRoot[rID] = append(t.coherence.overflowByRoot[rID], reason)
	}
	for i := range t.store.observations {
		obs := &t.store.observations[i]
		if obs.Layer != layeredfs.LayerTrunk {
			continue
		}
		if _, match := targetRoots[obs.Path.RootID]; !match && rootID != "" {
			continue
		}
		if isPrivateOrRetainedPathLocked(t, obs.Path) {
			continue
		}
		obs.Uncertain = true
		obs.Provenance = ProvenanceUncertainOverflow
		t.coherence.staleObsIDs[obs.ObservationID] = StaleReasonWatcherOverflowUncertain
		t.coherence.pendingNotice[obs.Path] = struct{}{}
		t.coherence.pendingObsRefs[obs.ObservationID] = struct{}{}
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
func (c *CoherenceTracker) PendingTrunkChangedNotice() (*TrunkChangedNotice, bool) {
	t := c.tr
	t.mu.RLock()
	defer t.mu.RUnlock()
	return buildNoticeLocked(t)
}

// LastTrunkChangedNotice returns the pending trunk.changed notice or the most recently consumed notice.
func (c *CoherenceTracker) LastTrunkChangedNotice() (*TrunkChangedNotice, bool) {
	t := c.tr
	t.mu.RLock()
	defer t.mu.RUnlock()
	if notice, ok := buildNoticeLocked(t); ok {
		return notice, true
	}
	if t.coherence.lastConsumedNotice != nil {
		cp := *t.coherence.lastConsumedNotice
		return &cp, true
	}
	return nil, false
}

// ConsumeTrunkChangedNotice returns and clears the pending trunk.changed notification for the next model request.
func (c *CoherenceTracker) ConsumeTrunkChangedNotice() (*TrunkChangedNotice, bool) {
	t := c.tr
	t.mu.Lock()
	defer t.mu.Unlock()
	notice, ok := buildNoticeLocked(t)
	if !ok {
		return nil, false
	}
	cp := *notice
	t.coherence.lastConsumedNotice = &cp
	t.coherence.pendingNotice = make(map[layeredfs.QualifiedPath]struct{})
	t.coherence.pendingObsRefs = make(map[string]struct{})
	return notice, true
}

func buildNoticeLocked(t *Tracker) (*TrunkChangedNotice, bool) {
	if len(t.coherence.pendingNotice) == 0 && len(t.coherence.overflowByRoot) == 0 {
		return nil, false
	}
	paths := make([]layeredfs.QualifiedPath, 0, len(t.coherence.pendingNotice))
	for p := range t.coherence.pendingNotice {
		paths = append(paths, p)
	}
	sortQualifiedPaths(paths)

	refs := make([]string, 0, len(t.coherence.pendingObsRefs))
	for ref := range t.coherence.pendingObsRefs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var reasons []string
	for _, rList := range t.coherence.overflowByRoot {
		reasons = append(reasons, rList...)
	}
	return &TrunkChangedNotice{
		EventType:            TrunkChangedEventType,
		ViewID:               t.viewID,
		AffectedPaths:        paths,
		ObservationRefs:      refs,
		UncertainOverflow:    len(reasons) > 0,
		OverflowReasons:      reasons,
		SourceGeneration:     t.store.sourceGen,
		GrantsAuthority:      false,
		EstablishesMergeBase: false,
	}, true
}
