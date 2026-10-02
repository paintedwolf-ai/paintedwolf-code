package hitl

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// decisionSubject is the single human-facing subject persisted on the durable
// decision chicklet — grant target labels (command, path, host, …).
func decisionSubject(row StoredCheckpoint) string {
	switch row.Kind {
	case api.CheckpointKindToolApproval:
		if raw, ok := row.Payload["approval_plan"].(map[string]any); ok {
			if plan, err := approvalPlanFromMap(raw); err == nil {
				labels := make([]string, 0, len(plan.Subject.Targets))
				for _, target := range plan.Subject.Targets {
					if label := strings.TrimSpace(target.Label); label != "" {
						labels = append(labels, label)
					}
				}
				return strings.Join(labels, "\n")
			}
		}
		return ""
	case api.CheckpointKindContentApply:
		if path := strings.TrimSpace(row.Path); path != "" {
			return observability.RedactCaptureText(path)
		}
		if raw, ok := row.Payload["content_apply"].(map[string]any); ok {
			if path := strings.TrimSpace(stringField(raw, "path")); path != "" {
				return observability.RedactCaptureText(path)
			}
		}
		return ""
	default:
		return ""
	}
}

// decisionTool is the chip-facing tool for the durable chicklet: presentation.tool
// when the plan carried one, else the stored ProposedAction tool.
func decisionTool(row StoredCheckpoint) string {
	if row.Kind == api.CheckpointKindToolApproval {
		if raw, ok := row.Payload["approval_plan"].(map[string]any); ok {
			if plan, err := approvalPlanFromMap(raw); err == nil {
				if tool := strings.TrimSpace(plan.Presentation.Tool); tool != "" {
					return tool
				}
			}
		}
	}
	return strings.TrimSpace(row.ToolName)
}

// decisionLocation is presentation.location rendered as origin → destination.
func decisionLocation(row StoredCheckpoint) string {
	if row.Kind != api.CheckpointKindToolApproval {
		return ""
	}
	raw, ok := row.Payload["approval_plan"].(map[string]any)
	if !ok {
		return ""
	}
	plan, err := approvalPlanFromMap(raw)
	if err != nil {
		return ""
	}
	return secretLocationLine(plan.Presentation.Location)
}

func cloneSecretLocation(loc *api.ApprovalSecretLocation) *api.ApprovalSecretLocation {
	if loc == nil {
		return nil
	}
	copied := *loc
	copied.SecretNames = append([]string(nil), loc.SecretNames...)
	copied.Recipients = append([]api.ApprovalSecretRecipient(nil), loc.Recipients...)
	return &copied
}

func decisionCausingCommand(row StoredCheckpoint) string {
	if row.Kind != api.CheckpointKindToolApproval {
		return ""
	}
	raw, ok := row.Payload["approval_plan"].(map[string]any)
	if !ok {
		return ""
	}
	plan, err := approvalPlanFromMap(raw)
	if err != nil {
		return ""
	}
	command := strings.TrimSpace(plan.Presentation.Command)
	if command == "" {
		return ""
	}
	for _, target := range plan.Subject.Targets {
		if strings.TrimSpace(target.Label) == command {
			return ""
		}
	}
	return command
}

