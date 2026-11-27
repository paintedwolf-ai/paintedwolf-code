package watchdiag

import (
	"strings"
	"time"
)

// Evaluate runs the four typed wait/watch contradiction checks against an
// immutable Observation. Benign operational states (healthy lag, retention
// gaps, late receipt lookups, healthy coalescing, and SSE reconnect repaints)
// raise zero contradictions.
func Evaluate(obs Observation, now time.Time) []Finding {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []Finding
	if f, ok := checkObservationIncomplete(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	if f, ok := checkWrongRecipient(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	if f, ok := checkDuplicateCommittedDelivery(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	if f, ok := checkDuplicateCommittedPresentation(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	if f, ok := checkTerminalDeliveryBeforeReport(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	if f, ok := checkWatchTurnPastStopFence(obs, now); ok {
		out = append(out, sanitizeFinding(f))
	}
	return out
}

func checkObservationIncomplete(obs Observation, now time.Time) (Finding, bool) {
	if !obs.Truncated && strings.TrimSpace(obs.IncompleteReason) == "" {
		return Finding{}, false
	}
	reason := strings.TrimSpace(obs.IncompleteReason)
	if reason == "" {
		reason = "truncated_watch_observation"
	}
	f := baseFinding(obs, now, RuleObservationLoss, DispositionIncomplete)
	f.Reason = reason
	f.Expected = map[string]any{"complete_watch_observation": true, "truncated": false}
	f.Observed = map[string]any{"truncated": obs.Truncated, "incomplete_reason": reason}
	return f, true
}

func checkDuplicateCommittedDelivery(obs Observation, now time.Time) (Finding, bool) {
	grouped := make(map[presentationIdentity][]DeliveryFact)
	var order []presentationIdentity

	for _, d := range obs.Deliveries {
		if !d.Committed {
			continue
		}
		watchKey := strings.TrimSpace(firstNonEmpty(d.WatchID, d.Handle, obs.WatchID, obs.Handle))
		occKey := strings.TrimSpace(d.OccurrenceID)
		if watchKey == "" || occKey == "" {
			continue
		}
		key := presentationIdentity{watchKey: watchKey, occurrenceKey: occKey}
		if _, exists := grouped[key]; !exists {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], d)
	}

	var sources []SourceRef
	var dupGroups, maxCommits int
	var firstKey presentationIdentity

	for _, key := range order {
		items := grouped[key]
		if len(items) <= 1 {
			continue
		}
		if dupGroups == 0 {
			firstKey = key
		}
		dupGroups++
		if len(items) > maxCommits {
			maxCommits = len(items)
		}
		for _, d := range items {
			sources = append(sources, SourceRef{
				SessionID:    firstNonEmpty(d.RoutedSessionID, d.OwnerSessionID, obs.SessionID),
				WorkerJobID:  firstNonEmpty(d.RoutedWorkerJobID, d.OwnerWorkerJobID, obs.WorkerJobID),
				WatchID:      firstNonEmpty(d.WatchID, obs.WatchID),
				Handle:       firstNonEmpty(d.Handle, obs.Handle),
				OccurrenceID: d.OccurrenceID,
				EvidenceRef:  firstNonEmpty(d.EvidenceRef, d.DeliveryID),
				OwningSource: resolveOwningSource(d.OwningSource, firstClock(d.ClockDomain, obs.ClockDomain), "watch_delivery", "delivery_boundary_commit"),
			})
		}
	}

	if dupGroups == 0 {
		return Finding{}, false
	}

	first := sources[0]
	f := baseFinding(obs, now, RuleDuplicateCommittedDelivery, DispositionContradiction)
	f.SessionID = firstNonEmpty(first.SessionID, f.SessionID)
	f.WorkerJobID = firstNonEmpty(first.WorkerJobID, f.WorkerJobID)
	f.WatchID = firstNonEmpty(first.WatchID, f.WatchID)
	f.Handle = firstNonEmpty(first.Handle, f.Handle)
	f.OccurrenceID = first.OccurrenceID
	f.EvidenceRef = first.EvidenceRef
	f.OwningSource = first.OwningSource
	f.Sources = sources
	f.Reason = "duplicate_committed_delivery_rows"
	f.Expected = map[string]any{
		"max_committed_deliveries_per_occurrence": 1,
		"watch_key":      firstKey.watchKey,
		"occurrence_key": firstKey.occurrenceKey,
	}
	f.Observed = map[string]any{
		"duplicate_groups":        dupGroups,
		"max_committed_count":     maxCommits,
		"total_committed_records": len(sources),
	}
	return f, true
}

func baseFinding(obs Observation, now time.Time, rule RuleID, disp Disposition) Finding {
	return Finding{
		SchemaVersion: SchemaVersion,
		Timestamp:     now.UTC(),
		RuleID:        rule,
		Disposition:   disp,
		SessionID:     obs.SessionID,
		WorkerJobID:   obs.WorkerJobID,
		WatchID:       obs.WatchID,
		Handle:        obs.Handle,
		OwningSource:  resolveOwningSource(obs.OwningSource, obs.ClockDomain, "", ""),
	}
}

func resolveOwningSource(src OwningSource, fallbackClock ClockDomain, fallbackComponent, fallbackPhase string) OwningSource {
	if src.Component == "" {
		src.Component = fallbackComponent
	}
	if src.Phase == "" {
		src.Phase = fallbackPhase
	}
	if src.ClockDomain == "" {
		src.ClockDomain = fallbackClock
	}
	return src
}

func recipientMismatch(ownerSess, ownerJob, routedSess, routedJob string) (string, bool) {
	ownerSess = strings.TrimSpace(ownerSess)
	ownerJob = strings.TrimSpace(ownerJob)
	routedSess = strings.TrimSpace(routedSess)
	routedJob = strings.TrimSpace(routedJob)

	if ownerSess != "" && routedSess != "" && ownerSess != routedSess {
		return "session_id_mismatch", true
	}
	if ownerJob != "" && routedJob != "" && ownerJob != routedJob {
		return "worker_job_id_mismatch", true
	}
	if ownerJob == "" && routedJob != "" {
		return "unexpected_worker_job_recipient", true
	}
	if ownerJob != "" && routedSess != "" && routedJob == "" {
		return "missing_worker_job_recipient", true
	}
	return "", false
}

func checkWrongRecipient(obs Observation, now time.Time) (Finding, bool) {
	var sources []SourceRef
	var reason string
	var expectedSess, expectedJob, observedSess, observedJob string

	recordMismatch := func(mismatch, oSess, oJob, rSess, rJob string, ref SourceRef) {
		if reason == "" {
			reason = mismatch
			expectedSess = oSess
			expectedJob = oJob
			observedSess = rSess
			observedJob = rJob
		}
		sources = append(sources, ref)
	}

	for _, d := range obs.Deliveries {
		ownerSess := firstNonEmpty(d.OwnerSessionID, obs.SessionID)
		ownerJob := firstNonEmpty(d.OwnerWorkerJobID, obs.WorkerJobID)
		if mismatch, bad := recipientMismatch(ownerSess, ownerJob, d.RoutedSessionID, d.RoutedWorkerJobID); bad {
			recordMismatch(mismatch, ownerSess, ownerJob, d.RoutedSessionID, d.RoutedWorkerJobID, SourceRef{
				SessionID:    d.RoutedSessionID,
				WorkerJobID:  d.RoutedWorkerJobID,
				WatchID:      firstNonEmpty(d.WatchID, obs.WatchID),
				Handle:       firstNonEmpty(d.Handle, obs.Handle),
				OccurrenceID: d.OccurrenceID,
				EvidenceRef:  firstNonEmpty(d.EvidenceRef, d.DeliveryID),
				OwningSource: resolveOwningSource(d.OwningSource, firstClock(d.ClockDomain, obs.ClockDomain), "watch_delivery", "delivery_route"),
			})
		}
	}

	for _, p := range obs.Presentations {
		if p.IsHistoryReread || p.IsReconnectRepaint || p.IsLateReceiptLookup {
			continue
		}
		ownerSess := firstNonEmpty(p.OwnerSessionID, obs.SessionID)
		ownerJob := firstNonEmpty(p.OwnerWorkerJobID, obs.WorkerJobID)
		if mismatch, bad := recipientMismatch(ownerSess, ownerJob, p.RoutedSessionID, p.RoutedWorkerJobID); bad {
			recordMismatch(mismatch, ownerSess, ownerJob, p.RoutedSessionID, p.RoutedWorkerJobID, SourceRef{
				SessionID:    p.RoutedSessionID,
				WorkerJobID:  p.RoutedWorkerJobID,
				WatchID:      firstNonEmpty(p.WatchID, obs.WatchID),
				Handle:       firstNonEmpty(p.Handle, obs.Handle),
				OccurrenceID: p.OccurrenceID,
				ClaimID:      p.ClaimID,
				TurnID:       p.TurnID,
				EvidenceRef:  p.EvidenceRef,
				OwningSource: resolveOwningSource(p.OwningSource, firstClock(p.ClockDomain, obs.ClockDomain), "watch_presentation", "presentation_route"),
			})
		}
	}

	if len(sources) == 0 {
		return Finding{}, false
	}

	first := sources[0]
	f := baseFinding(obs, now, RuleWrongRecipient, DispositionContradiction)
	f.SessionID = firstNonEmpty(expectedSess, f.SessionID)
	f.WorkerJobID = firstNonEmpty(expectedJob, f.WorkerJobID)
	f.WatchID = firstNonEmpty(first.WatchID, f.WatchID)
	f.Handle = firstNonEmpty(first.Handle, f.Handle)
	f.OccurrenceID = first.OccurrenceID
	f.ClaimID = first.ClaimID
	f.TurnID = first.TurnID
	f.EvidenceRef = first.EvidenceRef
	f.OwningSource = first.OwningSource
	f.Sources = sources
	f.Reason = reason
	f.Expected = map[string]any{
		"owner_session_id":    sanitizeSessionID(expectedSess),
		"owner_worker_job_id": expectedJob,
	}
	f.Observed = map[string]any{
		"routed_session_id":    sanitizeSessionID(observedSess),
		"routed_worker_job_id": observedJob,
		"mismatch_count":       len(sources),
	}
	return f, true
}

type presentationIdentity struct {
	watchKey      string
	occurrenceKey string
}

func checkDuplicateCommittedPresentation(obs Observation, now time.Time) (Finding, bool) {
	grouped := make(map[presentationIdentity][]PresentationFact)
	var order []presentationIdentity

	for _, p := range obs.Presentations {
		if !p.Committed || p.IsHistoryReread || p.IsReconnectRepaint || p.IsLateReceiptLookup {
			continue
		}
		watchKey := strings.TrimSpace(firstNonEmpty(p.WatchID, p.Handle, obs.WatchID, obs.Handle))
		occKey := strings.TrimSpace(firstNonEmpty(p.OccurrenceID, p.ClaimID))
		if watchKey == "" || occKey == "" {
			continue
		}
		key := presentationIdentity{watchKey: watchKey, occurrenceKey: occKey}
		if _, exists := grouped[key]; !exists {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], p)
	}

	var sources []SourceRef
	var dupGroups, maxCommits int
	var firstKey presentationIdentity

	for _, key := range order {
		items := grouped[key]
		if len(items) <= 1 {
			continue
		}
		if dupGroups == 0 {
			firstKey = key
		}
		dupGroups++
		if len(items) > maxCommits {
			maxCommits = len(items)
		}
		for _, p := range items {
			sources = append(sources, SourceRef{
				SessionID:    firstNonEmpty(p.RoutedSessionID, p.OwnerSessionID, obs.SessionID),
				WorkerJobID:  firstNonEmpty(p.RoutedWorkerJobID, p.OwnerWorkerJobID, obs.WorkerJobID),
				WatchID:      firstNonEmpty(p.WatchID, obs.WatchID),
				Handle:       firstNonEmpty(p.Handle, obs.Handle),
				OccurrenceID: p.OccurrenceID,
				ClaimID:      p.ClaimID,
				TurnID:       p.TurnID,
				EvidenceRef:  p.EvidenceRef,
				OwningSource: resolveOwningSource(p.OwningSource, firstClock(p.ClockDomain, obs.ClockDomain), "watch_presentation", "presentation_claim_commit"),
			})
		}
	}

	if dupGroups == 0 {
		return Finding{}, false
	}

	first := sources[0]
	f := baseFinding(obs, now, RuleDuplicateCommittedPresentation, DispositionContradiction)
	f.SessionID = firstNonEmpty(first.SessionID, f.SessionID)
	f.WorkerJobID = firstNonEmpty(first.WorkerJobID, f.WorkerJobID)
	f.WatchID = firstNonEmpty(first.WatchID, f.WatchID)
	f.Handle = firstNonEmpty(first.Handle, f.Handle)
	f.OccurrenceID = first.OccurrenceID
	f.ClaimID = first.ClaimID
	f.TurnID = first.TurnID
	f.EvidenceRef = first.EvidenceRef
	f.OwningSource = first.OwningSource
	f.Sources = sources
	f.Reason = "duplicate_committed_model_presentation"
	f.Expected = map[string]any{
		"max_committed_presentations_per_occurrence": 1,
		"watch_key":      firstKey.watchKey,
		"occurrence_key": firstKey.occurrenceKey,
	}
	f.Observed = map[string]any{
		"duplicate_groups":        dupGroups,
		"max_committed_count":     maxCommits,
		"total_committed_records": len(sources),
	}
	return f, true
}

func clockDomainsComparable(a, b ClockDomain) bool {
	if a == "" || b == "" {
		return true
	}
	return a == b
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstClock(values ...ClockDomain) ClockDomain {
	for _, v := range values {
		if clean := sanitizeClockDomain(v); clean != "" {
			return clean
		}
	}
	return ""
}
