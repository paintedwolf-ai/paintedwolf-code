package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// TranscriptRepository persists canonical messages and their durable presentation metadata.
type TranscriptRepository interface {
	AppendDraftVersion(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	AppendMessages(ctx context.Context, id string, msgs ...api.Message) error
	AppendMessagesTx(ctx context.Context, tx *sql.Tx, id string, msgs ...api.Message) error
	AppendMessagesWith(ctx context.Context, id string, mutate func(*sql.Tx) error, msgs ...api.Message) error
	CountDraftVersions(ctx context.Context, sessionID, slotID string) (int, error)
	GetMessage(ctx context.Context, sessionID, messageID string) (api.Message, error)
	GetMessages(ctx context.Context, id string) ([]api.Message, error)
	GetTranscriptPage(ctx context.Context, id string, q api.TranscriptPageQuery) (api.SessionTranscriptPage, error)
	GetWorkerJobMessages(ctx context.Context, sessionID, workerJobID string) ([]api.Message, error)
	LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error)
	LastTurnMessageContent(ctx context.Context, sessionID string) (string, error)
	ListDraftVersions(ctx context.Context, sessionID, slotID string) ([]api.DraftVersion, error)
	MaxMessageScreenGenerationInTree(ctx context.Context, rootSessionID string) (uint64, error)
	MessageIDsBelowScreenGeneration(ctx context.Context, sessionID string, generation uint64) ([]string, error)
	MutationEventsOutboxed() bool
	PatchLiveProjection(ctx context.Context, sessionID, messageID, content string, toolCalls []api.ToolCall) error
	PatchMessageNavigation(ctx context.Context, sessionID, messageID, contentHash string, expected, refs []api.NavigationReference) (api.Message, error)
	PatchMessageViewContext(ctx context.Context, sessionID, messageID string, view api.ViewContext) (api.Message, error)
	ReadMessageWindow(ctx context.Context, id string, window messageview.MessageWindow) ([]api.Message, error)
	ReadSourceTurn(ctx context.Context, id string, number int) (messageview.SourceTurn, error)
	StampMessageScreenGeneration(ctx context.Context, sessionID, messageID string, generation uint64) error
	ToolResultMessageID(ctx context.Context, sessionID, toolCallID string) (string, error)
	UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error)
	UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error)
}
