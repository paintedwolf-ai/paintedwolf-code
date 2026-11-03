package verification

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetObservationTracker registers an observation.Tracker for a session ID or workspace root.
func (o *ObservationBindings) SetObservationTracker(key string, tracker *observation.Tracker) {
	if o == nil || strings.TrimSpace(key) == "" {
		return
	}
	key = strings.TrimSpace(key)
	o.obsMu.Lock()
	defer o.obsMu.Unlock()
	if o.obsTrackers == nil {
		o.obsTrackers = make(map[string]*observation.Tracker)
	}
	if tracker == nil {
		delete(o.obsTrackers, key)
		return
	}
	o.obsTrackers[key] = tracker
}

// ObservationTracker returns the registered observation.Tracker for key, if any.
func (o *ObservationBindings) ObservationTracker(key string) *observation.Tracker {
	if o == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	o.obsMu.RLock()
	tr := o.obsTrackers[key]
	o.obsMu.RUnlock()
	if tr != nil {
		return tr
	}
	if o.workspaceTrackers != nil && key != "" {
		tr, _ = o.workspaceTrackers.TrackerForSession(key, key, key, key)
		return tr
	}
	return nil
}

// SetActiveCheckpoint records the active private checkpoint ID for a session ID or workspace root.
func (o *ObservationBindings) SetActiveCheckpoint(key, checkpointID string) {
	if o == nil || strings.TrimSpace(key) == "" {
		return
	}
	o.obsMu.Lock()
	defer o.obsMu.Unlock()
	if o.obsCheckpoints == nil {
		o.obsCheckpoints = make(map[string]string)
	}
	o.obsCheckpoints[strings.TrimSpace(key)] = strings.TrimSpace(checkpointID)
}

// BindCheckObservation snapshots and binds the current observed sources and scope generation
// from the registered tracker to checkID and checkpointID.
func (o *ObservationBindings) BindCheckObservation(
	key string,
	checkID string,
	checkpointID string,
	scopeRoots []string,
) (observation.VerificationBinding, bool) {
	tracker := o.ObservationTracker(key)
	if tracker == nil || strings.TrimSpace(checkID) == "" {
		return observation.VerificationBinding{}, false
	}
	binding := tracker.BindVerificationReceipt(strings.TrimSpace(checkID), strings.TrimSpace(checkpointID), scopeRoots)
	o.RegisterCheckBinding(checkID, binding)
	if checkpointID != "" {
		o.SetActiveCheckpoint(key, checkpointID)
	}
	if o.workspaceTrackers != nil {
		o.workspaceTrackers.BindVerificationReceipt(key, key, key, key, checkID, checkpointID, scopeRoots)
	}
	return binding, true
}

// RegisterCheckBinding records an explicit VerificationBinding for checkID.
func (o *ObservationBindings) RegisterCheckBinding(checkID string, binding observation.VerificationBinding) {
	if o == nil || strings.TrimSpace(checkID) == "" {
		return
	}
	o.obsMu.Lock()
	defer o.obsMu.Unlock()
	if o.obsBindings == nil {
		o.obsBindings = make(map[string]observation.VerificationBinding)
	}
	o.obsBindings[strings.TrimSpace(checkID)] = binding
}

// EvaluateCheckFreshness evaluates whether a bound verification check receipt is stale due to
// observed dependency changes across directories/roots, watcher overflow, or incomplete attribution.
func (o *ObservationBindings) EvaluateCheckFreshness(
	ctx context.Context,
	sess *api.Session,
	checkID string,
) (observation.ReceiptFreshnessResult, observation.VerificationBinding, bool) {
	if o == nil || o.svc == nil {
		return observation.ReceiptFreshnessResult{}, observation.VerificationBinding{}, false
	}
	tracker, checkpointID := resolveSessionTracker(o.svc, ctx, sess)
	if tracker == nil {
		return observation.ReceiptFreshnessResult{}, observation.VerificationBinding{}, false
	}
	o.obsMu.RLock()
	binding, ok := o.obsBindings[strings.TrimSpace(checkID)]
	o.obsMu.RUnlock()
	if !ok && o.workspaceTrackers != nil && sess != nil {
		binding, ok = o.workspaceTrackers.BindingForCheck(workercontext.Job(ctx), sess.ID, o.svc.SourceEvidenceRoot(ctx, sess), checkID)
	}
	if !ok {
		return observation.ReceiptFreshnessResult{}, observation.VerificationBinding{}, false
	}
	return tracker.EvaluateReceiptFreshness(binding, checkpointID), binding, true
}

// PendingTrunkChangedNotice returns any pending trunk.changed notice for sess without granting authority.
func (o *ObservationBindings) PendingTrunkChangedNotice(ctx context.Context, sess *api.Session) (*observation.TrunkChangedNotice, bool) {
	if o == nil || o.svc == nil {
		return nil, false
	}
	tracker, _ := resolveSessionTracker(o.svc, ctx, sess)
	if tracker == nil {
		return nil, false
	}
	if notice, ok := tracker.PendingTrunkChangedNotice(); ok {
		return notice, true
	}
	return tracker.LastTrunkChangedNotice()
}

// PromotionPreviewHints returns structured stale-observation review hints for sess.
func (o *ObservationBindings) PromotionPreviewHints(ctx context.Context, sess *api.Session) []observation.StaleObservationHint {
	if o == nil || o.svc == nil {
		return nil
	}
	tracker, _ := resolveSessionTracker(o.svc, ctx, sess)
	if tracker == nil {
		return nil
	}
	return tracker.BuildPromotionPreviewHints()
}

func applyObservedFreshness(m *Service, ctx context.Context, sess *api.Session, check *SourceCheckObservation) {
	if m == nil || check == nil || check.CheckID == "" {
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

func checkStaleByObservation(m *Service, ctx context.Context, sess *api.Session, checkID string) bool {
	res, _, ok := m.EvaluateCheckFreshness(ctx, sess, checkID)
	return ok && res.Stale
}

func resolveSessionTracker(m *Service, ctx context.Context, sess *api.Session) (*observation.Tracker, string) {
	if m == nil || sess == nil {
		return nil, ""
	}
	jobID := workercontext.Job(ctx)
	root := m.SourceEvidenceRoot(ctx, sess)
	m.obsMu.RLock()
	for _, k := range []string{jobID, sess.ID, root, sess.WorkspacePath} {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if tr := m.obsTrackers[k]; tr != nil {
			ckpt := m.obsCheckpoints[k]
			m.obsMu.RUnlock()
			return tr, ckpt
		}
	}
	m.obsMu.RUnlock()
	if m.workspaceTrackers != nil {
		return m.workspaceTrackers.TrackerForSession(jobID, sess.ID, root, sess.WorkspacePath)
	}
	return nil, ""
}

func autoBindPassingRun(m *Service, ctx context.Context, sess *api.Session, checkID, revision string) {
	checkID = strings.TrimSpace(checkID)
	if m == nil || sess == nil || checkID == "" {
		return
	}
	tracker, ckpt := resolveSessionTracker(m, ctx, sess)
	if tracker == nil {
		return
	}
	if ckpt == "" {
		ckpt = strings.TrimSpace(revision)
	}
	binding := tracker.BindVerificationReceipt(checkID, ckpt, nil)
	m.RegisterCheckBinding(checkID, binding)
	if m.workspaceTrackers != nil {
		root := m.SourceEvidenceRoot(ctx, sess)
		m.workspaceTrackers.BindVerificationReceipt(workercontext.Job(ctx), sess.ID, sess.WorkspacePath, root, checkID, ckpt, nil)
	}
}
