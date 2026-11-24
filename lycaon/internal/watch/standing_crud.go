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

const standingWatchColumns = `wr.watch_id, wr.project_id, COALESCE(wr.owner_person_id, ''),
	sws.name, sws.prompt, wr.conditions_json, wr.authority_json, COALESCE(sws.gate_json, ''),
	sws.min_gap_minutes, sws.target_json, sws.workflow_id, sws.model_id,
	sws.run_spend_limit_usd, sws.allow_unpriced, sws.daily_run_cap, sws.time_zone,
	wr.state, COALESCE(wr.state_reason, ''), sws.interval_anchor, sws.consecutive_failures,
	COALESCE(sws.kept_from_session_id, ''), COALESCE(sws.kept_from_handle, ''),
	COALESCE(sws.last_outcome, ''), wr.revision, wr.created_at, wr.updated_at, COALESCE(wr.ended_at, '')`

func scanStandingWatch(row scanner, now time.Time) (StandingWatch, error) {
	var sw StandingWatch
	var condJSON, authJSON, gateJSON, targetJSON, state, anchorStr string
	var keptSess, keptHandle, createdAt, updatedAt, endedAt string
	var allowUnpriced int
	if err := row.Scan(
		&sw.WatchID, &sw.ProjectID, &sw.OwnerPersonID,
		&sw.Name, &sw.Prompt, &condJSON, &authJSON, &gateJSON,
		&sw.MinGapMinutes, &targetJSON, &sw.WorkflowID, &sw.ModelID,
		&sw.RunSpendLimitUSD, &allowUnpriced, &sw.DailyRunCap, &sw.TimeZone,
		&state, &sw.StateReason, &anchorStr, &sw.ConsecutiveFailures,
		&keptSess, &keptHandle, &sw.LastOutcome, &sw.Revision, &createdAt, &updatedAt, &endedAt,
	); err != nil {
		return StandingWatch{}, err
	}
	sw.AllowUnpriced = allowUnpriced == 1
	sw.State = State(state)
	if err := json.Unmarshal([]byte(condJSON), &sw.Conditions); err != nil {
		return StandingWatch{}, err
	}
	_ = json.Unmarshal([]byte(authJSON), &sw.Authority)
	if gateJSON != "" {
		var g awaitstore.Condition
		if err := json.Unmarshal([]byte(gateJSON), &g); err != nil {
			return StandingWatch{}, err
		}
		sw.Gate = &g
	}
	if err := json.Unmarshal([]byte(targetJSON), &sw.Target); err != nil {
		return StandingWatch{}, err
	}
	if keptSess != "" && keptHandle != "" {
		sw.KeptFrom = &StandingKeptFrom{SessionID: keptSess, Handle: keptHandle}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, anchorStr); err == nil {
		sw.IntervalAnchor = parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		sw.CreatedAt = parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		sw.UpdatedAt = parsed.UTC()
	}
	if endedAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, endedAt); err == nil {
			u := parsed.UTC()
			sw.EndedAt = &u
		}
	}
	if sw.State == StateEnabled {
		if next, err := PreviewStandingSchedule(sw.Conditions, sw.TimeZone, sw.IntervalAnchor, now, 3); err == nil && len(next) > 0 {
			loc, lErr := loadConditionLocation(sw.TimeZone)
			if lErr != nil || loc == nil {
				loc = time.UTC
			}
			sw.NextRuns = make([]string, len(next))
			for i, t := range next {
				sw.NextRuns[i] = t.In(loc).Format(time.RFC3339)
			}
		}
	}
	return sw, nil
}

