package watch

import (
	"context"
	"database/sql"
	"encoding/json"
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

// observeCommittedDeliveryBoundary reads independent durable owner, committed
// delivery, and session stop-fence facts at the actual delivery/consumer
// boundary (CommitCollectBatch and commitAmbientWakeAdmission). When watchdiag
// is disabled, it returns immediately with zero allocations and zero DB reads.
func observeCommittedDeliveryBoundary(
	ctx context.Context,
	s *Store,
	routedSessionID, routedWorkerJobID, deliveryID, deliveryKind string,
	watchOriginatedTurn bool,
	watchIDs []string,
	batch CollectedBatch,
	admittedAt time.Time,
) {
	if !watchdiag.Enabled() || !s.Configured() {
		return
	}
	clk := diagClockDomain(s.clock)
	routedSessionID = strings.TrimSpace(routedSessionID)
	routedWorkerJobID = strings.TrimSpace(routedWorkerJobID)
	deliveryID = strings.TrimSpace(deliveryID)

	ownersByWatch := loadDiagRegistrationOwners(ctx, s, watchIDs, batch)
	deliveries := make([]watchdiag.DeliveryFact, 0, len(batch.Matches))
	targetOccs := make(map[string]struct{}, len(batch.Matches))

	for _, m := range batch.Matches {
		owner := ownersByWatch[m.WatchID]
		handle := m.Handle
		if handle == "" {
			handle = owner.handle
		}
		if m.OccurrenceID != "" {
			targetOccs[m.OccurrenceID] = struct{}{}
		}
		deliveries = append(deliveries, watchdiag.DeliveryFact{
			DeliveryID:        deliveryID,
			WatchID:           m.WatchID,
			Handle:            handle,
			OccurrenceID:      m.OccurrenceID,
			OwnerSessionID:    owner.sessionID,
			OwnerWorkerJobID:  owner.workerJobID,
			RoutedSessionID:   routedSessionID,
			RoutedWorkerJobID: routedWorkerJobID,
			Committed:         true,
			CommittedAt:       admittedAt,
			ClockDomain:       clk,
			OwningSource: watchdiag.OwningSource{
				Component:   "watch_delivery",
				Phase:       "delivery_boundary_commit",
				ClockDomain: clk,
				EvidenceRef: deliveryID,
			},
			EvidenceRef: deliveryID,
		})
	}

	presentations := loadDiagCommittedPresentations(ctx, s, routedSessionID, targetOccs, ownersByWatch, clk)

	obs := watchdiag.Observation{
		SessionID:     routedSessionID,
		WorkerJobID:   routedWorkerJobID,
		ClockDomain:   clk,
		Deliveries:    deliveries,
		Presentations: presentations,
		OwningSource: watchdiag.OwningSource{
			Component:   "watch_delivery",
			Phase:       "delivery_boundary_commit",
			ClockDomain: clk,
			EvidenceRef: deliveryID,
		},
	}
	if len(batch.Matches) > 0 {
		obs.WatchID = batch.Matches[0].WatchID
		obs.Handle = batch.Matches[0].Handle
	}

	if watchOriginatedTurn {
		obs.StopFence = loadDiagStopFence(ctx, s, routedSessionID, clk)
		if obs.StopFence != nil {
			var firstWatch, firstHandle, firstOcc string
			if len(batch.Matches) > 0 {
				firstWatch = batch.Matches[0].WatchID
				firstHandle = batch.Matches[0].Handle
				firstOcc = batch.Matches[0].OccurrenceID
			}
			obs.AdmittedTurns = []watchdiag.WatchTurnFact{
				{
					TurnID:          deliveryID,
					SessionID:       routedSessionID,
					WatchID:         firstWatch,
					Handle:          firstHandle,
					OccurrenceID:    firstOcc,
					WatchOriginated: true,
					Admitted:        true,
					AdmittedAt:      admittedAt,
					ClockDomain:     clk,
					OwningSource: watchdiag.OwningSource{
						Component:   "watch_wake",
						Phase:       "ambient_wake_admission",
						ClockDomain: clk,
						EvidenceRef: deliveryID,
					},
					EvidenceRef: deliveryID,
				},
			}
		}
	}

	watchdiag.Observe(obs)
}

func loadDiagRegistrationOwners(ctx context.Context, s *Store, watchIDs []string, batch CollectedBatch) map[string]regOwnerFact {
	idSet := make(map[string]struct{}, len(watchIDs)+len(batch.Matches))
	for _, id := range watchIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			idSet[trimmed] = struct{}{}
		}
	}
	for _, m := range batch.Matches {
		if trimmed := strings.TrimSpace(m.WatchID); trimmed != "" {
			idSet[trimmed] = struct{}{}
		}
	}
	out := make(map[string]regOwnerFact, len(idSet))
	for id := range idSet {
		var f regOwnerFact
		f.watchID = id
		err := s.db.QueryRowContext(ctx,
			`SELECT handle, COALESCE(session_id, ''), COALESCE(worker_job_id, '') FROM watch_registrations WHERE watch_id = ?`,
			id).Scan(&f.handle, &f.sessionID, &f.workerJobID)
		if err == nil {
			out[id] = f
		}
	}
	return out
}

