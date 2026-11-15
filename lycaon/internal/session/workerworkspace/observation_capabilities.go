package workerworkspace

import (
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/workspace"
)

// ViewCapabilityDescriptor describes the selected workspace view adapter and its
// declared visibility, cache invalidation, and read-attribution capabilities.
type ViewCapabilityDescriptor = observation.ViewCapabilityDescriptor

// SetViewCapabilitiesResolver configures an optional resolver that supplies the bound
// ViewCapabilityDescriptor for a worker workspace when EnsureTracker is called.
func (r *WorkspaceTrackerRegistry) SetViewCapabilitiesResolver(
	resolver func(jobID, childSessionID, workspacePath, branchRoot string) (observation.ViewCapabilityDescriptor, bool),
) {
	if r == nil {
		return
	}
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	r.capsResolver = resolver
}

// BindPreparedView binds a host-prepared *adapter.PreparedView (#1658) and its Core/AdapterFacts
// to the workspace tracker for keys.
func (r *WorkspaceTrackerRegistry) BindPreparedView(
	view *adapter.PreparedView,
	keys ...string,
) (*observation.Tracker, error) {
	if r == nil || view == nil {
		return nil, nil
	}
	allKeys := append([]string{view.ViewID}, keys...)
	tr, err := r.BindViewCapabilities(observation.ViewCapabilitiesFromPreparedView(view), allKeys...)
	if err != nil {
		return nil, err
	}
	if tr != nil && view.Core != nil {
		tr.BindCore(view.Core)
	}
	return tr, nil
}

// BindPreparedBranch resolves any prepared workspace view or durable AdapterFacts recorded
// for branchRoot (#1658) and binds its capability facts to the workspace tracker.
func (r *WorkspaceTrackerRegistry) BindPreparedBranch(
	branchRoot string,
	keys ...string,
) (*observation.Tracker, error) {
	if r == nil || strings.TrimSpace(branchRoot) == "" {
		return nil, nil
	}
	view, facts, viewID, ok := workspace.LookupBranchPreparedView(branchRoot)
	if !ok {
		return nil, nil
	}
	allKeys := append([]string{branchRoot, viewID}, keys...)
	if view != nil {
		return r.BindPreparedView(view, allKeys...)
	}
	return r.BindViewCapabilities(observation.ViewCapabilitiesFromAdapterFacts(viewID, facts), allKeys...)
}

// BindViewCapabilities binds the selected adapter's ViewCapabilityDescriptor to the workspace tracker for keys.
func (r *WorkspaceTrackerRegistry) BindViewCapabilities(
	caps observation.ViewCapabilityDescriptor,
	keys ...string,
) (*observation.Tracker, error) {
	if r == nil {
		return nil, nil
	}
	facts := observation.VisibilityFactsFromDescriptor(caps)
	if err := observation.ValidateVisibilityFacts(facts); err != nil {
		return nil, err
	}

	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.trackers == nil {
		r.trackers = make(map[string]*WorkspaceObservationState)
	}
	allKeys := append([]string{caps.ViewID}, keys...)
	indexCapabilitiesLocked(r, caps, allKeys...)

	st := lookupStateLocked(r, allKeys...)
	if st == nil {
		viewID := firstNonEmpty(allKeys...)
		if viewID == "" {
			return nil, nil
		}
		tr, err := observation.NewTracker(observation.TrackerConfig{
			ViewID: viewID,
			Facts:  facts,
		})
		if err != nil {
			return nil, err
		}
		st = &WorkspaceObservationState{
			Tracker:           tr,
			CapabilitiesBound: true,
			ViewCapabilities:  caps,
			Bindings:          make(map[string]observation.VerificationBinding),
		}
		indexStateLocked(r, st, allKeys...)
		return st.Tracker, nil
	}

	st.CapabilitiesBound = true
	st.ViewCapabilities = caps
	if st.Tracker == nil {
		viewID := firstNonEmpty(append(allKeys, st.JobID, st.ChildSessionID, st.BranchRoot, st.WorkspacePath, "workspace-view")...)
		tr, err := observation.NewTracker(observation.TrackerConfig{
			ViewID: viewID,
			Facts:  facts,
			Roots:  buildRootSpecs(st.WorkspacePath, st.Roots),
		})
		if err != nil {
			return nil, err
		}
		st.Tracker = tr
	} else if err := st.Tracker.SetVisibilityFacts(facts); err != nil {
		return nil, err
	}
	indexStateLocked(r, st, allKeys...)
	indexCapabilitiesLocked(r, caps, st.JobID, st.ChildSessionID, st.BranchRoot)
	return st.Tracker, nil
}

