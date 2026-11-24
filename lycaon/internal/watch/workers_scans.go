package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerWatchStateSnapshot provides initial reconciliation facts for coordinator worker and scan watches.
type WorkerWatchStateSnapshot struct {
	OpenDecisions         []OpenWorkerDecisionFact
	SettledWorkers        []SettledWorkerFact
	AllWorkersIdle        bool
	HasWorkerCycle        bool
	OverlayPromotePending bool
	CompletedScans        []ScanDoneFact
}

// OpenWorkerDecisionFact describes an already-open worker decision at registration time.
type OpenWorkerDecisionFact struct {
	WorkerHandle string
	JobID        string
	Request      api.WorkerDecisionRequest
}

// SettledWorkerFact describes a terminal worker round for handle reconciliation.
type SettledWorkerFact struct {
	WorkerHandle string
	JobID        string
	Status       string
	Summary      string
}

// ScanDoneFact describes a requested code scan for reconciliation or terminal notification.
type ScanDoneFact struct {
	ScanID        string
	Status        string
	Categories    string
	FindingsCount int
}

// WorkerWatchEvent describes a live worker lifecycle event delivered to a coordinator session.
type WorkerWatchEvent struct {
	Kind                  string
	WorkerHandle          string
	JobID                 string
	Outcome               string
	Status                string
	Summary               string
	OccurrenceID          string
	AllWorkersIdle        bool
	OverlayPromotePending bool
}

// CoordinatorStateLookup resolves initial snapshot facts for coordinator watches on registration.
type CoordinatorStateLookup func(ctx context.Context, sessionID string) (WorkerWatchStateSnapshot, error)

// AuthorityChecker rechecks whether a watch owner still holds the grants and scope required by reg.
type AuthorityChecker func(ctx context.Context, reg Registration) (allowed bool, reason string)

func DecisionOccurrenceID(jobID string, req api.WorkerDecisionRequest) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(jobID) + "\x00" + strings.TrimSpace(req.Question) + "\x00" + strings.Join(req.Options, "\x1f")))
	return "worker_decision:" + strings.TrimSpace(jobID) + ":" + hex.EncodeToString(sum[:8])
}

func decisionStatusCursorKey(workerHandle string) string {
	return "decision_status:" + strings.TrimSpace(workerHandle)
}

func levelArmedCursorKey(kind string, altIdx int) string {
	return fmt.Sprintf("level:%s:armed:%d", kind, altIdx)
}

func levelEpisodeCursorKey(kind string, altIdx int) string {
	return fmt.Sprintf("level:%s:ep:%d", kind, altIdx)
}

