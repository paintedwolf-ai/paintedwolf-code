package hitl

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// EventPublisher publishes checkpoint events and requests attention rebuilds.
// Outbox delivery still requires a separate attention rebuild.
type EventPublisher interface {
	PublishCheckpoint(ctx context.Context, projectIDOrDir, sessionID string, ev api.CheckpointEvent)
	PublishAttention(ctx context.Context)
}

// AuthzRecorder seals every human checkpoint outcome in the same transaction.
type AuthzRecorder interface {
	authzledger.Recorder
	authzledger.TransactionalRecorder
}

// Manager implements CheckpointManager with SQLite persistence and SSE notifications.
type Manager struct {
	authorityMutation sync.Mutex
	store             Store
	events            EventPublisher
	authzRecorder     AuthzRecorder
	// expiryFor returns how long a pending checkpoint may sit before it auto-expires
	// (resolving to denied — fail-safe). Overridable in tests.
	expiryFor func() time.Duration
	// onToolApprovalTerminal updates pending-coalesce spam guards after a
	// tool_approval reaches a terminal status (approve / deny / expire / edit).
	onToolApprovalTerminal     func(chatSessionID, grantKey string, status DecisionStatus)
	onToolApprovalRestored     func(StoredCheckpoint)
	onToolApprovalDenyRestored func(StoredCheckpoint)
	authorityInstaller         ApprovalAuthorityInstaller
	sessionAdmission           func(ctx context.Context, sessionID string, fn func() error) error
	// checkpointWait observes an execution blocked on a session's checkpoint.
	checkpointWait func(ctx context.Context, sessionID string) (end func())
	// resolutionLocks serializes resolution and expiry per checkpoint id.
	resolutionLocks keyedMutex
	// vault verifies presence and holds each chat's unlock for person-held values.
	vault vaultUnlock
	// expiries holds armed fail-safe expiries until they fire or shutdown.
	expiries expiryTimers
}

// SetSessionAdmission wires session admission.
func (m *Manager) SetSessionAdmission(admit func(ctx context.Context, sessionID string, fn func() error) error) {
	if m != nil {
		m.sessionAdmission = admit
	}
}

// NewManager constructs a checkpoint manager. store and authz are required.
func NewManager(store Store, events EventPublisher, authz AuthzRecorder) *Manager {
	if store == nil {
		panic("hitl: store is required")
	}
	if authz == nil {
		panic("hitl: authz recorder is required")
	}
	return &Manager{store: store, events: events, authzRecorder: authz, expiryFor: func() time.Duration { return DefaultCheckpointTimeout }}
}

// SetCheckpointExpiry overrides the fail-safe expiry window for tests. Passing
// nil restores the default window.
func (m *Manager) SetCheckpointExpiry(fn func() time.Duration) {
	if fn == nil {
		fn = func() time.Duration { return DefaultCheckpointTimeout }
	}
	m.expiryFor = fn
}

// SetToolApprovalTerminalHook observes terminal tool approvals.
func (m *Manager) SetToolApprovalTerminalHook(fn func(chatSessionID, grantKey string, status DecisionStatus)) {
	if m == nil {
		return
	}
	m.onToolApprovalTerminal = fn
}

// SetToolApprovalRestoreHook restores pending approval coalescing.
func (m *Manager) SetToolApprovalRestoreHook(fn func(StoredCheckpoint)) {
	if m == nil {
		return
	}
	m.onToolApprovalRestored = fn
}

// SetToolApprovalDenyRestoreHook restores denied approval coalescing.
func (m *Manager) SetToolApprovalDenyRestoreHook(fn func(StoredCheckpoint)) {
	if m != nil {
		m.onToolApprovalDenyRestored = fn
	}
}

// RestorePending restores pending checkpoints with their original expiry deadlines.
func (m *Manager) RestorePending(ctx context.Context) error {
	rows, err := m.store.ListPending(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		d := m.expiryFor()
		if d > 0 {
			remaining := row.CreatedAt.Add(d).Sub(now)
			if remaining <= 0 {
				if err := m.expirePending(ctx, row.ID, expiryReason); err != nil && !errors.Is(err, ErrCheckpointNotFound) {
					return err
				}
				continue
			}
			d = remaining
		}
		if row.Kind == api.CheckpointKindToolApproval && m.onToolApprovalRestored != nil {
			m.onToolApprovalRestored(row)
		}
		m.expiries.schedule(ctx, row.ID, d, m.expirePending)
	}
	return nil
}

