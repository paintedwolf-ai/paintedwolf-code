package authzcontext

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// LedgerRecorder adapts Ledger to authzledger.Recorder for hook sites.
type LedgerRecorder struct {
	Ledger *Ledger
}

// AppendApprovalGateTx seals a human approval decision inside the caller's
// resolving transaction (fail-closed).
func (a LedgerRecorder) AppendApprovalGateTx(ctx context.Context, tx *sql.Tx, rec authzledger.ApprovalDecisionRecord) error {
	if a.Ledger == nil {
		return authzledger.ErrSealFailed
	}
	return a.Ledger.AppendGateTx(ctx, tx, approvalRecordFromWire(rec))
}

// AppendCapabilityGateTx seals a capability / detection_resolved row inside the
// caller's resolving transaction (fail-closed).
func (a LedgerRecorder) AppendCapabilityGateTx(ctx context.Context, tx *sql.Tx, rec authzledger.CapabilityRecord) error {
	if a.Ledger == nil {
		return authzledger.ErrSealFailed
	}
	return a.Ledger.AppendGateTx(ctx, tx, capabilityRecordInput(rec))
}

// AppendHumanGateTx seals a non-tool human checkpoint outcome inside the
// caller's resolving transaction (fail-closed).
func (a LedgerRecorder) AppendHumanGateTx(ctx context.Context, tx *sql.Tx, rec authzledger.HumanGateRecord) error {
	if a.Ledger == nil {
		return authzledger.ErrSealFailed
	}
	return a.Ledger.AppendGateTx(ctx, tx, humanGateRecordInput(rec))
}

// AppendToolDenied logs a system deny best-effort.
func (a LedgerRecorder) AppendToolDenied(ctx context.Context, rec authzledger.ToolDeniedRecord) {
	if a.Ledger == nil {
		return
	}
	rejectCode := rec.RejectCode
	if rejectCode == "" {
		rejectCode = "approval_denied"
	}
	tier := rec.Tier
	if tier == "" {
		tier = settings.TierLabel(settings.ClassifyTier(hitl.ProposedAction{
			Tool:       rec.Tool,
			Args:       rec.Args,
			Files:      rec.Files,
			ProjectDir: rec.ProjectDir,
			SessionID:  rec.SessionID,
		}))
	}
	a.Ledger.AppendRecord(ctx, RecordInput{
		SessionID:  rec.SessionID,
		Action:     EventActionToolDenied,
		Outcome:    EventOutcomeDenied,
		ResolvedBy: ResolvedBySystemDeny,
		ToolName:   rec.Tool,
		RejectCode: rejectCode,
		Detail: DetailInput{
			Tool:          rec.Tool,
			Args:          rec.Args,
			Files:         rec.Files,
			ProjectDir:    rec.ProjectDir,
			Tier:          tier,
			RejectCode:    rejectCode,
			ApprovalRules: rec.ApprovalRules,
		},
	})
}

// AppendCapabilityRecord records a capability_* / detection_resolved row.
// Grants may set FailClosed; other capability rows are best-effort.
func (a LedgerRecorder) AppendCapabilityRecord(ctx context.Context, rec authzledger.CapabilityRecord) error {
	if a.Ledger == nil {
		if rec.FailClosed {
			return authzledger.ErrSealFailed
		}
		return nil
	}
	in := capabilityRecordInput(rec)
	if rec.FailClosed {
		if err := a.Ledger.AppendGate(ctx, in); err != nil {
			return authzledger.ErrSealFailed
		}
		return nil
	}
	a.Ledger.AppendRecord(ctx, in)
	return nil
}

// AppendDirectIPLifecycle records direct_ip_* lifecycle facts best-effort.
func (a LedgerRecorder) AppendDirectIPLifecycle(ctx context.Context, rec authzledger.DirectIPLifecycleRecord) {
	if a.Ledger == nil {
		return
	}
	action, outcome := mapDirectIPPhase(rec.Phase)
	if action == "" {
		return
	}
	ea := BuildExternalAccess(ExternalAccessInput{
		DeclaredDestinations: append([]string(nil), rec.DeclaredDestinations...),
		Direct:               true,
	})
	a.Ledger.AppendRecord(ctx, RecordInput{
		SessionID:  rec.SessionID,
		Action:     action,
		Outcome:    outcome,
		ResolvedBy: resolvedByFromAuthorizationSource(rec.AuthorizationSource),
		ToolName:   firstNonEmpty(rec.Tool, "command"),
		Detail: DetailInput{
			Tool:                firstNonEmpty(rec.Tool, "command"),
			AuthorizationSource: strings.TrimSpace(rec.AuthorizationSource),
			ExternalAccess:      ea,
		},
	})
}

