package hitl

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ContentApplyPayload is stored for content_apply checkpoints.
type ContentApplyPayload struct {
	Tool       string
	ToolCallID string
	Path       string
	Before     *string
	After      string
}

// ContentApplyResolve captures human resolution for content_apply.
type ContentApplyResolve struct {
	Decision      api.ContentApplyDecision `json:"decision"`
	ApprovedHunks []string                 `json:"approved_hunks,omitempty"`
	// FinalAfter is composed from the immutable plan.
	FinalAfter string `json:"final_after"`
	// Guidance accompanies a rejection.
	Guidance string `json:"guidance,omitempty"`
}

// CheckpointRequest creates a pending checkpoint.
type CheckpointRequest struct {
	SessionID   string
	Kind        api.CheckpointKind
	Title       string
	Description string
	Type        DecisionType
	// ToolCallID anchors the checkpoint to its transcript row.
	ToolCallID string
	// ProjectID is validated against the persisted session.
	ProjectID      string
	ProposedAction *ProposedAction
	// ApprovalPlan is the immutable approval decision surface.
	ApprovalPlan    *ApprovalPlan
	ApprovalMatches []ApprovalRuleMatch
	Explanation     *ApprovalExplanation
	GrantOffers     []ApprovalGrantOffer
	GrantDelta      string
	// Decision identifies the gate that caused the interruption.
	Decision *gate.Decision
	// Detection cites the contributing pack rule.
	Detection *DetectionMatch
	// DetectionEndpoints are the endpoints observed at the ask.
	DetectionEndpoints []authzledger.CapabilityEndpoint
	ContentApply       *ContentApplyPayload
	// AIRationalePending reserves space for an in-flight rationale.
	AIRationalePending bool
	// JoinedCount is how many identical pending asks share this checkpoint (≥1).
	JoinedCount int
	// Repeat carries frozen repeat diagnostics for this mint (count > 1 only).
	Repeat *RepeatContext
	// JoinedToolCallIDs lists tool_call ids that have joined (capped).
	JoinedToolCallIDs []string
	// Coalesce fields identify pending approval guard state.
	CoalesceChat     string
	CoalesceGrantKey string
	// ConsequenceBand / ConsequenceCode are presentation-only; minted after the gate decision.
	ConsequenceBand api.ConsequenceBand
	ConsequenceCode api.ConsequenceCode
	// SocketCapability is typed AF_UNIX context for a local-service tool_approval card.
	SocketCapability *SocketCapability
	// DirectIPCapability is typed one-action direct-IP context for a tool_approval card.
	DirectIPCapability *DirectIPCapability
	// DeclaredEndpoints is the reviewed destination set.
	DeclaredEndpoints *DeclaredEndpoints
	// SecretScreen is redaction-safe context for any matcher-triggered outbound ask.
	SecretScreen *SecretScreen
	// QuietSkipKeys are quiet keys already live for this chat; Quiet options are
	// not reminted for them.
	QuietSkipKeys []string
}

// RepeatContext freezes diagnostic counts at card creation; it does not select approval scope.
type RepeatContext struct {
	ReasonKey         string
	Count             int
	Asks              int
	Subjects          []string
	SubjectsTruncated bool
	SuppressedCount   int
}

// CheckpointResponse is the resolved checkpoint record.
type CheckpointResponse struct {
	CheckpointID  string
	Kind          api.CheckpointKind
	Status        DecisionStatus
	Result        *DecisionResult
	ContentResult *ContentApplyResolve
	ResolvedAt    *time.Time
	// ResolvedBy is the ledger resolution of a settled checkpoint.
	ResolvedBy string
}

// CheckpointAuthorizes reports whether a resolved card released its held
// operation. Status can be approved while Result.Approved is false.
func CheckpointAuthorizes(response *CheckpointResponse) bool {
	return response != nil && response.Status == DecisionStatusApproved &&
		response.Result != nil && response.Result.Approved
}

// CheckpointManager orchestrates human checkpoints (tool_approval, content_apply).
type CheckpointManager interface {
	RequestCheckpoint(ctx context.Context, req CheckpointRequest) (*CheckpointResponse, error)
	PollCheckpoint(ctx context.Context, checkpointID string) (*CheckpointResponse, error)
	ResolveCheckpoint(ctx context.Context, sessionID, checkpointID string, kind api.CheckpointKind, toolResult *DecisionResult, contentResult *ContentApplyResolve) (*CheckpointResponse, error)
	ListPendingForParent(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error)
	ListPending(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]api.CheckpointEvent, error)
	// OldestPendingCheckpoints reports each session scope’s oldest live checkpoint,
	// including worker children, for the cross-project attention view.
	OldestPendingCheckpoints(ctx context.Context) (map[string]time.Time, error)
	SessionApprovalDenied(ctx context.Context, sessionID string) (bool, error)
	// PatchPendingToolApprovalAIRationale sets ai_rationale on a still-pending
	// tool_approval checkpoint and republishes SSE only when the write applied.
	PatchPendingToolApprovalAIRationale(ctx context.Context, checkpointID, text string) error
	// ClearPendingToolApprovalAIRationale clears pending rationale and publishes applied changes.
	ClearPendingToolApprovalAIRationale(ctx context.Context, checkpointID string) error
	// PatchPendingToolApprovalJoined records joined callers and may raise the
	// open card's risk band; it fails with ErrCheckpointNotPending once the card settled.
	PatchPendingToolApprovalJoined(ctx context.Context, checkpointID string, joinedCount int, joinedToolCallIDs []string, joinerBand string, joinerCode string) error
}

// ApprovalOptionResolver applies a host-authored plan option.
type ApprovalOptionResolver interface {
	ResolveApprovalOption(ctx context.Context, sessionID, checkpointID, optionID string) (*CheckpointResponse, error)
	ResolveApprovalOptionBy(ctx context.Context, sessionID, checkpointID, optionID string, resolver ApprovalResolver) (*CheckpointResponse, error)
}

// ApprovalAuthorityInstallerSetter wires the host authority transaction used
// by ApprovalOptionResolver.
type ApprovalAuthorityInstallerSetter interface {
	SetApprovalAuthorityInstaller(ApprovalAuthorityInstaller)
}
