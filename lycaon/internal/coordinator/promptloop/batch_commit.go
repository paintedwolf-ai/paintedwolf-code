package promptloop

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// persistClassifiedToolOutcome stores and enriches one tool result.
func (l *toolBatch) persistClassifiedToolOutcome(
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
	bindReviewResult(&out.toolMsg, st)
	stored, transient := l.Projection.storageSafeMessage(ctx, out.toolMsg)
	history, err := l.Tools.commitToolResultWithOptionalNote(
		ctx, sessionID, history, stored, transient, out.agentNote, lastToolTS, st,
	)
	if err != nil {
		return history, err
	}
	return l.enrichCommittedToolRow(ctx, sessionID, sess, history, out.toolName, out.toolArgs, stored.ID, out.handleEligible, st)
}

// enrichCommittedToolRow applies evidence and compaction to a stored result.
func (l *toolBatch) enrichCommittedToolRow(
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
	if l.Projection.Deps.UpdateMessage != nil {
		if err := l.Projection.Deps.UpdateMessage(ctx, sessionID, messageID, stored); err != nil {
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

func (l *toolBatch) appendParallelResult(
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

func (l *toolBatch) appendParallelOutcomeLocked(
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
	bindReviewResult(&out.toolMsg, commit.st)
	stored, transient := l.Projection.storageSafeMessage(ctx, out.toolMsg)
	rows, transients := l.Tools.classifiedResultRows(ctx, stored, transient, out.agentNote, commit.lastToolTS)
	if err := l.Projection.persistStorageSafeMessages(ctx, commit.sessionID, rows); err != nil {
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

func (l *toolBatch) finishParallelToolOutcomes(
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

// stampCommitOrderTS assigns monotonically increasing timestamps in call order.
func stampCommitOrderTS(msg *api.Message, last *time.Time) {
	now := time.Now().UTC()
	if !now.After(*last) {
		now = last.Add(time.Microsecond)
	}
	*last = now
	msg.CreatedAt = now
}

// tagToolHandleOnCommit persists the evidence record and stamps the host handle on the tool result.
func (l *toolBatch) tagToolHandleOnCommit(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, msg *api.Message, eligible bool) error {
	if msg == nil {
		return nil
	}
	if sess != nil && toolName == "verify" && l.Tools.Deps.ConfirmVerifyResult != nil &&
		msg.ToolResult != nil && msg.ToolResult.Outcome == api.ToolResultOutcomeCompleted {
		content := strings.TrimSpace(msg.ToolResult.Content)
		if content == "" {
			content = strings.TrimSpace(msg.Content)
		}
		if stamped := l.Tools.Deps.ConfirmVerifyResult(sess, content); stamped != "" {
			msg.ToolResult.Content = stamped
		}
	}
	if !eligible {
		return nil
	}
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	handle := ""
	patchedContent := content
	if l.Tools.Deps.CommitEvidenceToolResult != nil && sess != nil {
		artifactID := ""
		if msg.ToolResult != nil && msg.ToolResult.Visual != nil {
			artifactID = msg.ToolResult.Visual.ID
		}
		var err error
		handle, patchedContent, err = l.Tools.Deps.CommitEvidenceToolResult(ctx, sessionID, sess, toolName, args, content, artifactID)
		if err != nil {
			return fmt.Errorf("record %s evidence: %w", toolName, err)
		}
	}
	content = patchedContent
	if handle == "" {
		return nil
	}
	// Re-stamp artifact_id after evidence rewrites the JSON.
	if msg.ToolResult != nil && msg.ToolResult.Visual != nil {
		content = visual.StampArtifactID(content, msg.ToolResult.Visual.ID)
	}
	msg.Content = guidance.PrependHandleTag(content, handle)
	if msg.ToolResult != nil {
		msg.ToolResult.Content = guidance.PrependHandleTag(content, handle)
		if msg.ToolResult.Visual != nil {
			msg.ToolResult.Visual.EvidenceHandle = handle
		}
		// Evidence handles do not change structured outcome fields.
	}
	msg.EvidenceHandles = append(append([]string(nil), msg.EvidenceHandles...), handle)
	return nil
}

// stampDietFieldsOnCommit sets durable Message diet stamps from machine producers.
func stampDietFieldsOnCommit(toolName string, msg *api.Message) {
	if msg == nil {
		return
	}
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	if !tooloutput.IsOverlayPromoteTool(toolName) {
		return
	}
	if compaction.IsOverlayPromoteConflictProtected(content) {
		msg.DietStamp = compaction.DietStampPreserveStructure
		msg.DietStampSource = compaction.DietStampSourceOverlayMerge
	}
}

// compactToolWireOnCommit compacts payloads after evidence handles are minted.
func (l *toolBatch) compactToolWireOnCommit(ctx context.Context, sess *api.Session, toolName string, msg *api.Message) {
	if l == nil || msg == nil || l.Tools.Deps.CompactToolWire == nil {
		return
	}
	stampDietFieldsOnCommit(toolName, msg)
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	out, meta := l.Tools.Deps.CompactToolWire(ctx, sess, toolName, content, compaction.CompactToolWireOpts{
		DietStamp:       msg.DietStamp,
		DietStampSource: msg.DietStampSource,
		EvidenceHandles: append([]string(nil), msg.EvidenceHandles...),
	})
	msg.Content = out
	if msg.ToolResult != nil {
		msg.ToolResult.Content = out
	}
	msg.CompactedChunk = meta
}

// retrievalSourceLabel formats host-observed retrieval attribution.
func retrievalSourceLabel(tool, observed string) string {
	label := strings.TrimSpace(tool)
	if label == "" {
		label = "retrieval"
	}
	// Redirects use only the handler's observed destination.
	detail := strings.TrimSpace(observed)
	if detail == "" {
		return label
	}
	return label + " · " + detail
}
