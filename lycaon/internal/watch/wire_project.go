package watch

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SetEventOutbox wires durable SSE event enqueuing and post-commit wakeups onto s.
func (s *Store) SetEventOutbox(outbox *eventoutbox.Outbox) {
	if s == nil || s.standing == nil {
		return
	}
	s.standing.mu.Lock()
	defer s.standing.mu.Unlock()
	if outbox == nil {
		s.standing.cb.EventPublisher = nil
		s.standing.cb.EventCommitNotify = nil
		return
	}
	s.standing.cb.EventPublisher = func(ctx context.Context, tx *sql.Tx, topic, projectID, sessionID, watchID, runID string, revision int) {
		enqueueWatchOutboxTx(ctx, tx, s, outbox, topic, projectID, sessionID, watchID, runID, revision)
	}
	s.standing.cb.EventCommitNotify = outbox.Notify
}

func enqueueWatchOutboxTx(
	ctx context.Context,
	tx *sql.Tx,
	s *Store,
	outbox *eventoutbox.Outbox,
	topic, projectID, sessionID, watchID, runID string,
	revision int,
) {
	projectID = strings.TrimSpace(projectID)
	if outbox == nil || tx == nil || uuid.Validate(projectID) != nil {
		return
	}
	switch topic {
	case string(wire.EventTopicWatchUpdated):
		ev := BuildWatchEventTx(ctx, s, tx, projectID, strings.TrimSpace(sessionID), strings.TrimSpace(watchID), revision)
		key := events.PublishKey{
			Project:        projectID,
			Session:        strings.TrimSpace(ev.SessionID),
			Facet:          ev.WatchID,
			EntityRevision: uint64(ev.Revision),
		}
		_ = outbox.EnqueueTx(ctx, tx, wire.EventTopicWatchUpdated, key, ev)
	case string(wire.EventTopicWatchRunUpdated):
		run, watchName, watchRev, err := LoadWatchRunEventRecordTx(ctx, tx, runID)
		if err != nil {
			return
		}
		if revision <= 0 {
			revision = watchRev
		}
		wr := WatchRunToWire(run, watchName)
		ev := wire.WatchRunEvent{
			RunID:     wr.RunID,
			WatchID:   wr.WatchID,
			ProjectID: wr.ProjectID,
			Revision:  revision,
			Run:       wr,
		}
		key := events.PublishKey{
			Project:        wr.ProjectID,
			Session:        strings.TrimSpace(wr.SessionID),
			Facet:          wr.RunID,
			EntityRevision: uint64(revision),
		}
		_ = outbox.EnqueueTx(ctx, tx, wire.EventTopicWatchRunUpdated, key, ev)
	}
}

const nanoUSDPerUSD = 1_000_000_000

// USDToNanoUSD converts floating-point USD to integer nano-USD.
func USDToNanoUSD(usd float64) int64 {
	return int64(math.Round(usd * nanoUSDPerUSD))
}

// NanoUSDToUSD converts integer nano-USD to floating-point USD.
func NanoUSDToUSD(nano int64) float64 {
	return float64(nano) / nanoUSDPerUSD
}

// ConditionToWire projects one runtime watch condition to its OpenAPI DTO.
func ConditionToWire(c awaitstore.Condition) wire.WatchCondition {
	procID := strings.TrimSpace(c.Handle)
	if procID == "" && len(c.Handles) > 0 {
		procID = strings.TrimSpace(c.Handles[0])
	}
	var cursorVal int64
	if c.Cursor != nil {
		cursorVal = *c.Cursor
	}
	var cal *wire.WatchCalendarSchedule
	if c.Calendar != nil {
		cal = &wire.WatchCalendarSchedule{
			Days:  append([]string(nil), c.Calendar.Days...),
			Times: append([]string(nil), c.Calendar.Times...),
		}
	}
	return wire.WatchCondition{
		Kind:         c.Kind,
		ProcessID:    procID,
		Cursor:       cursorVal,
		URL:          c.URL,
		Host:         c.Host,
		Port:         int(c.Port),
		DelayMs:      c.DelayMS,
		At:           c.At,
		EveryMinutes: c.EveryMinutes,
		Calendar:     cal,
		RootID:       c.Root,
		Path:         c.Path,
		Glob:         c.Glob,
		IncludeOwn:   c.IncludeOwn,
		Ref:          c.Ref,
	}
}