func loadDiagCommittedPresentations(
	ctx context.Context,
	s *Store,
	routedSessionID string,
	targetOccs map[string]struct{},
	ownersByWatch map[string]regOwnerFact,
	clk watchdiag.ClockDomain,
) []watchdiag.PresentationFact {
	if len(targetOccs) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT delivery_id, COALESCE(session_id, ''), COALESCE(worker_job_id, ''), destination_kind, payload_json
		 FROM watch_deliveries
		 WHERE session_id = ? AND state = 'committed' AND destination_kind IN ('request', 'ambient_wake')
		 ORDER BY committed_at DESC
		 LIMIT ?`,
		routedSessionID, maxDiagCommittedDeliveriesScan)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()

	var out []watchdiag.PresentationFact
	for rows.Next() {
		var dID, dSess, dJob, dKind, payload string
		if err := rows.Scan(&dID, &dSess, &dJob, &dKind, &payload); err != nil {
			continue
		}
		var b CollectedBatch
		if json.Unmarshal([]byte(payload), &b) != nil {
			continue
		}
		for _, m := range b.Matches {
			if _, wanted := targetOccs[m.OccurrenceID]; !wanted {
				continue
			}
			owner := ownersByWatch[m.WatchID]
			handle := m.Handle
			if handle == "" {
				handle = owner.handle
			}
			out = append(out, watchdiag.PresentationFact{
				ClaimID:           dID,
				WatchID:           m.WatchID,
				Handle:            handle,
				OccurrenceID:      m.OccurrenceID,
				OwnerSessionID:    owner.sessionID,
				OwnerWorkerJobID:  owner.workerJobID,
				RoutedSessionID:   dSess,
				RoutedWorkerJobID: dJob,
				Kind:              dKind,
				Committed:         true,
				TurnID:            dID,
				ClockDomain:       clk,
				OwningSource: watchdiag.OwningSource{
					Component:   "watch_delivery",
					Phase:       "delivery_boundary_commit",
					ClockDomain: clk,
					EvidenceRef: dID,
				},
				EvidenceRef: dID,
			})
		}
	}
	return out
}

func loadDiagStopFence(ctx context.Context, s *Store, sessionID string, clk watchdiag.ClockDomain) *watchdiag.StopFenceFact {
	var stopEpoch int64
	var stoppedAtRaw sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT stop_fence_seq, stopped_at FROM watch_session_state WHERE session_id = ?`,
		sessionID).Scan(&stopEpoch, &stoppedAtRaw)
	if err != nil || (!stoppedAtRaw.Valid || strings.TrimSpace(stoppedAtRaw.String) == "") && stopEpoch <= 0 {
		return nil
	}
	var stoppedAt time.Time
	if stoppedAtRaw.Valid && strings.TrimSpace(stoppedAtRaw.String) != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, stoppedAtRaw.String); parseErr == nil {
			stoppedAt = parsed.UTC()
		}
	}
	if stoppedAt.IsZero() && stopEpoch <= 0 {
		return nil
	}
	return &watchdiag.StopFenceFact{
		SessionID:   sessionID,
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