func (e *StandingEngine) Exists(ctx context.Context, projectID, watchID string) bool {
	if e == nil || !e.store.Configured() || strings.TrimSpace(projectID) == "" || strings.TrimSpace(watchID) == "" {
		return false
	}
	var count int
	err := e.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watch_registrations WHERE watch_id = ? AND project_id = ? AND owner_kind = 'person'`,
		strings.TrimSpace(watchID), strings.TrimSpace(projectID)).Scan(&count)
	return err == nil && count > 0
}

func (e *StandingEngine) Get(ctx context.Context, projectID, watchID string) (StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, &UnknownHandleError{Handle: watchID}
	}
	projectID, watchID = strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	sw, err := scanStandingWatch(e.store.db.QueryRowContext(ctx,
		`SELECT `+standingWatchColumns+`
		 FROM watch_registrations wr
		 JOIN standing_watch_settings sws ON sws.watch_id = wr.watch_id
		 WHERE wr.watch_id = ? AND wr.project_id = ?`,
		watchID, projectID), e.store.clock.Now())
	if errors.Is(err, sql.ErrNoRows) {
		var ownerKind string
		if qErr := e.store.db.QueryRowContext(ctx,
			`SELECT owner_kind FROM watch_registrations WHERE watch_id = ? AND project_id = ?`,
			watchID, projectID).Scan(&ownerKind); qErr == nil && ownerKind != string(OwnerPerson) {
			return StandingWatch{}, &NotPersonOwnedError{WatchID: watchID}
		}
		return StandingWatch{}, &UnknownHandleError{Handle: watchID}
	}
	return sw, err
}

func (e *StandingEngine) List(ctx context.Context, projectID string, includeCompleted bool) ([]StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return nil, nil
	}
	query := `SELECT ` + standingWatchColumns + `
		FROM watch_registrations wr
		JOIN standing_watch_settings sws ON sws.watch_id = wr.watch_id
		WHERE wr.project_id = ? AND wr.owner_kind = 'person'`
	if !includeCompleted {
		query += ` AND wr.state != 'completed'`
	}
	query += ` ORDER BY wr.created_at ASC, wr.watch_id ASC`
	rows, err := e.store.db.QueryContext(ctx, query, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	now := e.store.clock.Now()
	var out []StandingWatch
	for rows.Next() {
		sw, sErr := scanStandingWatch(rows, now)
		if sErr != nil {
			return nil, sErr
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}

func (e *StandingEngine) Create(ctx context.Context, actorPersonID string, in StandingWatchInput) (StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, fmt.Errorf("watch store is not configured")
	}
	actorPersonID = strings.TrimSpace(actorPersonID)
	if actorPersonID == "" {
		return StandingWatch{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "owner_person_id", Message: "person owner is required"}
	}
	now := e.store.clock.Now()
	prepared, err := normalizeStandingInput(ctx, e, in, now)
	if err != nil {
		return StandingWatch{}, err
	}
	cursors := map[string]string{standingLastEvalCursorKey: now.UTC().Format(time.RFC3339Nano)}
	initialManifests, gateBaselineJSON, err := initStandingBaselines(ctx, e, prepared, cursors)
	if err != nil {
		return StandingWatch{}, err
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return StandingWatch{}, err
	}
	defer func() { _ = tx.Rollback() }()
	watchID, err := insertStandingWatchTx(ctx, tx, actorPersonID, prepared, cursors, gateBaselineJSON, nil, now)
	if err != nil {
		return StandingWatch{}, err
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", prepared.ProjectID, "", watchID, "", 1)
	if err := tx.Commit(); err != nil {
		return StandingWatch{}, mapStandingSQLiteErr(err, prepared.Name)
	}
	for altIdx, snap := range initialManifests {
		e.store.manifests.set(watchID, altIdx, snap)
	}
	sw, err := e.Get(ctx, prepared.ProjectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	armStandingWatchRuntime(e, sw, now)
	return sw, nil
}

func (e *StandingEngine) KeepAgentWatch(ctx context.Context, actorPersonID, sessionID, handle string, in StandingWatchInput, baseRevision int) (StandingWatch, CancelReceipt, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, CancelReceipt{}, fmt.Errorf("watch store is not configured")
	}
	actorPersonID, sessionID, handle = strings.TrimSpace(actorPersonID), strings.TrimSpace(sessionID), strings.TrimSpace(handle)
	if actorPersonID == "" {
		return StandingWatch{}, CancelReceipt{}, &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: "owner_person_id", Message: "person owner is required"}
	}
	agentReg, err := loadAgentWatchForKeep(ctx, e.store, sessionID, handle)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	now := e.store.clock.Now()
	replacesID := populateKeepDefaults(ctx, e, agentReg, &in, now)
	prepared, err := normalizeStandingInput(ctx, e, in, now)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	cursors := map[string]string{standingLastEvalCursorKey: now.UTC().Format(time.RFC3339Nano)}
	initialManifests, gateBaselineJSON, err := initStandingBaselines(ctx, e, prepared, cursors)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}

	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	liveAgent, err := loadRegistrationByIDTx(ctx, tx, agentReg.WatchID)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	if liveAgent.Lifecycle.State != StateLive {
		return StandingWatch{}, CancelReceipt{}, &KeepUnavailableError{Handle: handle, Reason: "watch has already ended"}
	}

	var standingID string
	standingRev := 1
	if replacesID != "" {
		standingID = replacesID
		if standingRev, err = applyReplacesKeepTx(ctx, tx, actorPersonID, prepared, replacesID, baseRevision, cursors, gateBaselineJSON, now); err != nil {
			return StandingWatch{}, CancelReceipt{}, err
		}
	} else {
		keptFrom := &StandingKeptFrom{SessionID: sessionID, Handle: handle}
		if standingID, err = insertStandingWatchTx(ctx, tx, actorPersonID, prepared, cursors, gateBaselineJSON, keptFrom, now); err != nil {
			return StandingWatch{}, CancelReceipt{}, err
		}
	}

	finalReceipt, woke, err := finalizeKeptAgentWatchTx(ctx, tx, liveAgent, standingID, now)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", liveAgent.Owner.ProjectID, liveAgent.Owner.SessionID, liveAgent.WatchID, "", liveAgent.Lifecycle.Revision+1)
	publishWatchEventTx(ctx, e, tx, "watch.updated", prepared.ProjectID, "", standingID, "", standingRev)
	if err := tx.Commit(); err != nil {
		return StandingWatch{}, CancelReceipt{}, mapStandingSQLiteErr(err, prepared.Name)
	}
	retireWatchRuntime(e.store, liveAgent.WatchID)
	for _, w := range woke {
		e.store.heap.Remove(KindWaitDeadline, w.lease.ID)
		notifyLeaseWake(e.store, ctx, w.lease, w.winner)
	}
	for altIdx, snap := range initialManifests {
		e.store.manifests.set(standingID, altIdx, snap)
	}
	sw, err := e.Get(ctx, prepared.ProjectID, standingID)
	if err != nil {
		return StandingWatch{}, CancelReceipt{}, err
	}
	armStandingWatchRuntime(e, sw, now)
	return sw, finalReceipt, nil
}

func loadAgentWatchForKeep(ctx context.Context, s *Store, sessionID, handle string) (Registration, error) {
	reg, err := scanRegistration(s.db.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND handle = ?`,
		sessionID, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, &UnknownHandleError{Handle: handle}
	}
	if err != nil {
		return Registration{}, err
	}
	if reg.Owner.Kind == OwnerPerson || reg.Lifecycle.State != StateLive {
		return Registration{}, &KeepUnavailableError{Handle: handle, Reason: "watch has already ended"}
	}
	for _, c := range reg.Conditions {
		if c.Kind != "timer" && c.Kind != "git_ref" && c.Kind != "file_change" {
			return Registration{}, &KeepUnavailableError{Handle: handle, Reason: fmt.Sprintf("condition %s cannot be kept as a standing watch", c.Kind)}
		}
	}
	return reg, nil
}

