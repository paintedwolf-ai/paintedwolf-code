package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/observability/watchdiag"
)

const maxDiagCommittedDeliveriesScan = 64

func diagClockDomain(c Clock) watchdiag.ClockDomain {
	if c == nil {
		return watchdiag.ClockDomainSystem
	}
	if _, ok := c.(RealClock); ok {
		return watchdiag.ClockDomainSystem
	}
	if _, ok := c.(*RealClock); ok {
		return watchdiag.ClockDomainSystem
	}
	return watchdiag.ClockDomainManual
}

type regOwnerFact struct {
	watchID     string
	handle      string
	sessionID   string
	workerJobID string
}

func parseHistoricalStopFence(sessionID string, stopEpoch int64, stoppedAtRaw sql.NullString, clk watchdiag.ClockDomain) *watchdiag.StopFenceFact {
	var stoppedAt time.Time
	if stoppedAtRaw.Valid && strings.TrimSpace(stoppedAtRaw.String) != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, stoppedAtRaw.String); err == nil {
			stoppedAt = parsed.UTC()
		}
	}
	if stoppedAt.IsZero() && stopEpoch <= 0 {
		return nil
	}
	return &watchdiag.StopFenceFact{
		SessionID:   strings.TrimSpace(sessionID),
		StopEpoch:   stopEpoch,
		StoppedAt:   stoppedAt,
		ClockDomain: clk,
		OwningSource: watchdiag.OwningSource{
			Component:   "watch_stop_fence",
			Phase:       "stop_fence_commit",
			ClockDomain: clk,
		},
	}
}

type deliveryBoundarySnapshot struct {
	routedSessionID     string
	routedWorkerJobID   string
	deliveryID          string
	deliveryKind        string
	watchOriginatedTurn bool
	admittedAt          time.Time
	clockDomain         watchdiag.ClockDomain
	regOwners           map[string]regOwnerFact
	historicalStopFence *watchdiag.StopFenceFact
	matches             []RecordedMatch
}

// observeCommittedDeliveryBoundary enqueues a non-blocking bounded async
// observation task from CommitCollectBatch / commitAmbientWakeAdmission.
// It performs ZERO synchronous SQLite reads or JSON decoding on the hot path:
// all already-held immutable facts (registration owners, exact committed
// delivery ID, and historical stop fence) are captured inline, and any
// cross-delivery/claim reads run on the observer's bounded background worker
// using the observer-owned context.
func observeCommittedDeliveryBoundary(
	s *Store,
	routedSessionID, routedWorkerJobID, deliveryID, deliveryKind string,
	watchOriginatedTurn bool,
	regOwners map[string]regOwnerFact,
	historicalStopFence *watchdiag.StopFenceFact,
	batch CollectedBatch,
	admittedAt time.Time,
) {
	if !watchdiag.Enabled() || !s.Configured() {
		return
	}
	snap := deliveryBoundarySnapshot{
		routedSessionID:     strings.TrimSpace(routedSessionID),
		routedWorkerJobID:   strings.TrimSpace(routedWorkerJobID),
		deliveryID:          strings.TrimSpace(deliveryID),
		deliveryKind:        strings.TrimSpace(deliveryKind),
		watchOriginatedTurn: watchOriginatedTurn,
		admittedAt:          admittedAt.UTC(),
		clockDomain:         diagClockDomain(s.clock),
		regOwners:           maps.Clone(regOwners),
		matches:             slices.Clone(batch.Matches),
	}
	if historicalStopFence != nil {
		cp := *historicalStopFence
		snap.historicalStopFence = &cp
	}
	var firstWatch, firstHandle string
	if len(snap.matches) > 0 {
		firstWatch = snap.matches[0].WatchID
		firstHandle = snap.matches[0].Handle
	}
	watchdiag.ObserveTask(watchdiag.Task{
		SessionID: snap.routedSessionID,
		WatchID:   firstWatch,
		Handle:    firstHandle,
		Fetch: func(taskCtx context.Context) (watchdiag.Observation, error) {
			return buildDeliveryBoundaryObservation(taskCtx, s, snap)
		},
	})
}

func buildDeliveryBoundaryObservation(taskCtx context.Context, s *Store, snap deliveryBoundarySnapshot) (watchdiag.Observation, error) {
	targetOccs := make(map[string]struct{}, len(snap.matches))
	missingOwner := false
	for _, m := range snap.matches {
		if m.OccurrenceID != "" {
			targetOccs[m.OccurrenceID] = struct{}{}
		}
		if _, ok := snap.regOwners[m.WatchID]; !ok {
			missingOwner = true
		}
	}

	deliveries, truncated, err := loadDiagCommittedDeliveries(taskCtx, s, snap, targetOccs)
	if err != nil {
		return watchdiag.Observation{}, err
	}
	presentations, err := loadDiagAdmittedPresentationClaims(taskCtx, s, snap, targetOccs)
	if err != nil {
		return watchdiag.Observation{}, err
	}

	obs := watchdiag.Observation{
		SessionID:     snap.routedSessionID,
		WorkerJobID:   snap.routedWorkerJobID,
		ClockDomain:   snap.clockDomain,
		Truncated:     truncated,
		Deliveries:    deliveries,
		Presentations: presentations,
		OwningSource: watchdiag.OwningSource{
			Component:   "watch_delivery",
			Phase:       "delivery_boundary_commit",
			ClockDomain: snap.clockDomain,
			EvidenceRef: snap.deliveryID,
		},
	}
	if truncated {
		obs.IncompleteReason = "committed_deliveries_scan_truncated"
	} else if missingOwner {
		obs.IncompleteReason = "registration_owner_unavailable"
	}
	if len(snap.matches) > 0 {
		obs.WatchID = snap.matches[0].WatchID
		obs.Handle = snap.matches[0].Handle
	}

	if snap.watchOriginatedTurn && snap.historicalStopFence != nil {
		obs.StopFence = snap.historicalStopFence
		var firstWatch, firstHandle, firstOcc string
		if len(snap.matches) > 0 {
			firstWatch = snap.matches[0].WatchID
			firstHandle = snap.matches[0].Handle
			firstOcc = snap.matches[0].OccurrenceID
		}
		obs.AdmittedTurns = []watchdiag.WatchTurnFact{
			{
				TurnID:          snap.deliveryID,
				SessionID:       snap.routedSessionID,
				WatchID:         firstWatch,
				Handle:          firstHandle,
				OccurrenceID:    firstOcc,
				WatchOriginated: true,
				Admitted:        true,
				AdmittedAt:      snap.admittedAt,
				ClockDomain:     snap.clockDomain,
				OwningSource: watchdiag.OwningSource{
					Component:   "watch_wake",
					Phase:       "ambient_wake_admission",
					ClockDomain: snap.clockDomain,
					EvidenceRef: snap.deliveryID,
				},
				EvidenceRef: snap.deliveryID,
			},
		}
	}

	return obs, nil
}