// ConditionFromWire converts an OpenAPI watch condition DTO to a runtime condition.
func ConditionFromWire(c wire.WatchCondition) awaitstore.Condition {
	var handles []string
	if strings.TrimSpace(c.ProcessID) != "" {
		handles = []string{strings.TrimSpace(c.ProcessID)}
	}
	var cursorPtr *int64
	if c.Kind == "process_output" || c.Cursor > 0 {
		v := c.Cursor
		cursorPtr = &v
	}
	var cal *awaitstore.CalendarSpec
	if c.Calendar != nil {
		cal = &awaitstore.CalendarSpec{
			Days:  append([]string(nil), c.Calendar.Days...),
			Times: append([]string(nil), c.Calendar.Times...),
		}
	}
	var port uint16
	if c.Port > 0 && c.Port <= 65535 {
		port = uint16(c.Port)
	}
	return awaitstore.Condition{
		Kind:         strings.TrimSpace(c.Kind),
		Handles:      handles,
		Handle:       strings.TrimSpace(c.ProcessID),
		Cursor:       cursorPtr,
		URL:          strings.TrimSpace(c.URL),
		Host:         strings.TrimSpace(c.Host),
		Port:         port,
		DelayMS:      c.DelayMs,
		At:           strings.TrimSpace(c.At),
		EveryMinutes: c.EveryMinutes,
		Calendar:     cal,
		Root:         strings.TrimSpace(c.RootID),
		Path:         strings.TrimSpace(c.Path),
		Glob:         strings.TrimSpace(c.Glob),
		IncludeOwn:   c.IncludeOwn,
		Ref:          strings.TrimSpace(c.Ref),
	}
}

// GateToWire projects a standing-watch gate selector to its OpenAPI DTO.
func GateToWire(g *awaitstore.Condition) *wire.WatchGate {
	if g == nil {
		return nil
	}
	return &wire.WatchGate{
		Kind:   g.Kind,
		RootID: g.Root,
		Path:   g.Path,
		Glob:   g.Glob,
		Ref:    g.Ref,
	}
}

// GateFromWire converts an OpenAPI gate DTO to a runtime condition selector.
func GateFromWire(g *wire.WatchGate) *awaitstore.Condition {
	if g == nil {
		return nil
	}
	return &awaitstore.Condition{
		Kind: strings.TrimSpace(g.Kind),
		Root: strings.TrimSpace(g.RootID),
		Path: strings.TrimSpace(g.Path),
		Glob: strings.TrimSpace(g.Glob),
		Ref:  strings.TrimSpace(g.Ref),
	}
}

// WatchRunToWire projects a WatchRun to its OpenAPI DTO.
func WatchRunToWire(r WatchRun, watchName string) wire.WatchRun {
	var detail map[string]any
	if len(r.ConditionDetail) > 0 {
		_ = json.Unmarshal(r.ConditionDetail, &detail)
	}
	return wire.WatchRun{
		RunID:             r.RunID,
		WatchID:           r.WatchID,
		WatchName:         watchName,
		ProjectID:         r.ProjectID,
		Status:            string(r.Status),
		ScheduledAt:       r.ScheduledFor.UTC(),
		SessionID:         r.SessionID,
		MissedCount:       r.MissedCount,
		ConditionDetail:   detail,
		SpendLimitNanoUsd: USDToNanoUSD(r.SpendLimitUSD),
		AllowUnpriced:     r.AllowUnpriced,
		CreatedAt:         r.CreatedAt.UTC(),
		StartedAt:         r.StartedAt,
		EndedAt:           r.EndedAt,
	}
}

