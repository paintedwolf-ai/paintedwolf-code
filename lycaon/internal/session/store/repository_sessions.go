package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/pkg/api"
)

// SessionRepository persists session identity, lifecycle and workspace binding.
type SessionRepository interface {
	SessionEventsOutboxed() bool
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error)
	CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error)
	Delete(ctx context.Context, id string) error
	DeleteWorktreeBinding(ctx context.Context, sessionID string) error
	ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error)
	Get(ctx context.Context, id string) (*api.Session, error)
	GetWorktreeBinding(ctx context.Context, sessionID string) (*WorktreeBinding, bool, error)
	HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error)
	LatestFinishes(ctx context.Context) (map[string]time.Time, error)
	List(ctx context.Context) ([]*api.Session, error)
	ListAttentionCandidates(ctx context.Context) ([]attention.Candidate, error)
	ListBusySessionIDs(ctx context.Context, limit int) ([]string, error)
	ListProjectSummaries(ctx context.Context, q SummaryQuery) (SummaryPage, error)
	MovePinnedSession(ctx context.Context, id string, position int) error
	PinSession(ctx context.Context, id string) error
	PutWorktreeBinding(ctx context.Context, b WorktreeBinding) error
	ReadExecutionState(ctx context.Context, root string) (ExecutionState, error)
	ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error
	SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error)
	SessionTreeMembers(ctx context.Context, rootID string) ([]SessionTreeMember, error)
	SetPostureTx(ctx context.Context, tx *sql.Tx, sessionID string, posture api.SessionPosture) error
	SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error
	SetSessionStatusEvent(
		ctx context.Context,
		id string,
		status api.SessionStatus,
		hostError *api.SessionHostError,
	) error
	UnpinSession(ctx context.Context, id string) error
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
	UpdateSessionWith(ctx context.Context, id string, fn func(*api.Session), mutate func(*sql.Tx) error) error
	UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error)
	WorkspaceNotificationIDs(ctx context.Context, root, after string) ([]string, error)
}