// RestoreRejectedToolApprovalDenials restores denied approval coalescing.
func (m *Manager) RestoreRejectedToolApprovalDenials(ctx context.Context) error {
	rows, err := m.store.ListRejectedToolApprovals(ctx)
	if err != nil {
		return err
	}
	if m.onToolApprovalDenyRestored == nil {
		return nil
	}
	for _, row := range rows {
		m.onToolApprovalDenyRestored(row)
	}
	return nil
}

func (m *Manager) notifyToolApprovalTerminal(row StoredCheckpoint, status DecisionStatus) {
	if m == nil || m.onToolApprovalTerminal == nil || row.Kind != api.CheckpointKindToolApproval {
		return
	}
	key := stringField(row.Payload, "coalesce_grant_key")
	if key == "" {
		return
	}
	chat := stringField(row.Payload, "coalesce_chat")
	if chat == "" {
		chat = row.SessionID
	}
	m.onToolApprovalTerminal(chat, key, status)
}

// expirePending serializes expiry with human resolution and skips resolved rows.
func (m *Manager) expirePending(ctx context.Context, checkpointID, reason string) error {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending {
		return nil
	}
	result := DecisionResult{Approved: false, Comments: reason}
	now := time.Now().UTC()
	resolution := Resolution{By: authzledger.ResolvedByExpiry}
	viaOutbox, err := m.store.resolveCheckpoint(ctx, *row, DecisionStatusExpired, &result, nil, now, resolution,
		m.resolutionSeal(ctx, DecisionStatusExpired))
	if err != nil {
		return err
	}
	row.Status = DecisionStatusExpired
	row.Result = &result
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		// Expiry releases coalescing without recording a denial.
		m.notifyToolApprovalTerminal(*row, DecisionStatusExpired)
	}
	return nil
}

// Store returns the checkpoint persistence layer.
func (m *Manager) Store() Store {
	return m.store
}

