package authzledger

import (
	"context"
	"database/sql"
	"strings"
)

const (
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"

	ResolvedByHuman      = "human"
	ResolvedByExpiry     = "expiry"
	ResolvedBySystemDeny = "system_deny"
	ResolvedByUserStop   = "user_stop"
	// ResolvedByHostStop cancels a prompt because the session stopped without
	// a person stopping it.
	ResolvedByHostStop = "host_stop"
	ResolvedByPolicy   = "policy"

	GrantScopeOnce       = "once"
	GrantScopeSession    = "session"
	GrantScopePersistent = "persistent"

	AuthorizationSourceHuman    = "human"
	AuthorizationSourceNeverAsk = "never_ask"
	AuthorizationSourceExpiry   = "expiry"
	AuthorizationSourceLease    = "lease"
	AuthorizationSourceUserStop = "user_stop"
	AuthorizationSourceHostStop = "host_stop"
	AuthorizationSourcePolicy   = "policy"
	// AuthorizationSourceTrustedDestination is a standing device-scope decision
	// that one exact provider destination may receive detected credentials.
	AuthorizationSourceTrustedDestination = "trusted_destination"
	// AuthorizationSourceHostComposition is the host's rule that a request it
	// composed for itself carries no credential, so nobody is asked.
	AuthorizationSourceHostComposition = "host_composition"

	AskSuppressedCausePriorDeny         = "prior_deny"
	AskSuppressedCauseJoinedOpenCard    = "joined_open_card"
	AskSuppressedCauseDuplicateToolCall = "duplicate_tool_call"
	AskSuppressedCauseQuiet             = "quiet"

	AskFamilyToolApproval    = "tool_approval"
	AskFamilyWriteRoot       = "write_root"
	AskFamilyReadPath        = "read_path"
	AskFamilyLocalListen     = "local_listen"
	AskFamilyLoopbackConnect = "loopback_connect"
)

// Caller-authored ledger actions share these spellings with authzcontext.
const (
	ActionAskSuppressed = "ask_suppressed"

	// ActionSecretReceipt records a redacted send whose receipt the agent may
	// contest; ActionSecretContest records a consumed receipt raising a card.
	ActionSecretReceipt        = "secret_receipt"
	ActionSecretContest        = "secret_contest"
	ActionSecretPermissionUsed = "secret_permission_used"
	// ActionSecretDestinationTrusted records a detected credential sent
	// unchanged because the destination is trusted in device configuration.
	ActionSecretDestinationTrusted = "secret_destination_trusted"
	// ActionSecretHostComposedRedacted records a detected credential stripped
	// from a request the host composed for its own use, without a card.
	ActionSecretHostComposedRedacted = "secret_host_composed_redacted"
	// ActionSecretChatLocalRelease records a secret the host generated for the
	// chat handed to local recipients without a card, as posture allows.
	ActionSecretChatLocalRelease = "secret_chat_local_release"

	ActionCapabilityRequested = "capability_requested"
	ActionCapabilityGranted   = "capability_granted"
	ActionCapabilityDenied    = "capability_denied"
	ActionCapabilityRevoked   = "capability_revoked"
	ActionCapabilityApplied   = "capability_applied"
	ActionDetectionResolved   = "detection_resolved"

	ActionContentApplyResolved = "content_apply_resolved"
	ActionBlueprintApproved    = "blueprint_approved"
	ActionBlueprintSuperseded  = "blueprint_superseded"
	ActionBlueprintRevoked     = "blueprint_revoked"
)

// ApprovalDecisionRecord is machine state for approval_decision events.
type ApprovalDecisionRecord struct {
	ToolCallID string
	SessionID  string
	Tool       string
	Args       map[string]any
	Files      []string
	ProjectDir string
	Tier       string
	Outcome    string // allowed|denied
	ResolvedBy string // human|expiry|system_deny|user_stop|policy
	// ResolverPersonID is the person who answered or stopped; empty otherwise.
	ResolverPersonID string
	RejectCode       string
	GrantScope       string // once|session|persistent
	CheckpointID     string
	PlanID           string
	ActionDigest     string
	SelectedOptionID string
	GrantIDs         []string
	SubjectKind      string
	SubjectTitle     string
	// Gate is the primary approval reason; Reasons records every contributing gate.
	Gate          string
	Reasons       []string
	ApprovalRules []ApprovalRuleCitation
	// ResolverPolicy names the standing policy rule when ResolvedBy is policy.
	ResolverPolicy *PolicyIdentity
}

// PolicyIdentity names the standing policy rule that settled a decision without a person.
type PolicyIdentity struct {
	PackID string `json:"pack_id" yaml:"pack_id"`
	UnitID string `json:"unit_id" yaml:"unit_id"`
	RuleID string `json:"rule_id" yaml:"rule_id"`
}