// StandingWatchToWire projects a StandingWatch and its recent runs to its OpenAPI DTO.
func StandingWatchToWire(sw StandingWatch, runs []WatchRun) wire.StandingWatch {
	conds := make([]wire.WatchCondition, len(sw.Conditions))
	for i, c := range sw.Conditions {
		conds[i] = ConditionToWire(c)
	}
	var stateReason *string
	if strings.TrimSpace(sw.StateReason) != "" {
		reason := sw.StateReason
		stateReason = &reason
	}
	var keptFrom *wire.StandingWatchOrigin
	if sw.KeptFrom != nil && sw.KeptFrom.SessionID != "" && sw.KeptFrom.Handle != "" {
		keptFrom = &wire.StandingWatchOrigin{
			SessionID: sw.KeptFrom.SessionID,
			Handle:    sw.KeptFrom.Handle,
		}
	}
	var nextRunAt *time.Time
	if len(sw.NextRuns) > 0 {
		if parsed, err := time.Parse(time.RFC3339, sw.NextRuns[0]); err == nil {
			u := parsed.UTC()
			nextRunAt = &u
		}
	}
	var activeRun *wire.WatchRun
	var recentWire []wire.WatchRun
	if len(runs) > 0 {
		recentWire = make([]wire.WatchRun, 0, len(runs))
		for _, r := range runs {
			wr := WatchRunToWire(r, sw.Name)
			if activeRun == nil && (r.Status == RunQueued || r.Status == RunRunning || r.Status == RunWaitingForOwner) {
				cp := wr
				activeRun = &cp
			}
			recentWire = append(recentWire, wr)
		}
	}
	rev := sw.Revision
	if rev <= 0 {
		rev = 1
	}
	return wire.StandingWatch{
		WatchID:              sw.WatchID,
		ProjectID:            sw.ProjectID,
		OwnerPersonID:        sw.OwnerPersonID,
		Name:                 sw.Name,
		Prompt:               sw.Prompt,
		Conditions:           conds,
		ConditionSummary:     SummarizeConditions(sw.Conditions, sw.TimeZone),
		Gate:                 GateToWire(sw.Gate),
		MinGapMinutes:        sw.MinGapMinutes,
		Target:               wire.StandingWatchTarget{Kind: sw.Target.Kind, SessionID: sw.Target.SessionID},
		WorkflowID:           sw.WorkflowID,
		ModelID:              sw.ModelID,
		RunSpendLimitNanoUsd: USDToNanoUSD(sw.RunSpendLimitUSD),
		AllowUnpriced:        sw.AllowUnpriced,
		DailyRunCap:          sw.DailyRunCap,
		TimeZone:             sw.TimeZone,
		State:                string(sw.State),
		StateReason:          stateReason,
		KeptFrom:             keptFrom,
		NextRunAt:            nextRunAt,
		LastOutcome:          sw.LastOutcome,
		ActiveRun:            activeRun,
		RecentRuns:           recentWire,
		Revision:             rev,
		CreatedAt:            sw.CreatedAt.UTC(),
		UpdatedAt:            sw.UpdatedAt.UTC(),
	}
}

// ReceiptToWire converts a CancelReceipt and/or latest match into an OpenAPI AgentWatchReceipt.
func ReceiptToWire(reg Registration, cancelRec *CancelReceipt, latestMatch *RecordedMatch, recordedAt *time.Time) *wire.AgentWatchReceipt {
	if cancelRec == nil && latestMatch == nil && reg.Lifecycle.State != StateTerminal {
		return nil
	}
	out := &wire.AgentWatchReceipt{
		Handle:          reg.Handle,
		State:           string(reg.Lifecycle.State),
		TerminalOutcome: string(reg.Lifecycle.Outcome),
	}
	if recordedAt != nil {
		out.RecordedAt = *recordedAt
	}
	if cancelRec != nil {
		out.State = string(cancelRec.State)
		if cancelRec.Outcome != "" {
			out.TerminalOutcome = string(cancelRec.Outcome)
		}
		out.StandingWatchID = cancelRec.StandingWatchID
	}
	if latestMatch != nil {
		out.MatchedKind = latestMatch.SourceKind
		occ := int64(latestMatch.Seq)
		out.Occurrence = occ
		var detail map[string]any
		if json.Unmarshal(latestMatch.Evidence, &detail) == nil {
			out.Detail = detail
		}
	}
	return out
}