func loadDiagCommittedDeliveries(
	ctx context.Context,
	s *Store,
	snap deliveryBoundarySnapshot,
	targetOccs map[string]struct{},
) ([]watchdiag.DeliveryFact, bool, error) {
	if len(targetOccs) == 0 {
		return nil, false, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT delivery_id, COALESCE(session_id, ''), COALESCE(worker_job_id, ''), payload_json
		 FROM watch_deliveries
		 WHERE session_id = ? AND state = 'committed' AND destination_kind IN ('request', 'ambient_wake')
		 ORDER BY committed_at DESC
		 LIMIT ?`,
		snap.routedSessionID, maxDiagCommittedDeliveriesScan+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	var out []watchdiag.DeliveryFact
	rowCount := 0
	truncated := false
	for rows.Next() {
		rowCount++
		if rowCount > maxDiagCommittedDeliveriesScan {
			truncated = true
			break
		}
		var dID, dSess, dJob, payload string
		if err := rows.Scan(&dID, &dSess, &dJob, &payload); err != nil {
			return nil, false, err
		}
		var b CollectedBatch
		if json.Unmarshal([]byte(payload), &b) != nil {
			continue
		}
		for _, m := range b.Matches {
			if _, wanted := targetOccs[m.OccurrenceID]; !wanted {
				continue
			}
			owner := snap.regOwners[m.WatchID]
			handle := m.Handle
			if handle == "" {
				handle = owner.handle
			}
			out = append(out, watchdiag.DeliveryFact{
				DeliveryID:        dID,
				WatchID:           m.WatchID,
				Handle:            handle,
				OccurrenceID:      m.OccurrenceID,
				OwnerSessionID:    owner.sessionID,
				OwnerWorkerJobID:  owner.workerJobID,
				RoutedSessionID:   dSess,
				RoutedWorkerJobID: dJob,
				Committed:         true,
				CommittedAt:       snap.admittedAt,
				ClockDomain:       snap.clockDomain,
				OwningSource: watchdiag.OwningSource{
					Component:   "watch_delivery",
					Phase:       "delivery_boundary_commit",
					ClockDomain: snap.clockDomain,
					EvidenceRef: dID,
				},
				EvidenceRef: dID,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// loadDiagAdmittedPresentationClaims reads actual admitted presentation claims
// separately from watch_deliveries rows so delivery-row contradictions remain
// distinct from model presentation claims.
func loadDiagAdmittedPresentationClaims(
	ctx context.Context,
	s *Store,
	snap deliveryBoundarySnapshot,
	targetOccs map[string]struct{},
) ([]watchdiag.PresentationFact, error) {
	if len(targetOccs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT claim_id, COALESCE(session_id, ''), COALESCE(worker_job_id, ''), producer_occurrence_id, claim_kind, COALESCE(receipt_id, '')
		 FROM watch_presentation_claims
		 WHERE session_id = ? AND state = 'admitted'
		 ORDER BY created_at DESC
		 LIMIT ?`,
		snap.routedSessionID, maxDiagCommittedDeliveriesScan)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	watchByOcc := make(map[string]RecordedMatch, len(snap.matches))
	for _, m := range snap.matches {
		watchByOcc[m.OccurrenceID] = m
	}

	var out []watchdiag.PresentationFact
	for rows.Next() {
		var claimID, sessID, jobID, occID, claimKind, receiptID string
		if err := rows.Scan(&claimID, &sessID, &jobID, &occID, &claimKind, &receiptID); err != nil {
			return nil, err
		}
		m, wanted := watchByOcc[occID]
		if !wanted {
			continue
		}
		owner := snap.regOwners[m.WatchID]
		handle := m.Handle
		if handle == "" {
			handle = owner.handle
		}
		out = append(out, watchdiag.PresentationFact{
			ClaimID:           claimID,
			WatchID:           m.WatchID,
			Handle:            handle,
			OccurrenceID:      occID,
			OwnerSessionID:    owner.sessionID,
			OwnerWorkerJobID:  owner.workerJobID,
			RoutedSessionID:   sessID,
			RoutedWorkerJobID: jobID,
			Kind:              claimKind,
			Committed:         true,
			TurnID:            receiptID,
			ClockDomain:       snap.clockDomain,
			OwningSource: watchdiag.OwningSource{
				Component:   "watch_presentation_claims",
				Phase:       "presentation_claim_admitted",
				ClockDomain: snap.clockDomain,
				EvidenceRef: claimID,
			},
			EvidenceRef: claimID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
