package promptloop

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// persistClassifiedToolOutcome stores and enriches one tool result.
func (l *PromptLoop) persistClassifiedToolOutcome(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	history []api.Message,
	out toolCallOutcome,
	lastToolTS *time.Time,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	ctx = context.WithoutCancel(ctx)
	stampCommitOrderTS(&out.toolMsg, lastToolTS)
	stored, transient := l.storageSafeMessage(ctx, out.toolMsg)
	history, err := l.commitToolResultWithOptionalNote(
		ctx, sessionID, history, stored, transient, out.agentNote, lastToolTS, st,
	)
	if err != nil {
		return history, err
	}
	return l.enrichCommittedToolRow(ctx, sessionID, sess, history, out.toolName, out.toolArgs, stored.ID, out.handleEligible, st)
}

// enrichCommittedToolRow applies evidence and compaction to a stored result.
func (l *PromptLoop) enrichCommittedToolRow(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	history []api.Message,
	toolName string,
	args map[string]any,
	messageID string,
	handleEligible bool,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	idx := messageIndexByID(history, messageID)
	if idx < 0 {
		return history, nil
	}
	stored := history[idx]
	var raw *api.Message
	if overlay, ok := st.lookupSecretStorage(messageID); ok {
		stored = overlay.stored
		transient := overlay.transient
		raw = &transient
	}
	before := stored.Content
	if stored.ToolResult != nil {
		before = stored.ToolResult.Content
	}
	beforeHandles := append([]string(nil), stored.EvidenceHandles...)
	if err := l.tagToolHandleOnCommit(ctx, sessionID, sess, toolName, toolArgsForEnrich(stored, args), &stored, handleEligible); err != nil {
		return history, err
	}
	l.compactToolWireOnCommit(ctx, sess, toolName, &stored)
	if !toolRowNeedsEnrichmentPatch(before, beforeHandles, stored) {
		return history, nil
	}
	if l.Deps.UpdateMessage != nil {
		if err := l.Deps.UpdateMessage(ctx, sessionID, messageID, stored); err != nil {
			return history, err
		}
	}
	if raw != nil {
		st.rememberSecretStorageOverlay(stored, *raw)
		history[idx] = transientMessageFromStored(stored, raw)
		return history, nil
	}
	history[idx] = stored
	return history, nil
}

func toolArgsForEnrich(msg api.Message, args map[string]any) map[string]any {
	if msg.ToolResult != nil && msg.ToolResult.ToolArgs != nil {
		return msg.ToolResult.ToolArgs
	}
	return args
}

func toolRowNeedsEnrichmentPatch(beforeContent string, beforeHandles []string, after api.Message) bool {
	afterContent := after.Content
	if after.ToolResult != nil {
		afterContent = after.ToolResult.Content
	}
	if beforeContent != afterContent {
		return true
	}
	if len(beforeHandles) != len(after.EvidenceHandles) {
		return true
	}
	for i := range beforeHandles {
		if beforeHandles[i] != after.EvidenceHandles[i] {
			return true
		}
	}
	return after.CompactedChunk != nil
}

func messageIndexByID(history []api.Message, id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for i := range history {
		if history[i].ID == id {
			return i
		}
	}
	return -1
}

type parallelBatchCommit struct {
	mu                   sync.Mutex
	history              []api.Message
	reserved             []time.Time
	lastToolTS           *time.Time
	turnTools            []string
	anyTaskEnqueued      bool
	taskEnqueuedThisTurn int
	lastTaskMessageID    string
	st                   *promptLoopTurnState
	sess                 *api.Session
	sessionID            string
}

func reserveCommitTimestamps(n int, last *time.Time) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		var msg api.Message
		stampCommitOrderTS(&msg, last)
		out[i] = msg.CreatedAt
	}
	return out
}

func newParallelBatchCommit(
	history []api.Message,
	run []api.ToolCall,
	lastToolTS *time.Time,
	turnTools []string,
	anyTaskEnqueued bool,
	taskEnqueuedThisTurn int,
	lastTaskMessageID string,
	st *promptLoopTurnState,
	sess *api.Session,
	sessionID string,
) *parallelBatchCommit {
	return &parallelBatchCommit{
		history:              history,
		reserved:             reserveCommitTimestamps(len(run), lastToolTS),
		lastToolTS:           lastToolTS,
		turnTools:            turnTools,
		anyTaskEnqueued:      anyTaskEnqueued,
		taskEnqueuedThisTurn: taskEnqueuedThisTurn,
		lastTaskMessageID:    lastTaskMessageID,
		st:                   st,
		sess:                 sess,
		sessionID:            sessionID,
	}
}

func (l *PromptLoop) appendParallelResult(
	ctx context.Context,
	commit *parallelBatchCommit,
	out *toolCallOutcome,
) {
	if commit == nil || out == nil {
		return
	}
	if out.err != nil || strings.TrimSpace(out.toolMsg.ID) == "" {
		return
	}
	commit.mu.Lock()
	defer commit.mu.Unlock()
	if err := l.appendParallelOutcomeLocked(ctx, commit, out); err != nil {
		out.err = err
	}
}

func (l *PromptLoop) appendParallelOutcomeLocked(
	ctx context.Context,
	commit *parallelBatchCommit,
	out *toolCallOutcome,
) error {
	ctx = context.WithoutCancel(ctx)
	if out.index >= 0 && out.index < len(commit.reserved) {
		out.toolMsg.CreatedAt = commit.reserved[out.index]
	} else {
		stampCommitOrderTS(&out.toolMsg, commit.lastToolTS)
	}
	stored, transient := l.storageSafeMessage(ctx, out.toolMsg)
	rows, transients := l.classifiedResultRows(ctx, stored, transient, out.agentNote, commit.lastToolTS)
	if err := l.persistStorageSafeMessages(ctx, commit.sessionID, rows); err != nil {
		return err
	}
	out.committed = true
	out.storedID = stored.ID
	out.persistedStored = rows
	out.persistedTransient = transients
	commit.turnTools = l.settleToolRow(ctx, commit.sess, commit.sessionID, out.toolName, commit.turnTools, commit.st)
	if out.taskCommitted {
		commit.anyTaskEnqueued = true
		commit.taskEnqueuedThisTurn++
		commit.lastTaskMessageID = out.taskMessageID
	}
	return nil
}

func (l *PromptLoop) finishParallelToolOutcomes(
	ctx context.Context,
	commit *parallelBatchCommit,
	outcomes []toolCallOutcome,
) error {
	if commit == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	commit.mu.Lock()
	defer commit.mu.Unlock()
	for i := range outcomes {
		out := &outcomes[i]
		if out.err != nil || strings.TrimSpace(out.toolMsg.ID) == "" {
			continue
		}
		if !out.committed {
			if err := l.appendParallelOutcomeLocked(ctx, commit, out); err != nil {
				return err
			}
		}
		commit.history = foldStorageSafeMessages(commit.history, out.persistedStored, out.persistedTransient, commit.st)
		var err error
		commit.history, err = l.enrichCommittedToolRow(
			ctx, commit.sessionID, commit.sess, commit.history, out.toolName, out.toolArgs, out.storedID, out.handleEligible, commit.st,
		)
		if err != nil {
			return err
		}
	}
	return nil
}