func wireApprovalPlan(plan *ApprovalPlan) api.ApprovalPlan {
	out := api.ApprovalPlan{
		ID: plan.ID, ActionDigest: plan.ActionDigest,
		Stage:   api.ApprovalPlanStage(plan.Stage),
		Subject: api.ApprovalSubject{Kind: api.ApprovalSubjectKind(plan.Subject.Kind), Title: plan.Subject.Title, Summary: plan.Subject.Summary},
		Presentation: api.ApprovalPlanPresentation{
			IgnoreCandidateID: plan.Presentation.IgnoreCandidateID,
			FileChanges:       append([]api.ApprovalFileChange(nil), plan.Presentation.FileChanges...),
			Action:            plan.Presentation.Action, Tool: plan.Presentation.Tool, Command: plan.Presentation.Command,
			Location: cloneSecretLocation(plan.Presentation.Location),
			Impact:   plan.Presentation.Impact, Who: plan.Presentation.Who, IfWrong: plan.Presentation.IfWrong,
			AllowLine: plan.Presentation.AllowLine, Lead: plan.Presentation.Lead, Gate: plan.Presentation.Gate,
			OptionNote:      plan.Presentation.OptionNote,
			GrantDelta:      plan.Presentation.GrantDelta,
			ConsequenceBand: api.ConsequenceBand(plan.Presentation.ConsequenceBand),
			ConsequenceCode: api.ConsequenceCode(plan.Presentation.ConsequenceCode),
		},
		Reasons:             append([]api.ApprovalGate(nil), plan.Reasons...),
		RecommendedOptionID: plan.RecommendedOptionID,
	}
	out.ElevatedEffects = savedOptionElevatedEffects(plan.Options)
	out.HeldRelease = wireHeldRelease(plan.Held)
	for _, cited := range plan.Presentation.Cited {
		out.Presentation.Cited = append(out.Presentation.Cited, api.PresentedFact{
			Gate: cited.Gate, Key: cited.Key, Value: cited.Value, Source: cited.Source,
		})
	}
	if plan.Presentation.Detection != nil {
		out.Presentation.Detection = &api.DetectionMatch{
			PackID: plan.Presentation.Detection.PackID, RuleID: plan.Presentation.Detection.RuleID,
			RuleTitle: plan.Presentation.Detection.RuleTitle, Level: plan.Presentation.Detection.Level,
			CorrelationID: plan.Presentation.Detection.CorrelationID,
		}
	}
	for _, rule := range plan.Presentation.ApprovalRules {
		out.Presentation.ApprovalRules = append(out.Presentation.ApprovalRules, api.ApprovalRuleCitation{
			Category: rule.Category, Pattern: rule.Pattern, Effect: rule.Effect, Command: rule.Command,
			UnitID: rule.UnitID, PackID: rule.PackID, Scope: rule.Scope,
		})
	}
	for _, target := range plan.Subject.Targets {
		out.Subject.Targets = append(out.Subject.Targets, api.ApprovalTarget{Kind: target.Kind, Label: target.Label, Details: cloneArgs(target.Details)})
	}
	for _, option := range plan.Options {
		out.Options = append(out.Options, api.ApprovalOption{
			ID: option.ID, Kind: api.ApprovalOptionKind(option.Kind), Rung: api.ApprovalOptionRung(option.Rung),
			Scope: api.ApprovalGrantScope(option.Scope), Group: option.Group,
			Title: option.Title, Coverage: option.Coverage, ExpiresWhen: option.ExpiresWhen,
			ReaskWhen: option.ReaskWhen, DecisionAction: api.ApprovalOptionDecision(option.DecisionAction),
			Disabled: option.Disabled, Note: option.Note,
		})
	}
	return out
}

// wireHeldRelease names the held values and recipients.
func wireHeldRelease(held *HeldRelease) *api.ApprovalHeldRelease {
	if held.empty() {
		return nil
	}
	out := &api.ApprovalHeldRelease{
		ChatSessionID: held.ChatSessionID, Recipients: WireSecretRecipients(held.Recipients), UnlockOnly: held.UnlockOnly,
	}
	for _, secret := range held.Secrets {
		out.Secrets = append(out.Secrets, api.ApprovalHeldSecret{
			Reference: secretmatch.ReferenceToken(secret.SecretID), Name: secret.Name, Version: secret.Version,
		})
	}
	return out
}

func savedOptionElevatedEffects(options []ApprovalOption) []api.ElevatedAccessEffect {
	var effects []api.ElevatedAccessEffect
	for _, option := range options {
		if option.Disabled || (option.Kind != ApprovalOptionLease && option.Kind != ApprovalOptionQuiet) {
			continue
		}
		for _, delta := range option.Authority {
			if delta.Grant != nil {
				effects = append(effects, ElevatedGrantEffects(*delta.Grant)...)
			}
			if delta.AskQuiet != nil {
				effects = append(effects, delta.AskQuiet.ElevatedEffects...)
			}
		}
	}
	slices.Sort(effects)
	return slices.Compact(effects)
}

