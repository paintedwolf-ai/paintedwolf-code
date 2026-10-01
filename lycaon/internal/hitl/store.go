package hitl

import (
	"context"
	"database/sql"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// StoredCheckpoint is a persisted human checkpoint row.
type StoredCheckpoint struct {
	ID        string
	SessionID string
	// ProjectID is the owning session’s project, enforced by the store.
	ProjectID string
	// ProjectDir is the folder at mint time.
	ProjectDir    string
	Kind          api.CheckpointKind
	Status        DecisionStatus
	Type          DecisionType
	Title         string
	Description   string
	ToolName      string
	Path          string
	Args          map[string]any
	Files         []string
	Payload       map[string]any
	Result        *DecisionResult
	ContentResult *ContentApplyResolve
	CreatedAt     time.Time
	ResolvedAt    *time.Time
	Resolution    *Resolution
}

// Resolution records how a checkpoint was settled and by whom.
type Resolution struct {
	// By uses the ledger's resolved_by vocabulary: human, expiry, user_stop,
	// host_stop, or policy.
	By string
	// PersonID is the person who answered or stopped; empty otherwise.
	PersonID string
	// Policy names the standing rule of a policy resolution; the ledger records it.
	Policy *authzledger.PolicyIdentity
}

// Store persists human checkpoints.
type Store interface {
	EventsViaOutbox() bool
	SessionProjectID(ctx context.Context, sessionID string) (string, error)
	Insert(ctx context.Context, row StoredCheckpoint) error
	Get(ctx context.Context, checkpointID string) (*StoredCheckpoint, error)
	resolveCheckpoint(ctx context.Context, row StoredCheckpoint, status DecisionStatus, result *DecisionResult, contentResult *ContentApplyResolve, resolvedAt time.Time, resolution Resolution, seal func(*sql.Tx, StoredCheckpoint) error) (bool, error)
	ListBySession(ctx context.Context, sessionID string, status DecisionStatus, kind *api.CheckpointKind) ([]StoredCheckpoint, error)
	ListPendingForParent(ctx context.Context, sessionID string, kind *api.CheckpointKind) ([]StoredCheckpoint, error)
	// ListPending returns every live checkpoint across sessions so restart can
	// restore expiry and coalescing before serving new actions.
	ListPending(ctx context.Context) ([]StoredCheckpoint, error)
	// OldestPendingBySession reports the oldest checkpoint in each session's
	// scope, including its workers; empty scopes are absent.
	OldestPendingBySession(ctx context.Context) (map[string]time.Time, error)
	// ListRejectedToolApprovals returns the denials still awaiting their chat's
	// next user intent boundary.
	ListRejectedToolApprovals(ctx context.Context) ([]StoredCheckpoint, error)
	LatestResolvedToolApprovalStatus(ctx context.Context, sessionID string) (DecisionStatus, bool, error)
	// SetPendingAIRationale writes Payload["ai_rationale"] (and clears the
	// ai_rationale_pending flag) only while status is pending. Returns
	// applied=false when the row is missing or already resolved.
	SetPendingAIRationale(ctx context.Context, checkpointID, text string) (applied bool, err error)
	// ClearPendingAIRationaleFlag clears Payload["ai_rationale_pending"] only while
	// status is pending — the fail-soft path when a rationale call produced nothing.
	// Returns applied=false when the row is missing, already resolved, or the flag
	// was not set.
	ClearPendingAIRationaleFlag(ctx context.Context, checkpointID string) (applied bool, err error)
	// SetPendingJoined writes joined_count / joined_tool_call_ids only while pending.
	// joinerBand/joinerCode may raise consequence_band on the open card (never lower).
	SetPendingJoined(ctx context.Context, checkpointID string, joinedCount int, joinedToolCallIDs []string, joinerBand string, joinerCode string) (applied bool, err error)
	prepareApprovalOperation(context.Context, string, string, ApprovalOption) error
	commitApprovalOperation(context.Context, StoredCheckpoint, *DecisionResult, time.Time, Resolution, func(*sql.Tx) error) (bool, error)
	rollbackApprovalOperation(context.Context, string) error
	preparedApprovalOperations(context.Context) ([]preparedApprovalOperation, error)
	chatGrants(context.Context, time.Time) ([]ChatGrant, error)
	forgetChatGrant(context.Context, string) (bool, error)
}
