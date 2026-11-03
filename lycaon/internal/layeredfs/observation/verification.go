package observation

import (
	"sort"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// BindVerificationReceipt creates a VerificationBinding bound to checkpointID, current observed
// sources, effective read attribution completeness, and enclosing scope source generations.
func (v *VerificationTracker) BindVerificationReceipt(
	receiptID string,
	checkpointID string,
	scopeRoots []string,
) VerificationBinding {
	t := v.tr
	t.mu.RLock()
	defer t.mu.RUnlock()

	observed := make([]SourceObservation, len(t.store.observations))
	copy(observed, t.store.observations)

	completeness := t.facts.AttributionCompleteness
	if completeness == "" {
		completeness = CompletenessUnknown
	}
	for _, obs := range observed {
		if obs.Layer == layeredfs.LayerTrunk && (obs.Completeness != CompletenessComplete || obs.Provenance == ProvenanceIndexDerived || obs.Uncertain) {
			if completeness == CompletenessComplete {
				completeness = CompletenessPartialKernelCached
			}
			break
		}
	}

	roots := append([]string(nil), scopeRoots...)
	if len(roots) == 0 {
		for rID := range t.rootsByID {
			roots = append(roots, rID)
		}
		sort.Strings(roots)
	}

	rootGens := make(map[string]uint64, len(roots))
	for _, rID := range roots {
		rootGens[rID] = rootGenLocked(t, rID)
	}

	return VerificationBinding{
		ReceiptID:                   receiptID,
		ViewID:                      t.viewID,
		CheckpointID:                checkpointID,
		ObservedSources:             observed,
		ReadAttributionCompleteness: completeness,
		EnclosingScopeRoots:         roots,
		ScopeSourceGeneration:       t.store.sourceGen,
		RootSourceGenerations:       rootGens,
		BoundAt:                     t.clock(),
	}
}

// EvaluateReceiptFreshness evaluates whether trunk changes, watcher overflows, or incomplete
// read attribution invalidate a verification receipt.
func (v *VerificationTracker) EvaluateReceiptFreshness(
	binding VerificationBinding,
	currentCheckpointID string,
) ReceiptFreshnessResult {
	t := v.tr
	t.mu.RLock()
	defer t.mu.RUnlock()

	res := ReceiptFreshnessResult{
		ReceiptID:           binding.ReceiptID,
		CheckpointID:        binding.CheckpointID,
		EvaluatedGeneration: t.store.sourceGen,
	}

	if binding.CheckpointID != "" && currentCheckpointID != "" && binding.CheckpointID != currentCheckpointID {
		res.Stale = true
		res.StaleReason = StaleReasonCheckpointMismatch
		return res
	}

	invalidPathsMap := make(map[layeredfs.QualifiedPath]struct{})
	obsRefsMap := make(map[string]struct{})
	hasOverflow := false
	hasObservedChange := false

	for _, obs := range binding.ObservedSources {
		qpath := normalizeQPath(obs.Path)
		if latestIdx, ok := t.store.obsByID[obs.ObservationID]; ok {
			obs = t.store.observations[latestIdx]
		}
		if obs.Layer != layeredfs.LayerTrunk || isPrivateOrRetainedPathLocked(t, qpath) {
			continue
		}
		if obs.Uncertain || obs.Provenance == ProvenanceUncertainOverflow {
			hasOverflow = true
			invalidPathsMap[qpath] = struct{}{}
			obsRefsMap[obs.ObservationID] = struct{}{}
			continue
		}
		if reason, stale := t.coherence.staleObsIDs[obs.ObservationID]; stale {
			if reason == StaleReasonWatcherOverflowUncertain {
				hasOverflow = true
			} else {
				hasObservedChange = true
			}
			invalidPathsMap[qpath] = struct{}{}
			obsRefsMap[obs.ObservationID] = struct{}{}
			continue
		}
		if trunkChangedSinceObservationLocked(t, qpath, obs) {
			hasObservedChange = true
			invalidPathsMap[qpath] = struct{}{}
			obsRefsMap[obs.ObservationID] = struct{}{}
		}
	}

	for _, rID := range effectiveScopeRootsLocked(t, binding) {
		if len(t.coherence.overflowByRoot[rID]) > 0 {
			hasOverflow = true
		}
	}

	if len(invalidPathsMap) > 0 {
		for p := range invalidPathsMap {
			res.InvalidatingPaths = append(res.InvalidatingPaths, p)
		}
		sortQualifiedPaths(res.InvalidatingPaths)
	}
	if len(obsRefsMap) > 0 {
		for ref := range obsRefsMap {
			res.ObservationRefs = append(res.ObservationRefs, ref)
		}
		sort.Strings(res.ObservationRefs)
	}
	res.UncertainOverflow = hasOverflow

	if hasObservedChange {
		res.Stale = true
		res.StaleReason = StaleReasonObservedDependencyChanged
		return res
	}
	if hasOverflow {
		res.Stale = true
		res.StaleReason = StaleReasonWatcherOverflowUncertain
		return res
	}
	if isAttributionIncomplete(binding) && scopeGenerationAdvancedLocked(t, binding) {
		res.Stale = true
		res.StaleReason = StaleReasonIncompleteAttributionScopeAdvanced
		return res
	}
	return res
}

// BuildPromotionPreviewHints returns stale-observation review hints for promotion preview (#1663).
// Hints never substitute for exact-base conflict checks or target revalidation.
func (v *VerificationTracker) BuildPromotionPreviewHints() []StaleObservationHint {
	t := v.tr
	t.mu.RLock()
	obsCopy := make([]SourceObservation, len(t.store.observations))
	copy(obsCopy, t.store.observations)
	t.mu.RUnlock()

	return v.BuildPromotionPreviewHintsForBinding(VerificationBinding{
		ViewID:          t.viewID,
		ObservedSources: obsCopy,
	})
}

// BuildPromotionPreviewHintsForBinding builds stale-observation review hints for a specific binding.
func (v *VerificationTracker) BuildPromotionPreviewHintsForBinding(binding VerificationBinding) []StaleObservationHint {
	t := v.tr
	t.mu.RLock()
	defer t.mu.RUnlock()

	var hints []StaleObservationHint
	seenObs := make(map[string]struct{})
	for _, obs := range binding.ObservedSources {
		if _, dup := seenObs[obs.ObservationID]; dup {
			continue
		}
		if latestIdx, ok := t.store.obsByID[obs.ObservationID]; ok {
			obs = t.store.observations[latestIdx]
		}
		qpath := normalizeQPath(obs.Path)
		if obs.Layer != layeredfs.LayerTrunk || isPrivateOrRetainedPathLocked(t, qpath) {
			continue
		}
		reason := t.coherence.staleObsIDs[obs.ObservationID]
		if reason == "" && obs.Uncertain {
			reason = StaleReasonWatcherOverflowUncertain
		}
		if reason == "" && trunkChangedSinceObservationLocked(t, qpath, obs) {
			reason = StaleReasonObservedDependencyChanged
		}
		if reason == "" {
			continue
		}
		seenObs[obs.ObservationID] = struct{}{}
		curState := t.coherence.trunkState[qpath]
		curGen := curState.generation
		if curGen == 0 {
			curGen = rootGenLocked(t, qpath.RootID)
		}
		hints = append(hints, StaleObservationHint{
			Path:                      qpath,
			ObservationID:             obs.ObservationID,
			ObservedSHA256:            obs.SHA256,
			CurrentSHA256:             curState.sha256,
			ObservedGeneration:        obs.SourceGeneration,
			CurrentGeneration:         curGen,
			Provenance:                obs.Provenance,
			Uncertain:                 obs.Uncertain,
			Reason:                    reason,
			SubstitutesExactBaseCheck: false,
		})
	}
	sort.Slice(hints, func(i, j int) bool {
		if hints[i].Path.RootID != hints[j].Path.RootID {
			return hints[i].Path.RootID < hints[j].Path.RootID
		}
		if hints[i].Path.Rel != hints[j].Path.Rel {
			return hints[i].Path.Rel < hints[j].Path.Rel
		}
		return hints[i].ObservationID < hints[j].ObservationID
	})
	return hints
}

func trunkChangedSinceObservationLocked(t *Tracker, qpath layeredfs.QualifiedPath, obs SourceObservation) bool {
	st, ok := t.coherence.trunkState[qpath]
	if !ok {
		return false
	}
	if st.deleted {
		return true
	}
	if obs.SHA256 != "" && st.sha256 != "" {
		return obs.SHA256 != st.sha256
	}
	if obs.Ino != 0 && st.ino != 0 && obs.Ino != st.ino {
		return true
	}
	if obs.Size != st.size {
		return true
	}
	if t.facts.UntouchedVisibility == VisibilityFixed && st.generation > 1 {
		return true
	}
	return st.generation > obs.SourceGeneration
}

func isAttributionIncomplete(binding VerificationBinding) bool {
	if binding.ReadAttributionCompleteness != CompletenessComplete {
		return true
	}
	if len(binding.ObservedSources) == 0 {
		return true
	}
	for _, obs := range binding.ObservedSources {
		if obs.Layer == layeredfs.LayerTrunk && (obs.Completeness != CompletenessComplete || obs.Provenance == ProvenanceIndexDerived || obs.Uncertain) {
			return true
		}
	}
	return false
}

func effectiveScopeRootsLocked(t *Tracker, binding VerificationBinding) []string {
	if len(binding.EnclosingScopeRoots) > 0 {
		return binding.EnclosingScopeRoots
	}
	roots := make([]string, 0, len(t.store.rootGens))
	for rID := range t.store.rootGens {
		roots = append(roots, rID)
	}
	sort.Strings(roots)
	return roots
}

func scopeGenerationAdvancedLocked(t *Tracker, binding VerificationBinding) bool {
	if len(binding.EnclosingScopeRoots) == 0 {
		return t.store.sourceGen > binding.ScopeSourceGeneration
	}
	for _, rID := range binding.EnclosingScopeRoots {
		curRootGen := rootGenLocked(t, rID)
		boundGen := binding.ScopeSourceGeneration
		if g, ok := binding.RootSourceGenerations[rID]; ok && g > 0 {
			boundGen = g
		}
		if curRootGen > boundGen {
			return true
		}
	}
	return false
}
