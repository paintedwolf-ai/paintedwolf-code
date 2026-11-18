package verification

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	if m.workspaceTrackers == nil {
		return nil, ""
	}
	return m.workspaceTrackers.TrackerForSession(workercontext.Job(ctx), sess.ID, m.SourceEvidenceRoot(ctx, sess), sess.WorkspacePath)
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
