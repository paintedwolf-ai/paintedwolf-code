package watchdiag

import (
	"strings"
	"time"
)

func indexPublicationPhases(phases []PublicationPhaseFact) (map[string][]PublicationPhaseFact, map[string][]PublicationPhaseFact) {
	byOcc := make(map[string][]PublicationPhaseFact)
	byHandle := make(map[string][]PublicationPhaseFact)
	for _, pub := range phases {
		if strings.TrimSpace(pub.Phase) != PhaseProcessReportPublished {
			continue
		}
		if occ := strings.TrimSpace(pub.OccurrenceID); occ != "" {
			byOcc[occ] = append(byOcc[occ], pub)
		}
		if h := strings.TrimSpace(pub.Handle); h != "" {
			byHandle[h] = append(byHandle[h], pub)
		}
	}
	return byOcc, byHandle
}

func deliverySourceRef(obs Observation, d DeliveryFact) SourceRef {
	return SourceRef{
		SessionID:    firstNonEmpty(d.RoutedSessionID, d.OwnerSessionID, obs.SessionID),
		WorkerJobID:  firstNonEmpty(d.RoutedWorkerJobID, d.OwnerWorkerJobID, obs.WorkerJobID),
		WatchID:      firstNonEmpty(d.WatchID, obs.WatchID),
		Handle:       firstNonEmpty(d.Handle, obs.Handle),
		OccurrenceID: d.OccurrenceID,
		EvidenceRef:  firstNonEmpty(d.EvidenceRef, d.DeliveryID),
		OwningSource: resolveOwningSource(d.OwningSource, firstClock(d.ClockDomain, obs.ClockDomain), "watch_delivery", "terminal_delivery_commit"),
	}
}

func publicationSourceRef(obs Observation, d DeliveryFact, pub PublicationPhaseFact) SourceRef {
	return SourceRef{
		SessionID:    firstNonEmpty(pub.SessionID, obs.SessionID),
		Handle:       firstNonEmpty(pub.Handle, d.Handle, obs.Handle),
		OccurrenceID: firstNonEmpty(pub.OccurrenceID, d.OccurrenceID),
		EvidenceRef:  pub.EvidenceRef,
		OwningSource: resolveOwningSource(pub.OwningSource, firstClock(pub.ClockDomain, obs.ClockDomain), "processcontrol", PhaseProcessReportPublished),
	}
}

func checkTerminalDeliveryBeforeReport(obs Observation, now time.Time) (Finding, bool) {
	pubByOcc, pubByHandle := indexPublicationPhases(obs.PublicationPhases)

	var sources []SourceRef
	var reason string
	var missingCount, prematureCount int
	var deliveryClock, pubClock ClockDomain

	for _, d := range obs.Deliveries {
		if !d.Terminal || !d.Committed || !d.RequiresReport {
			continue
		}
		pub, found := matchPublicationPhase(d, obs.SessionID, pubByOcc, pubByHandle)
		if !found {
			if obs.RetentionGap {
				continue
			}
			missingCount++
			if reason == "" {
				reason = "missing_process_report_published_phase"
				deliveryClock = firstClock(d.ClockDomain, d.OwningSource.ClockDomain, obs.ClockDomain)
			}
			sources = append(sources, deliverySourceRef(obs, d))
			continue
		}

		if deliveryPrecedesPublication(d, pub, obs.ClockDomain) {
			prematureCount++
			if reason == "" {
				reason = "terminal_delivery_committed_before_report_published"
				deliveryClock = firstClock(d.ClockDomain, d.OwningSource.ClockDomain, obs.ClockDomain)
				pubClock = firstClock(pub.ClockDomain, pub.OwningSource.ClockDomain, obs.ClockDomain)
			}
			sources = append(sources, deliverySourceRef(obs, d), publicationSourceRef(obs, d, pub))
		}
	}

	if len(sources) == 0 {
		return Finding{}, false
	}

	first := sources[0]
	f := baseFinding(obs, now, RuleTerminalDeliveryBeforeReport, DispositionContradiction)
	f.SessionID = firstNonEmpty(first.SessionID, f.SessionID)
	f.WorkerJobID = firstNonEmpty(first.WorkerJobID, f.WorkerJobID)
	f.WatchID = firstNonEmpty(first.WatchID, f.WatchID)
	f.Handle = firstNonEmpty(first.Handle, f.Handle)
	f.OccurrenceID = first.OccurrenceID
	f.EvidenceRef = first.EvidenceRef
	f.OwningSource = first.OwningSource
	f.Sources = sources
	f.Reason = reason
	f.Expected = map[string]any{
		"required_phase": PhaseProcessReportPublished,
		"order":          "report_published_before_or_at_terminal_delivery",
	}
	f.Observed = map[string]any{
		"missing_report_count":   missingCount,
		"premature_commit_count": prematureCount,
		"delivery_clock_domain":  deliveryClock,
		"report_clock_domain":    pubClock,
	}
	return f, true
}

func matchPublicationPhase(
	d DeliveryFact,
	obsSessionID string,
	byOcc map[string][]PublicationPhaseFact,
	byHandle map[string][]PublicationPhaseFact,
) (PublicationPhaseFact, bool) {
	targetSess := strings.TrimSpace(firstNonEmpty(d.OwnerSessionID, d.RoutedSessionID, obsSessionID))
	if occ := strings.TrimSpace(d.OccurrenceID); occ != "" {
		if pub, ok := pickMatchingPublication(byOcc[occ], targetSess); ok {
			return pub, true
		}
	}
	if h := strings.TrimSpace(d.Handle); h != "" {
		if pub, ok := pickMatchingPublication(byHandle[h], targetSess); ok {
			return pub, true
		}
	}
	return PublicationPhaseFact{}, false
}

