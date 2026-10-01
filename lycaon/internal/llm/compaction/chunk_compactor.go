package compaction

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

// spillLog records agent-readable spill paths created during compaction.
var spillLog = observability.LazyComponent("compactor")

// recallPointer links the compacted banner to the full observation.
func (c ContentChunk) recallPointer() string {
	terms := make([]string, 0, len(c.EvidenceHandles))
	for _, h := range c.EvidenceHandles {
		if h = strings.TrimSpace(h); h != "" {
			terms = append(terms, "handle:"+h)
		}
	}
	if len(terms) == 0 {
		return ""
	}
	return "; recall " + strings.Join(terms, " OR ")
}

// CompactedChunkMeta records in-place chunk compaction metadata.
type CompactedChunkMeta struct {
	OriginalTokens  int       `json:"original_tokens"`
	CompactedTokens int       `json:"compacted_tokens"`
	Kind            ChunkKind `json:"kind"`
	Strategy        string    `json:"strategy"`
	At              time.Time `json:"at"`
}

// ChunkCompactor shrinks individual oversized message blocks.
type ChunkCompactor struct {
	cfg       CompactionConfig
	summarize Summarizer
}

// Summarizer performs lite-model text summarization.
type Summarizer interface {
	Summarize(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error)
}

func NewChunkCompactor(cfg CompactionConfig, s Summarizer) *ChunkCompactor {
	return &ChunkCompactor{cfg: cfg, summarize: s}
}

// CompactChunk shrinks one content block using ClassifyMessageDiet strategy.
func (c *ChunkCompactor) CompactChunk(ctx context.Context, chunk ContentChunk, content string) (string, CompactedChunkMeta, error) {
	meta := CompactedChunkMeta{
		OriginalTokens: tokenest.EstimateDefault(content),
		Kind:           chunk.Kind,
		At:             time.Now().UTC(),
	}
	class := chunk.Class
	if class.Eligibility != DietEligibilityEligible {
		meta.CompactedTokens = meta.OriginalTokens
		return content, meta, nil
	}
	meta.Strategy = string(class.Strategy)

	var out string
	var err error

	switch class.Strategy {
	case evidence.TranscriptDietPreserveStructure:
		preserved, ok := TrimOverlayPromoteChunk(content)
		if ok {
			meta.Strategy = "overlay_promote_preserve"
			out = preserved
			if tokenest.EstimateDefault(out) > c.cfg.ChunkTargetTokens {
				out = truncateHeadTail(out, c.cfg.ChunkTargetTokens)
			}
			meta.CompactedTokens = tokenest.EstimateDefault(out)
			return out, meta, nil
		}
		// Preserve the body when no structural trimmer applies.
		meta.Strategy = ""
		meta.CompactedTokens = meta.OriginalTokens
		return content, meta, nil
	case evidence.TranscriptDietAnchorResidue:
		spillOut, strategy, ok := c.compactSpillSummarize(chunk, content, meta.OriginalTokens)
		if ok {
			meta.Strategy = strategy
			out = spillOut
			meta.CompactedTokens = tokenest.EstimateDefault(out)
			return out, meta, nil
		}
		meta.Strategy = ""
		meta.CompactedTokens = meta.OriginalTokens
		return content, meta, nil
	case evidence.TranscriptDietSpillPointer:
		out, meta.Strategy = c.compactSpillPointer(ctx, chunk, content, meta.OriginalTokens)
		meta.CompactedTokens = tokenest.EstimateDefault(out)
		return out, meta, nil
	case evidence.TranscriptDietShapeBounded:
		out, meta.Strategy = c.compactSpillPointer(ctx, chunk, content, meta.OriginalTokens)
		meta.CompactedTokens = tokenest.EstimateDefault(out)
		return out, meta, nil
	case evidence.TranscriptDietAgeDefault:
	}

	strategy := c.strategyFor(chunk, content)
	meta.Strategy = strategy
	switch strategy {
	case "truncate":
		out = truncateHeadTail(content, c.cfg.ChunkTargetTokens)
	case "spill_index":
		out, meta.Strategy = c.compactSpillIndex(ctx, chunk, content, meta.OriginalTokens)
	default:
		out, err = c.summarizeChunk(ctx, chunk, content)
		if err != nil {
			meta.Strategy = "truncate"
			out = truncateHeadTail(content, c.cfg.ChunkTargetTokens)
			err = nil
		} else {
			lines := toolkit.CountLines(content)
			out = hostmarker.CompactionBanner(fmt.Sprintf(
				"%s — %d lines → summary; original ~%d tokens%s",
				chunk.Kind, lines, meta.OriginalTokens, chunk.recallPointer())) + "\n\n" + out
		}
	}
	meta.CompactedTokens = tokenest.EstimateDefault(out)
	return out, meta, err
}

