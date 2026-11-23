package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
)

// Register validates and stores a watch subscription, or replays an existing invocation receipt.
func (s *Store) Register(ctx context.Context, req RegisterRequest) (RegisterReceipt, error) {
	if !s.Configured() {
		return RegisterReceipt{}, fmt.Errorf("watch store is not configured")
	}
	now := s.clock.Now()
	normalized, err := normalizeRegisterRequest(req, now)
	if err != nil {
		return RegisterReceipt{}, err
	}
	if s.isTreeStopping(normalized.Owner.SessionID) {
		return RegisterReceipt{}, ErrSessionStopped
	}
	initialMatches, err := collectInitialProcessMatches(ctx, s, normalized)
	if err != nil {
		return RegisterReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RegisterReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if stopped, err := isStoppedTx(ctx, tx, normalized.Owner.SessionID); err != nil {
		return RegisterReceipt{}, err
	} else if stopped {
		return RegisterReceipt{}, ErrSessionStopped
	}

	receipt, reg, replayed, err := registerWatchTx(ctx, tx, normalized, now, initialMatches)
	if err != nil {
		return RegisterReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return RegisterReceipt{}, err
	}
	if !replayed && reg.Lifecycle.State == StateLive {
		s.heap.Upsert(KindWatchExpiry, reg.WatchID, reg.Lifecycle.ExpiresAt)
	}
	return receipt, nil
}

func normalizeRegisterRequest(req RegisterRequest, now time.Time) (RegisterRequest, error) {
	req.Owner = req.Owner.Normalized()
	req.InvocationID = strings.TrimSpace(req.InvocationID)
	req.DerivedCommandHandle = strings.TrimSpace(req.DerivedCommandHandle)
	if req.Owner.SessionID == "" || req.Owner.ProjectID == "" {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "owner", Message: "session and project are required"}
	}
	if req.Delivery == "" {
		req.Delivery = DeliveryCollect
	}
	if req.Delivery != DeliveryCollect && req.Delivery != DeliveryWake {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "delivery", Message: "delivery must be collect or wake"}
	}
	if req.Owner.Kind == OwnerJob && req.Delivery == DeliveryWake {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_WAKE_NOT_ALLOWED", Field: "delivery", Message: "worker watches cannot request ambient wake"}
	}
	if req.Standing != nil {
		if req.Delivery == DeliveryWake {
			return RegisterRequest{}, &ValidationError{Code: "WATCH_DRAFT_INVALID", Field: "delivery", Fields: []string{"standing", "delivery"}, Message: "standing watch drafts force delivery=collect and cannot use wake"}
		}
		req.Delivery = DeliveryCollect
	}
	if len(req.Conditions) == 0 || len(req.Conditions) > MaxAlternatives {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "conditions", Message: "conditions must contain 1 to 8 items"}
	}
	allTerminal := true
	hasTimer := false
	for i, cond := range req.Conditions {
		kind := strings.TrimSpace(cond.Kind)
		if kind == "" || kind == ConditionWatchMatch || kind == "program_done" {
			return RegisterRequest{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: fmt.Sprintf("conditions.%d.kind", i), Message: fmt.Sprintf("condition kind %q is not allowed on watch", kind)}
		}
		if kind == "timer" {
			hasTimer = true
		}
		if !isTerminalCondition(cond) {
			allTerminal = false
		}
	}
	if req.Repeat && allTerminal {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_REPEAT_UNSUPPORTED", Field: "repeat", Message: "all conditions are terminal one-shots and cannot repeat"}
	}
	if req.Repeat && hasTimer && req.Delivery == DeliveryWake {
		return RegisterRequest{}, &ValidationError{Code: "WATCH_WAKE_NOT_ALLOWED", Field: "delivery", Message: "repeating timer watches cannot request ambient wake"}
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = now.Add(DefaultLifetime)
	} else {
		req.ExpiresAt = req.ExpiresAt.UTC()
		ahead := req.ExpiresAt.Sub(now)
		if ahead < MinLifetime || ahead > MaxLifetime {
			return RegisterRequest{}, &ValidationError{Code: "WATCH_EXPIRY_INVALID", Field: "expires_at", Message: "expires_at must be between 1 minute and 30 days ahead"}
		}
	}
	if req.Cursors == nil {
		req.Cursors = map[string]string{}
	}
	if req.Standing != nil {
		if raw, err := json.Marshal(req.Standing); err == nil {
			req.Cursors[standingDraftCursorKey] = string(raw)
		}
	}
	return req, nil
}

