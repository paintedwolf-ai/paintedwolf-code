// Package session defines the session orchestration layer and its persistence port.
package session

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// QueryStore reads session state.
type QueryStore interface {
	// HostOwner returns the person holding the host's device credential.
	HostOwner(ctx context.Context) (people.Person, error)
	ReadExecutionState(ctx context.Context, rootSessionID string) (store.ExecutionState, error)
	MutationEventsOutboxed() bool
	Get(ctx context.Context, id string) (*api.Session, error)
	List(ctx context.Context) ([]*api.Session, error)
	WorkspaceNotificationIDs(ctx context.Context, root, after string) ([]string, error)
	ExistingSessionIDs(ctx context.Context, ids []string) (map[string]bool, error)
	// ListProjectSummaries pages a project's top-level chats.
	ListProjectSummaries(ctx context.Context, q store.SummaryQuery) (store.SummaryPage, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
	GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error)
	GetMessagesAfterOrd(ctx context.Context, sessionID string, afterOrd int64, limit int) ([]api.Message, error)
	GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error)
	// LastAssistantMessageContent and LastTurnMessageContent read one row from
	// the transcript tail; "" when no such row exists.
	LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error)
	LastTurnMessageContent(ctx context.Context, sessionID string) (string, error)
	ListBusySessionIDs(ctx context.Context, limit int) ([]string, error)
	// UserTurnOrdinal returns the newest ordinal used for source stamps.
	UserTurnOrdinal(ctx context.Context, sessionID string) (int, error)
	UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error)
	// PutTurnSourceBrief records the source-change brief a turn opened with;
	// TurnSourceBriefs returns them keyed by each turn's opening message.
	PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID, briefJSON string) error
	TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error)
	GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
	// LatestTurnClock reads the durable clock of a root session's newest visible user turn.
	LatestTurnClock(ctx context.Context, sessionID string) (store.TurnClock, bool, error)
	// SessionTreeIDs resolves tree-scoped evidence membership.
	SessionTreeIDs(ctx context.Context, rootSessionID string) ([]string, error)
	SessionTreeMembers(ctx context.Context, rootSessionID string) ([]store.SessionTreeMember, error)
	// MessageIDsBelowScreenGeneration returns rows last screened before the
	// given evidence revision.
	MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error)
	// MaxMessageScreenGenerationInTree preserves monotonic sweep revisions.
	MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error)
	GetCompactionView(ctx context.Context, sessionID string) (*store.CompactionView, bool, error)
	CompactionViewCurrent(ctx context.Context, sessionID string, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) (bool, error)
	ListDraftVersions(ctx context.Context, sessionID, slotID string) ([]api.DraftVersion, error)
	CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error)
	// GetWorktreeBinding returns the session's worktree binding; ok=false when unbound.
	GetWorktreeBinding(ctx context.Context, sessionID string) (*store.WorktreeBinding, bool, error)
	GetPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, error)
	ListQueuedUserPromptSubmissions(ctx context.Context, sessionID string) ([]store.PromptSubmission, error)
	ListPendingModelOutputProjections(ctx context.Context) ([]store.PendingModelOutputProjection, error)
	SessionSecretExposure(ctx context.Context, sessionID string) (bool, error)
	SessionUntrustedContent(sessionID string) bool
	SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error)
	ListOperationPromptAttachmentRetentions(ctx context.Context, operationID string) ([]store.PromptAttachmentRetention, error)
	PromptAttachmentBlobRetained(ctx context.Context, projectID, blobID string) (bool, error)
	ListPromptAttachmentReclaimCandidates(ctx context.Context, projectID string, createdBefore time.Time, limit int) ([]string, error)
	PromptAttachmentStorageUsage(ctx context.Context, projectID string) (int64, error)
	GetRewindOperation(ctx context.Context, id string) (*store.RewindOperation, error)
	RewindOperationsForRecovery(ctx context.Context) ([]store.RewindOperation, error)
	// RewindOperationsForRecoverySession scopes RewindOperationsForRecovery to
	// one session, for repair after a panic caught mid-rewind.
	RewindOperationsForRecoverySession(ctx context.Context, sessionID string) ([]store.RewindOperation, error)
	// CommittedRewindOperationsForSweep lists committed rewinds awaiting the
	// boot disk sweep of their journal, checkpoint anchor, and retained row.
	CommittedRewindOperationsForSweep(ctx context.Context) ([]store.RewindOperationSweepItem, error)
	// HasActiveProjectSessions reports whether a project has unarchived active sessions.
	HasActiveProjectSessions(ctx context.Context, projectID string, activeSince time.Time) (bool, error)
}