// AppendMediatedEndpoint records an observed mediated endpoint.
func (a LedgerRecorder) AppendMediatedEndpoint(ctx context.Context, rec authzledger.MediatedEndpointRecord) {
	if a.Ledger == nil {
		return
	}
	action := EventActionMediatedEndpoint
	if rec.FoldIntoApplied {
		action = EventActionCapabilityApplied
	}
	in := ExternalAccessInput{
		Endpoints:            capabilityEndpointsToInput(rec.Endpoints),
		Sockets:              capabilitySocketsToInput(rec.Sockets),
		DeclaredDestinations: append([]string(nil), rec.DeclaredDestinations...),
		Direct:               rec.Direct,
		Detections:           capabilityDetectionsToAPI(rec.Detections),
	}
	ea := BuildExternalAccess(in)
	a.Ledger.AppendRecord(ctx, RecordInput{
		SessionID:  rec.SessionID,
		Action:     action,
		Outcome:    EventOutcomeAllowed,
		ResolvedBy: resolvedByFromAuthorizationSource(rec.AuthorizationSource),
		ToolName:   firstNonEmpty(rec.Tool, "command"),
		Detail: DetailInput{
			Tool:                firstNonEmpty(rec.Tool, "command"),
			AuthorizationSource: strings.TrimSpace(rec.AuthorizationSource),
			ExternalAccess:      ea,
		},
	})
}

func capabilityRecordInput(rec authzledger.CapabilityRecord) RecordInput {
	action := EventAction(strings.TrimSpace(rec.Action))
	if action == "" {
		action = EventActionCapabilityApplied
	}
	outcome := EventOutcomeAllowed
	if rec.Outcome == authzledger.OutcomeDenied {
		outcome = EventOutcomeDenied
	}
	ea := BuildExternalAccess(ExternalAccessInput{
		Endpoints:            capabilityEndpointsToInput(rec.Endpoints),
		Sockets:              capabilitySocketsToInput(rec.Sockets),
		DeclaredDestinations: append([]string(nil), rec.DeclaredDestinations...),
		Direct:               rec.Direct,
		FullBypass:           rec.FullBypass,
		Detections:           capabilityDetectionsToAPI(rec.Detections),
		AffirmativeNone:      rec.AffirmativeNone,
	})
	return RecordInput{
		SessionID:        rec.SessionID,
		Action:           action,
		Outcome:          outcome,
		ResolvedBy:       resolvedByFromWire(rec.ResolvedBy, rec.AuthorizationSource),
		ResolverPersonID: rec.ResolverPersonID,
		ToolName:         rec.Tool,
		RejectCode:       rec.RejectCode,
		Detail: DetailInput{
			ToolCallID:          rec.ToolCallID,
			Tool:                rec.Tool,
			RejectCode:          rec.RejectCode,
			AuthorizationSource: strings.TrimSpace(rec.AuthorizationSource),
			SuppressionCause:    rec.SuppressionCause,
			AskFamily:           rec.AskFamily,
			ExternalAccess:      ea,
		},
	}
}

func mapDirectIPPhase(phase string) (EventAction, EventOutcome) {
	switch strings.TrimSpace(phase) {
	case "started":
		return EventActionDirectIPStarted, EventOutcomeAllowed
	case "completed":
		return EventActionDirectIPCompleted, EventOutcomeAllowed
	case "reconstructed":
		return EventActionDirectIPReconstructed, EventOutcomeAllowed
	case "lease_reused":
		return EventActionDirectIPLeaseReused, EventOutcomeAllowed
	case "requested":
		return EventActionCapabilityRequested, EventOutcomeAllowed
	case "approved":
		return EventActionCapabilityGranted, EventOutcomeAllowed
	case "denied":
		return EventActionCapabilityDenied, EventOutcomeDenied
	default:
		return "", EventOutcomeDenied
	}
}

func resolvedByFromAuthorizationSource(src string) ResolvedBy {
	switch strings.TrimSpace(src) {
	case authzledger.AuthorizationSourceExpiry:
		return ResolvedByExpiry
	case authzledger.AuthorizationSourceUserStop:
		return ResolvedByUserStop
	case authzledger.AuthorizationSourceHostStop:
		return ResolvedByHostStop
	case authzledger.AuthorizationSourcePolicy:
		return ResolvedByPolicy
	default:
		// Standing decisions made in Settings resolve as human.
		return ResolvedByHuman
	}
}