const standingDraftCursorKey = "_standing_draft"

func isTerminalCondition(cond awaitstore.Condition) bool {
	switch cond.Kind {
	case ConditionProcessDone:
		return len(cond.Handles) > 0
	case "scan_done":
		return true
	case "timer":
		return strings.TrimSpace(cond.At) != ""
	default:
		return false
	}
}

func collectInitialProcessMatches(ctx context.Context, s *Store, req RegisterRequest) ([]MatchInput, error) {
	var out []MatchInput
	seen := map[string]bool{}
	for _, cond := range req.Conditions {
		if cond.Kind != ConditionProcessDone {
			continue
		}
		for _, handle := range cond.Handles {
			handle = strings.TrimSpace(handle)
			if handle == "" || seen[handle] {
				continue
			}
			seen[handle] = true
			fact, done, err := s.lookupProcess(ctx, req.Owner.SessionID, handle)
			if err != nil {
				return nil, err
			}
			if !done {
				continue
			}
			occ := fact.OccurrenceID
			if occ == "" {
				occ = "process_done:" + handle
			}
			outcome := fact.Outcome
			if outcome == "" {
				outcome = "satisfied"
			}
			out = append(out, MatchInput{
				SourceKind:   ConditionProcessDone,
				OccurrenceID: occ,
				Outcome:      outcome,
				Evidence: map[string]any{
					"kind":    ConditionProcessDone,
					"handle":  handle,
					"outcome": outcome,
					"code":    fact.Code,
					"report":  fact.Report,
				},
			})
			if !req.Repeat {
				return out, nil
			}
		}
	}
	return out, nil
}

