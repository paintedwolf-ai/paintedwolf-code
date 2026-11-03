package verification

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetObservationTracker registers an observation.Tracker for a session ID or workspace root.
func (m *Service) SetObservationTracker(key string, tracker *observation.Tracker) {
	if m == nil || strings.TrimSpace(key) == "" {
		return
	}
	key = strings.TrimSpace(key)
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if m.obsTrackers == nil {
		m.obsTrackers = make(map[string]*observation.Tracker)
	}
	if tracker == nil {
		delete(m.obsTrackers, key)
		return
	}
	m.obsTrackers[key] = tracker
}

// ObservationTracker returns the registered observation.Tracker for key, if any.
func (m *Service) ObservationTracker(key string) *observation.Tracker {
	if m == nil {
		return nil
	}
	m.obsMu.RLock()
	defer m.obsMu.RUnlock()
	return m.obsTrackers[strings.TrimSpace(key)]
}

// SetActiveCheckpoint records the active private checkpoint ID for a session ID or workspace root.
func (m *Service) SetActiveCheckpoint(key, checkpointID string) {
	if m == nil || strings.TrimSpace(key) == "" {
		return
	}
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if m.obsCheckpoints == nil {
		m.obsCheckpoints = make(map[string]string)
	}
	m.obsCheckpoints[strings.TrimSpace(key)] = strings.TrimSpace(checkpointID)
}

// BindCheckObservation snapshots and binds the current observed sources and scope generation
// from the registered tracker to checkID and checkpointID.
func (m *Service) BindCheckObservation(
	key string,
	checkID string,
	checkpointID string,
	scopeRoots []string,
) (observation.VerificationBinding, bool) {
	tracker := m.ObservationTracker(key)
	if tracker == nil || strings.TrimSpace(checkID) == "" {
		return observation.VerificationBinding{}, false
	}
	binding := tracker.BindVerificationReceipt(strings.TrimSpace(checkID), strings.TrimSpace(checkpointID), scopeRoots)
	m.RegisterCheckBinding(checkID, binding)
	if checkpointID != "" {
		m.SetActiveCheckpoint(key, checkpointID)
	}
	return binding, true
}

// RegisterCheckBinding records an explicit VerificationBinding for checkID.
func (m *Service) RegisterCheckBinding(checkID string, binding observation.VerificationBinding) {
	if m == nil || strings.TrimSpace(checkID) == "" {
		return
	}
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if m.obsBindings == nil {
		m.obsBindings = make(map[string]observation.VerificationBinding)
	}
	m.obsBindings[strings.TrimSpace(checkID)] = binding
}

// EvaluateCheckFreshness evaluates whether a bound verification check receipt is stale due to
// observed dependency changes across directories/roots, watcher overflow, or incomplete attribution.
func (m *Service) EvaluateCheckFreshness(
	ctx context.Context,
	sess *api.Session,
	checkID string,
) (observation.ReceiptFreshnessResult, observation.VerificationBinding, bool) {
	tracker, checkpointID := m.resolveSessionTracker(ctx, sess)
	if tracker == nil {
		return observation.ReceiptFreshnessResult{}, observation.VerificationBinding{}, false
	}
	m.obsMu.RLock()
	binding, ok := m.obsBindings[strings.TrimSpace(checkID)]
	m.obsMu.RUnlock()
	if !ok {
		return observation.ReceiptFreshnessResult{}, observation.VerificationBinding{}, false
	}
	return tracker.EvaluateReceiptFreshness(binding, checkpointID), binding, true
}

// PendingTrunkChangedNotice returns any pending trunk.changed notice for sess without granting authority.
func (m *Service) PendingTrunkChangedNotice(ctx context.Context, sess *api.Session) (*observation.TrunkChangedNotice, bool) {
	tracker, _ := m.resolveSessionTracker(ctx, sess)
	if tracker == nil {
		return nil, false
	}
	return tracker.PendingTrunkChangedNotice()
}

// PromotionPreviewHints returns structured stale-observation review hints for sess.
func (m *Service) PromotionPreviewHints(ctx context.Context, sess *api.Session) []observation.StaleObservationHint {
	tracker, _ := m.resolveSessionTracker(ctx, sess)
	if tracker == nil {
		return nil
	}
	return tracker.BuildPromotionPreviewHints()
}

func (m *Service) applyObservedFreshness(ctx context.Context, sess *api.Session, check *SourceCheckObservation) {
	if check == nil || check.CheckID == "" {
		return
	}
	res, binding, ok := m.EvaluateCheckFreshness(ctx, sess, check.CheckID)
	if !ok {
		return
	}
	check.AttributionCompleteness = binding.ReadAttributionCompleteness
	check.UncertainOverflow = res.UncertainOverflow
	if res.Stale {
		check.Match = SourceChanged
		check.StaleReason = res.StaleReason
		check.InvalidatingPaths = res.InvalidatingPaths
		check.ObservationRefs = res.ObservationRefs
	}
}

func (m *Service) checkStaleByObservation(ctx context.Context, sess *api.Session, checkID string) bool {
	res, _, ok := m.EvaluateCheckFreshness(ctx, sess, checkID)
	return ok && res.Stale
}

func (m *Service) resolveSessionTracker(ctx context.Context, sess *api.Session) (*observation.Tracker, string) {
	if m == nil || sess == nil {
		return nil, ""
	}
	root := m.SourceEvidenceRoot(ctx, sess)
	m.obsMu.RLock()
	defer m.obsMu.RUnlock()
	for _, k := range []string{sess.ID, root, sess.WorkspacePath} {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if tr := m.obsTrackers[k]; tr != nil {
			return tr, m.obsCheckpoints[k]
		}
	}
	return nil, ""
}
