package compaction

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompactToolWireOpts carries commit-time diet stamps for wire compact.
type CompactToolWireOpts struct {
	DietStamp       string
	DietStampSource string
	EvidenceHandles []string
}

// CompactToolWireIfOversized reduces oversized tool payloads before persistence.
// Commit-time classification bypasses recency protection but retains pinned rows.
func (c *SimpleCompactor) CompactToolWireIfOversized(ctx context.Context, info SessionInfo, toolName, content string, opts CompactToolWireOpts) (string, CompactedChunkMeta, bool, error) {
	meta := CompactedChunkMeta{}
	content = strings.TrimSpace(content)
	if content == "" || !c.cfg.Enabled {
		return content, meta, false, nil
	}
	ctx = withCompactionSession(ctx, info)
	tokens := tokenest.EstimateDefault(content)
	threshold := c.cfg.ChunkTokenThreshold
	if threshold <= 0 {
		threshold = DefaultCompactionConfig().ChunkTokenThreshold
	}
	msg := ContextMessage{
		Role:            string(api.MessageRoleTool),
		Content:         content,
		ToolName:        toolName,
		DietStamp:       opts.DietStamp,
		DietStampSource: opts.DietStampSource,
		EvidenceHandles: append([]string(nil), opts.EvidenceHandles...),
	}
	class := ClassifyMessageDiet(ClassifyMessageDietInput{
		Index:         0,
		Messages:      []ContextMessage{msg},
		ForceEligible: true,
		Binding:       evidence.ActiveBinding(),
	})
	if class.SkipOversizedChunk(content) {
		return content, meta, false, nil
	}
	if tokens < threshold {
		return content, meta, false, nil
	}
	ch := ContentChunk{
		Kind:              ChunkKindToolResult,
		Tokens:            tokens,
		ToolName:          toolName,
		HostDataDir:       info.HostDataDir,
		MaxToolSpillBytes: info.MaxToolSpillBytes,
		EvidenceHandles:   append([]string(nil), opts.EvidenceHandles...),
		Class:             class,
	}
	out, compactMeta, err := c.chunks.CompactChunk(ctx, ch, content)
	if err != nil {
		return content, meta, false, err
	}
	if compactMeta.Strategy == "" || out == content || compactMeta.CompactedTokens >= compactMeta.OriginalTokens {
		return content, meta, false, nil
	}
	projected := msg
	projected.Content = out
	useful, err := c.usefulCompaction(info, []ContextMessage{msg}, []ContextMessage{projected}, max(1, c.cfg.ChunkProtectedMinSavingsTokens))
	if err != nil {
		return content, meta, false, err
	}
	if !useful {
		return content, meta, false, nil
	}
	return out, compactMeta, true, nil
}