// compactSpillPointer spills full bytes and keeps a handle/spill-path residue.
func (c *ChunkCompactor) compactSpillPointer(ctx context.Context, chunk ContentChunk, content string, originalTokens int) (string, string) {
	if tooloutput.IsStructuredToolJSON(content) {
		out, strategy := c.compactStructuredTool(chunk, content, originalTokens)
		if strategy != "" {
			strategy = "spill_pointer"
		}
		return out, strategy
	}
	return c.compactSpillIndex(ctx, chunk, content, originalTokens)
}

// compactSpillIndex retains an outline and verbatim excerpts; lists use bounded windows.
func (c *ChunkCompactor) compactSpillIndex(ctx context.Context, chunk ContentChunk, content string, originalTokens int) (string, string) {
	if tooloutput.IsStructuredToolJSON(content) {
		return c.compactStructuredTool(chunk, content, originalTokens)
	}
	retained := chunk.recallPointer()
	if strings.TrimSpace(chunk.HostDataDir) != "" {
		spill := tooloutput.SpillWholeRaw(chunk.HostDataDir, tooloutput.Screened(content), chunk.MaxToolSpillBytes)
		if spill.SpillPath == "" || spill.SpillCapped || spill.RejectCode != "" {
			return content, ""
		}
		retained += "; full observation at " + spill.SpillPath
	}
	banner := hostmarker.CompactionBanner(fmt.Sprintf("%s — original ~%d tokens%s", chunk.Kind, originalTokens, retained)) + "\n"
	index := toolOutputIndex(ctx, content, c.chunkTargetBytes()/4)
	prefix := banner + index + "\n" + hostmarker.VerbatimHeadTail + "\n"
	remaining := (c.chunkTargetBytes() - len(prefix)) / tokenest.DefaultDivisor
	if remaining <= 0 {
		return content, ""
	}
	out := prefix + truncateHeadTail(content, remaining)
	if tokenest.EstimateDefault(out) >= originalTokens {
		return content, ""
	}
	return out, "spill_index"
}

func (c *ChunkCompactor) chunkTargetBytes() int {
	target := c.cfg.ChunkTargetTokens
	if target <= 0 {
		target = DefaultCompactionConfig().ChunkTargetTokens
	}
	return target * tokenest.DefaultDivisor
}

func toolOutputIndex(ctx context.Context, content string, maxBytes int) string {
	outline := fileoutline.AnalyzeText(ctx, "", []byte(content))
	var b strings.Builder
	if outline.LogDigest != nil {
		fmt.Fprintf(&b, "log digest (format %s, %d/%d records parsed)\n",
			outline.LogDigest.Format, outline.LogDigest.ParsedCount, outline.LogDigest.RecordCount)
	} else if len(outline.Symbols) > 0 {
		b.WriteString("map (outline; 1-based lines):\n")
		for _, symbol := range outline.Symbols {
			line := fmt.Sprintf("  L%-5d %s %s\n", symbol.Line, symbol.Kind, symbol.Name)
			if b.Len()+len(line) > maxBytes {
				break
			}
			b.WriteString(line)
		}
	}
	if b.Len() > maxBytes {
		return ""
	}
	return b.String()
}