// BindVisibilityFacts binds explicit observation.VisibilityFacts to the workspace tracker for keys.
func (r *WorkspaceTrackerRegistry) BindVisibilityFacts(
	facts observation.VisibilityFacts,
	keys ...string,
) (*observation.Tracker, error) {
	facts = observation.NormalizeVisibilityFacts(facts)
	if err := observation.ValidateVisibilityFacts(facts); err != nil {
		return nil, err
	}
	return r.BindViewCapabilities(observation.ViewCapabilityDescriptor{
		AdapterName:                        facts.AdapterName,
		Mode:                               facts.Mode,
		UntouchedVisibility:                facts.UntouchedVisibility,
		LiveVisible:                        facts.LiveVisible,
		SupportsInvalidation:               facts.SupportsInvalidation,
		SupportsInvalidationUpcall:         facts.SupportsInvalidationUpcall,
		SupportsNegativeLookupInvalidation: facts.SupportsNegativeLookupInvalidation,
		SupportsDirCookieInvalidation:      facts.SupportsDirCookieInvalidation,
		InvalidationVerified:               facts.InvalidationVerified,
		Completeness:                       facts.Completeness,
		AttributionCompleteness:            facts.AttributionCompleteness,
	}, keys...)
}

func resolvePreparedBranchViewCapabilities(
	jobID, childSessionID, workspacePath, branchRoot string,
) (observation.ViewCapabilityDescriptor, bool) {
	for _, candidate := range []string{branchRoot, workspacePath} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		_, facts, viewID, ok := workspace.LookupBranchPreparedView(candidate)
		if ok {
			return observation.ViewCapabilitiesFromAdapterFacts(firstNonEmpty(jobID, viewID, childSessionID), facts), true
		}
	}
	return observation.ViewCapabilityDescriptor{}, false
}

func resolveCapabilitiesLocked(
	r *WorkspaceTrackerRegistry,
	st *WorkspaceObservationState,
	jobID, childSessionID, workspacePath, branchRoot string,
) (observation.ViewCapabilityDescriptor, bool) {
	if st != nil && st.CapabilitiesBound {
		return st.ViewCapabilities, true
	}
	for _, raw := range []string{jobID, childSessionID, branchRoot, workspacePath} {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		if caps, ok := r.boundCaps[k]; ok {
			return caps, true
		}
		if cleaned := cleanOptionalPath(k); cleaned != "" {
			if caps, ok := r.boundCaps[cleaned]; ok {
				return caps, true
			}
		}
	}
	if r.capsResolver != nil {
		if caps, ok := r.capsResolver(jobID, childSessionID, workspacePath, branchRoot); ok {
			return caps, true
		}
	}
	return resolvePreparedBranchViewCapabilities(jobID, childSessionID, workspacePath, branchRoot)
}

func indexCapabilitiesLocked(
	r *WorkspaceTrackerRegistry,
	caps observation.ViewCapabilityDescriptor,
	keys ...string,
) {
	if r == nil {
		return
	}
	if r.boundCaps == nil {
		r.boundCaps = make(map[string]observation.ViewCapabilityDescriptor)
	}
	for _, raw := range keys {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		r.boundCaps[k] = caps
		if cleaned := cleanOptionalPath(k); cleaned != "" {
			r.boundCaps[cleaned] = caps
		}
	}
}