// CommandStore writes session state.
type CommandStore interface {
	Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
	CreateWithStatus(ctx context.Context, req api.CreateSessionRequest, projectID string, status api.SessionStatus) (*api.Session, error)
	CreateChild(ctx context.Context, parent *api.Session, req api.SpawnChildRequest) (*api.Session, error)
	Delete(ctx context.Context, id string) error
	AppendMessages(ctx context.Context, id string, msgs ...api.Message) error
	UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error)
	PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error)
	StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error
	PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error
	TruncateMessagesFrom(ctx context.Context, sessionID, anchorMessageID string) (int, error)
	PutCompactionView(ctx context.Context, sessionID string, view store.CompactionView) error
	AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	UpdateSession(ctx context.Context, id string, fn func(*api.Session)) error
	// PinSession, UnpinSession, and MovePinnedSession own pin_rank; UpdateSession
	// only clears it when a chat is archived or changes project.
	PinSession(ctx context.Context, id string) error
	UnpinSession(ctx context.Context, id string) error
	MovePinnedSession(ctx context.Context, id string, position int) error
	UpdateTitleIfUnset(ctx context.Context, id, title string) (bool, error)
	ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error
	SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error
	SetSessionStatusEvent(ctx context.Context, id string, status api.SessionStatus, hostError *api.SessionHostError) error
	PutWorktreeBinding(ctx context.Context, b store.WorktreeBinding) error
	DeleteWorktreeBinding(ctx context.Context, sessionID string) error
	PutPromptSubmission(ctx context.Context, in store.PromptSubmission) (*store.PromptSubmission, bool, error)
	ClaimPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, bool, error)
	ClaimPromptSubmissions(ctx context.Context, ids []string) ([]store.PromptSubmission, bool, error)
	FinishPromptSubmission(ctx context.Context, id, claimToken string, status store.PromptSubmissionStatus, resultJSON string, failure store.PromptSubmissionFailure) error
	ListUnsettledUserPromptSubmissionIDs(ctx context.Context, sessionID string) ([]string, error)
	CancelQueuedPromptSubmissions(ctx context.Context, ids []string) error
	InterruptPromptSubmissionsBySession(ctx context.Context, sessionID string) error
	InterruptRunningPromptSubmissionsBySession(ctx context.Context, sessionID string) error
	UpdateQueuedPromptSubmissionInputs(ctx context.Context, updates []store.PromptSubmissionInputUpdate) (bool, error)
	RecoverPromptSubmissions(ctx context.Context) ([]string, error)
	BeginTurn(ctx context.Context, in store.TurnStart) (store.TurnExecution, error)
	LatestTurnStatus(ctx context.Context, sessionID string) (store.TurnStatus, error)
	CheckpointTurn(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error
	ProjectLiveModelOutput(ctx context.Context, out store.LiveModelOutput) error
	ConfigureModelLimit(ctx context.Context, sessionID string, limit int) error
	AdmitModelResponse(ctx context.Context, sessionID, attemptID string) error
	SealTurnCloseout(ctx context.Context, commit store.TurnCloseoutCommit) error
	SettleModelOutput(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error)
	MarkModelOutputProjected(ctx context.Context, outputID string) error
	FinishTurn(ctx context.Context, turnID, attemptID string, status store.TurnStatus, finalOutputID, resultJSON, failure string) (store.Turn, error)
	RecoverTurns(ctx context.Context) ([]store.Turn, error)
	// RecoverTurnsForSession leaves other sessions' active turns untouched.
	RecoverTurnsForSession(ctx context.Context, sessionID string) ([]store.Turn, error)
	// PutTurnClock records a visible user turn clock once its opening message exists.
	PutTurnClock(ctx context.Context, clock store.TurnClock) error
	// PutTurnLoadReceipt records what a load decision established and returns
	// the receipt with its id.
	PutTurnLoadReceipt(ctx context.Context, receipt store.TurnLoadReceipt) (store.TurnLoadReceipt, error)
	// ListTurnLoadReceiptsForTurns returns the receipts of the turns the given
	// user messages opened, in recording order.
	ListTurnLoadReceiptsForTurns(ctx context.Context, openingMessageIDs []string) ([]store.TurnLoadReceipt, error)
	// LatestTurnLoadReceipt returns the session's most recent receipt.
	LatestTurnLoadReceipt(ctx context.Context, sessionID string) (store.TurnLoadReceipt, bool, error)
	// SettleAbandonedTurnClocks pauses clocks left running by process loss.
	SettleAbandonedTurnClocks(ctx context.Context) (int, error)
	// LatestTurnProgress is when any of the session's turns last did durable
	// work; zero when it has none.
	LatestTurnProgress(ctx context.Context, sessionID string) (time.Time, error)
	SeedSecretExposure(ctx context.Context, sessionID string) error
	SeedUntrustedContent(ctx context.Context, sessionID string) error
	UpsertEvidenceRecord(ctx context.Context, sessionID string, record evidence.Record) error
	RecordPromptAttachmentBlob(ctx context.Context, projectID, blobID string, byteSize int64) error
	DeletePromptAttachmentBlob(ctx context.Context, projectID, blobID string) error
	PrepareRewind(ctx context.Context, op store.RewindOperation) error
	SetRewindPhase(ctx context.Context, id, status, detail string) error
	CommitRewind(ctx context.Context, id, sessionID, anchorMessageID string, response api.RewindSessionResponse) (int, error)
	// DeleteRewindOperation removes one committed rewind row older than
	// olderThan; any other row is left in place.
	DeleteRewindOperation(ctx context.Context, id string, olderThan time.Time) error
}

// Store provides session persistence.
type Store interface {
	store.CheckpointRepository
	messageview.ChunkProjectionStore
	messageview.CompactionAttemptStore
	QueryStore
	CommandStore
	guidance.EvidenceLedgerStore
}