func collectInitialCoordinatorMatches(ctx context.Context, s *Store, req RegisterRequest) ([]MatchInput, map[string]string, error) {
	if req.Owner.Kind != OwnerSession {
		return nil, nil, nil
	}
	needsSnapshot := false
	for _, cond := range req.Conditions {
		switch cond.Kind {
		case "worker_decision_needed", "worker_failed", "next_worker_done", "all_workers_idle", "overlay_promote_pending", "scan_done":
			needsSnapshot = true
		}
	}
	if !needsSnapshot {
		return nil, nil, nil
	}
	snap, ok, err := lookupCoordinatorState(s, ctx, req.Owner.SessionID)
	if err != nil || !ok {
		return nil, nil, err
	}

	var out []MatchInput
	cursors := map[string]string{}
	for altIdx, cond := range req.Conditions {
		switch cond.Kind {
		case "worker_decision_needed":
			for _, dec := range snap.OpenDecisions {
				if !workerHandleSelected(cond.Handles, dec.WorkerHandle, dec.JobID) {
					continue
				}
				occ := DecisionOccurrenceID(dec.JobID, dec.Request)
				handle := nonEmptyHandle(dec.WorkerHandle, dec.JobID)
				cursors[decisionStatusCursorKey(handle)] = "open"
				out = append(out, MatchInput{
					SourceKind:   "worker_decision_needed",
					OccurrenceID: occ,
					Outcome:      "needs_decision",
					Evidence: map[string]any{
						"kind":            "worker_decision_needed",
						"worker":          handle,
						"job_id":          dec.JobID,
						"question":        dec.Request.Question,
						"options":         dec.Request.Options,
						"blocker_class":   string(dec.Request.BlockerClass),
						"decision_status": "open",
					},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		case "worker_failed":
			if len(cond.Handles) == 0 {
				continue
			}
			for _, w := range snap.SettledWorkers {
				if w.Status != "failed" || !workerHandleSelected(cond.Handles, w.WorkerHandle, w.JobID) {
					continue
				}
				handle := nonEmptyHandle(w.WorkerHandle, w.JobID)
				out = append(out, MatchInput{
					SourceKind:   "worker_failed",
					OccurrenceID: "worker_failed:" + w.JobID,
					Outcome:      "failed",
					Evidence: map[string]any{
						"kind":    "worker_failed",
						"worker":  handle,
						"job_id":  w.JobID,
						"summary": w.Summary,
					},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		case "next_worker_done":
			if len(cond.Handles) == 0 {
				continue
			}
			for _, w := range snap.SettledWorkers {
				if !workerHandleSelected(cond.Handles, w.WorkerHandle, w.JobID) {
					continue
				}
				handle := nonEmptyHandle(w.WorkerHandle, w.JobID)
				outcome := "satisfied"
				if w.Status == "failed" {
					outcome = "failed"
				}
				out = append(out, MatchInput{
					SourceKind:   "next_worker_done",
					OccurrenceID: "worker_terminal:" + w.JobID,
					Outcome:      outcome,
					Evidence: map[string]any{
						"kind":    "next_worker_done",
						"worker":  handle,
						"job_id":  w.JobID,
						"status":  w.Status,
						"summary": w.Summary,
					},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		case "all_workers_idle":
			if snap.HasWorkerCycle && snap.AllWorkersIdle {
				cursors[levelArmedCursorKey("all_workers_idle", altIdx)] = "false"
				cursors[levelEpisodeCursorKey("all_workers_idle", altIdx)] = "1"
				out = append(out, MatchInput{
					SourceKind:   "all_workers_idle",
					OccurrenceID: fmt.Sprintf("all_workers_idle:%d:ep1", altIdx),
					Outcome:      "satisfied",
					Evidence:     map[string]any{"kind": "all_workers_idle"},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		case "overlay_promote_pending":
			if snap.OverlayPromotePending {
				cursors[levelArmedCursorKey("overlay_promote_pending", altIdx)] = "false"
				cursors[levelEpisodeCursorKey("overlay_promote_pending", altIdx)] = "1"
				out = append(out, MatchInput{
					SourceKind:   "overlay_promote_pending",
					OccurrenceID: fmt.Sprintf("overlay_promote_pending:%d:ep1", altIdx),
					Outcome:      "satisfied",
					Evidence:     map[string]any{"kind": "overlay_promote_pending"},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		case "scan_done":
			for _, sc := range snap.CompletedScans {
				out = append(out, MatchInput{
					SourceKind:   "scan_done",
					OccurrenceID: "scan_done:" + sc.ScanID,
					Outcome:      "satisfied",
					Evidence: map[string]any{
						"kind":           "scan_done",
						"scan_id":        sc.ScanID,
						"status":         sc.Status,
						"categories":     sc.Categories,
						"findings_count": sc.FindingsCount,
					},
				})
				if !req.Repeat {
					return out, cursors, nil
				}
			}
		}
	}
	return out, cursors, nil
}

// RecordWorkerDecisionOpened delivers an open worker decision to matching coordinator watches
// without answering or clearing the underlying decision.
func (s *Store) RecordWorkerDecisionOpened(ctx context.Context, parentSessionID, workerHandle, jobID string, req api.WorkerDecisionRequest) (bool, error) {
	if !s.Configured() || strings.TrimSpace(parentSessionID) == "" || strings.TrimSpace(jobID) == "" {
		return false, nil
	}
	regs, err := liveSessionWatches(s, ctx, parentSessionID)
	if err != nil || len(regs) == 0 {
		return false, err
	}
	handle := nonEmptyHandle(workerHandle, jobID)
	occ := DecisionOccurrenceID(jobID, req)
	owner := SessionOwner(parentSessionID)
	claimedAny := false

	for _, reg := range regs {
		matched := false
		for _, cond := range reg.Conditions {
			if (cond.Kind == "worker_decision_needed" || cond.Kind == "next_worker_done") &&
				workerHandleSelected(cond.Handles, workerHandle, jobID) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		dec, cErr := ClaimPresentation(ctx, s, owner, occ, ClaimWatchEvidence, false, "")
		if cErr != nil {
			return claimedAny, cErr
		}
		if dec.AlreadyAdmitted {
			_, _ = recordAlreadyAdmittedMatch(s, ctx, reg.WatchID, MatchInput{
				SourceKind:   "worker_decision_needed",
				OccurrenceID: occ,
				Outcome:      "needs_decision",
				RetainedRef:  dec.ExistingReceiptID,
				Evidence: map[string]any{
					"kind":            "worker_decision_needed",
					"worker":          handle,
					"job_id":          jobID,
					"question":        req.Question,
					"decision_status": "open",
				},
			}, map[string]string{decisionStatusCursorKey(handle): "open"}, dec.ExistingReceiptID)
			claimedAny = true
			continue
		}
		if !dec.Granted {
			continue
		}
		for _, cond := range reg.Conditions {
			if cond.Kind != "worker_decision_needed" && cond.Kind != "next_worker_done" {
				continue
			}
			if !workerHandleSelected(cond.Handles, workerHandle, jobID) {
				continue
			}
			_, ok, rErr := s.RecordMatch(ctx, reg.WatchID, MatchInput{
				SourceKind:   cond.Kind,
				OccurrenceID: occ,
				Outcome:      "needs_decision",
				Evidence: map[string]any{
					"kind":            cond.Kind,
					"worker":          handle,
					"job_id":          jobID,
					"question":        req.Question,
					"options":         req.Options,
					"blocker_class":   string(req.BlockerClass),
					"decision_status": "open",
				},
			}, map[string]string{decisionStatusCursorKey(handle): "open"})
			if rErr != nil {
				return claimedAny, rErr
			}
			if ok {
				claimedAny = true
			}
			break
		}
	}
	return claimedAny, nil
}

// RecordWorkerDecisionClosed updates the observed decision status (closed or retried) on matching watches
// so Inspect and subsequent batch deliveries report the current status.
func (s *Store) RecordWorkerDecisionClosed(ctx context.Context, parentSessionID, workerHandle, jobID, status string) error {
	return recordWorkerDecisionClosed(ctx, s, parentSessionID, workerHandle, jobID, status)
}

func recordWorkerDecisionClosed(ctx context.Context, s *Store, parentSessionID, workerHandle, jobID, status string) error {
	if !s.Configured() || strings.TrimSpace(parentSessionID) == "" {
		return nil
	}
	if status == "" {
		status = "closed"
	}
	handle := nonEmptyHandle(workerHandle, jobID)
	key := decisionStatusCursorKey(handle)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND owner_kind = 'session' ORDER BY seq ASC`,
		strings.TrimSpace(parentSessionID))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var targetIDs []string
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			continue
		}
		if reg.Cursors[key] != "" {
			targetIDs = append(targetIDs, reg.WatchID)
			continue
		}
		for _, cond := range reg.Conditions {
			if cond.Kind == "worker_decision_needed" && workerHandleSelected(cond.Handles, workerHandle, jobID) {
				targetIDs = append(targetIDs, reg.WatchID)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	for _, id := range targetIDs {
		_ = updateWatchCursor(s, ctx, id, key, status)
	}
	return nil
}

// RecordWorkerEvent records worker completion, failure, notice, message, idle, and overlay transitions.
func (s *Store) RecordWorkerEvent(ctx context.Context, parentSessionID string, ev WorkerWatchEvent) (bool, error) {
	if !s.Configured() || strings.TrimSpace(parentSessionID) == "" {
		return false, nil
	}
	regs, err := liveSessionWatches(s, ctx, parentSessionID)
	if err != nil || len(regs) == 0 {
		return false, err
	}
	handle := nonEmptyHandle(ev.WorkerHandle, ev.JobID)
	occ := strings.TrimSpace(ev.OccurrenceID)
	if occ == "" && ev.JobID != "" {
		occ = fmt.Sprintf("worker_%s:%s", ev.Kind, ev.JobID)
	}
	owner := SessionOwner(parentSessionID)
	claimed := false

	for _, reg := range regs {
		for altIdx, cond := range reg.Conditions {
			switch cond.Kind {
			case "next_worker_done":
				if (ev.Kind == "terminal" || ev.Kind == "notice" || ev.Kind == "message") &&
					workerHandleSelected(cond.Handles, ev.WorkerHandle, ev.JobID) {
					outcome := ev.Outcome
					if outcome == "" {
						outcome = "satisfied"
					}
					if ok, err := claimAndRecordSessionWatch(s, ctx, owner, reg.WatchID, occ, MatchInput{
						SourceKind:   "next_worker_done",
						OccurrenceID: occ,
						Outcome:      outcome,
						Evidence: map[string]any{
							"kind":    "next_worker_done",
							"worker":  handle,
							"job_id":  ev.JobID,
							"outcome": outcome,
							"status":  ev.Status,
							"summary": ev.Summary,
						},
					}); err != nil {
						return claimed, err
					} else if ok {
						claimed = true
					}
				}
			case "worker_failed":
				if ev.Kind == "terminal" && (ev.Outcome == "failed" || ev.Status == "failed") &&
					workerHandleSelected(cond.Handles, ev.WorkerHandle, ev.JobID) {
					failOcc := occ
					if failOcc == "" {
						failOcc = "worker_failed:" + ev.JobID
					}
					if ok, err := claimAndRecordSessionWatch(s, ctx, owner, reg.WatchID, failOcc, MatchInput{
						SourceKind:   "worker_failed",
						OccurrenceID: failOcc,
						Outcome:      "failed",
						Evidence: map[string]any{
							"kind":    "worker_failed",
							"worker":  handle,
							"job_id":  ev.JobID,
							"summary": ev.Summary,
						},
					}); err != nil {
						return claimed, err
					} else if ok {
						claimed = true
					}
				}
			case "all_workers_idle":
				ok, err := evaluateLevelCondition(s, ctx, reg, altIdx, "all_workers_idle", ev.AllWorkersIdle)
				if err != nil {
					return claimed, err
				}
				if ok {
					claimed = true
				}
			case "overlay_promote_pending":
				ok, err := evaluateLevelCondition(s, ctx, reg, altIdx, "overlay_promote_pending", ev.OverlayPromotePending)
				if err != nil {
					return claimed, err
				}
				if ok {
					claimed = true
				}
			}
		}
	}
	return claimed, nil
}

func evaluateLevelCondition(s *Store, ctx context.Context, reg Registration, altIdx int, kind string, satisfied bool) (bool, error) {
	armedKey := levelArmedCursorKey(kind, altIdx)
	epKey := levelEpisodeCursorKey(kind, altIdx)
	armed := reg.Cursors[armedKey] != "false"
	if !satisfied {
		if !armed {
			_ = updateWatchCursor(s, ctx, reg.WatchID, armedKey, "true")
		}
		return false, nil
	}
	if !armed {
		return false, nil
	}
	ep, _ := strconv.Atoi(reg.Cursors[epKey])
	ep++
	occID := fmt.Sprintf("%s:%d:ep%d", kind, altIdx, ep)
	_, ok, err := s.RecordMatch(ctx, reg.WatchID, MatchInput{
		SourceKind:   kind,
		OccurrenceID: occID,
		Outcome:      "satisfied",
		Evidence:     map[string]any{"kind": kind},
	}, map[string]string{
		armedKey: "false",
		epKey:    strconv.Itoa(ep),
	})
	return ok, err
}

func claimAndRecordSessionWatch(s *Store, ctx context.Context, owner Owner, watchID, occ string, input MatchInput) (bool, error) {
	if occ != "" {
		dec, err := ClaimPresentation(ctx, s, owner, occ, ClaimWatchEvidence, false, "")
		if err != nil {
			return false, err
		}
		if dec.AlreadyAdmitted {
			input.RetainedRef = dec.ExistingReceiptID
			ok, rErr := recordAlreadyAdmittedMatch(s, ctx, watchID, input, nil, dec.ExistingReceiptID)
			return ok, rErr
		}
		if !dec.Granted {
			return false, nil
		}
	}
	_, ok, err := s.RecordMatch(ctx, watchID, input, nil)
	return ok, err
}

// RecordScanDone delivers a finished code scan to matching live watches in sessionID.
func (s *Store) RecordScanDone(ctx context.Context, sessionID string, scan ScanDoneFact) (bool, error) {
	if !s.Configured() || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(scan.ScanID) == "" {
		return false, nil
	}
	regs, err := liveSessionWatches(s, ctx, sessionID)
	if err != nil || len(regs) == 0 {
		return false, err
	}
	occ := "scan_done:" + strings.TrimSpace(scan.ScanID)
	owner := SessionOwner(sessionID)
	claimed := false
	for _, reg := range regs {
		hasScan := false
		for _, c := range reg.Conditions {
			if c.Kind == "scan_done" {
				hasScan = true
				break
			}
		}
		if !hasScan {
			continue
		}
		ok, rErr := claimAndRecordSessionWatch(s, ctx, owner, reg.WatchID, occ, MatchInput{
			SourceKind:   "scan_done",
			OccurrenceID: occ,
			Outcome:      "satisfied",
			Evidence: map[string]any{
				"kind":           "scan_done",
				"scan_id":        scan.ScanID,
				"status":         scan.Status,
				"categories":     scan.Categories,
				"findings_count": scan.FindingsCount,
			},
		})
		if rErr != nil {
			return claimed, rErr
		}
		if ok {
			claimed = true
		}
	}
	return claimed, nil
}

// LiveScanWatchSessions lists distinct coordinator sessions with a live scan_done watch.
func (s *Store) LiveScanWatchSessions(ctx context.Context) ([]string, error) {
	if !s.Configured() {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT session_id FROM watch_registrations WHERE owner_kind = 'session' AND state = 'live' AND conditions_json LIKE '%scan_done%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func liveSessionWatches(s *Store, ctx context.Context, sessionID string) ([]Registration, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND owner_kind = 'session' AND state = 'live' ORDER BY seq ASC`,
		strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Registration
	for rows.Next() {
		reg, sErr := scanRegistration(rows)
		if sErr != nil {
			return nil, sErr
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

func workerHandleSelected(wanted []string, workerHandle, jobID string) bool {
	if len(wanted) == 0 {
		return true
	}
	workerHandle = strings.TrimSpace(workerHandle)
	jobID = strings.TrimSpace(jobID)
	for _, raw := range wanted {
		h := strings.TrimSpace(raw)
		if h != "" && (h == workerHandle || h == jobID) {
			return true
		}
	}
	return false
}

func nonEmptyHandle(workerHandle, jobID string) string {
	if w := strings.TrimSpace(workerHandle); w != "" {
		return w
	}
	return strings.TrimSpace(jobID)
}

func extractDecisionStatusMap(cursors map[string]string) map[string]string {
	var out map[string]string
	for k, v := range cursors {
		if worker, ok := strings.CutPrefix(k, "decision_status:"); ok && worker != "" && v != "" {
			if out == nil {
				out = make(map[string]string)
			}
			out[worker] = v
		}
	}
	return out
}
