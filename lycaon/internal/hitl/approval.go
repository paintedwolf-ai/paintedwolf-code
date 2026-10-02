package hitl

import (
	"context"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalRuleMatch is one policy rule that governed an approval decision.
type ApprovalRuleMatch struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Effect   string `json:"effect"`
	Command  string `json:"command,omitempty"`
	UnitID   string `json:"unit_id,omitempty"`
	PackID   string `json:"pack_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// DetectionMatch records the rule and declared effects that raised an ask.
type DetectionMatch struct {
	PackID        string
	RuleID        string
	RuleTitle     string
	Level         string
	External      bool
	Local         bool
	Unrecoverable bool
	Tagged        bool
	CorrelationID string // informational related-alert grouping; never authorization identity
}

// ApprovalResult is the outcome of approval evaluation.
type ApprovalResult struct {
	Denied bool
	// DenyCode is the machine code for an enforcement block. Empty unless Denied.
	DenyCode string
	// DenySubject is the path the block names, so the reject can cite it.
	DenySubject string
	// Decision names the gates and facts requiring an answer.
	Decision *gate.Decision
	// MatchedRules are ordered from device to project scope.
	MatchedRules []ApprovalRuleMatch
	// HostResourceApproval records an independent host-resource ask.
	HostResourceApproval bool
	// DetectionCitation preserves structured rule identity for the ledger.
	DetectionCitation *DetectionMatch
	// GrantDelta explains why exact-action authority did not cover this action.
	GrantDelta string
	// FileAccess freezes exact canonical paths before review. It becomes
	// invocation-local authority only after this action is approved.
	FileAccess []GrantedPathDelta
}

// Required reports whether a human must answer before this action proceeds.
func (r *ApprovalResult) Required() bool { return r != nil && r.Decision != nil && !r.Denied }

// AutoApproved reports whether the action proceeds without an interruption.
func (r *ApprovalResult) AutoApproved() bool { return r != nil && r.Decision == nil && !r.Denied }

// Gate returns the primary gate for this result, or the empty gate when silent.
func (r *ApprovalResult) Gate() api.ApprovalGate {
	if r == nil || r.Decision == nil {
		return ""
	}
	return r.Decision.Primary
}

// ApprovalGate evaluates whether an action requires human approval.
type ApprovalGate interface {
	Evaluate(ctx context.Context, action ProposedAction) (*ApprovalResult, error)
	// GrantOffers returns reusable choices for the gated action.
	GrantOffers(action ProposedAction, result *ApprovalResult) []ApprovalGrantOffer
	// AbsorbedGrantOffers omits the time rung already supplied by the card.
	AbsorbedGrantOffers(action ProposedAction, result *ApprovalResult) []ApprovalGrantOffer
	// ApplyGrant activates a checkpoint grant.
	ApplyGrant(grant ApprovalGrant) (created bool, err error)
	// GrantCovers is true only for an explicit chat or durable lease.
	GrantCovers(action ProposedAction) bool
	// HostResourceLeaseCovers is true when a live host-resource lease covers
	// every named catalog id. Device scope matches by id only.
	HostResourceLeaseCovers(action ProposedAction) bool
	// RevokeGrant removes a chat or durable grant. found is false when no grant existed.
	RevokeGrant(id string) (found bool, err error)
	// RevokeGrantInstalledBy removes a grant only when its installing operation still identifies it.
	RevokeGrantInstalledBy(id, operationID string) (found bool, err error)
	// ListGrants returns active chat and durable grants visible to a chat.
	ListGrants(chatSessionID string) []ApprovalGrant
	// SecretFingerprintsCovered checks release authority for one outbound seam.
	SecretFingerprintsCovered(chatSessionID, projectID, destinationID, surface string, fingerprints []string) bool
	// SecretRedactionStanding reports complete standing-redaction coverage.
	SecretRedactionStanding(projectID string, fingerprints []string) bool
	// PutAskQuiet installs a chat-keyed ask quiet. ttlSeconds 0 means until revoked.
	PutAskQuiet(q AskQuiet, ttlSeconds int) (AskQuiet, bool)
	// AskQuietLive reports whether key is currently quieted for the chat.
	AskQuietLive(chatSessionID, key string) (AskQuiet, bool)
	// NoteAskQuietSuppressed increments the suppressed counter on a live quiet.
	NoteAskQuietSuppressed(chatSessionID, key string)
	// ListAskQuiets returns live quiets for chat, or all chats when chat is empty.
	ListAskQuiets(chatSessionID string) []AskQuiet
	// RevokeAskQuiet drops one quiet by id. Returns false when unknown.
	RevokeAskQuiet(id string) bool
	// RevokeAskQuietInstalledBy drops a quiet only when its installing operation still identifies it.
	RevokeAskQuietInstalledBy(id, operationID string) bool
	// ForgetSession drops all remembered approvals for a chat session (on session close).
	ForgetSession(chatSessionID string)
}
