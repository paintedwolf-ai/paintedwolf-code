package hitl

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzledger"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/pkg/api"
)

// EventsViaOutbox reports whether committed mutations own event delivery.
func (s *SQLStore) EventsViaOutbox() bool { return s.outbox != nil }

// Worker registration and checkpoint changes share commit order. A child
// approval must never arrive before the worker that gives it a parent view.
func (s *SQLStore) enqueueCheckpointTx(ctx context.Context, tx *sql.Tx, row StoredCheckpoint) error {
	if s.outbox == nil {
		return nil
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicCheckpoint,
		events.PublishKey{Project: row.ProjectID, Session: row.SessionID}, StoredCheckpointToEvent(row))
}

type EventPublisher interface {
	PublishCheckpoint(ctx context.Context, projectIDOrDir, sessionID string, ev api.CheckpointEvent)
	PublishAttention(ctx context.Context)
}

// announceResolved skips events staged by the outbox and always refreshes attention.
func (m *Checkpoints) announceResolved(ctx context.Context, row StoredCheckpoint, viaOutbox bool) {
	if !viaOutbox {
		m.publishEvent(ctx, row)
		return
	}
	if m.events != nil {
		m.events.PublishAttention(ctx)
	}
}

func (m *Checkpoints) publishEvent(ctx context.Context, row StoredCheckpoint) {
	if m.events == nil || m.Store.EventsViaOutbox() {
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
		Tool:             action.Invocation.Tool,
		Args:             action.Invocation.Args,
		Files:            action.Invocation.Files,
		ProjectDir:       action.Scope.ProjectDir,
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
Invocation: ActionInvocation{
Tool: row.ToolName,
Args: cloneArgs(row.Args),
Files: append([]string(nil), row.Files...),
},
Scope: ActionScope{
ProjectID: row.ProjectID,
ProjectDir: row.ProjectDir,
SessionID: row.SessionID,
},
}
}

// Ensure Manager implements CheckpointManager.
var _ CheckpointManager = (*Checkpoints)(nil)
