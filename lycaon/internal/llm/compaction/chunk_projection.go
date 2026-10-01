package compaction

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

func (c *SimpleCompactor) chunkRevision(ctx context.Context, info SessionInfo, msg ContextMessage, ch ContentChunk, minSavings int) string {
	prompt, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionChunkSummarySystemRef)
	if err != nil {
		return ""
	}
	model, err := c.summaryRevision()
	if err != nil {
		return ""
	}
	revision, _ := RevisionDigest(struct {
		Version                                                            int
		Message                                                            ContextMessage
		Policy                                                             CompactionConfig
		Class                                                              MessageDietClass
		Prompt, SummaryModel, Model, ProviderID, Encoding, AuthorityNotice string
		Minimum                                                            int
		MaxToolSpillBytes                                                  int
	}{compactionRevision, msg, c.cfg, ch.Class, prompt, model, info.Model, info.ProviderID, modelinfo.DefaultModelContextWindows().TextEncoding(info.Model), transcript.ContentAuthorityNotice(), minSavings, info.MaxToolSpillBytes})
	return revision
}

type chunkProjectionResult struct {
	Message   ContextMessage
	Accepted  bool
	Attempted bool
}

func (c *SimpleCompactor) projectChunk(ctx context.Context, info SessionInfo, ch ContentChunk, msg ContextMessage, minSavings int) chunkProjectionResult {
	revision := c.chunkRevision(ctx, info, msg, ch, minSavings)
	memo := info.ChunkProjections
	if memo != nil && msg.ID != "" && revision != "" {
		prior, found, err := memo.GetChunkProjection(ctx, info.ID, msg.ID)
		if err != nil {
			slog.WarnContext(ctx, "chunk projection lookup failed", "session_id", info.ID, "message_id", msg.ID, "error", err)
			return chunkProjectionResult{Message: msg}
		}
		if found && prior.Revision == revision {
			if prior.Meta == nil {
				return chunkProjectionResult{Message: msg}
			}
			projected := applyChunkProjection(msg, prior)
			slog.DebugContext(ctx, "chunk projection reused", "session_id", info.ID, "message_id", msg.ID, "source_revision", revision)
			return chunkProjectionResult{Message: projected, Accepted: true}
		}
	}
	content, meta, err := c.chunks.CompactChunk(ctx, ch, msg.Content)
	if ctx.Err() != nil {
		return chunkProjectionResult{Message: msg}
	}
	projection := messageview.ChunkProjection{Revision: revision}
	accepted := err == nil && meta.Strategy != "" && msg.Content != content && meta.OriginalTokens-meta.CompactedTokens >= max(1, minSavings)
	if accepted {
		projection.Content = content
		projection.Meta = &api.CompactedChunkMeta{
			OriginalTokens: meta.OriginalTokens, CompactedTokens: meta.CompactedTokens,
			Kind: string(meta.Kind), Strategy: meta.Strategy, CompactedAt: meta.At,
		}
	}
	if accepted {
		accepted, err = c.usefulCompaction(info, []ContextMessage{msg}, []ContextMessage{applyChunkProjection(msg, projection)}, max(1, minSavings))
		if !accepted {
			projection.Content, projection.Meta = "", nil
		}
	}
	// Transient failures remain retryable.
	// Spill projections depend on file retention and are inexpensive to rebuild.
	if err == nil && memo != nil && msg.ID != "" && revision != "" && (!accepted || meta.Strategy == "summarize" || meta.Strategy == "truncate") {
		if err := memo.PutChunkProjection(ctx, info.ID, msg.ID, projection); err != nil {
			slog.WarnContext(ctx, "chunk projection save failed", "session_id", info.ID, "message_id", msg.ID, "error", err)
		}
	}
	slog.DebugContext(ctx, "chunk compaction attempted", "session_id", info.ID, "message_id", msg.ID,
		"source_revision", revision, "accepted", accepted, "tokens_before", meta.OriginalTokens, "tokens_after", meta.CompactedTokens)
	if !accepted {
		return chunkProjectionResult{Message: msg, Attempted: true}
	}
	return chunkProjectionResult{Message: applyChunkProjection(msg, projection), Accepted: true, Attempted: true}
}

func applyChunkProjection(msg ContextMessage, projection messageview.ChunkProjection) ContextMessage {
	meta := projection.Meta
	msg.SourceContext = sourceref.Mentioned(sourceref.Mentioned(msg.SourceContext, msg.Content), projection.Content)
	msg.Content = projection.Content
	msg.TrustTier = compactedTrustFloor([]ContextMessage{msg}, nil)
	if meta.Strategy == "summarize" {
		msg.Origin = api.MessageOriginModel
	}
	msg.Authority = api.ContentAuthorityNone
	msg.ContentParts = []api.MessageContentPart{{Content: msg.Content, Origin: msg.Origin,
		Authority: msg.Authority, TrustTier: msg.TrustTier, Source: "compacted_chunk"}}
	msg.ModelProjected = false
	msg.CompactedChunk = &CompactedChunkMeta{OriginalTokens: meta.OriginalTokens,
		CompactedTokens: meta.CompactedTokens, Kind: ChunkKind(meta.Kind), Strategy: meta.Strategy, At: meta.CompactedAt}
	if msg.ToolImageTokens > 0 {
		msg.DropVisualOnCompact = true
		msg.ToolImageTokens = 0
	}
	return msg
}
