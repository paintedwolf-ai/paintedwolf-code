package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

type secretStorageOverlay struct {
	stored    api.Message
	transient api.Message
}

type toolResultStorageProjection struct {
	content string
	args    map[string]any
}

func (l toolInvocations) storageSafeMessage(ctx context.Context, msg api.Message) (api.Message, *api.Message) {
	if l.PromptLoop == nil || l.Deps.RedactMessageForStorage == nil {
		return msg, nil
	}
	stored, changed := l.Deps.RedactMessageForStorage(ctx, msg)
	if !changed {
		return stored, nil
	}
	transient := msg
	return stored, &transient
}

// projectToolResultForStorage applies the durable transcript policy before a
// tool payload can cross an earlier persistence seam such as an overflow spill.
func (l toolInvocations) projectToolResultForStorage(ctx context.Context, content string, args map[string]any) toolResultStorageProjection {
	stored, _ := l.storageSafeMessage(ctx, api.Message{
		Content: content,
		ToolResult: &api.ToolResult{
			Content:  content,
			ToolArgs: args,
		},
	})
	projection := toolResultStorageProjection{content: stored.Content, args: args}
	if stored.ToolResult != nil {
		projection.content = stored.ToolResult.Content
		projection.args = stored.ToolResult.ToolArgs
	}
	return projection
}

// transientMessageFromStored preserves durable host metadata while restoring
// only the raw text needed by the next provider-bound secret decision.
func transientMessageFromStored(stored api.Message, raw *api.Message) api.Message {
	if raw == nil {
		return stored
	}
	out := stored
	// Restored raw bytes are not redacted, so the stored spans do not apply.
	out.HostSecretRedaction = raw.HostSecretRedaction
	out.Content = prependEvidenceHandles(raw.Content, stored.EvidenceHandles)
	out.ContentParts = append([]api.MessageContentPart(nil), raw.ContentParts...)
	out.ToolCalls = append([]api.ToolCall(nil), raw.ToolCalls...)
	if stored.ToolResult != nil && raw.ToolResult != nil {
		clone := *stored.ToolResult
		clone.Content = prependEvidenceHandles(raw.ToolResult.Content, stored.EvidenceHandles)
		clone.ToolArgs = raw.ToolResult.ToolArgs
		out.ToolResult = &clone
	}
	return out
}

func prependEvidenceHandles(content string, handles []string) string {
	for _, handle := range handles {
		if strings.TrimSpace(handle) != "" {
			content = guidance.PrependHandleTag(content, handle)
		}
	}
	return content
}

func (st *promptLoopTurnState) lookupSecretStorage(id string) (secretStorageOverlay, bool) {
	if st == nil || len(st.secretStorageOverlays) == 0 {
		return secretStorageOverlay{}, false
	}
	overlay, ok := st.secretStorageOverlays[strings.TrimSpace(id)]
	return overlay, ok
}

func (st *promptLoopTurnState) rememberSecretStorageOverlay(stored, transient api.Message) {
	if st == nil || strings.TrimSpace(stored.ID) == "" {
		return
	}
	if st.secretStorageOverlays == nil {
		st.secretStorageOverlays = make(map[string]secretStorageOverlay)
	}
	st.secretStorageOverlays[stored.ID] = secretStorageOverlay{stored: stored, transient: transient}
}

func (st *promptLoopTurnState) applySecretStorageOverlays(history []api.Message) []api.Message {
	if st == nil || len(st.secretStorageOverlays) == 0 {
		return history
	}
	for i := range history {
		if overlay, ok := st.secretStorageOverlays[history[i].ID]; ok {
			// Compaction may have advanced the stored copy since registration; keep
			// the latest one and restore raw fields onto it.
			overlay.stored = history[i]
			overlay.transient = transientMessageFromStored(history[i], &overlay.transient)
			st.secretStorageOverlays[history[i].ID] = overlay
			history[i] = overlay.transient
		}
	}
	return history
}

func (st *promptLoopTurnState) clearSecretStorageOverlays() {
	if st == nil || len(st.secretStorageOverlays) == 0 {
		return
	}
	for i := range st.history {
		if overlay, ok := st.secretStorageOverlays[st.history[i].ID]; ok {
			st.history[i] = overlay.stored
		}
	}
	clear(st.secretStorageOverlays)
}

func (st *promptLoopTurnState) hasSecretStorageOverlays() bool {
	return st != nil && len(st.secretStorageOverlays) > 0
}
