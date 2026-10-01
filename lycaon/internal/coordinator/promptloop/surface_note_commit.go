package promptloop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// commitToolResultWithOptionalNote appends the tool row, and its agent note
// when present, as one AppendMessages batch.
func (l *PromptLoop) commitToolResultWithOptionalNote(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	toolMsg api.Message,
	transientToolMsg *api.Message,
	note *tools.AgentNoteCapture,
	lastToolTS *time.Time,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	stored, transient := l.classifiedResultRows(ctx, toolMsg, transientToolMsg, note, lastToolTS)
	return l.appendStorageSafeMessages(ctx, sessionID, history, stored, transient, st)
}

func (l *PromptLoop) classifiedResultRows(
	ctx context.Context,
	toolMsg api.Message,
	transientTool *api.Message,
	note *tools.AgentNoteCapture,
	lastToolTS *time.Time,
) ([]api.Message, []*api.Message) {
	if note == nil || strings.TrimSpace(note.MessageID) == "" || strings.TrimSpace(note.Content) == "" {
		return []api.Message{toolMsg}, []*api.Message{transientTool}
	}
	if toolMsg.ToolResult != nil {
		toolMsg.ToolResult.UiVisibility = api.ToolResultUiVisibilityBenign
	}
	noteMsg := api.Message{
		SourceContext: note.SourceContext,
		ID:            note.MessageID,
		Role:          api.MessageRoleAssistant,
		Kind:          api.MessageKindAgentNote,
		Visibility:    api.MessageVisibilityTranscript,
		Content:       note.Content,
		Grounding:     note.Grounding,
		ArtifactIDs:   note.ArtifactIDs,
	}
	stampCommitOrderTS(&noteMsg, lastToolTS)
	storedNote, transientNote := l.storageSafeMessage(ctx, noteMsg)
	return []api.Message{toolMsg, storedNote}, []*api.Message{transientTool, transientNote}
}

func (l *PromptLoop) appendStorageSafeMessages(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	stored []api.Message,
	transient []*api.Message,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if err := l.persistStorageSafeMessages(ctx, sessionID, stored); err != nil {
		return history, err
	}
	return foldStorageSafeMessages(history, stored, transient, st), nil
}

func (l *PromptLoop) persistStorageSafeMessages(ctx context.Context, sessionID string, stored []api.Message) error {
	if l == nil || l.Deps.AppendMessages == nil {
		return fmt.Errorf("append messages not configured")
	}
	return l.Deps.AppendMessages(ctx, sessionID, stored...)
}

func foldStorageSafeMessages(
	history []api.Message,
	stored []api.Message,
	transient []*api.Message,
	st *promptLoopTurnState,
) []api.Message {
	for i := range stored {
		var raw *api.Message
		if i < len(transient) {
			raw = transient[i]
		}
		promptMsg := transientMessageFromStored(stored[i], raw)
		history = append(history, promptMsg)
		if raw != nil {
			st.rememberSecretStorageOverlay(stored[i], promptMsg)
		}
	}
	return history
}