func registerWatchTx(ctx context.Context, tx *sql.Tx, req RegisterRequest, now time.Time, initial []MatchInput) (RegisterReceipt, Registration, bool, error) {
	if req.InvocationID != "" {
		existing, found, err := findByInvocationTx(ctx, tx, req.Owner.SessionID, req.InvocationID)
		if err != nil {
			return RegisterReceipt{}, Registration{}, false, err
		}
		if found {
			pending, err := countPendingMatchesTx(ctx, tx, existing.WatchID)
			if err != nil {
				return RegisterReceipt{}, Registration{}, false, err
			}
			return buildRegisterReceipt(existing, pending), existing, true, nil
		}
	}
	if req.DerivedCommandHandle != "" {
		existing, found, err := findByDerivedCommandTx(ctx, tx, req.Owner, req.DerivedCommandHandle)
		if err != nil {
			return RegisterReceipt{}, Registration{}, false, err
		}
		if found {
			pending, err := countPendingMatchesTx(ctx, tx, existing.WatchID)
			if err != nil {
				return RegisterReceipt{}, Registration{}, false, err
			}
			return buildRegisterReceipt(existing, pending), existing, true, nil
		}
	} else {
		if err := enforceExplicitCapacityTx(ctx, tx, req.Owner); err != nil {
			return RegisterReceipt{}, Registration{}, false, err
		}
	}

	seq, err := allocateWatchSeqTx(ctx, tx, req.Owner.SessionID)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	handle := fmt.Sprintf("watch-%d", seq)
	watchID := uuid.NewString()
	condBytes, err := json.Marshal(req.Conditions)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	authBytes, err := json.Marshal(req.Authority)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	cursorBytes, err := json.Marshal(req.Cursors)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	expStr := req.ExpiresAt.UTC().Format(time.RFC3339Nano)
	repeatInt := 0
	if req.Repeat {
		repeatInt = 1
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_registrations (
		watch_id, project_id, owner_kind, session_id, worker_job_id, handle, seq,
		derived_command_handle, creating_invocation_id, conditions_json, authority_json,
		cursors_json, delivery, repeat, expires_at, state, created_at, updated_at
	) VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, 'live', ?, ?)`,
		watchID, req.Owner.ProjectID, string(req.Owner.Kind), req.Owner.SessionID, req.Owner.WorkerJobID,
		handle, seq, req.DerivedCommandHandle, req.InvocationID, string(condBytes), string(authBytes),
		string(cursorBytes), string(req.Delivery), repeatInt, expStr, nowStr, nowStr,
	); err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}

	for _, match := range initial {
		if _, _, err := insertMatchTx(ctx, tx, watchID, match, nil, now); err != nil {
			return RegisterReceipt{}, Registration{}, false, err
		}
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	pending, err := countPendingMatchesTx(ctx, tx, watchID)
	if err != nil {
		return RegisterReceipt{}, Registration{}, false, err
	}
	return buildRegisterReceipt(reg, pending), reg, false, nil
}

func buildRegisterReceipt(reg Registration, pending int) RegisterReceipt {
	state := "pending"
	if pending > 0 || reg.Lifecycle.Outcome == OutcomeCompleted {
		state = "matched"
	}
	out := RegisterReceipt{
		WatchID:     reg.WatchID,
		Handle:      reg.Handle,
		State:       state,
		Delivery:    reg.Lifecycle.Delivery,
		Destination: reg.Owner.Destination(),
		ExpiresAt:   reg.Lifecycle.ExpiresAt.UTC().Format(time.RFC3339),
		WaitCondition: awaitstore.Condition{
			Kind:    ConditionWatchMatch,
			Handles: []string{reg.Handle},
		},
		PendingMatches: pending,
		Hint:           "When you need this result, pass this condition to wait.",
	}
	if reg.Cursors[standingDraftCursorKey] != "" {
		out.StandingDraft = true
		out.OverlappingStanding = []OverlappingStandingWatch{}
	}
	return out
}

func findByInvocationTx(ctx context.Context, tx *sql.Tx, sessionID, invocationID string) (Registration, bool, error) {
	reg, err := scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND creating_invocation_id = ?`,
		sessionID, invocationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, false, nil
	}
	return reg, err == nil, err
}

func findByDerivedCommandTx(ctx context.Context, tx *sql.Tx, owner Owner, cmdHandle string) (Registration, bool, error) {
	reg, err := scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND COALESCE(worker_job_id, '') = ? AND derived_command_handle = ?`,
		owner.SessionID, owner.WorkerJobID, cmdHandle))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, false, nil
	}
	return reg, err == nil, err
}

func enforceExplicitCapacityTx(ctx context.Context, tx *sql.Tx, owner Owner) error {
	rows, err := tx.QueryContext(ctx, `SELECT handle, state, slot_released FROM watch_registrations
		WHERE session_id = ? AND owner_kind = ? AND COALESCE(worker_job_id, '') = ?
		  AND (derived_command_handle IS NULL OR derived_command_handle = '')
		  AND slot_released = 0
		ORDER BY seq ASC`,
		owner.SessionID, string(owner.Kind), owner.WorkerJobID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	liveCount := 0
	slotCount := 0
	var reclaimable []string
	for rows.Next() {
		var handle, state string
		var released int
		if err := rows.Scan(&handle, &state, &released); err != nil {
			return err
		}
		slotCount++
		if state == string(StateLive) {
			liveCount++
		}
		reclaimable = append(reclaimable, handle)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if liveCount >= MaxLiveExplicitWatches || slotCount >= MaxTotalExplicitSlots {
		return &CapacityError{
			LiveCount:          liveCount,
			SlotCount:          slotCount,
			ReclaimableHandles: reclaimable,
		}
	}
	return nil
}

func allocateWatchSeqTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO watch_session_state (session_id, next_seq) VALUES (?, 1) ON CONFLICT(session_id) DO NOTHING`,
		sessionID); err != nil {
		return 0, err
	}
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT next_seq FROM watch_session_state WHERE session_id = ?`, sessionID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE watch_session_state SET next_seq = next_seq + 1 WHERE session_id = ?`, sessionID); err != nil {
		return 0, err
	}
	return seq, nil
}