// AgentWatchToWire projects a session or job watch registration to its OpenAPI DTO.
func AgentWatchToWire(ctx context.Context, s *Store, reg Registration) wire.AgentWatch {
	conds := make([]wire.WatchCondition, len(reg.Conditions))
	for i, c := range reg.Conditions {
		conds[i] = ConditionToWire(c)
	}
	tz := ""
	if len(reg.Conditions) > 0 {
		tz = reg.Conditions[0].TimeZone
	}
	var draft *wire.AgentStandingDraft
	var overlaps []wire.OverlappingStandingWatch
	if reg.Standing != nil {
		draft = &wire.AgentStandingDraft{
			Name:     reg.Standing.Name,
			Prompt:   reg.Standing.Prompt,
			Replaces: reg.Standing.Replaces,
		}
		if s != nil && s.Standing() != nil && reg.Lifecycle.State == StateLive {
			rawOverlaps := s.Standing().Overlapping(ctx, reg.Owner.ProjectID, reg.Conditions)
			overlaps = make([]wire.OverlappingStandingWatch, 0, len(rawOverlaps))
			for _, ov := range rawOverlaps {
				summary := ov.Name
				if sw, err := s.Standing().Get(ctx, reg.Owner.ProjectID, ov.WatchID); err == nil {
					summary = SummarizeConditions(sw.Conditions, sw.TimeZone)
				}
				overlaps = append(overlaps, wire.OverlappingStandingWatch{
					WatchID:          ov.WatchID,
					Name:             ov.Name,
					State:            ov.State,
					ConditionSummary: summary,
				})
			}
		}
	}
	var finalRec *wire.AgentWatchReceipt
	if s != nil && s.Configured() {
		if cRec, mRec, recAt, err := LoadFinalReceipt(ctx, s.db, reg.WatchID); err == nil {
			finalRec = ReceiptToWire(reg, cRec, mRec, recAt)
		}
	}
	rev := reg.Lifecycle.Revision
	if rev <= 0 {
		rev = 1
	}
	return wire.AgentWatch{
		WatchID:             reg.WatchID,
		Handle:              reg.Handle,
		OwnerKind:           string(reg.Owner.Kind),
		ProjectID:           reg.Owner.ProjectID,
		SessionID:           reg.Owner.SessionID,
		WorkerID:            reg.Owner.WorkerJobID,
		SourceProcessID:     reg.DerivedCommandHandle,
		Label:               "",
		Conditions:          conds,
		ConditionSummary:    SummarizeConditions(reg.Conditions, tz),
		Delivery:            string(reg.Lifecycle.Delivery),
		Repeat:              reg.Lifecycle.Repeat,
		Derived:             reg.DerivedCommandHandle != "",
		Keepable:            IsAgentWatchKeepable(reg),
		State:               string(reg.Lifecycle.State),
		TerminalOutcome:     string(reg.Lifecycle.Outcome),
		StandingDraft:       draft,
		OverlappingStanding: overlaps,
		FinalReceipt:        finalRec,
		Revision:            rev,
		ExpiresAt:           reg.Lifecycle.ExpiresAt.UTC(),
		CreatedAt:           reg.CreatedAt.UTC(),
		UpdatedAt:           reg.UpdatedAt.UTC(),
	}
}

// CreateRequestToInput converts CreateStandingWatchRequest to StandingWatchInput.
func CreateRequestToInput(projectID string, req wire.CreateStandingWatchRequest) StandingWatchInput {
	conds := make([]awaitstore.Condition, len(req.Conditions))
	for i, c := range req.Conditions {
		conds[i] = ConditionFromWire(c)
	}
	in := StandingWatchInput{
		ProjectID:     strings.TrimSpace(projectID),
		Name:          req.Name,
		Prompt:        req.Prompt,
		Conditions:    conds,
		Gate:          GateFromWire(req.Gate),
		WorkflowID:    req.WorkflowID,
		ModelID:       req.ModelID,
		AllowUnpriced: &req.AllowUnpriced,
		TimeZone:      req.TimeZone,
	}
	if req.MinGapMinutes > 0 {
		v := req.MinGapMinutes
		in.MinGapMinutes = &v
	}
	if req.Target != nil {
		in.Target = &StandingTarget{Kind: req.Target.Kind, SessionID: req.Target.SessionID}
	}
	if req.RunSpendLimitNanoUsd > 0 {
		usd := NanoUSDToUSD(req.RunSpendLimitNanoUsd)
		in.RunSpendLimitUSD = &usd
	}
	if req.DailyRunCap > 0 {
		capVal := req.DailyRunCap
		in.DailyRunCap = &capVal
	}
	return in
}

