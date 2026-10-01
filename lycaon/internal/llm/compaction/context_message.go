package compaction

import (
	"time"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

// ContextMessage is a session message used by the compactor.
type ContextMessage struct {
	ID                 string
	Role               string
	Content            string
	Origin             api.MessageOrigin
	Authority          api.ContentAuthority
	TrustTier          api.ContentTrustTier
	ContentParts       []api.MessageContentPart
	Visibility         api.MessageVisibility
	CheckpointDecision *api.CheckpointDecisionMeta
	ToolFeedback       []api.ToolFeedback
	ToolCalls          []api.ToolCall
	// ToolCallID resolves the producing tool across assistant and result rows.
	ToolCallID           string
	ToolName             string
	SourceContext        *api.SourceContext
	EvidenceHandles      []string
	HostSecretRedaction  *api.HostSecretRedactionMeta
	DietStamp            string
	DietStampSource      string
	WorkerSummary        *api.WorkerSummaryMeta
	CompactedChunk       *CompactedChunkMeta
	CompactionCheckpoint bool
	// ContextPinned protects instructions throughout compaction.
	ContextPinned bool
	// PromptCacheBreakpoint marks a stable boundary for provider caches.
	PromptCacheBreakpoint api.PromptCacheTier
	// ToolImageTokens estimates the tool result's perceive image. It is charged
	// only while the image sits inside the perception window.
	ToolImageTokens int
	// AttachmentImageTokens estimates the images a prompt attached.
	AttachmentImageTokens int
	// DropVisualOnCompact clears ToolResult.Visual when this row's text is compacted.
	DropVisualOnCompact bool
	// ModelProjected prevents duplicate data markers after an IR round trip.
	ModelProjected bool
}

// ContextMessageFromAPI converts wire messages for compaction.
func ContextMessageFromAPI(m api.Message) ContextMessage {
	m = api.NormalizeMessageProvenance(m)
	out := ContextMessage{
		ID:                    m.ID,
		Role:                  string(m.Role),
		Content:               m.Content,
		Origin:                m.Origin,
		Authority:             m.Authority,
		TrustTier:             m.TrustTier,
		ContentParts:          append([]api.MessageContentPart(nil), m.ContentParts...),
		Visibility:            m.Visibility,
		ToolCalls:             append([]api.ToolCall(nil), m.ToolCalls...),
		CompactionCheckpoint:  m.CompactionCheckpoint,
		ContextPinned:         m.ContextPinned,
		PromptCacheBreakpoint: m.PromptCacheBreakpoint,
		EvidenceHandles:       append([]string(nil), m.EvidenceHandles...),
		SourceContext:         m.SourceContext,
		HostSecretRedaction:   cloneHostSecretRedaction(m.HostSecretRedaction),
		DietStamp:             m.DietStamp,
		DietStampSource:       m.DietStampSource,
		ModelProjected:        m.ModelProjected,
	}
	if m.ToolResult != nil {
		if out.Content == "" {
			out.Content = m.ToolResult.Content
		}
		out.CheckpointDecision = m.ToolResult.CheckpointDecision
		out.ToolFeedback = append([]api.ToolFeedback(nil), m.ToolResult.Feedback...)
		out.ToolCallID = m.ToolResult.ToolCallID
		out.ToolName = m.ToolResult.Tool
		if v := m.ToolResult.Visual; v != nil && v.Perceive {
			out.ToolImageTokens = providerwire.ImageTokenEstimate(v.Width, v.Height)
		}
	}
	out.AttachmentImageTokens = len(m.ArtifactIDs) * providerwire.UnsizedImageTokenEstimate
	if m.WorkerSummary != nil {
		cp := *m.WorkerSummary
		out.WorkerSummary = &cp
	}
	if m.CompactedChunk != nil {
		meta := CompactedChunkMeta{
			OriginalTokens:  m.CompactedChunk.OriginalTokens,
			CompactedTokens: m.CompactedChunk.CompactedTokens,
			Kind:            ChunkKind(m.CompactedChunk.Kind),
			Strategy:        m.CompactedChunk.Strategy,
			At:              m.CompactedChunk.CompactedAt,
		}
		out.CompactedChunk = &meta
	}
	return out
}

// ContextMessageToAPI converts compactor messages back to wire DTOs.
func ContextMessageToAPI(m ContextMessage) api.Message {
	out := api.Message{
		ID:                    m.ID,
		Role:                  api.MessageRole(m.Role),
		Content:               m.Content,
		Origin:                m.Origin,
		Authority:             m.Authority,
		TrustTier:             m.TrustTier,
		ContentParts:          append([]api.MessageContentPart(nil), m.ContentParts...),
		Visibility:            m.Visibility,
		ToolCalls:             append([]api.ToolCall(nil), m.ToolCalls...),
		CompactionCheckpoint:  m.CompactionCheckpoint,
		ContextPinned:         m.ContextPinned,
		PromptCacheBreakpoint: m.PromptCacheBreakpoint,
		CreatedAt:             time.Now().UTC(),
		EvidenceHandles:       append([]string(nil), m.EvidenceHandles...),
		SourceContext:         m.SourceContext,
		HostSecretRedaction:   cloneHostSecretRedaction(m.HostSecretRedaction),
		DietStamp:             m.DietStamp,
		DietStampSource:       m.DietStampSource,
		ModelProjected:        m.ModelProjected,
	}
	if m.Role == string(api.MessageRoleTool) {
		out.ToolResult = &api.ToolResult{ToolCallID: m.ToolCallID, Tool: m.ToolName, Content: m.Content,
			CheckpointDecision: m.CheckpointDecision, Feedback: append([]api.ToolFeedback(nil), m.ToolFeedback...)}
	}
	if m.WorkerSummary != nil {
		cp := *m.WorkerSummary
		out.WorkerSummary = &cp
	}
	if m.CompactedChunk != nil {
		out.CompactedChunk = &api.CompactedChunkMeta{
			OriginalTokens:  m.CompactedChunk.OriginalTokens,
			CompactedTokens: m.CompactedChunk.CompactedTokens,
			Kind:            string(m.CompactedChunk.Kind),
			Strategy:        m.CompactedChunk.Strategy,
			CompactedAt:     m.CompactedChunk.At,
		}
	}
	return out
}

func cloneHostSecretRedaction(meta *api.HostSecretRedactionMeta) *api.HostSecretRedactionMeta {
	if meta == nil {
		return nil
	}
	clone := *meta
	return &clone
}

// ContextMessagesFromAPI converts a slice of wire messages.
func ContextMessagesFromAPI(msgs []api.Message) []ContextMessage {
	out := make([]ContextMessage, len(msgs))
	for i := range msgs {
		out[i] = ContextMessageFromAPI(msgs[i])
	}
	return out
}

// ContextMessagesToAPI converts compactor messages to wire DTOs.
// Original messages restore transcript fields omitted by the content IR.
func ContextMessagesToAPI(msgs []ContextMessage, original []api.Message) []api.Message {
	byID := make(map[string]api.Message, len(original))
	for _, m := range original {
		byID[m.ID] = m
	}
	out := make([]api.Message, len(msgs))
	for i, m := range msgs {
		out[i] = ContextMessageToAPI(m)
		if m.ID == "" {
			continue
		}
		canonical, ok := byID[m.ID]
		if !ok {
			continue
		}
		out[i] = api.RehydrateTranscriptProjectionFields(out[i], canonical)
		out[i].CreatedAt = canonical.CreatedAt
		if canonical.ToolResult != nil {
			cp := *canonical.ToolResult
			if m.CompactedChunk != nil {
				cp.Content = m.Content
			}
			if m.DropVisualOnCompact {
				cp.Visual = nil
			}
			out[i].ToolResult = &cp
		}
	}
	return out
}