func populateKeepDefaults(ctx context.Context, e *StandingEngine, agentReg Registration, in *StandingWatchInput, now time.Time) string {
	if in.ProjectID == "" {
		in.ProjectID = agentReg.Owner.ProjectID
	}
	if len(in.Authority.Roots) == 0 && len(agentReg.Authority.Roots) > 0 {
		in.Authority = agentReg.Authority
	}
	if in.Name == "" && agentReg.Standing != nil {
		in.Name = agentReg.Standing.Name
	}
	if in.Prompt == "" && agentReg.Standing != nil {
		in.Prompt = agentReg.Standing.Prompt
	}
	anchor := agentReg.CreatedAt
	if rawAnchor := agentReg.Cursors[timerAnchorCursorKey]; rawAnchor != "" {
		if parsed, pErr := time.Parse(time.RFC3339Nano, rawAnchor); pErr == nil {
			anchor = parsed.UTC()
		}
	}
	_, _, defTZ, _ := lookupProjectDefaults(ctx, e, in.ProjectID)
	tz := strings.TrimSpace(in.TimeZone)
	if tz == "" {
		tz = defTZ
	}
	src := in.Conditions
	if len(src) == 0 {
		src = agentReg.Conditions
	}
	converted := make([]awaitstore.Condition, len(src))
	for i, c := range src {
		if len(in.Conditions) == 0 {
			c.IncludeOwn = false
		}
		converted[i] = ConvertAgentTimerForStanding(c, agentReg.Lifecycle.Repeat, anchor, now, tz)
	}
	in.Conditions = converted
	if agentReg.Standing != nil {
		return strings.TrimSpace(agentReg.Standing.Replaces)
	}
	return ""
}