// KeepRequestToInput converts KeepWatchRequest to StandingWatchInput and base revision.
func KeepRequestToInput(projectID string, req wire.KeepWatchRequest) (StandingWatchInput, int) {
	conds := make([]awaitstore.Condition, len(req.Conditions))
	for i, c := range req.Conditions {
		conds[i] = ConditionFromWire(c)
	}
	in := StandingWatchInput{
		ProjectID:  strings.TrimSpace(projectID),
		Name:       req.Name,
		Prompt:     req.Prompt,
		Conditions: conds,
		Gate:       GateFromWire(req.Gate),
		WorkflowID: req.WorkflowID,
		ModelID:    req.ModelID,
		TimeZone:   req.TimeZone,
	}
	if req.AllowUnpriced {
		v := true
		in.AllowUnpriced = &v
	}
	if req.MinGapMinutes > 0 {
		v := req.MinGapMinutes
		in.MinGapMinutes = &v
	}
	if req.Target != nil {
		in.Target = &StandingTarget{Kind: req.Target.Kind, SessionID: req.Target.SessionID}
	}
	if req.RunSpendLimitNanoUsd > 0 {
		usd := NanoUSDToUSD(req.RunSpendLimitNanoUsd)
		in.RunSpendLimitUSD = &usd
	}
	if req.DailyRunCap > 0 {
		capVal := req.DailyRunCap
		in.DailyRunCap = &capVal
	}
	return in, req.Revision
}

// UpdateRequestToPatch converts UpdateStandingWatchRequest to StandingWatchPatch and base revision.
func UpdateRequestToPatch(req wire.UpdateStandingWatchRequest) (StandingWatchPatch, int) {
	var conds []awaitstore.Condition
	if len(req.Conditions) > 0 {
		conds = make([]awaitstore.Condition, len(req.Conditions))
		for i, c := range req.Conditions {
			conds[i] = ConditionFromWire(c)
		}
	}
	patch := StandingWatchPatch{
		Name:          req.Name,
		Prompt:        req.Prompt,
		Conditions:    conds,
		MinGapMinutes: req.MinGapMinutes,
		WorkflowID:    req.WorkflowID,
		ModelID:       req.ModelID,
		AllowUnpriced: req.AllowUnpriced,
		DailyRunCap:   req.DailyRunCap,
		TimeZone:      req.TimeZone,
	}
	if req.ClearGate {
		patch.SetGate = true
		patch.Gate = nil
	} else if req.Gate != nil {
		patch.SetGate = true
		patch.Gate = GateFromWire(req.Gate)
	}
	if req.Target != nil {
		patch.Target = &StandingTarget{Kind: req.Target.Kind, SessionID: req.Target.SessionID}
	}
	if req.RunSpendLimitNanoUsd != nil {
		usd := NanoUSDToUSD(*req.RunSpendLimitNanoUsd)
		patch.RunSpendLimitUSD = &usd
	}
	if req.State != nil && *req.State == string(StateEnabled) {
		patch.Enable = true
	}
	return patch, req.Revision
}

