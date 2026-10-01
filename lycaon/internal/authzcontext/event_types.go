package authzcontext

import "github.com/lycaon/lycaon/internal/authzledger"

// EventAction is the typed authz_events.action vocabulary.
type EventAction string

// Actions a caller writes onto a record mirror authzledger, so each has one
// spelling; the rest are derived here and named here.
const (
	EventActionApprovalDecision EventAction = "approval_decision"
	EventActionToolDenied       EventAction = "tool_denied"

	EventActionCapabilityRequested EventAction = authzledger.ActionCapabilityRequested
	EventActionCapabilityGranted   EventAction = authzledger.ActionCapabilityGranted
	EventActionCapabilityDenied    EventAction = authzledger.ActionCapabilityDenied
	EventActionCapabilityRevoked   EventAction = authzledger.ActionCapabilityRevoked
	EventActionCapabilityApplied   EventAction = authzledger.ActionCapabilityApplied

	// EventActionDirectIPLeaseReused records each invocation authorized by an existing lease.
	EventActionDirectIPLeaseReused   EventAction = "direct_ip_lease_reused"
	EventActionDirectIPStarted       EventAction = "direct_ip_started"
	EventActionDirectIPCompleted     EventAction = "direct_ip_completed"
	EventActionDirectIPReconstructed EventAction = "direct_ip_reconstructed"

	EventActionMediatedEndpoint  EventAction = "mediated_endpoint"
	EventActionDetectionResolved EventAction = authzledger.ActionDetectionResolved
	EventActionAskSuppressed     EventAction = authzledger.ActionAskSuppressed

	// Only secret receipts and contests participate in interactive review.
	EventActionSecretPermissionUsed       EventAction = authzledger.ActionSecretPermissionUsed
	EventActionSecretReceipt              EventAction = authzledger.ActionSecretReceipt
	EventActionSecretContest              EventAction = authzledger.ActionSecretContest
	EventActionSecretDestinationTrusted   EventAction = authzledger.ActionSecretDestinationTrusted
	EventActionSecretHostComposedRedacted EventAction = authzledger.ActionSecretHostComposedRedacted
	EventActionSecretChatLocalRelease     EventAction = authzledger.ActionSecretChatLocalRelease

	// Non-tool gates land in the same event chain as tool approvals. All three
	// blueprint grant transitions are recorded; blueprint_superseded is the one
	// no person resolves.
	EventActionContentApplyResolved EventAction = authzledger.ActionContentApplyResolved
	EventActionBlueprintApproved    EventAction = authzledger.ActionBlueprintApproved
	EventActionBlueprintSuperseded  EventAction = authzledger.ActionBlueprintSuperseded
	EventActionBlueprintRevoked     EventAction = authzledger.ActionBlueprintRevoked
)

// EventOutcome is the typed authz_events.outcome vocabulary.
type EventOutcome string

const (
	EventOutcomeAllowed EventOutcome = authzledger.OutcomeAllowed
	EventOutcomeDenied  EventOutcome = authzledger.OutcomeDenied
)

// ResolvedBy is the typed authz_events.resolved_by vocabulary.
type ResolvedBy string

const (
	ResolvedByHuman      ResolvedBy = authzledger.ResolvedByHuman
	ResolvedByExpiry     ResolvedBy = authzledger.ResolvedByExpiry
	ResolvedBySystemDeny ResolvedBy = authzledger.ResolvedBySystemDeny
	ResolvedByUserStop   ResolvedBy = authzledger.ResolvedByUserStop
	ResolvedByHostStop   ResolvedBy = authzledger.ResolvedByHostStop
	ResolvedByPolicy     ResolvedBy = authzledger.ResolvedByPolicy
)

// GrantScope records how long a human grant applies (detail_json, not resolved_by).
type GrantScope string

// Values mirror authzledger, which writes them onto the persisted rows this
// package reads back.
const (
	GrantScopeOnce       GrantScope = authzledger.GrantScopeOnce
	GrantScopeSession    GrantScope = authzledger.GrantScopeSession
	GrantScopePersistent GrantScope = authzledger.GrantScopePersistent
)

// AllEventActions lists the action enum for contract tests.
func AllEventActions() []EventAction {
	return []EventAction{
		EventActionApprovalDecision,
		EventActionToolDenied,
		EventActionCapabilityRequested,
		EventActionCapabilityGranted,
		EventActionCapabilityDenied,
		EventActionCapabilityRevoked,
		EventActionCapabilityApplied,
		EventActionDirectIPLeaseReused,
		EventActionDirectIPStarted,
		EventActionDirectIPCompleted,
		EventActionDirectIPReconstructed,
		EventActionMediatedEndpoint,
		EventActionDetectionResolved,
		EventActionAskSuppressed,
		EventActionSecretPermissionUsed,
		EventActionSecretReceipt,
		EventActionSecretContest,
		EventActionSecretDestinationTrusted,
		EventActionSecretHostComposedRedacted,
		EventActionSecretChatLocalRelease,
		EventActionContentApplyResolved,
		EventActionBlueprintApproved,
		EventActionBlueprintSuperseded,
		EventActionBlueprintRevoked,
	}
}

// AllEventOutcomes lists the outcome enum for contract tests.
func AllEventOutcomes() []EventOutcome {
	return []EventOutcome{EventOutcomeAllowed, EventOutcomeDenied}
}

// AllResolvedBy lists the resolved_by enum for contract tests.
func AllResolvedBy() []ResolvedBy {
	return []ResolvedBy{ResolvedByHuman, ResolvedByExpiry, ResolvedBySystemDeny, ResolvedByUserStop, ResolvedByHostStop, ResolvedByPolicy}
}