// Complete reports whether every part of the identity is present.
func (p PolicyIdentity) Complete() bool {
	return strings.TrimSpace(p.PackID) != "" && strings.TrimSpace(p.UnitID) != "" && strings.TrimSpace(p.RuleID) != ""
}

// ApprovalRuleCitation is redaction-safe extension policy provenance.
type ApprovalRuleCitation struct {
	Category string
	Pattern  string
	Effect   string
	UnitID   string
	PackID   string
	Scope    string
}

// ToolDeniedRecord is machine state for tool_denied events.
type ToolDeniedRecord struct {
	SessionID       string
	ParentSessionID string
	Tool            string
	Args            map[string]any
	Files           []string
	ProjectDir      string
	Tier            string
	RejectCode      string
	BlockReason     string
	ApprovalRules   []ApprovalRuleCitation
}

// CapabilityEndpoint is one mediated observation for capability / mediated ledger rows.
type CapabilityEndpoint struct {
	Host      string
	Port      uint16
	Transport string
	Allowed   bool
	Attempts  int
}

// CapabilitySocket is one applied local-service socket grant for capability ledger rows.
type CapabilitySocket struct {
	ApprovedPath string
	ResolvedPath string
	Scope        string
}

// CapabilityDetection cites a winning detection on an exact action.
type CapabilityDetection struct {
	PackID    string
	RuleID    string
	RuleTitle string
	Level     string
	ActionID  string
}

// CapabilityRecord is machine state for capability, detection, ask-suppression,
// and secret events.
type CapabilityRecord struct {
	ToolCallID string
	SessionID  string
	Action     string // an Action* constant
	Outcome    string // allowed|denied
	ResolvedBy string // human|expiry|system_deny|user_stop|policy
	// ResolverPersonID is the person whose action settled this record.
	ResolverPersonID     string
	Tool                 string
	RejectCode           string
	SuppressionCause     string // an AskSuppressedCause* constant (ask_suppressed only)
	AskFamily            string // an AskFamily* constant (ask_suppressed only)
	AuthorizationSource  string // an AuthorizationSource* constant
	FailClosed           bool   // when true, store failure blocks (grants)
	Endpoints            []CapabilityEndpoint
	Sockets              []CapabilitySocket
	DeclaredDestinations []string
	Direct               bool
	FullBypass           bool
	Detections           []CapabilityDetection
	AffirmativeNone      bool
}

// DirectIPLifecycleRecord is machine state for direct_ip_* lifecycle events.
type DirectIPLifecycleRecord struct {
	SessionID            string
	Phase                string // requested|approved|denied|lease_reused|started|completed|reconstructed
	Tool                 string
	AuthorizationSource  string
	DeclaredDestinations []string
	Background           bool
}

// MediatedEndpointRecord is machine state for mediated endpoint events.
type MediatedEndpointRecord struct {
	SessionID            string
	Tool                 string
	AuthorizationSource  string
	Endpoints            []CapabilityEndpoint
	Sockets              []CapabilitySocket
	DeclaredDestinations []string
	Direct               bool
	Detections           []CapabilityDetection
	// FoldIntoApplied emits capability_applied instead of mediated_endpoint when true.
	FoldIntoApplied bool
}

// HumanGateRecord is machine state for non-tool checkpoint outcomes.
type HumanGateRecord struct {
	SessionID  string
	Action     string // content_apply_resolved|blueprint_approved|blueprint_superseded|blueprint_revoked
	Outcome    string // allowed|denied
	ResolvedBy string // human|expiry|system_deny|user_stop|policy
	// ResolverPersonID is the person who answered or stopped; empty otherwise.
	ResolverPersonID string
	Tool             string
	Files            []string
	ProjectDir       string
	// ContentDecision is the content_apply verdict (approve|approve_partial|reject).
	ContentDecision string
	// BlueprintDigest is the reviewed content digest for blueprint gates.
	BlueprintDigest string
}

// Recorder persists observations; checkpoint outcomes use TransactionalRecorder.
type Recorder interface {
	AppendToolDenied(ctx context.Context, rec ToolDeniedRecord)
	AppendCapabilityRecord(ctx context.Context, rec CapabilityRecord) error
	AppendDirectIPLifecycle(ctx context.Context, rec DirectIPLifecycleRecord)
	AppendMediatedEndpoint(ctx context.Context, rec MediatedEndpointRecord)
}

// TransactionalRecorder seals decisions atomically with their resolving transaction.
// An append error aborts the commit.
type TransactionalRecorder interface {
	AppendApprovalGateTx(ctx context.Context, tx *sql.Tx, rec ApprovalDecisionRecord) error
	AppendCapabilityGateTx(ctx context.Context, tx *sql.Tx, rec CapabilityRecord) error
	AppendHumanGateTx(ctx context.Context, tx *sql.Tx, rec HumanGateRecord) error
}