// RequestCheckpoint creates a pending checkpoint and publishes SSE.
func (m *Manager) RequestCheckpoint(ctx context.Context, req CheckpointRequest) (*CheckpointResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	if req.Kind == "" {
		return nil, fmt.Errorf("kind required")
	}
	projectID, err := m.checkpointProject(ctx, req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	row := StoredCheckpoint{
		ID:          id,
		SessionID:   req.SessionID,
		ProjectID:   projectID,
		Kind:        req.Kind,
		Status:      DecisionStatusPending,
		Type:        req.Type,
		Title:       req.Title,
		Description: req.Description,
		CreatedAt:   now,
		Payload:     map[string]any{},
	}
	if row.Type == "" {
		row.Type = DecisionTypeApprove
	}
	if req.Kind == api.CheckpointKindToolApproval {
		plan := req.ApprovalPlan
		if plan == nil {
			var err error
			plan, err = CompileCheckpointApprovalPlan(req)
			if err != nil {
				return nil, err
			}
		}
		if req.ProposedAction == nil || plan.ActionDigest == "" || plan.ActionDigest != GrantKey(*req.ProposedAction) {
			return nil, fmt.Errorf("approval plan action does not match proposed action")
		}
		stored, err := storeApprovalPlan(plan)
		if err != nil {
			return nil, err
		}
		row.Payload["approval_plan"] = stored
		seedPayloadConsequence(&row, plan)
		seedPayloadDetection(&row, req)
	}
	switch req.Kind {
	case api.CheckpointKindToolApproval:
		if err := applyToolApprovalRequest(&row, req); err != nil {
			return nil, err
		}
	case api.CheckpointKindContentApply:
		if req.ContentApply == nil {
			return nil, fmt.Errorf("content_apply payload required")
		}
		plan, err := CompileContentApplyPlan(*req.ContentApply)
		if err != nil {
			return nil, err
		}
		stored, err := storeContentApplyPlan(plan)
		if err != nil {
			return nil, err
		}
		row.ToolName = plan.Tool
		row.Path = plan.Path
		if row.Title == "" {
			row.Title = fmt.Sprintf("Review edit: %s", plan.Path)
		}
		row.Payload["content_apply_plan"] = stored
	default:
		return nil, fmt.Errorf("unsupported checkpoint kind %q", req.Kind)
	}
	if err := m.store.Insert(ctx, row); err != nil {
		return nil, err
	}
	m.publishEvent(ctx, row)
	if m.expiryFor != nil {
		m.expiries.schedule(ctx, id, m.expiryFor(), m.expirePending)
	}
	return storedToCheckpointResponse(&row), nil
}

// checkpointProject validates declared ownership against the persisted session.
func (m *Manager) checkpointProject(ctx context.Context, req CheckpointRequest) (string, error) {
	projectID, err := m.store.SessionProjectID(ctx, req.SessionID)
	if err != nil {
		return "", fmt.Errorf("checkpoint session project: %w", err)
	}
	if req.ProjectID != "" && req.ProjectID != projectID {
		return "", fmt.Errorf("checkpoint project differs from session")
	}
	if action := req.ProposedAction; action != nil {
		if action.ProjectID != "" && action.ProjectID != projectID {
			return "", fmt.Errorf("checkpoint action project differs from session")
		}
		if action.SessionID != "" && action.SessionID != req.SessionID {
			return "", fmt.Errorf("checkpoint action session differs from request")
		}
	}
	return projectID, nil
}

// SetCheckpointWaitObserver observes each interval an execution blocks on a
// checkpoint, by the checkpoint's session.
func (m *Manager) SetCheckpointWaitObserver(observe func(ctx context.Context, sessionID string) (end func())) {
	if m != nil {
		m.checkpointWait = observe
	}
}

// ObserveCheckpointWait reports a blocked execution to the wait observer.
func (m *Manager) ObserveCheckpointWait(ctx context.Context, checkpointID string) func() {
	if m == nil || m.checkpointWait == nil {
		return func() {}
	}
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil || row == nil {
		return func() {}
	}
	return m.checkpointWait(ctx, row.SessionID)
}

// PollCheckpoint waits for authority installation to settle.
func (m *Manager) PollCheckpoint(ctx context.Context, checkpointID string) (*CheckpointResponse, error) {
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	return storedToCheckpointResponse(row), nil
}

// ResolveCheckpoint applies a human resolution.
func (m *Manager) ResolveCheckpoint(ctx context.Context, sessionID, checkpointID string, kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) (*CheckpointResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.sessionAdmission != nil {
		var response *CheckpointResponse
		err := m.sessionAdmission(ctx, sessionID, func() error {
			var resolveErr error
			response, resolveErr = m.resolveCheckpointAdmitted(ctx, sessionID, checkpointID, kind, toolResult, contentResult)
			return resolveErr
		})
		return response, err
	}
	return m.resolveCheckpointAdmitted(ctx, sessionID, checkpointID, kind, toolResult, contentResult)
}

func (m *Manager) resolveCheckpointAdmitted(ctx context.Context, sessionID, checkpointID string, kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) (*CheckpointResponse, error) {
	if kind == api.CheckpointKindContentApply {
		return m.resolveContentApply(ctx, sessionID, checkpointID, contentResult)
	}
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if row.SessionID != sessionID {
		return nil, ErrCheckpointNotFound
	}
	if row.Kind != kind {
		return nil, ErrCheckpointKindMismatch
	}
	if kind == api.CheckpointKindToolApproval && toolResult != nil && toolResult.Approved {
		return nil, ErrApprovalOptionRequired
	}
	status := decisionStatusFromResolve(kind, toolResult, contentResult)
	if row.Status != DecisionStatusPending {
		if row.Status == status && reflect.DeepEqual(row.Result, toolResult) && reflect.DeepEqual(row.ContentResult, contentResult) {
			return storedToCheckpointResponse(row), nil
		}
		return nil, ErrCheckpointNotPending
	}
	resolution, err := personResolution(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	viaOutbox, err := m.store.resolveCheckpoint(ctx, *row, status, toolResult, contentResult, now, resolution,
		m.resolutionSeal(ctx, status))
	if err != nil {
		return nil, err
	}
	row.Status = status
	row.Result = toolResult
	row.ContentResult = contentResult
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	if row.Kind == api.CheckpointKindToolApproval {
		m.notifyToolApprovalTerminal(*row, status)
	}
	return storedToCheckpointResponse(row), nil
}

// PatchPendingToolApprovalAIRationale updates and republishes only still-pending cards.
func (m *Manager) PatchPendingToolApprovalAIRationale(ctx context.Context, checkpointID, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if checkpointID == "" || text == "" {
		return nil
	}
	applied, err := m.store.SetPendingAIRationale(ctx, checkpointID, text)
	if err != nil || !applied || m.store.EventsViaOutbox() {
		return err
	}
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	// Re-check after load: a concurrent resolve can win between the write and Get.
	if row.Status != DecisionStatusPending {
		return nil
	}
	if row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// ClearPendingToolApprovalAIRationale clears the pending flag after an empty summary.
// Only a still-pending checkpoint is updated and republished.
func (m *Manager) ClearPendingToolApprovalAIRationale(ctx context.Context, checkpointID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkpointID == "" {
		return nil
	}
	applied, err := m.store.ClearPendingAIRationaleFlag(ctx, checkpointID)
	if err != nil || !applied || m.store.EventsViaOutbox() {
		return err
	}
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending || row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// PatchPendingToolApprovalJoined bumps joined_count / joined_tool_call_ids on a
// still-pending tool_approval checkpoint and republishes SSE when applied. It
// holds the card's resolution lock, so a joiner registers before the decision
// commits or gets ErrCheckpointNotPending.
func (m *Manager) PatchPendingToolApprovalJoined(ctx context.Context, checkpointID string, joinedCount int, joinedToolCallIDs []string, joinerBand string, joinerCode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkpointID == "" {
		return nil
	}
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()
	applied, err := m.store.SetPendingJoined(ctx, checkpointID, joinedCount, joinedToolCallIDs, joinerBand, joinerCode)
	if err != nil {
		return err
	}
	if !applied {
		return ErrCheckpointNotPending
	}
	if m.store.EventsViaOutbox() {
		return nil
	}
	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if row.Status != DecisionStatusPending || row.Kind != api.CheckpointKindToolApproval {
		return nil
	}
	m.publishEvent(ctx, *row)
	return nil
}

// ListPending returns pending checkpoints for a session.
func (m *Manager) ListPending(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	rows, err := m.store.ListBySession(ctx, sessionID, DecisionStatusPending, kind)
	if err != nil {
		return nil, err
	}
	out := make([]api.CheckpointEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredCheckpointToEvent(row))
	}
	return out, nil
}

// OldestPendingCheckpoints reports, per session, when the oldest live human
// checkpoint was issued. It feeds the cross-project attention view in one read.
func (m *Manager) OldestPendingCheckpoints(ctx context.Context) (map[string]time.Time, error) {
	return m.store.OldestPendingBySession(ctx)
}

// SessionApprovalDenied is true when the latest resolved tool_approval was rejected.
func (m *Manager) SessionApprovalDenied(ctx context.Context, sessionID string) (bool, error) {
	status, ok, err := m.store.LatestResolvedToolApprovalStatus(ctx, sessionID)
	if err != nil || !ok {
		return false, err
	}
	return status == DecisionStatusRejected || status == DecisionStatusExpired, nil
}

// announceResolved skips events staged by the outbox and always refreshes attention.
func (m *Manager) announceResolved(ctx context.Context, row StoredCheckpoint, viaOutbox bool) {
	if !viaOutbox {
		m.publishEvent(ctx, row)
		return
	}
	if m.events != nil {
		m.events.PublishAttention(ctx)
	}
}

func (m *Manager) publishEvent(ctx context.Context, row StoredCheckpoint) {
	if m.events == nil || m.store.EventsViaOutbox() {
		return
	}
	m.events.PublishCheckpoint(ctx, row.ProjectID, row.SessionID, StoredCheckpointToEvent(row))
}

func checkpointDecisionMeta(row StoredCheckpoint, status DecisionStatus) api.CheckpointDecisionMeta {
	decision := api.CheckpointDecisionMeta{
		CheckpointID:   row.ID,
		Kind:           row.Kind,
		Status:         api.CheckpointStatus(status),
		Tool:           decisionTool(row),
		Subject:        decisionSubject(row),
		CausingCommand: decisionCausingCommand(row),
		Location:       decisionLocation(row),
	}
	if status == DecisionStatusRejected {
		decision.Guidance = rejectionGuidance(row)
	}
	if row.Result != nil && len(row.Result.GrantIDs) > 0 {
		decision.GrantIDs = append([]string(nil), row.Result.GrantIDs...)
		decision.GrantScope = api.ApprovalGrantScope(row.Result.GrantScope)
		decision.GrantTitle = strings.TrimSpace(row.Result.GrantTitle)
	}
	return decision
}

// rejectionGuidance reads human direction from each checkpoint's result shape.
func rejectionGuidance(row StoredCheckpoint) string {
	if row.Result != nil {
		if comments := strings.TrimSpace(row.Result.Comments); comments != "" {
			return comments
		}
	}
	if row.ContentResult != nil {
		return strings.TrimSpace(row.ContentResult.Guidance)
	}
	return ""
}

func checkpointToolCallID(row StoredCheckpoint) string {
	if row.Payload != nil {
		if id := stringField(row.Payload, "tool_call_id"); id != "" {
			return id
		}
		if apply := contentApplyFromPayload(row); apply != nil {
			if id := strings.TrimSpace(apply.ToolCallID); id != "" {
				return id
			}
		}
	}
	if row.Args != nil {
		if id, ok := row.Args["tool_call_id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func approvalRecordInput(row StoredCheckpoint, status DecisionStatus) authzledger.ApprovalDecisionRecord {
	action := proposedActionFromCheckpoint(row)
	rec := authzledger.ApprovalDecisionRecord{
		ToolCallID:       checkpointToolCallID(row),
		SessionID:        row.SessionID,
		CheckpointID:     row.ID,
		Tool:             action.Tool,
		Args:             action.Args,
		Files:            action.Files,
		ProjectDir:       action.ProjectDir,
		ResolvedBy:       resolutionBy(row.Resolution),
		ResolverPersonID: resolutionPersonID(row.Resolution),
		ResolverPolicy:   resolutionPolicy(row.Resolution),
		GrantScope:       authzledger.GrantScopeOnce,
	}
	if raw, ok := row.Payload["approval_plan"].(map[string]any); ok {
		if plan, err := approvalPlanFromMap(raw); err == nil {
			rec.PlanID = plan.ID
			rec.ActionDigest = plan.ActionDigest
			rec.SubjectKind = string(plan.Subject.Kind)
			rec.SubjectTitle = plan.Subject.Title
			rec.Gate = string(plan.Presentation.Gate)
			for _, reason := range plan.Reasons {
				rec.Reasons = append(rec.Reasons, string(reason))
			}
			for _, rule := range plan.Presentation.ApprovalRules {
				rec.ApprovalRules = append(rec.ApprovalRules, authzledger.ApprovalRuleCitation{
					Category: rule.Category, Pattern: rule.Pattern, Effect: rule.Effect,
					UnitID: rule.UnitID, PackID: rule.PackID, Scope: rule.Scope,
				})
			}
		}
	}
	if row.Result != nil {
		rec.SelectedOptionID = row.Result.OptionID
		rec.GrantIDs = append([]string(nil), row.Result.GrantIDs...)
		switch row.Result.GrantScope {
		case ApprovalGrantScopeChat:
			rec.GrantScope = authzledger.GrantScopeSession
		case ApprovalGrantScopeProject, ApprovalGrantScopeDevice:
			rec.GrantScope = authzledger.GrantScopePersistent
		}
	}
	switch status {
	case DecisionStatusApproved:
		rec.Outcome = authzledger.OutcomeAllowed
	default:
		rec.Outcome = authzledger.OutcomeDenied
		rec.RejectCode = approvaloutcome.CodeApprovalDenied
		if status == DecisionStatusExpired {
			rec.RejectCode = approvaloutcome.CodeApprovalExpired
		}
		if status == DecisionStatusCanceled {
			rec.RejectCode = approvaloutcome.CodeApprovalCanceled
		}
	}
	return rec
}

func proposedActionFromCheckpoint(row StoredCheckpoint) ProposedAction {
	return ProposedAction{
		Tool:       row.ToolName,
		Args:       cloneArgs(row.Args),
		Files:      append([]string(nil), row.Files...),
		ProjectID:  row.ProjectID,
		ProjectDir: row.ProjectDir,
		SessionID:  row.SessionID,
	}
}

// Ensure Manager implements CheckpointManager.
var _ CheckpointManager = (*Manager)(nil)

// ListPendingForParent includes worker checkpoints without enumerating old workers.
func (m *Manager) ListPendingForParent(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	rows, err := m.store.ListPendingForParent(ctx, sessionID, kind)
	if err != nil {
		return nil, err
	}
	out := make([]api.CheckpointEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredCheckpointToEvent(row))
	}
	return out, nil
}