func contentApplyFromPayload(row StoredCheckpoint) *api.ContentApplyPayload {
	raw, _ := row.Payload["content_apply_plan"].(map[string]any)
	plan, err := contentApplyPlanFromMap(raw)
	if err != nil {
		return nil
	}
	before := cloneStringPointer(plan.Before)
	if before != nil {
		redacted := observability.RedactCaptureText(*before)
		before = &redacted
	}
	hunks := plan.WireHunks()
	for i := range hunks {
		hunks[i].Path = observability.RedactCaptureText(hunks[i].Path)
		hunks[i].Before = observability.RedactCaptureText(hunks[i].Before)
		hunks[i].After = observability.RedactCaptureText(hunks[i].After)
	}
	return &api.ContentApplyPayload{
		Tool: observability.RedactCaptureText(plan.Tool), ToolCallID: plan.ToolCallID,
		Path: observability.RedactCaptureText(plan.Path), Before: before,
		After: observability.RedactCaptureText(plan.After), Hunks: hunks,
	}
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	value, _ := m[key].(bool)
	return value
}

func intField(m map[string]any, key string) int {
	switch value := m[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func stringSliceField(m map[string]any, key string) []string {
	switch values := m[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func decisionStatusFromResolve(kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) DecisionStatus {
	return decisionStatusFromResult(derefDecisionResult(toolResult))
}

func derefDecisionResult(r *DecisionResult) DecisionResult {
	if r == nil {
		return DecisionResult{Approved: false}
	}
	return *r
}

func decisionStatusFromResult(result DecisionResult) DecisionStatus {
	if !result.Approved {
		return DecisionStatusRejected
	}
	return DecisionStatusApproved
}

func storedToCheckpointResponse(row *StoredCheckpoint) *CheckpointResponse {
	if row == nil {
		return nil
	}
	return &CheckpointResponse{
		CheckpointID:  row.ID,
		Kind:          row.Kind,
		Status:        row.Status,
		Result:        row.Result,
		ContentResult: row.ContentResult,
		ResolvedAt:    row.ResolvedAt,
		ResolvedBy:    resolutionBy(row.Resolution),
	}
}

// StoredCheckpointToEvent maps a stored row to the wire checkpoint event (SSE + list).
func StoredCheckpointToEvent(row StoredCheckpoint) api.CheckpointEvent {
	ev := api.CheckpointEvent{
		ID:        row.ID,
		SessionID: row.SessionID,
		Kind:         row.Kind,
		Status:       api.CheckpointStatus(row.Status),
		IssuedAt:     row.CreatedAt,
	}
	switch row.Kind {
	case api.CheckpointKindToolApproval:
		raw, _ := row.Payload["approval_plan"].(map[string]any)
		plan, err := approvalPlanFromMap(raw)
		if err != nil {
			break
		}
		ev.ToolApproval = &api.ToolApprovalPayload{ToolCallID: stringField(row.Payload, "tool_call_id"), Plan: wireApprovalPlan(plan)}
		ev.ToolApproval.AIRationale = stringField(row.Payload, "ai_rationale")
		ev.ToolApproval.AIRationalePending = boolField(row.Payload, "ai_rationale_pending")
		if n := intField(row.Payload, "joined_count"); n > 0 {
			ev.ToolApproval.JoinedCount = n
		}
		if raw, ok := row.Payload["repeat"].(map[string]any); ok {
			if count, asks := intField(raw, "count"), intField(raw, "asks"); count > 1 || asks > 1 {
				ev.ToolApproval.Repeat = &api.ApprovalRepeat{
					ReasonKey:         stringField(raw, "reason_key"),
					Count:             count,
					Asks:              asks,
					Subjects:          stringSliceField(raw, "subjects"),
					SubjectsTruncated: boolField(raw, "subjects_truncated"),
					SuppressedCount:   intField(raw, "suppressed_count"),
				}
			}
		}
		ev.ToolApproval.JoinedToolCallIDs = stringSliceField(row.Payload, "joined_tool_call_ids")
		if band := stringField(row.Payload, "consequence_band"); band != "" {
			ev.ToolApproval.ConsequenceBand = api.ConsequenceBand(band)
		}
		if code := stringField(row.Payload, "consequence_code"); code != "" {
			ev.ToolApproval.ConsequenceCode = api.ConsequenceCode(code)
		}
	case api.CheckpointKindContentApply:
		if p := contentApplyFromPayload(row); p != nil {
			ev.ContentApply = p
		}
	}
	return ev
}

func cloneArgs(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