func pickMatchingPublication(candidates []PublicationPhaseFact, targetSess string) (PublicationPhaseFact, bool) {
	for _, pub := range candidates {
		pubSess := strings.TrimSpace(pub.SessionID)
		if targetSess != "" && pubSess != "" && pubSess != targetSess {
			continue
		}
		return pub, true
	}
	return PublicationPhaseFact{}, false
}

func deliveryPrecedesPublication(d DeliveryFact, pub PublicationPhaseFact, obsClock ClockDomain) bool {
	if d.CommittedSeq > 0 && pub.RecordedSeq > 0 {
		return d.CommittedSeq < pub.RecordedSeq
	}
	dClock := firstClock(d.ClockDomain, d.OwningSource.ClockDomain, obsClock)
	pClock := firstClock(pub.ClockDomain, pub.OwningSource.ClockDomain, obsClock)
	if !clockDomainsComparable(dClock, pClock) {
		return false
	}
	if !d.CommittedAt.IsZero() && !pub.RecordedAt.IsZero() {
		return d.CommittedAt.Before(pub.RecordedAt)
	}
	return false
}

func checkWatchTurnPastStopFence(obs Observation, now time.Time) (Finding, bool) {
	fence := obs.StopFence
	if fence == nil || (fence.StopEpoch <= 0 && fence.StoppedAt.IsZero()) {
		return Finding{}, false
	}
	fenceClock := firstClock(fence.ClockDomain, fence.OwningSource.ClockDomain, obs.ClockDomain)
	fenceSource := resolveOwningSource(fence.OwningSource, fenceClock, "watch_stop_fence", "stop_fence_commit")

	var sources []SourceRef
	var reason string
	var maxTurnEpoch int64

	for _, turn := range obs.AdmittedTurns {
		if !turn.WatchOriginated || !turn.Admitted {
			continue
		}
		if fence.SessionID != "" && turn.SessionID != "" && turn.SessionID != fence.SessionID && fence.RootSessionID == "" {
			continue
		}

		pastByEpoch := fence.StopEpoch > 0 && turn.StopEpoch > 0 && turn.StopEpoch >= fence.StopEpoch
		turnClock := firstClock(turn.ClockDomain, turn.OwningSource.ClockDomain, obs.ClockDomain)
		pastByTime := !fence.StoppedAt.IsZero() && !turn.AdmittedAt.IsZero() &&
			clockDomainsComparable(fenceClock, turnClock) &&
			turn.AdmittedAt.After(fence.StoppedAt)

		if !pastByEpoch && !pastByTime {
			continue
		}
		if reason == "" {
			if pastByEpoch {
				reason = "watch_turn_admitted_at_or_after_stop_epoch"
			} else {
				reason = "watch_turn_admitted_after_stop_timestamp"
			}
		}
		if turn.StopEpoch > maxTurnEpoch {
			maxTurnEpoch = turn.StopEpoch
		}
		sources = append(sources, SourceRef{
			SessionID:    firstNonEmpty(turn.SessionID, fence.SessionID, obs.SessionID),
			WatchID:      firstNonEmpty(turn.WatchID, obs.WatchID),
			Handle:       firstNonEmpty(turn.Handle, obs.Handle),
			OccurrenceID: turn.OccurrenceID,
			ClaimID:      turn.ClaimID,
			TurnID:       turn.TurnID,
			StopEpoch:    turn.StopEpoch,
			EvidenceRef:  turn.EvidenceRef,
			OwningSource: resolveOwningSource(turn.OwningSource, turnClock, "session_loop", "turn_admission"),
		})
	}

	if len(sources) == 0 {
		return Finding{}, false
	}

	sources = append(sources, SourceRef{
		SessionID:    firstNonEmpty(fence.SessionID, obs.SessionID),
		StopEpoch:    fence.StopEpoch,
		EvidenceRef:  fence.EvidenceRef,
		OwningSource: fenceSource,
	})

	first := sources[0]
	f := baseFinding(obs, now, RuleWatchTurnPastStopFence, DispositionContradiction)
	f.SessionID = firstNonEmpty(first.SessionID, fence.SessionID, f.SessionID)
	f.WatchID = firstNonEmpty(first.WatchID, f.WatchID)
	f.Handle = firstNonEmpty(first.Handle, f.Handle)
	f.OccurrenceID = first.OccurrenceID
	f.ClaimID = first.ClaimID
	f.TurnID = first.TurnID
	f.StopEpoch = fence.StopEpoch
	f.EvidenceRef = firstNonEmpty(first.EvidenceRef, fence.EvidenceRef)
	f.OwningSource = first.OwningSource
	f.Sources = sources
	f.Reason = reason
	f.Expected = map[string]any{
		"watch_turns_past_stop_fence": 0,
		"stop_epoch":                  fence.StopEpoch,
		"stop_clock_domain":           fenceClock,
	}
	f.Observed = map[string]any{
		"admitted_watch_turns_past_fence": len(sources) - 1,
		"max_turn_stop_epoch":             maxTurnEpoch,
	}
	return f, true
}