// BuildWatchEventTx constructs a WatchEvent inside tx before commit.
func BuildWatchEventTx(ctx context.Context, s *Store, tx *sql.Tx, projectID, sessionID, watchID string, revision int) wire.WatchEvent {
	if revision <= 0 {
		revision = 1
	}
	sw, reg, err := LoadWatchEventRecordTx(ctx, s, tx, projectID, watchID)
	if err == nil && sw != nil {
		swWire := StandingWatchToWire(*sw, nil)
		pw := wire.ProjectWatch{
			WatchID:       sw.WatchID,
			OwnerKind:     string(OwnerPerson),
			ProjectID:     sw.ProjectID,
			Revision:      swWire.Revision,
			StandingWatch: &swWire,
		}
		return wire.WatchEvent{
			Action:    "updated",
			WatchID:   sw.WatchID,
			OwnerKind: string(OwnerPerson),
			ProjectID: sw.ProjectID,
			Revision:  swWire.Revision,
			Watch:     &pw,
		}
	}
	if err == nil && reg != nil {
		aw := AgentWatchToWireTx(tx, *reg)
		pw := wire.ProjectWatch{
			WatchID:    reg.WatchID,
			OwnerKind:  string(reg.Owner.Kind),
			ProjectID:  reg.Owner.ProjectID,
			SessionID:  reg.Owner.SessionID,
			Handle:     reg.Handle,
			Revision:   aw.Revision,
			AgentWatch: &aw,
		}
		return wire.WatchEvent{
			Action:    "updated",
			WatchID:   reg.WatchID,
			OwnerKind: string(reg.Owner.Kind),
			ProjectID: reg.Owner.ProjectID,
			SessionID: reg.Owner.SessionID,
			Handle:    reg.Handle,
			Revision:  aw.Revision,
			Watch:     &pw,
		}
	}
	ownerKind := string(OwnerPerson)
	if strings.TrimSpace(sessionID) != "" {
		ownerKind = string(OwnerSession)
	}
	return wire.WatchEvent{
		Action:    "deleted",
		WatchID:   watchID,
		OwnerKind: ownerKind,
		ProjectID: projectID,
		SessionID: sessionID,
		Revision:  revision,
	}
}

// AgentWatchToWireTx projects an agent watch inside tx so uncommitted final receipts are included.
func AgentWatchToWireTx(tx *sql.Tx, reg Registration) wire.AgentWatch {
	conds := make([]wire.WatchCondition, len(reg.Conditions))
	for i, c := range reg.Conditions {
		conds[i] = ConditionToWire(c)
	}
	tz := ""
	if len(reg.Conditions) > 0 {
		tz = reg.Conditions[0].TimeZone
	}
	var draft *wire.AgentStandingDraft
	if reg.Standing != nil {
		draft = &wire.AgentStandingDraft{
			Name:     reg.Standing.Name,
			Prompt:   reg.Standing.Prompt,
			Replaces: reg.Standing.Replaces,
		}
	}
	var finalRec *wire.AgentWatchReceipt
	if tx != nil {
		if cRec, mRec, recAt, err := LoadFinalReceipt(context.Background(), tx, reg.WatchID); err == nil {
			finalRec = ReceiptToWire(reg, cRec, mRec, recAt)
		}
	}
	rev := reg.Lifecycle.Revision
	if rev <= 0 {
		rev = 1
	}
	return wire.AgentWatch{
		WatchID:         reg.WatchID,
		Handle:          reg.Handle,
		OwnerKind:       string(reg.Owner.Kind),
		ProjectID:       reg.Owner.ProjectID,
		SessionID:       reg.Owner.SessionID,
		WorkerID:        reg.Owner.WorkerJobID,
		SourceProcessID: reg.DerivedCommandHandle,
		Conditions:      conds,
		ConditionSummary: SummarizeConditions(reg.Conditions, tz),
		Delivery:        string(reg.Lifecycle.Delivery),
		Repeat:          reg.Lifecycle.Repeat,
		Derived:         reg.DerivedCommandHandle != "",
		Keepable:        IsAgentWatchKeepable(reg),
		State:           string(reg.Lifecycle.State),
		TerminalOutcome: string(reg.Lifecycle.Outcome),
		StandingDraft:   draft,
		FinalReceipt:    finalRec,
		Revision:        rev,
		ExpiresAt:       reg.Lifecycle.ExpiresAt.UTC(),
		CreatedAt:       reg.CreatedAt.UTC(),
		UpdatedAt:       reg.UpdatedAt.UTC(),
	}
}