func finalizeKeptAgentWatchTx(ctx context.Context, tx *sql.Tx, liveAgent Registration, standingID string, now time.Time) (CancelReceipt, []wokeLease, error) {
	finalReceipt, woke, err := cancelRegistrationTx(ctx, tx, liveAgent, OutcomeKept, now)
	if err != nil {
		return CancelReceipt{}, nil, err
	}
	finalReceipt.StandingWatchID = standingID
	rawFinal, err := json.Marshal(finalReceipt)
	if err != nil {
		return CancelReceipt{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations SET final_receipt_json = ? WHERE watch_id = ?`, string(rawFinal), liveAgent.WatchID); err != nil {
		return CancelReceipt{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watch_deliveries SET payload_json = ? WHERE delivery_id = ?`, string(rawFinal), finalReceipt.ReceiptID); err != nil {
		return CancelReceipt{}, nil, err
	}
	if err := insertTerminalOutcomeMatchTx(ctx, tx, liveAgent.WatchID, MatchInput{
		SourceKind:   "standing_keep",
		OccurrenceID: "kept:" + standingID,
		Outcome:      string(OutcomeKept),
		RetainedRef:  finalReceipt.ReceiptID,
		Evidence: map[string]any{
			"kind": "standing_keep", "outcome": string(OutcomeKept),
			"watch_id": standingID, "standing_watch_id": standingID,
		},
	}, now); err != nil {
		return CancelReceipt{}, nil, err
	}
	return finalReceipt, woke, nil
}

func insertTerminalOutcomeMatchTx(ctx context.Context, tx *sql.Tx, watchID string, input MatchInput, now time.Time) error {
	evBytes, err := json.Marshal(input.Evidence)
	if err != nil {
		return err
	}
	nowStr := now.UTC().Format(time.RFC3339Nano)
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT match_seq + 1 FROM watch_registrations WHERE watch_id = ?`, watchID).Scan(&seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watch_matches (
		match_id, watch_id, seq, source_kind, source_occurrence_id, outcome,
		evidence_json, retained_ref, restricted, reservation_state, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), 0, 'pending', ?)`,
		uuid.NewString(), watchID, seq, input.SourceKind, input.OccurrenceID,
		input.Outcome, string(evBytes), input.RetainedRef, nowStr); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE watch_registrations SET match_seq = ? WHERE watch_id = ?`, seq, watchID)
	return err
}

func (e *StandingEngine) CancelByPerson(ctx context.Context, sessionID, handle string) (CancelReceipt, error) {
	if e == nil || !e.store.Configured() {
		return CancelReceipt{}, &UnknownHandleError{Handle: handle}
	}
	sessionID, handle = strings.TrimSpace(sessionID), strings.TrimSpace(handle)
	now := e.store.clock.Now()
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return CancelReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := scanRegistration(tx.QueryRowContext(ctx,
		`SELECT `+registrationColumns+` FROM watch_registrations WHERE session_id = ? AND handle = ?`,
		sessionID, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return CancelReceipt{}, &UnknownHandleError{Handle: handle}
	}
	if err != nil {
		return CancelReceipt{}, err
	}
	outcome := OutcomeCanceled
	if reg.Standing != nil {
		outcome = OutcomeDismissed
	}
	wasLive := reg.Lifecycle.State == StateLive
	receipt, woke, err := cancelRegistrationTx(ctx, tx, reg, outcome, now)
	if err != nil {
		return CancelReceipt{}, err
	}
	if wasLive && outcome == OutcomeDismissed {
		_ = insertTerminalOutcomeMatchTx(ctx, tx, reg.WatchID, MatchInput{
			SourceKind: "standing_dismiss", OccurrenceID: "dismissed:" + reg.WatchID,
			Outcome: string(OutcomeDismissed), RetainedRef: receipt.ReceiptID,
			Evidence: map[string]any{"kind": "standing_dismiss", "outcome": string(OutcomeDismissed), "reason": "person declined standing watch proposal"},
		}, now)
	}
	if wasLive {
		publishWatchEventTx(ctx, e, tx, "watch.updated", reg.Owner.ProjectID, reg.Owner.SessionID, reg.WatchID, "", reg.Lifecycle.Revision+1)
	}
	if err := tx.Commit(); err != nil {
		return CancelReceipt{}, err
	}
	retireWatchRuntime(e.store, reg.WatchID)
	for _, w := range woke {
		e.store.heap.Remove(KindWaitDeadline, w.lease.ID)
		notifyLeaseWake(e.store, ctx, w.lease, w.winner)
	}
	return receipt, nil
}

func (e *StandingEngine) Update(ctx context.Context, actorPersonID, projectID, watchID string, baseRevision int, patch StandingWatchPatch) (StandingWatch, error) {
	if e == nil || !e.store.Configured() {
		return StandingWatch{}, fmt.Errorf("watch store is not configured")
	}
	actorPersonID, projectID, watchID = strings.TrimSpace(actorPersonID), strings.TrimSpace(projectID), strings.TrimSpace(watchID)
	current, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	takeover := current.State == StateDisabled && patch.Enable
	if current.OwnerPersonID != actorPersonID && !takeover {
		return StandingWatch{}, &NotOwnerError{WatchID: watchID}
	}
	if baseRevision <= 0 || current.Revision != baseRevision {
		return StandingWatch{}, &RevisionConflictError{WatchID: watchID, CurrentRevision: current.Revision}
	}

	merged, conditionsChanged, gateChanged, tzChanged := mergeStandingPatch(current, patch)
	now := e.store.clock.Now()
	prepared, err := normalizeStandingInput(ctx, e, merged, now)
	if err != nil {
		return StandingWatch{}, err
	}
	timerChanged := tzChanged || (conditionsChanged && (hasStandingTimer(current.Conditions) || hasStandingTimer(prepared.Conditions)))
	var newManifests map[int]map[string]string
	var newGateBaseline string
	cursors := map[string]string{}
	if conditionsChanged || gateChanged {
		if newManifests, newGateBaseline, err = initStandingBaselines(ctx, e, prepared, cursors); err != nil {
			return StandingWatch{}, err
		}
	}

	if err := commitStandingUpdate(ctx, e, current, prepared, actorPersonID, baseRevision, takeover, timerChanged, gateChanged, cursors, newGateBaseline, now); err != nil {
		return StandingWatch{}, err
	}
	for altIdx, snap := range newManifests {
		e.store.manifests.set(watchID, altIdx, snap)
	}
	if takeover {
		emitStandingNotice(ctx, e, projectID, watchID, prepared.Name, NoticeStandingWatchDisabled, "", true)
	}
	sw, err := e.Get(ctx, projectID, watchID)
	if err != nil {
		return StandingWatch{}, err
	}
	retireWatchRuntime(e.store, watchID)
	if sw.State == StateEnabled {
		armStandingWatchRuntime(e, sw, now)
	}
	return sw, nil
}

func mergeStandingPatch(current StandingWatch, patch StandingWatchPatch) (merged StandingWatchInput, condChanged, gateChanged, tzChanged bool) {
	merged = StandingWatchInput{
		ProjectID: current.ProjectID, Name: current.Name, Prompt: current.Prompt,
		Conditions: append([]awaitstore.Condition(nil), current.Conditions...), Gate: current.Gate,
		MinGapMinutes: &current.MinGapMinutes, Target: &current.Target,
		WorkflowID: current.WorkflowID, ModelID: current.ModelID,
		RunSpendLimitUSD: &current.RunSpendLimitUSD, AllowUnpriced: &current.AllowUnpriced,
		DailyRunCap: &current.DailyRunCap, TimeZone: current.TimeZone, Authority: current.Authority,
	}
	if patch.Name != nil {
		merged.Name = *patch.Name
	}
	if patch.Prompt != nil {
		merged.Prompt = *patch.Prompt
	}
	if patch.Conditions != nil {
		condChanged = !StandingConditionsEqual(current.Conditions, patch.Conditions)
		merged.Conditions = patch.Conditions
	}
	if patch.SetGate {
		gateChanged = !sameOptionalCondition(current.Gate, patch.Gate)
		merged.Gate = patch.Gate
	}
	if patch.MinGapMinutes != nil {
		merged.MinGapMinutes = patch.MinGapMinutes
	}
	if patch.Target != nil {
		merged.Target = patch.Target
	}
	if patch.WorkflowID != nil {
		merged.WorkflowID = *patch.WorkflowID
	}
	if patch.ModelID != nil {
		merged.ModelID = *patch.ModelID
	}
	if patch.RunSpendLimitUSD != nil {
		merged.RunSpendLimitUSD = patch.RunSpendLimitUSD
	}
	if patch.AllowUnpriced != nil {
		merged.AllowUnpriced = patch.AllowUnpriced
	}
	if patch.DailyRunCap != nil {
		merged.DailyRunCap = patch.DailyRunCap
	}
	if patch.TimeZone != nil && strings.TrimSpace(*patch.TimeZone) != current.TimeZone {
		tzChanged = true
		merged.TimeZone = *patch.TimeZone
	}
	return merged, condChanged, gateChanged, tzChanged
}

func commitStandingUpdate(
	ctx context.Context,
	e *StandingEngine,
	current StandingWatch,
	prepared preparedStandingInput,
	actorPersonID string,
	baseRevision int,
	takeover, timerChanged, gateChanged bool,
	cursors map[string]string,
	newGateBaseline string,
	now time.Time,
) error {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	reg, err := loadRegistrationByIDTx(ctx, tx, current.WatchID)
	if err != nil {
		return err
	}
	if reg.Lifecycle.Revision != baseRevision {
		return &RevisionConflictError{WatchID: current.WatchID, CurrentRevision: reg.Lifecycle.Revision}
	}
	if err := checkUniqueStandingNameTx(ctx, tx, current.ProjectID, prepared.Name, current.WatchID); err != nil {
		return err
	}
	newOwner, newState, newReason := current.OwnerPersonID, current.State, current.StateReason
	if takeover {
		newOwner, newState, newReason = actorPersonID, StateEnabled, ""
	}
	newAnchor := current.IntervalAnchor
	nowStr := now.UTC().Format(time.RFC3339Nano)
	mergedCursors := reg.Cursors
	if mergedCursors == nil {
		mergedCursors = map[string]string{}
	}
	for k, v := range cursors {
		mergedCursors[k] = v
	}
	if timerChanged || takeover {
		newAnchor = now.UTC()
		mergedCursors[standingLastEvalCursorKey] = nowStr
		if _, err := tx.ExecContext(ctx,
			`UPDATE watch_runs SET status = 'canceled', ended_at = ?
			 WHERE watch_id = ? AND status = 'pending' AND run_key LIKE 'timer:%'`,
			nowStr, current.WatchID); err != nil {
			return err
		}
	}
	condBytes, err := json.Marshal(prepared.Conditions)
	if err != nil {
		return err
	}
	cursorBytes, err := json.Marshal(mergedCursors)
	if err != nil {
		return err
	}
	targetBytes, err := json.Marshal(prepared.Target)
	if err != nil {
		return err
	}
	var gateStr string
	if prepared.Gate != nil {
		gb, gErr := json.Marshal(prepared.Gate)
		if gErr != nil {
			return gErr
		}
		gateStr = string(gb)
	}
	nextRev := reg.Lifecycle.Revision + 1
	if _, err := tx.ExecContext(ctx, `UPDATE watch_registrations
		SET owner_person_id = ?, conditions_json = ?, cursors_json = ?, state = ?, state_reason = NULLIF(?, ''),
		    revision = ?, updated_at = ?
		WHERE watch_id = ?`,
		newOwner, string(condBytes), string(cursorBytes), string(newState), newReason, nextRev, nowStr, current.WatchID); err != nil {
		return err
	}
	if gateChanged || prepared.Gate == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings
			SET name = ?, name_folded = ?, prompt = ?, gate_json = NULLIF(?, ''), gate_baseline_json = NULLIF(?, ''),
			    min_gap_minutes = ?, target_json = ?, workflow_id = ?, model_id = ?, run_spend_limit_usd = ?,
			    allow_unpriced = ?, daily_run_cap = ?, time_zone = ?, interval_anchor = ?,
			    consecutive_failures = CASE WHEN ? = 1 THEN 0 ELSE consecutive_failures END
			WHERE watch_id = ?`,
			prepared.Name, strings.ToLower(prepared.Name), prepared.Prompt, gateStr, newGateBaseline,
			prepared.MinGapMinutes, string(targetBytes), prepared.WorkflowID, prepared.ModelID, prepared.RunSpendLimitUSD,
			boolToInt(prepared.AllowUnpriced), prepared.DailyRunCap, prepared.TimeZone, newAnchor.Format(time.RFC3339Nano),
			boolToInt(takeover), current.WatchID); err != nil {
			return mapStandingSQLiteErr(err, prepared.Name)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE standing_watch_settings
			SET name = ?, name_folded = ?, prompt = ?, min_gap_minutes = ?, target_json = ?, workflow_id = ?,
			    model_id = ?, run_spend_limit_usd = ?, allow_unpriced = ?, daily_run_cap = ?, time_zone = ?,
			    interval_anchor = ?, consecutive_failures = CASE WHEN ? = 1 THEN 0 ELSE consecutive_failures END
			WHERE watch_id = ?`,
			prepared.Name, strings.ToLower(prepared.Name), prepared.Prompt,
			prepared.MinGapMinutes, string(targetBytes), prepared.WorkflowID, prepared.ModelID, prepared.RunSpendLimitUSD,
			boolToInt(prepared.AllowUnpriced), prepared.DailyRunCap, prepared.TimeZone, newAnchor.Format(time.RFC3339Nano),
			boolToInt(takeover), current.WatchID); err != nil {
			return mapStandingSQLiteErr(err, prepared.Name)
		}
	}
	publishWatchEventTx(ctx, e, tx, "watch.updated", current.ProjectID, "", current.WatchID, "", nextRev)
	return mapStandingSQLiteErr(tx.Commit(), prepared.Name)
}