func resolvedByFromWire(resolvedBy, authSource string) ResolvedBy {
	switch strings.TrimSpace(resolvedBy) {
	case authzledger.ResolvedByExpiry:
		return ResolvedByExpiry
	case authzledger.ResolvedBySystemDeny:
		return ResolvedBySystemDeny
	case authzledger.ResolvedByUserStop:
		return ResolvedByUserStop
	case authzledger.ResolvedByHostStop:
		return ResolvedByHostStop
	case authzledger.ResolvedByPolicy:
		return ResolvedByPolicy
	case authzledger.ResolvedByHuman:
		return ResolvedByHuman
	default:
		return resolvedByFromAuthorizationSource(authSource)
	}
}

func capabilityEndpointsToInput(in []authzledger.CapabilityEndpoint) []ExternalAccessEndpointInput {
	out := make([]ExternalAccessEndpointInput, 0, len(in))
	for _, ep := range in {
		out = append(out, ExternalAccessEndpointInput{
			Host:      ep.Host,
			Port:      ep.Port,
			Transport: ep.Transport,
			Allowed:   ep.Allowed,
			Attempts:  ep.Attempts,
		})
	}
	return out
}

func capabilitySocketsToInput(in []authzledger.CapabilitySocket) []ExternalAccessSocketInput {
	out := make([]ExternalAccessSocketInput, 0, len(in))
	for _, s := range in {
		out = append(out, ExternalAccessSocketInput{
			ApprovedPath: s.ApprovedPath,
			ResolvedPath: s.ResolvedPath,
			Scope:        s.Scope,
		})
	}
	return out
}

func capabilityDetectionsToAPI(in []authzledger.CapabilityDetection) []api.ExternalAccessDetection {
	out := make([]api.ExternalAccessDetection, 0, len(in))
	for _, d := range in {
		out = append(out, api.ExternalAccessDetection{
			PackID:    d.PackID,
			RuleID:    d.RuleID,
			RuleTitle: d.RuleTitle,
			Level:     d.Level,
			ActionID:  d.ActionID,
		})
	}
	return out
}

func humanGateRecordInput(rec authzledger.HumanGateRecord) RecordInput {
	outcome := EventOutcomeAllowed
	if rec.Outcome == authzledger.OutcomeDenied {
		outcome = EventOutcomeDenied
	}
	return RecordInput{
		SessionID:        rec.SessionID,
		Action:           EventAction(strings.TrimSpace(rec.Action)),
		Outcome:          outcome,
		ResolvedBy:       resolvedByFromWire(rec.ResolvedBy, ""),
		ResolverPersonID: rec.ResolverPersonID,
		ToolName:         rec.Tool,
		Detail: DetailInput{
			Tool:            rec.Tool,
			Files:           rec.Files,
			ProjectDir:      rec.ProjectDir,
			ContentDecision: rec.ContentDecision,
			BlueprintDigest: rec.BlueprintDigest,
		},
	}
}

func approvalRecordFromWire(rec authzledger.ApprovalDecisionRecord) RecordInput {
	tier := rec.Tier
	if tier == "" {
		tier = settings.TierLabel(settings.ClassifyTier(hitl.ProposedAction{
			Tool:       rec.Tool,
			Args:       rec.Args,
			Files:      rec.Files,
			ProjectDir: rec.ProjectDir,
			SessionID:  rec.SessionID,
		}))
	}
	out := RecordInput{
		SessionID:        rec.SessionID,
		Action:           EventActionApprovalDecision,
		ResolverPersonID: rec.ResolverPersonID,
		ToolName:         rec.Tool,
		RejectCode:       rec.RejectCode,
		Detail: DetailInput{
			ToolCallID:       rec.ToolCallID,
			Tool:             rec.Tool,
			Args:             rec.Args,
			Files:            rec.Files,
			ProjectDir:       rec.ProjectDir,
			Tier:             tier,
			RejectCode:       rec.RejectCode,
			GrantScope:       GrantScope(rec.GrantScope),
			CheckpointID:     rec.CheckpointID,
			PlanID:           rec.PlanID,
			ActionDigest:     rec.ActionDigest,
			SelectedOptionID: rec.SelectedOptionID,
			GrantIDs:         rec.GrantIDs,
			SubjectKind:      rec.SubjectKind,
			SubjectTitle:     rec.SubjectTitle,
			Gate:             rec.Gate,
			Reasons:          rec.Reasons,
			ApprovalRules:    rec.ApprovalRules,
			ResolverPolicy:   rec.ResolverPolicy,
		},
	}
	switch rec.Outcome {
	case authzledger.OutcomeAllowed:
		out.Outcome = EventOutcomeAllowed
	default:
		out.Outcome = EventOutcomeDenied
	}
	out.ResolvedBy = resolvedByFromWire(rec.ResolvedBy, "")
	if out.Detail.GrantScope == "" {
		out.Detail.GrantScope = GrantScopeOnce
	}
	return out
}