// compactStructuredTool retains a complete structured observation and fits its model view.
func (c *ChunkCompactor) compactStructuredTool(chunk ContentChunk, content string, originalTokens int) (string, string) {
	out := tooloutput.SpillWholeToolOutput(strings.TrimSpace(chunk.HostDataDir), tooloutput.Screened(content), chunk.MaxToolSpillBytes)
	if out.SpillPath == "" || out.SpillCapped || out.RejectCode != "" {
		return content, ""
	}
	residue := tooloutput.InjectWireSpillPath(content, out.SpillPath)
	residue = withoutPriorCompactionBanner(residue)
	banner := hostmarker.CompactionBanner(fmt.Sprintf(
		"%s — original ~%d tokens; bounded view; full observation at wire_spill_path%s",
		chunk.Kind, originalTokens, chunk.recallPointer())) + "\n"
	budget := c.chunkTargetBytes() - len(banner)
	if budget <= 0 {
		return content, ""
	}
	if fitted, ok := tooloutput.FitWireJSON(residue, budget); ok {
		residue = fitted
	}
	if len(residue) > budget || tokenest.EstimateDefault(banner+residue) >= originalTokens {
		return content, ""
	}
	spillLog.Debug("structured tool output compacted", "original_tokens", originalTokens,
		"compacted_tokens", tokenest.EstimateDefault(banner+residue), "wire_spill_path", out.SpillPath)
	return banner + residue, "spill_structured"
}

func (c *ChunkCompactor) strategyFor(chunk ContentChunk, content string) string {
	if chunk.Kind == ChunkKindToolResult {
		return "spill_index"
	}
	if strings.Contains(content, hostmarker.CompactionBannerOpen) {
		return "truncate"
	}
	if chunk.Kind == ChunkKindUnknown && c.cfg.ChunkStrategyDefault != "summarize" {
		return "truncate"
	}
	if c.cfg.ChunkStrategyDefault == "truncate" {
		return "truncate"
	}
	return "summarize"
}

func (c *ChunkCompactor) summarizerExcerpt(content string) string {
	threshold := c.cfg.ChunkTokenThreshold
	if threshold <= 0 {
		threshold = DefaultCompactionConfig().ChunkTokenThreshold
	}
	return truncateHeadTail(content, threshold)
}

func (c *ChunkCompactor) summarizeChunk(ctx context.Context, chunk ContentChunk, content string) (string, error) {
	if c.summarize == nil {
		return truncateHeadTail(content, c.cfg.ChunkTargetTokens), nil
	}
	prompt := fmt.Sprintf("Summarize this %s for coordinator continuation.\n\n%s", chunk.Kind, c.summarizerExcerpt(content))
	systemPrompt, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionChunkSummarySystemRef)
	if err != nil {
		return "", err
	}
	return c.summarize.Summarize(ctx, systemPrompt, prompt, c.cfg.ChunkTargetTokens)
}

// truncateHeadTail keeps head and tail portions within token budget.
func truncateHeadTail(content string, targetTokens int) string {
	if targetTokens <= 0 {
		targetTokens = DefaultCompactionConfig().ChunkTargetTokens
	}
	maxChars := targetTokens * 4
	if len(content) <= maxChars {
		return content
	}
	head := maxChars / 2
	middle := "\n..." + hostmarker.CompactionBanner("middle omitted") + "...\n"
	tail := maxChars - head - len(middle)
	if tail < 0 {
		return strings.ToValidUTF8(content[:maxChars], "")
	}
	if head+tail >= len(content) {
		return strings.ToValidUTF8(content[:maxChars], "")
	}
	// Both cuts are byte offsets, so either can land mid-rune.
	return strings.ToValidUTF8(content[:head], "") + middle + strings.ToValidUTF8(content[len(content)-tail:], "")
}
